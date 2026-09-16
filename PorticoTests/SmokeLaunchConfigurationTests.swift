import Foundation
import XCTest
@testable import PorticoApplication

final class SmokeLaunchConfigurationTests: XCTestCase {
    func testAdmissionRejectsMismatchedFoundationRootBeforeStoreClosure() throws {
        var called = false
        let expected = URL(fileURLWithPath: "/private/tmp/PorticoSmokeTests/Library/Application Support/Portico Dev", isDirectory: true)
        let configuration = SmokeLaunchConfiguration(expectedRootURL: expected)

        XCTAssertFalse(SmokeRootAdmission.run(
            configuration: configuration,
            resolvedRootURL: expected.deletingLastPathComponent(),
            prepareStore: { called = true }
        ))
        XCTAssertFalse(called)
        XCTAssertTrue(SmokeRootAdmission.run(
            configuration: configuration,
            resolvedRootURL: expected,
            prepareStore: { called = true }
        ))
        XCTAssertTrue(called)
    }

    func testRequiresExactConsentAndCanonicalTemporaryDevRoot() throws {
        let root = FileManager.default.temporaryDirectory
            .appendingPathComponent("PorticoSmokeTests-\(UUID().uuidString)/Library/Application Support/Portico Dev", isDirectory: true)
        defer { try? FileManager.default.removeItem(at: root.deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()) }
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)

        XCTAssertThrowsError(try SmokeLaunchConfiguration.current(
            environment: [SmokeLaunchConfiguration.expectedRootEnvironmentKey: root.path],
            variant: .dev
        ))
        XCTAssertThrowsError(try SmokeLaunchConfiguration.current(
            environment: [
                SmokeLaunchConfiguration.consentEnvironmentKey: "yes",
                SmokeLaunchConfiguration.expectedRootEnvironmentKey: root.path,
            ],
            variant: .dev
        ))
        XCTAssertThrowsError(try SmokeLaunchConfiguration.current(
            environment: [
                SmokeLaunchConfiguration.consentEnvironmentKey: SmokeLaunchConfiguration.consentValue,
                SmokeLaunchConfiguration.expectedRootEnvironmentKey: "/Users/chris/Library/Application Support/Portico Dev",
            ],
            variant: .dev
        ))
        XCTAssertThrowsError(try SmokeLaunchConfiguration.current(
            environment: [
                SmokeLaunchConfiguration.consentEnvironmentKey: SmokeLaunchConfiguration.consentValue,
                SmokeLaunchConfiguration.expectedRootEnvironmentKey: root.deletingLastPathComponent().path,
            ],
            variant: .dev
        ))
        XCTAssertEqual(try SmokeLaunchConfiguration.current(
            environment: [
                SmokeLaunchConfiguration.consentEnvironmentKey: SmokeLaunchConfiguration.consentValue,
                SmokeLaunchConfiguration.expectedRootEnvironmentKey: root.path,
            ],
            variant: .dev
        )?.expectedRootURL, root.standardizedFileURL)
    }

    func testAdmitsCanonicalTemporaryDevRootAndRejectsAnEquivalentSymlinkPath() throws {
        let temporaryRoot = URL(fileURLWithPath: "/private/tmp", isDirectory: true)
            .appendingPathComponent("PorticoSmokeTests-\(UUID().uuidString)", isDirectory: true)
        let validRoot = temporaryRoot
            .appendingPathComponent("Library", isDirectory: true)
            .appendingPathComponent("Application Support", isDirectory: true)
            .appendingPathComponent("Portico Dev", isDirectory: true)
        let symlinkRoot = URL(fileURLWithPath: "/private/tmp", isDirectory: true)
            .appendingPathComponent("PorticoSmokeSymlink-\(UUID().uuidString)", isDirectory: true)
        let externalSupportDirectory = URL(fileURLWithPath: "/private/tmp", isDirectory: true)
            .appendingPathComponent("PorticoSmokeOutside-\(UUID().uuidString)", isDirectory: true)
            .appendingPathComponent("Library", isDirectory: true)
            .appendingPathComponent("Application Support", isDirectory: true)
        let linkedSupportDirectory = symlinkRoot
            .appendingPathComponent("Library", isDirectory: true)
            .appendingPathComponent("Application Support", isDirectory: true)
        defer {
            try? FileManager.default.removeItem(at: temporaryRoot)
            try? FileManager.default.removeItem(at: symlinkRoot)
            try? FileManager.default.removeItem(at: externalSupportDirectory.deletingLastPathComponent().deletingLastPathComponent())
        }
        try FileManager.default.createDirectory(at: validRoot, withIntermediateDirectories: true)
        try FileManager.default.createDirectory(
            at: externalSupportDirectory.appendingPathComponent("Portico Dev", isDirectory: true),
            withIntermediateDirectories: true
        )
        try FileManager.default.createDirectory(at: linkedSupportDirectory.deletingLastPathComponent(), withIntermediateDirectories: true)

        let validConfiguration = try SmokeLaunchConfiguration.current(
            environment: [
                SmokeLaunchConfiguration.consentEnvironmentKey: SmokeLaunchConfiguration.consentValue,
                SmokeLaunchConfiguration.expectedRootEnvironmentKey: validRoot.path,
            ],
            variant: .dev
        )
        XCTAssertEqual(validConfiguration?.expectedRootURL, validRoot.standardizedFileURL)

        try FileManager.default.createSymbolicLink(
            at: linkedSupportDirectory,
            withDestinationURL: externalSupportDirectory
        )

        XCTAssertThrowsError(
            try SmokeLaunchConfiguration.current(
                environment: [
                    SmokeLaunchConfiguration.consentEnvironmentKey: SmokeLaunchConfiguration.consentValue,
                    SmokeLaunchConfiguration.expectedRootEnvironmentKey: linkedSupportDirectory
                        .appendingPathComponent("Portico Dev", isDirectory: true).path,
                ],
                variant: .dev
            )
        )
    }
}
