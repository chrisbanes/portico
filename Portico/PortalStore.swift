import Foundation

enum PortalStoreError: Error {
    case alreadyExists
    case unsupportedVersion(Int)
}

struct PortalStore {
    let rootURL: URL

    var configurationURL: URL {
        rootURL.appendingPathComponent("portal-v1.json", isDirectory: false)
    }

    func load() throws -> PortalConfiguration? {
        guard FileManager.default.fileExists(atPath: configurationURL.path) else {
            return nil
        }
        let data = try Data(contentsOf: configurationURL)
        let envelope = try JSONDecoder().decode(PortalConfigurationEnvelope.self, from: data)
        guard envelope.version == PortalConfigurationEnvelope.currentVersion else {
            throw PortalStoreError.unsupportedVersion(envelope.version)
        }
        return envelope.portal
    }

    func save(_ portal: PortalConfiguration) throws {
        guard !FileManager.default.fileExists(atPath: configurationURL.path) else {
            throw PortalStoreError.alreadyExists
        }
        try FileManager.default.createDirectory(
            at: rootURL,
            withIntermediateDirectories: true,
            attributes: [.posixPermissions: 0o700]
        )
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: rootURL.path)
        let data = try JSONEncoder().encode(PortalConfigurationEnvelope(portal: portal))
        try data.write(to: configurationURL, options: .atomic)
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: configurationURL.path)
    }
}
