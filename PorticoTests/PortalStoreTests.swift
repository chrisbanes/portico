import Foundation
import XCTest
@testable import Portico

final class PortalStoreTests: XCTestCase {
    func testMigratesValidVersionOnePortalIntoUnboundVersionTwoInstallation() throws {
        let root = temporaryRoot()
        let store = PortalStore(rootURL: root)
        let legacyURL = root.appendingPathComponent("portal-v1.json", isDirectory: false)
        let installationURL = root.appendingPathComponent("installation-v2.json", isDirectory: false)
        let portal = PortalConfiguration(
            id: UUID(uuidString: "9f55ca93-d7b3-4eab-a871-310ea576005a")!,
            name: "hermes",
            localAppPort: 8787,
            createdAt: Date(timeIntervalSince1970: 1_786_000_000)
        )
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        try historicalVersionOneData().write(to: legacyURL)

        _ = try store.load()

        XCTAssertTrue(FileManager.default.fileExists(atPath: installationURL.path))
        XCTAssertFalse(FileManager.default.fileExists(atPath: legacyURL.path))
        XCTAssertEqual(try permissions(of: root), 0o700)
        XCTAssertEqual(try permissions(of: installationURL), 0o600)
        XCTAssertEqual(try store.loadInstallation(), InstallationRecord(portals: [portal]))
    }

    func testFailedVersionTwoWritePreservesVersionOneSource() throws {
        struct ExpectedFailure: Error {}
        let root = temporaryRoot()
        let legacyURL = root.appendingPathComponent("portal-v1.json", isDirectory: false)
        let store = PortalStore(rootURL: root, writeData: { _, _ in throw ExpectedFailure() })
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        let source = historicalVersionOneData()
        try source.write(to: legacyURL)

        XCTAssertThrowsError(try store.loadInstallation())
        XCTAssertEqual(try Data(contentsOf: legacyURL), source)
        XCTAssertFalse(FileManager.default.fileExists(atPath: store.installationURL.path))
    }

    func testValidVersionTwoIsAuthoritativeAndRemovesStaleVersionOne() throws {
        let root = temporaryRoot()
        let store = PortalStore(rootURL: root)
        let authoritative = InstallationRecord(
            tailnetBinding: TailnetBinding(name: "opaque-tailnet-id", magicDNSSuffix: "example.ts.net"),
            portals: [makePortal(name: "authoritative")]
        )
        try store.save(authoritative)
        try historicalVersionOneData(name: "stale").write(to: store.legacyConfigurationURL)

        XCTAssertEqual(try store.loadInstallation(), authoritative)
        XCTAssertFalse(FileManager.default.fileExists(atPath: store.legacyConfigurationURL.path))
    }

    func testInstallationRoundTripsPortalLifecycleBindingAndAlert() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        var pending = makePortal()
        pending.lifecycle = .pendingTailnetRejection
        let alert = InstallationAlert(
            id: UUID(uuidString: "1f93e456-69ec-445a-8374-d7fc5558d0c7")!,
            kind: .crossTailnetRejection,
            portalName: "hermes",
            assignedName: "hermes-1",
            expectedMagicDNSSuffix: "one.ts.net",
            rejectedMagicDNSSuffix: "two.ts.net",
            createdAt: Date(timeIntervalSince1970: 1_786_000_100)
        )
        let installation = InstallationRecord(
            tailnetBinding: TailnetBinding(name: "opaque-tailnet-id", magicDNSSuffix: "one.ts.net"),
            portals: [pending],
            alerts: [alert]
        )

        try store.save(installation)

        XCTAssertEqual(try store.loadInstallation(), installation)
        XCTAssertEqual(try permissions(of: store.rootURL), 0o700)
        XCTAssertEqual(try permissions(of: store.installationURL), 0o600)
    }

    func testSavedPortalReloadsWithSameImmutableDefinition() throws {
        let root = temporaryRoot()
        let store = PortalStore(rootURL: root)
        let portal = PortalConfiguration(
            id: UUID(uuidString: "9f55ca93-d7b3-4eab-a871-310ea576005a")!,
            name: "hermes",
            localAppPort: 8787,
            createdAt: Date(timeIntervalSince1970: 1_786_000_000)
        )

        try store.save(portal)

        XCTAssertEqual(try store.load(), portal)
        XCTAssertEqual(try permissions(of: root), 0o700)
        XCTAssertEqual(try permissions(of: store.installationURL), 0o600)
        XCTAssertThrowsError(try store.save(portal))
    }

    func testSavingFirstPortalPreservesInstallationBindingAndAlerts() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        let alert = InstallationAlert(
            id: UUID(uuidString: "1f93e456-69ec-445a-8374-d7fc5558d0c7")!,
            kind: .crossTailnetRejection,
            portalName: "atlas",
            assignedName: nil,
            expectedMagicDNSSuffix: "one.ts.net",
            rejectedMagicDNSSuffix: nil,
            createdAt: Date(timeIntervalSince1970: 1_786_000_100)
        )
        let binding = TailnetBinding(name: "opaque-tailnet-id", magicDNSSuffix: "one.ts.net")
        try store.save(InstallationRecord(tailnetBinding: binding, alerts: [alert]))

        let portal = makePortal()
        try store.save(portal)

        XCTAssertEqual(
            try store.loadInstallation(),
            InstallationRecord(tailnetBinding: binding, portals: [portal], alerts: [alert])
        )
    }

    func testMissingConfigurationLoadsAsNil() throws {
        XCTAssertNil(try PortalStore(rootURL: temporaryRoot()).load())
    }

    func testCorruptConfigurationFailsWithoutReplacement() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        try FileManager.default.createDirectory(at: store.rootURL, withIntermediateDirectories: true)
        let corrupt = Data("not-json".utf8)
        try corrupt.write(to: store.configurationURL)

        XCTAssertThrowsError(try store.load())
        XCTAssertEqual(try Data(contentsOf: store.configurationURL), corrupt)
    }

    func testUnsupportedVersionFailsWithoutReplacement() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        try FileManager.default.createDirectory(at: store.rootURL, withIntermediateDirectories: true)
        let unsupported = Data(#"{"version":2,"portal":{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","localAppPort":8787,"createdAt":"2026-08-03T00:00:00Z"}}"#.utf8)
        try unsupported.write(to: store.configurationURL)

        XCTAssertThrowsError(try store.load())
        XCTAssertEqual(try Data(contentsOf: store.configurationURL), unsupported)
    }

    private func temporaryRoot() -> URL {
        FileManager.default.temporaryDirectory
            .appendingPathComponent("PorticoTests-\(UUID().uuidString)", isDirectory: true)
    }

    private func makePortal(name: String = "hermes") -> PortalConfiguration {
        PortalConfiguration(
            id: UUID(uuidString: "9f55ca93-d7b3-4eab-a871-310ea576005a")!,
            name: name,
            localAppPort: 8787,
            createdAt: Date(timeIntervalSince1970: 1_786_000_000)
        )
    }

    private func historicalVersionOneData(name: String = "hermes") -> Data {
        Data(
            #"{"version":1,"portal":{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"\#(name)","localAppPort":8787,"createdAt":807692800}}"#.utf8
        )
    }

    private func permissions(of url: URL) throws -> Int {
        let attributes = try FileManager.default.attributesOfItem(atPath: url.path)
        return (attributes[.posixPermissions] as? NSNumber)?.intValue ?? -1
    }
}
