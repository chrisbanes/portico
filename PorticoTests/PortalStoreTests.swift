import Foundation
import XCTest
@testable import Portico

final class PortalStoreTests: XCTestCase {
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
        XCTAssertEqual(try permissions(of: store.configurationURL), 0o600)
        XCTAssertThrowsError(try store.save(portal))
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

    private func permissions(of url: URL) throws -> Int {
        let attributes = try FileManager.default.attributesOfItem(atPath: url.path)
        return (attributes[.posixPermissions] as? NSNumber)?.intValue ?? -1
    }
}
