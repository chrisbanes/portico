import XCTest
@testable import Portico

final class PortalConfigurationTests: XCTestCase {
    func testRemoteAppDestinationCanonicalizesAndRejectsLoopback() throws {
        XCTAssertEqual(
            try XCTUnwrap(PortalDestination(remoteAppScheme: .https, host: "App.Example.COM", port: 443)),
            .remoteApp(scheme: .https, host: "app.example.com", port: 443)
        )
        for host in ["localhost", "127.0.0.1", "[::1]", "::1", "::ffff:127.0.0.1", "0.0.0.0", "::", "example.com."] {
            XCTAssertNil(PortalDestination(remoteAppScheme: .https, host: host, port: 443), host)
        }
    }

    func testDestinationRejectsMissingExtraAndCrossKindJSONMembers() throws {
        for json in [
            #"{"kind":"localApp"}"#,
            #"{"kind":"localApp","port":80,"host":"example.com"}"#,
            #"{"kind":"remoteApp","scheme":"https","host":"example.com","port":443,"extra":true}"#,
            #"{"kind":"remoteApp","scheme":"https","host":"example.com"}"#,
        ] {
            XCTAssertThrowsError(try JSONDecoder().decode(PortalDestination.self, from: Data(json.utf8)), json)
        }
    }

    func testLocalAppPortalDestinationAcceptsOnlyValidPorts() throws {
        XCTAssertEqual(try XCTUnwrap(PortalDestination(localAppPort: 1)).localAppPort, 1)
        XCTAssertEqual(try XCTUnwrap(PortalDestination(localAppPort: 65535)).localAppPort, 65535)
        XCTAssertNil(PortalDestination(localAppPort: 0))
    }

    func testVersionFourInstallationDefaultsRequireLoggingChoiceAndHaveNotOfferedLogin() {
        let installation = InstallationRecord()

        XCTAssertEqual(installation.version, 4)
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
