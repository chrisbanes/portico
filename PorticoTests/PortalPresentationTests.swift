import XCTest
@testable import PorticoApplication

final class PortalPresentationTests: XCTestCase {
    func testFirstRunGuidance() {
        XCTAssertEqual(
            PortalPresentation.prerequisiteGuidance,
            [
                "Enable MagicDNS for your tailnet.",
                "Enable HTTPS certificates for your tailnet.",
                "Approve the Portal device when your tailnet requires device approval.",
                "Portal names are published to Certificate Transparency logs when certificates are issued.",
            ]
        )
        XCTAssertTrue(PortalPresentation.showsPrerequisiteGuidance(portalCount: 0))
        XCTAssertFalse(PortalPresentation.showsPrerequisiteGuidance(portalCount: 1))
    }

    func testPortalIdentityStatesCollisionAndLastKnownURLRemainSeparate() {
        let portal = PortalConfiguration(
            id: UUID(uuidString: "9F55CA93-D7B3-4EAB-A871-310EA576005A")!,
            name: "hermes",
            localAppPort: 8787,
            createdAt: Date(),
            desiredState: .enabled
        )
        let status = PortalStatusPayload(
            state: .online,
            stableNodeId: nil,
            assignedName: "hermes-1",
            portalURL: URL(string: "https://hermes-1.example.ts.net/"),
            addresses: [],
            publicAccessStatus: .off,
            magicDNSSuffix: "example.ts.net"
        )

        let presentation = PortalPresentation(
            portal: portal,
            status: status,
            reachability: .unavailable,
            isStale: true
        )

        XCTAssertEqual(presentation.portalName, "hermes")
        XCTAssertEqual(presentation.assignedName, "hermes-1")
        XCTAssertEqual(presentation.desiredState, "Enabled")
        XCTAssertEqual(presentation.tailscaleState, "Online — Last Known")
        XCTAssertEqual(presentation.localAppReachability, "Unavailable")
        XCTAssertEqual(presentation.portalURLLabel, "Portal URL — Last Known")
        XCTAssertEqual(
            presentation.collisionExplanation,
            "Tailscale assigned “hermes-1” because “hermes” was unavailable. Portal Name remains “hermes”."
        )
    }

    func testOnlineRemoteAppHasOnlyFixedPortalPresentation() {
        let portal = PortalConfiguration(
            id: UUID(uuidString: "9F55CA93-D7B3-4EAB-A871-310EA576005A")!,
            name: "hermes",
            destination: .remoteApp(scheme: .https, host: "app.example.com", port: 443),
            createdAt: Date(),
            desiredState: .enabled
        )
        let status = PortalStatusPayload(
            state: .online,
            stableNodeId: nil,
            assignedName: "hermes",
            portalURL: URL(string: "https://hermes.example.ts.net/"),
            addresses: [],
            publicAccessStatus: .off,
            magicDNSSuffix: "example.ts.net"
        )

        let presentation = PortalPresentation(
            portal: portal,
            status: status,
            reachability: .unknown,
            isStale: false
        )

        XCTAssertNil(portal.localAppPort)
        XCTAssertEqual(presentation.portalName, "hermes")
        XCTAssertEqual(presentation.assignedName, "hermes")
        XCTAssertEqual(presentation.desiredState, "Enabled")
        XCTAssertEqual(presentation.tailscaleState, "Online")
        XCTAssertEqual(presentation.portalURLLabel, "Portal URL")
        XCTAssertNil(presentation.collisionExplanation)
    }

    func testPublicAccessPresentationUsesOnlyStoredModeAndStructuredStatus() {
        let states: [PortalTailscaleState] = [.online, .connecting, .error]
        let reachabilities: [LocalAppReachabilityState] = [.reachable, .unavailable]
        let configurations: [(PortalPublicAccess, PortalStatusPayload?, Bool, String)] = [
            (.private, nil, false, "Private — Off"),
            (.private, nil, true, "Private — Off"),
            (.private, PortalStatusPayload(
                state: .error,
                stableNodeId: nil,
                assignedName: nil,
                portalURL: nil,
                addresses: [],
                publicAccessStatus: .blocked
            ), true, "Private — Off"),
            (.public, nil, false, "Public — Status unavailable"),
            (.public, nil, true, "Public — Status unavailable"),
            (.public, PortalStatusPayload(
                state: .online,
                stableNodeId: nil,
                assignedName: nil,
                portalURL: nil,
                addresses: [],
                publicAccessStatus: .off
            ), false, "Public — Off"),
            (.public, PortalStatusPayload(
                state: .connecting,
                stableNodeId: nil,
                assignedName: nil,
                portalURL: nil,
                addresses: [],
                publicAccessStatus: .off
            ), true, "Public — Off — Last Known"),
        ]

        for desiredState in [PortalDesiredState.enabled, .stopped] {
            for state in states {
                for reachability in reachabilities {
                    for (publicAccess, status, isStale, expected) in configurations {
                        let portal = PortalConfiguration(
                            id: UUID(),
                            name: "hermes",
                            destination: .localApp(port: 8787),
                            createdAt: Date(),
                            desiredState: desiredState,
                            publicAccess: publicAccess
                        )
                        let stateStatus = status.map { status in
                            PortalStatusPayload(
                                state: state,
                                stableNodeId: status.stableNodeId,
                                assignedName: status.assignedName,
                                portalURL: status.portalURL,
                                addresses: status.addresses,
                                publicAccessStatus: status.publicAccessStatus
                            )
                        }
                        let presentation = PortalPresentation(
                            portal: portal,
                            status: stateStatus,
                            reachability: reachability,
                            isStale: isStale
                        )

                        XCTAssertEqual(presentation.publicAccessState, expected)
                    }
                }
            }
        }
    }

    func testAnnouncementsAreFixedAndContainNoRuntimeFacts() {
        XCTAssertEqual(PorticoAnnouncement.text(for: .helperConnected), "Helper connected.")
        XCTAssertEqual(PorticoAnnouncement.text(for: .helperTerminalFailure), "Helper unavailable.")
        XCTAssertEqual(PorticoAnnouncement.text(for: .portalOnline), "Portal online.")
        XCTAssertEqual(PorticoAnnouncement.text(for: .removalSucceeded), "Portal removal completed.")
        XCTAssertEqual(PorticoAnnouncement.text(for: .removalFailed), "Portal removal failed.")
        XCTAssertEqual(PorticoAnnouncement.text(for: .preferenceRestartCompleted), "Logging preference restart completed.")
        XCTAssertEqual(PorticoAnnouncement.text(for: .preferenceRestartFailed), "Logging preference restart failed.")
    }

    func testUnavailableInstallationOverridesConnectingHelperPresentation() {
        let presentation = HelperStatusPresentation(
            isInstallationAvailable: false,
            helperAvailability: .connecting
        )

        XCTAssertEqual(presentation.title, "Saved configuration unavailable")
        XCTAssertEqual(presentation.symbolName, "exclamationmark.triangle")
    }

    func testProtocolMismatchHasSafeHelperPresentation() {
        let presentation = HelperStatusPresentation(
            isInstallationAvailable: true,
            helperAvailability: .protocolMismatch
        )

        XCTAssertEqual(presentation.title, "Helper protocol mismatch")
        XCTAssertEqual(presentation.symbolName, "exclamationmark.triangle")
    }

    func testUnavailableInstallationOverridesOwnershipFailurePresentation() {
        let presentation = HelperStatusPresentation(
            isInstallationAvailable: false,
            helperAvailability: .ownershipFailure
        )

        XCTAssertEqual(presentation.title, "Saved configuration unavailable")
        XCTAssertEqual(presentation.symbolName, "exclamationmark.triangle")
    }

    func testOwnershipFailureHasSafeHelperPresentation() {
        let presentation = HelperStatusPresentation(
            isInstallationAvailable: true,
            helperAvailability: .ownershipFailure
        )

        XCTAssertEqual(presentation.title, "Helper ownership failure")
        XCTAssertEqual(presentation.symbolName, "exclamationmark.triangle")
    }
}
