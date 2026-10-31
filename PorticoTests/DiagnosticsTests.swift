import XCTest
@testable import PorticoApplication

final class DiagnosticsTests: XCTestCase {
    func testHistoryEvictsOldestAndReportContainsOnlyAllowlistedFacts() {
        var nextTimestamp: TimeInterval = 0
        let history = DiagnosticHistory(dateProvider: {
            defer { nextTimestamp += 1 }
            return Date(timeIntervalSince1970: nextTimestamp)
        })
        history.record(.helper(.failed))
        for index in 0..<200 {
            history.record(.portal(
                name: "hermes",
                desired: .enabled,
                tailscale: .online,
                reachability: .reachable,
                stale: index.isMultiple(of: 2)
            ))
        }

        XCTAssertEqual(history.entries.count, 200)
        XCTAssertEqual(history.entries.first?.timestamp, Date(timeIntervalSince1970: 1))
        XCTAssertEqual(history.entries.last?.timestamp, Date(timeIntervalSince1970: 200))

        let portal = PortalConfiguration(
            id: UUID(uuidString: "11111111-1111-1111-1111-111111111111")!,
            name: "hermes",
            localAppPort: 8787,
            createdAt: Date()
        )
        let facts = PortalDiagnosticFacts(
            portalName: "hermes",
            assignedName: "hermes-1",
            portalURL: URL(string: "https://hermes-1.example.ts.net/"),
            addresses: ["100.64.0.1"],
            magicDNSSuffix: "example.ts.net",
            desiredState: portal.desiredState,
            tailscaleState: .online,
            reachability: .reachable,
            isStale: true
        )
        let report = DiagnosticReportRenderer.render(
            versions: DiagnosticVersions(porticoShort: "1.2", porticoBuild: "34", helperProtocol: 3),
            helper: .failed,
            isInstallationAvailable: true,
            portals: [facts],
            history: history.entries
        )

        for value in [
            "Portico 1.2 (34)", "Helper protocol 3", "hermes", "hermes-1",
            "https://hermes-1.example.ts.net/", "100.64.0.1", "example.ts.net",
            "Desired: enabled", "Tailscale: online", "Reachability: reachable", "Facts: stale",
            "Helper: unavailable",
        ] {
            XCTAssertTrue(report.contains(value), value)
        }
        for excluded in [
            portal.id.uuidString, "secret-stable-node-id", "secret-opaque-tailnet",
            "https://login.tailscale.com/a/secret", "/Users/chris/private",
            "Authorization: Bearer secret", "Cookie: secret", "--secret-argument", "request-body-secret",
        ] {
            XCTAssertFalse(report.contains(excluded), excluded)
        }
    }

    func testRecordingTheSamePortalEventTwiceKeepsOneEntry() {
        let history = DiagnosticHistory()

        history.record(portalEvent("hermes"))
        history.record(portalEvent("hermes"))

        XCTAssertEqual(history.entries.count, 1)
    }

    func testInterleavedUnchangedPortalEventsKeepOneEntryPerPortalUntilOneChanges() {
        let history = DiagnosticHistory()

        for name in ["a", "b", "a", "b"] {
            history.record(portalEvent(name))
        }
        XCTAssertEqual(history.entries.count, 2)

        history.record(portalEvent("a", tailscale: .stopped))
        XCTAssertEqual(history.entries.count, 3)
        XCTAssertEqual(history.entries.last?.event, portalEvent("a", tailscale: .stopped))
    }

    func testHelperEventsStillRecordAroundUnchangedPortalEvents() {
        let history = DiagnosticHistory()

        history.record(portalEvent("hermes"))
        history.record(.helper(.failed))
        history.record(portalEvent("hermes"))
        history.record(.helper(.failed))

        XCTAssertEqual(history.entries.map(\.event), [
            portalEvent("hermes"), .helper(.failed), .helper(.failed),
        ])
    }

    private func portalEvent(
        _ name: String,
        tailscale: PortalTailscaleState = .online
    ) -> DiagnosticEvent {
        .portal(
            name: name,
            desired: .enabled,
            tailscale: tailscale,
            reachability: .reachable,
            stale: false
        )
    }

    func testUnavailableInstallationReportSuppressesHelperAndPortalFacts() {
        let portal = PortalConfiguration(
            id: UUID(uuidString: "11111111-1111-1111-1111-111111111111")!,
            name: "hermes",
            localAppPort: 8787,
            createdAt: Date()
        )
        let report = DiagnosticReportRenderer.render(
            versions: DiagnosticVersions(porticoShort: "1.2", porticoBuild: "34", helperProtocol: 3),
            helper: .connecting,
            isInstallationAvailable: false,
            portals: [PortalDiagnosticFacts(
                portalName: portal.name,
                assignedName: "hermes-1",
                portalURL: URL(string: "https://hermes-1.example.ts.net/"),
                addresses: ["100.64.0.1"],
                magicDNSSuffix: "example.ts.net",
                desiredState: portal.desiredState,
                tailscaleState: .online,
                reachability: .reachable,
                isStale: false
            )],
            history: []
        )

        XCTAssertTrue(report.contains("Helper: saved configuration unavailable"))
        XCTAssertTrue(report.contains("Portals: unavailable"))
        for excluded in ["Helper: connecting", "hermes", "100.64.0.1", "example.ts.net"] {
            XCTAssertFalse(report.contains(excluded), excluded)
        }
    }

    func testProtocolMismatchDiagnosticIsSanitized() {
        let report = DiagnosticReportRenderer.render(
            versions: DiagnosticVersions(porticoShort: nil, porticoBuild: nil, helperProtocol: 4),
            helper: .protocolMismatch,
            isInstallationAvailable: true,
            portals: [],
            history: [.init(timestamp: Date(timeIntervalSince1970: 0), event: .helper(.protocolMismatch))]
        )

        XCTAssertTrue(report.contains("Helper: protocol mismatch"))
        XCTAssertFalse(report.contains("request-1"))
    }

    func testOwnershipFailureDiagnosticIsSanitized() {
        let report = DiagnosticReportRenderer.render(
            versions: DiagnosticVersions(porticoShort: nil, porticoBuild: nil, helperProtocol: 4),
            helper: .ownershipFailure,
            isInstallationAvailable: true,
            portals: [],
            history: [.init(timestamp: Date(timeIntervalSince1970: 0), event: .helper(.ownershipFailure))]
        )

        XCTAssertTrue(report.contains("Helper: ownership failure"))
        XCTAssertFalse(report.contains("/Applications/Portico.app"))
    }

    func testDiagnosticReportUsesTheSelectedAppDisplayName() {
        let report = DiagnosticReportRenderer.render(
            versions: DiagnosticVersions(
                appName: "Portico Dev",
                porticoShort: "1.2",
                porticoBuild: "34",
                helperProtocol: 5
            ),
            helper: .connected,
            isInstallationAvailable: true,
            portals: [],
            history: []
        )

        XCTAssertTrue(report.contains("Portico Dev 1.2 (34)"))
        XCTAssertFalse(report.contains("Portico 1.2 (34)"))
    }
}
