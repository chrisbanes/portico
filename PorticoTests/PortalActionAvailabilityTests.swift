import XCTest
@testable import PorticoApplication

final class PortalActionAvailabilityTests: XCTestCase {
    func testAuthoritativeActionMatrix() {
        let activeEnabled = PortalActionContext(
            loggingPreference: .enabled,
            inputsValid: true,
            helperAvailability: .connected,
            lifecycle: .active,
            desiredState: .enabled,
            tailscaleState: .online,
            hasPortalURL: true,
            hasTailnetBinding: true,
            portalCount: 1,
            isEditedDestinationValid: true
        )
        let available = PortalActionAvailability(context: activeEnabled)
        XCTAssertTrue(available.addPortal)
        XCTAssertTrue(available.refreshLocalApps)
        XCTAssertTrue(available.stop)
        XCTAssertTrue(available.editDestination)
        XCTAssertTrue(available.copyPortalURL)
        XCTAssertTrue(available.openPortalURL)
        XCTAssertTrue(available.diagnostics)
        XCTAssertTrue(available.remove)
        XCTAssertTrue(available.settings)
        XCTAssertFalse(available.start)
        XCTAssertFalse(available.authenticate)
        XCTAssertFalse(available.retryRemoval)
        XCTAssertFalse(available.resetTailnet)

        var stoppedOffline = activeEnabled
        stoppedOffline.helperAvailability = .failed
        stoppedOffline.desiredState = .stopped
        stoppedOffline.isTailscaleFactsStale = true
        let offline = PortalActionAvailability(context: stoppedOffline)
        XCTAssertTrue(offline.addPortal)
        XCTAssertTrue(offline.start)
        XCTAssertTrue(offline.editDestination)
        XCTAssertTrue(offline.copyPortalURL)
        XCTAssertTrue(offline.remove)
        XCTAssertFalse(offline.refreshLocalApps)
        XCTAssertFalse(offline.openPortalURL)
        XCTAssertFalse(offline.authenticate)

        var authenticating = activeEnabled
        authenticating.tailscaleState = .authenticating
        authenticating.hasPortalURL = false
        XCTAssertTrue(PortalActionAvailability(context: authenticating).authenticate)
        authenticating.isAuthenticationPending = true
        XCTAssertFalse(PortalActionAvailability(context: authenticating).authenticate)
        authenticating.isAuthenticationPending = false
        authenticating.isTailscaleFactsStale = true
        XCTAssertFalse(PortalActionAvailability(context: authenticating).authenticate)

        var removing = activeEnabled
        removing.lifecycle = .pendingRemoval
        removing.removalState = .failed
        let removal = PortalActionAvailability(context: removing)
        XCTAssertTrue(removal.retryRemoval)
        XCTAssertTrue(removal.diagnostics)
        XCTAssertFalse(removal.remove)
        XCTAssertFalse(removal.start)

        let reset = PortalActionAvailability(context: PortalActionContext(
            loggingPreference: .undecided,
            inputsValid: true,
            helperAvailability: .awaitingLoggingChoice,
            hasTailnetBinding: true,
            portalCount: 0
        ))
        XCTAssertFalse(reset.addPortal)
        XCTAssertTrue(reset.resetTailnet)
        XCTAssertTrue(reset.settings)
        XCTAssertTrue(reset.diagnostics)
    }

    func testActiveAndStoppedPortalsAllowOnlyValidDestinationEdits() {
        for desiredState in [PortalDesiredState.enabled, .stopped] {
            let valid = PortalActionAvailability(context: PortalActionContext(
                loggingPreference: .enabled,
                inputsValid: true,
                helperAvailability: .connected,
                lifecycle: .active,
                desiredState: desiredState,
                isEditedDestinationValid: true
            ))
            XCTAssertTrue(valid.editDestination)

            let invalid = PortalActionAvailability(context: PortalActionContext(
                loggingPreference: .enabled,
                inputsValid: true,
                helperAvailability: .connected,
                lifecycle: .active,
                desiredState: desiredState,
                isEditedDestinationValid: false
            ))
            XCTAssertFalse(invalid.editDestination)
        }
    }
}
