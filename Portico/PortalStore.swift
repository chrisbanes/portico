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
        writeData: @escaping (Data, URL) throws -> Void = { data, url in
            try data.write(to: url, options: .atomic)
        }
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
            if FileManager.default.fileExists(atPath: legacyConfigurationURL.path) {
                try? FileManager.default.removeItem(at: legacyConfigurationURL)
            }
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
        let installation = InstallationRecord(portals: [envelope.portal.migrated])
        try save(installation)
        try FileManager.default.removeItem(at: legacyConfigurationURL)
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
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: installationURL.path)
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
