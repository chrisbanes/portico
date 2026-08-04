import XCTest
@testable import Portico

final class HelperProtocolTests: XCTestCase {
    func testVersionFourReconciliationFixturesRoundTrip() throws {
        let requestFixture = #"{"version":4,"requestId":"reconcile-1","command":"reconcilePortals","payload":{"portals":[{"portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A","portalName":"hermes","destination":{"kind":"localApp","port":8787},"desiredState":"enabled"},{"portalId":"5EA74329-3144-4BA2-925F-138D14D61FCC","portalName":"atlas","destination":{"kind":"remoteApp","scheme":"https","host":"app.example.com","port":443},"desiredState":"stopped"}]}}"#
        let responseFixture = #"{"version":4,"requestId":"reconcile-1","result":{"entries":[{"portalId":"5EA74329-3144-4BA2-925F-138D14D61FCC","outcome":"converged"},{"portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A","outcome":"startFailed"}]}}"#

        try assertRoundTrip(requestFixture, as: HelperRequest<ReconcilePortalsPayload>.self)
        let response = try JSONDecoder().decode(
            HelperResponse<ReconcilePortalsResult>.self,
            from: Data(responseFixture.utf8)
        )

        XCTAssertEqual(
            response.result?.entries,
            [
                ReconcilePortalEntry(
                    portalId: UUID(uuidString: "5ea74329-3144-4ba2-925f-138d14d61fcc")!,
                    outcome: .converged
                ),
                ReconcilePortalEntry(
                    portalId: UUID(uuidString: "9f55ca93-d7b3-4eab-a871-310ea576005a")!,
                    outcome: .startFailed
                ),
            ]
        )
        try assertRoundTrip(responseFixture, as: HelperResponse<ReconcilePortalsResult>.self)
    }

    func testDecodesVersionFourHandshakeResponse() throws {
        let fixture = Data(#"{"version":4,"requestId":"request-1","result":{"protocolVersion":4}}"#.utf8)

        let response = try JSONDecoder().decode(HelperResponse<HandshakeResult>.self, from: fixture)

        XCTAssertEqual(response.version, 4)
        XCTAssertEqual(response.requestId, "request-1")
        XCTAssertEqual(response.result?.protocolVersion, 4)
        XCTAssertNil(response.error)
    }

    func testDecodesStructuredProtocolError() throws {
        let fixture = Data(#"{"version":4,"requestId":"request-2","error":{"code":"unknownCommand","message":"unsupported command"}}"#.utf8)

        let response = try JSONDecoder().decode(HelperResponse<HandshakeResult>.self, from: fixture)

        XCTAssertEqual(response.version, 4)
        XCTAssertEqual(response.requestId, "request-2")
        XCTAssertNil(response.result)
        XCTAssertEqual(response.error, HelperProtocolError(code: "unknownCommand", message: "unsupported command"))
    }

    func testVersionFourPortalFixturesRoundTrip() throws {
        let portalID = UUID(uuidString: "9f55ca93-d7b3-4eab-a871-310ea576005a")!
        try assertRoundTrip(
            #"{"version":4,"requestId":"auth-1","command":"authenticatePortal","payload":{"portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A"}}"#,
            as: HelperRequest<AuthenticatePortalPayload>.self
        )
        try assertRoundTrip(
            #"{"version":4,"requestId":"cleanup-1","command":"cleanupRejectedPortal","payload":{"portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A"}}"#,
            as: HelperRequest<CleanupRejectedPortalPayload>.self
        )
        try assertRoundTrip(
            #"{"version":4,"requestId":"remove-1","command":"removePortal","payload":{"portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A"}}"#,
            as: HelperRequest<RemovePortalPayload>.self
        )
        let statusFixture = #"{"version":4,"event":"portalStatus","portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A","payload":{"state":"online","stableNodeId":"node-1","assignedName":"hermes-1","portalURL":"https:\/\/hermes-1.example.ts.net\/","addresses":["100.64.0.1","fd7a:115c:a1e0::1"],"tailnetName":"opaque-identity-do-not-display","magicDNSSuffix":"example.ts.net"}}"#
        let status = try JSONDecoder().decode(HelperEvent<PortalStatusPayload>.self, from: Data(statusFixture.utf8))
        XCTAssertEqual(status.portalId, portalID)
        XCTAssertEqual(status.payload.state, .online)
        XCTAssertEqual(status.payload.portalURL, URL(string: "https://hermes-1.example.ts.net/"))
        XCTAssertEqual(status.payload.tailnetName, "opaque-identity-do-not-display")
        XCTAssertEqual(status.payload.magicDNSSuffix, "example.ts.net")
        try assertRoundTrip(statusFixture, as: HelperEvent<PortalStatusPayload>.self)

        let authenticationFixture = #"{"version":4,"event":"authenticationURL","portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A","payload":{"url":"https:\/\/login.tailscale.com\/a\/secret"}}"#
        try assertRoundTrip(authenticationFixture, as: HelperEvent<AuthenticationURLPayload>.self)
    }

    func testDecodesVersionFourDiscoverLocalAppsResult() throws {
        let fixture = Data(#"{"version":4,"requestId":"discover-1","result":{"candidates":[{"localAppPort":3000,"processLabel":"node","suggestedPortalName":"hermes"},{"localAppPort":8787,"processLabel":"python3"}]}}"#.utf8)

        let response = try JSONDecoder().decode(HelperResponse<DiscoverLocalAppsResult>.self, from: fixture)

        XCTAssertEqual(
            response.result?.candidates,
            [
                LocalAppCandidatePayload(localAppPort: 3000, processLabel: "node", suggestedPortalName: "hermes"),
                LocalAppCandidatePayload(localAppPort: 8787, processLabel: "python3", suggestedPortalName: nil),
            ]
        )
        XCTAssertNil(response.error)
    }

    private func assertRoundTrip<Value: Codable>(_ fixture: String, as type: Value.Type) throws {
        let decoder = JSONDecoder()
        let value = try decoder.decode(type, from: Data(fixture.utf8))
        let original = try JSONSerialization.jsonObject(with: Data(fixture.utf8)) as! NSDictionary
        let encoded = try JSONSerialization.jsonObject(with: JSONEncoder().encode(value)) as! NSDictionary
        XCTAssertEqual(encoded, original)
    }
}
