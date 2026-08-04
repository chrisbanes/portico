import Foundation

enum PortalStoreError: Error {
    case alreadyExists
    case unsupportedVersion(Int)
}

struct PortalStore {
    let rootURL: URL
    private let writeData: (Data, URL) throws -> Void

    init(
        rootURL: URL,
        writeData: @escaping (Data, URL) throws -> Void = securelyWrite
    ) {
        self.rootURL = rootURL
        self.writeData = writeData
    }

    var configurationURL: URL {
        legacyConfigurationURL
    }

    var legacyConfigurationURL: URL {
        rootURL.appendingPathComponent("portal-v1.json", isDirectory: false)
    }

    var installationURL: URL {
        rootURL.appendingPathComponent("installation-v3.json", isDirectory: false)
    }

    var versionTwoInstallationURL: URL {
        rootURL.appendingPathComponent("installation-v2.json", isDirectory: false)
    }

    func load() throws -> PortalConfiguration? {
        try loadInstallation().portals.first
    }

    func save(_ portal: PortalConfiguration) throws {
        var installation = try loadInstallation()
        guard installation.portals.isEmpty else {
            throw PortalStoreError.alreadyExists
        }
        installation.portals.append(portal)
        try save(installation)
    }

    func loadInstallation() throws -> InstallationRecord {
        if FileManager.default.fileExists(atPath: installationURL.path) {
            let data = try Data(contentsOf: installationURL)
            let installation = try JSONDecoder().decode(InstallationRecord.self, from: data)
            guard installation.version == InstallationRecord.currentVersion else {
                throw PortalStoreError.unsupportedVersion(installation.version)
            }
            removeOlderFiles()
            return installation
        }
        if FileManager.default.fileExists(atPath: versionTwoInstallationURL.path) {
            let data = try Data(contentsOf: versionTwoInstallationURL)
            let historical = try JSONDecoder().decode(VersionTwoInstallationRecord.self, from: data)
            guard historical.version == VersionTwoInstallationRecord.currentVersion else {
                throw PortalStoreError.unsupportedVersion(historical.version)
            }
            let installation = historical.migrated
            try save(installation)
            removeOlderFiles()
            return installation
        }
        guard FileManager.default.fileExists(atPath: legacyConfigurationURL.path) else {
            return InstallationRecord()
        }
        let data = try Data(contentsOf: legacyConfigurationURL)
        let envelope = try JSONDecoder().decode(LegacyPortalConfigurationEnvelope.self, from: data)
        guard envelope.version == LegacyPortalConfigurationEnvelope.currentVersion else {
            throw PortalStoreError.unsupportedVersion(envelope.version)
        }
        let installation = InstallationRecord(
            portals: [envelope.portal.migrated],
            operationalLogging: .enabled,
            launchAtLoginOffer: .notOffered
        )
        try save(installation)
        removeOlderFiles()
        return installation
    }

    func save(_ installation: InstallationRecord) throws {
        try FileManager.default.createDirectory(
            at: rootURL,
            withIntermediateDirectories: true,
            attributes: [.posixPermissions: 0o700]
        )
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: rootURL.path)
        let data = try JSONEncoder().encode(installation)
        try writeData(data, installationURL)
    }

    private func removeOlderFiles() {
        for url in [versionTwoInstallationURL, legacyConfigurationURL]
        where FileManager.default.fileExists(atPath: url.path) {
            try? FileManager.default.removeItem(at: url)
        }
    }
}

private struct VersionTwoInstallationRecord: Decodable {
    static let currentVersion = 2

    let version: Int
    let tailnetBinding: TailnetBinding?
    let portals: [VersionTwoPortalConfiguration]
    let alerts: [InstallationAlert]

    var migrated: InstallationRecord {
        let isPristine = tailnetBinding == nil && portals.isEmpty && alerts.isEmpty
        return InstallationRecord(
            tailnetBinding: tailnetBinding,
            portals: portals.map(\.migrated),
            alerts: alerts,
            operationalLogging: isPristine ? .undecided : .enabled,
            launchAtLoginOffer: .notOffered
        )
    }
}

private struct VersionTwoPortalConfiguration: Decodable {
    let id: UUID
    let name: String
    let localAppPort: UInt16
    let createdAt: Date
    let desiredState: PortalDesiredState
    let lifecycle: PortalLifecycle
    let removalAssignedName: String?

    private enum CodingKeys: String, CodingKey {
        case id
        case name
        case localAppPort
        case createdAt
        case desiredState
        case lifecycle
        case removalAssignedName
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        id = try container.decode(UUID.self, forKey: .id)
        name = try container.decode(String.self, forKey: .name)
        localAppPort = try container.decode(UInt16.self, forKey: .localAppPort)
        createdAt = try container.decode(Date.self, forKey: .createdAt)
        desiredState = try container.contains(.desiredState)
            ? container.decode(PortalDesiredState.self, forKey: .desiredState)
            : .enabled
        lifecycle = try container.decode(PortalLifecycle.self, forKey: .lifecycle)
        removalAssignedName = lifecycle == .pendingRemoval
            ? try container.decodeIfPresent(String.self, forKey: .removalAssignedName)
            : nil
    }

    var migrated: PortalConfiguration {
        PortalConfiguration(
            id: id,
            name: name,
            localAppPort: localAppPort,
            createdAt: createdAt,
            desiredState: desiredState,
            lifecycle: lifecycle,
            removalAssignedName: removalAssignedName
        )
    }
}

private struct LegacyPortalConfigurationEnvelope: Decodable {
    static let currentVersion = 1

    let version: Int
    let portal: LegacyPortalConfiguration
}

private struct LegacyPortalConfiguration: Decodable {
    let id: UUID
    let name: String
    let localAppPort: UInt16
    let createdAt: Date

    var migrated: PortalConfiguration {
        PortalConfiguration(
            id: id,
            name: name,
            localAppPort: localAppPort,
            createdAt: createdAt,
            lifecycle: .active
        )
    }
}

private func securelyWrite(_ data: Data, to destinationURL: URL) throws {
    let fileManager = FileManager.default
    let temporaryURL = destinationURL
        .deletingLastPathComponent()
        .appendingPathComponent(".\(destinationURL.lastPathComponent).\(UUID().uuidString).tmp")
    defer { try? fileManager.removeItem(at: temporaryURL) }

    guard fileManager.createFile(
        atPath: temporaryURL.path,
        contents: nil,
        attributes: [.posixPermissions: 0o600]
    ) else {
        throw CocoaError(.fileWriteUnknown)
    }
    try data.write(to: temporaryURL)
    try fileManager.setAttributes([.posixPermissions: 0o600], ofItemAtPath: temporaryURL.path)

    if fileManager.fileExists(atPath: destinationURL.path) {
        _ = try fileManager.replaceItemAt(
            destinationURL,
            withItemAt: temporaryURL,
            backupItemName: nil,
            options: .usingNewMetadataOnly
        )
    } else {
        try fileManager.moveItem(at: temporaryURL, to: destinationURL)
    }
}
