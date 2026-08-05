import Foundation
import XCTest
@testable import PorticoApplication

final class ProcessHelperLauncherTests: XCTestCase {
    func testEnabledLoggingRemovesInheritedOptOutWithoutChangingOtherVariables() throws {
        let environment = try ProcessHelperLauncher.childEnvironment(
            for: .enabled,
            inherited: ["PATH": "/usr/bin", "TS_NO_LOGS_NO_SUPPORT": "true"]
        )

        XCTAssertEqual(environment, ["PATH": "/usr/bin"])
    }

    func testDisabledLoggingSetsExactOptOutValue() throws {
        let environment = try ProcessHelperLauncher.childEnvironment(
            for: .disabled,
            inherited: ["PATH": "/usr/bin", "TS_NO_LOGS_NO_SUPPORT": "false"]
        )

        XCTAssertEqual(environment, ["PATH": "/usr/bin", "TS_NO_LOGS_NO_SUPPORT": "true"])
    }

    func testUndecidedLoggingCannotConstructHelperEnvironment() {
        XCTAssertThrowsError(
            try ProcessHelperLauncher.childEnvironment(for: .undecided, inherited: [:])
        )
    }
}
