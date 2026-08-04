import XCTest
@testable import Portico

final class DiagnosticsTests: XCTestCase {
    func testHistoryEvictsOldestAndReportContainsOnlyAllowlistedFacts() {
        var nextTimestamp: TimeInterval = 0
        let history = DiagnosticHistory(dateProvider: {
            defer { nextTimestamp += 1 }
            return Date(timeIntervalSince1970: nextTimestamp)
        })
        history.record(.helper(.failed))
        for _ in 0..<200 {
            history.record(.portal(
                name: "hermes",
                desired: .enabled,
                tailscale: .online,
                reachability: .reachable,
                stale: false
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
}
