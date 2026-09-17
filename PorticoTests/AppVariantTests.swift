import XCTest
@testable import PorticoApplication

final class AppVariantTests: XCTestCase {
    func testDebugMetadataSelectsTheClosedDevVariant() throws {
        let variant = try AppVariant(info: [
            "CFBundleDisplayName": "Portico Dev",
            "PorticoSupportDirectory": "Portico Dev",
            "PorticoLaunchAtLoginAvailable": "NO",
            "CFBundleIdentifier": "dev.chrisbanes.Portico.Debug",
        ])

        XCTAssertEqual(variant.displayName, "Portico Dev")
        XCTAssertEqual(variant.supportDirectoryName, "Portico Dev")
        XCTAssertFalse(variant.isLaunchAtLoginAvailable)
        XCTAssertEqual(variant.bundleIdentifier, "dev.chrisbanes.Portico.Debug")
    }

    func testReleaseMetadataSelectsTheClosedProductionVariant() throws {
        let variant = try AppVariant(info: [
            "CFBundleDisplayName": "Portico",
            "PorticoSupportDirectory": "Portico",
            "PorticoLaunchAtLoginAvailable": "YES",
            "CFBundleIdentifier": "dev.chrisbanes.Portico",
        ])

        XCTAssertEqual(variant.displayName, "Portico")
        XCTAssertEqual(variant.supportDirectoryName, "Portico")
        XCTAssertTrue(variant.isLaunchAtLoginAvailable)
        XCTAssertEqual(variant.bundleIdentifier, "dev.chrisbanes.Portico")
    }

    func testMissingMalformedAndFallbackMetadataAreRejected() {
        let invalidMetadata: [[String: Any]] = [
            [:],
            [
                "CFBundleDisplayName": "",
                "PorticoSupportDirectory": "Portico Dev",
                "PorticoLaunchAtLoginAvailable": "NO",
                "CFBundleIdentifier": "dev.chrisbanes.Portico.Debug",
            ],
            [
                "CFBundleDisplayName": "Portico Dev",
                "PorticoSupportDirectory": "Portico",
                "PorticoLaunchAtLoginAvailable": "NO",
                "CFBundleIdentifier": "dev.chrisbanes.Portico.Debug",
            ],
            [
                "CFBundleDisplayName": "Portico",
                "PorticoSupportDirectory": "Portico",
                "PorticoLaunchAtLoginAvailable": "no",
                "CFBundleIdentifier": "dev.chrisbanes.Portico",
            ],
            [
                "CFBundleDisplayName": "Portico Dev",
                "PorticoSupportDirectory": "Portico Dev",
                "PorticoLaunchAtLoginAvailable": "NO",
                "CFBundleIdentifier": "dev.chrisbanes.Portico",
            ],
        ]

        for metadata in invalidMetadata {
            XCTAssertThrowsError(try AppVariant(info: metadata))
        }
    }

    func testDecodedMetadataMustMatchTheCurrentBuildVariant() throws {
        let debugMetadata: [String: Any] = [
            "CFBundleDisplayName": "Portico Dev",
            "PorticoSupportDirectory": "Portico Dev",
            "PorticoLaunchAtLoginAvailable": "NO",
            "CFBundleIdentifier": "dev.chrisbanes.Portico.Debug",
        ]
        let releaseMetadata: [String: Any] = [
            "CFBundleDisplayName": "Portico",
            "PorticoSupportDirectory": "Portico",
            "PorticoLaunchAtLoginAvailable": "YES",
            "CFBundleIdentifier": "dev.chrisbanes.Portico",
        ]

        XCTAssertEqual(try AppVariant.select(info: debugMetadata, expected: .dev), .dev)
        XCTAssertEqual(try AppVariant.select(info: releaseMetadata, expected: .production), .production)
        XCTAssertThrowsError(try AppVariant.select(info: releaseMetadata, expected: .dev))
        XCTAssertThrowsError(try AppVariant.select(info: debugMetadata, expected: .production))
    }
}
