import XCTest
@testable import Portico

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

    func testAnnouncementsAreFixedAndContainNoRuntimeFacts() {
        XCTAssertEqual(PorticoAnnouncement.text(for: .helperConnected), "Helper connected.")
        XCTAssertEqual(PorticoAnnouncement.text(for: .helperTerminalFailure), "Helper unavailable.")
        XCTAssertEqual(PorticoAnnouncement.text(for: .portalOnline), "Portal online.")
        XCTAssertEqual(PorticoAnnouncement.text(for: .removalSucceeded), "Portal removal completed.")
        XCTAssertEqual(PorticoAnnouncement.text(for: .removalFailed), "Portal removal failed.")
        XCTAssertEqual(PorticoAnnouncement.text(for: .preferenceRestartCompleted), "Logging preference restart completed.")
        XCTAssertEqual(PorticoAnnouncement.text(for: .preferenceRestartFailed), "Logging preference restart failed.")
    }
}
