import XCTest
@testable import Portico

final class PortalConfigurationTests: XCTestCase {
    func testLocalAppPortalDestinationAcceptsOnlyValidPorts() throws {
        XCTAssertEqual(try XCTUnwrap(PortalDestination(localAppPort: 1)).localAppPort, 1)
        XCTAssertEqual(try XCTUnwrap(PortalDestination(localAppPort: 65535)).localAppPort, 65535)
        XCTAssertNil(PortalDestination(localAppPort: 0))
    }

    func testVersionThreeInstallationDefaultsRequireLoggingChoiceAndHaveNotOfferedLogin() {
        let installation = InstallationRecord()

        XCTAssertEqual(installation.version, 3)
        XCTAssertEqual(installation.operationalLogging, .undecided)
        XCTAssertEqual(installation.launchAtLoginOffer, .notOffered)
    }

    func testAcceptsDNSLabelAndPortBoundaries() throws {
        for name in ["a", "portal-1", String(repeating: "a", count: 63)] {
            XCTAssertNoThrow(try PortalInputValidator.validate(name: name, port: "1"))
            XCTAssertNoThrow(try PortalInputValidator.validate(name: name, port: "65535"))
        }
    }

    func testRejectsInvalidNames() {
        for name in ["", "A", "-portal", "portal-", "portal_name", "portal.name", String(repeating: "a", count: 64)] {
            XCTAssertThrowsError(try PortalInputValidator.validate(name: name, port: "8080"), name)
        }
    }

    func testRejectsInvalidPorts() {
        for port in ["", "0", "65536", "-1", "+80", " 80", "80 ", "8.0", "http"] {
            XCTAssertThrowsError(try PortalInputValidator.validate(name: "portal", port: port), port)
        }
    }
}
