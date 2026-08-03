import Foundation
import XCTest
@testable import Portico

@MainActor
final class PortalControllerTests: XCTestCase {
    private let portalID = UUID(uuidString: "9f55ca93-d7b3-4eab-a871-310ea576005a")!

    func testAddValidatesBeforeCreatingOnePortalUUID() throws {
        let client = FakePortalHelperClient()
        let store = PortalStore(rootURL: temporaryRoot())
        var UUIDCreationCount = 0
        let controller = PortalController(
            store: store,
            helper: client,
            uuidProvider: { UUIDCreationCount += 1; return self.portalID },
            dateProvider: { Date(timeIntervalSince1970: 1_786_000_000) },
            openURL: { _ in XCTFail("unexpected URL") }
        )

        controller.portalName = "Invalid Name"
        controller.localAppPort = "8787"
        controller.addPortal()
        XCTAssertNil(controller.portal)
        XCTAssertEqual(UUIDCreationCount, 0)
        XCTAssertTrue(client.started.isEmpty)

        controller.portalName = "hermes"
        controller.localAppPort = "8787"
        controller.addPortal()
        let portal = try XCTUnwrap(controller.portal)
        XCTAssertEqual(portal.id, portalID)
        XCTAssertEqual(UUIDCreationCount, 1)
        XCTAssertEqual(client.started, [portal])
        XCTAssertEqual(try store.load(), portal)

        controller.addPortal()
        XCTAssertEqual(UUIDCreationCount, 1)
        XCTAssertEqual(client.started.count, 1)
    }

    func testStartsSavedPortalAfterHandshakeWithSameDefinition() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        let saved = PortalConfiguration(id: portalID, name: "hermes", localAppPort: 8787, createdAt: Date(timeIntervalSince1970: 1_786_000_000))
        try store.save(saved)
        let client = FakePortalHelperClient(availability: .connecting)
        let controller = PortalController(store: store, helper: client, openURL: { _ in })

        XCTAssertEqual(controller.portal, saved)
        XCTAssertTrue(client.started.isEmpty)

        client.connect()
        XCTAssertEqual(client.started, [saved])
    }

    func testPresentsOnlyMatchingStructuredStatus() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        let saved = PortalConfiguration(id: portalID, name: "hermes", localAppPort: 8787, createdAt: Date())
        try store.save(saved)
        let client = FakePortalHelperClient()
        let controller = PortalController(store: store, helper: client, openURL: { _ in })
        let status = PortalStatusPayload(
            state: .online,
            stableNodeId: "node-1",
            assignedName: "hermes-1",
            portalURL: URL(string: "https://hermes-1.example.ts.net/"),
            addresses: ["100.64.0.1"]
        )

        client.send(.status(UUID(), status))
        XCTAssertNil(controller.status)
        client.send(.status(portalID, status))
        XCTAssertEqual(controller.status, status)
    }

    func testOpensAuthenticationURLOnlyForPendingExplicitAction() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        let saved = PortalConfiguration(id: portalID, name: "hermes", localAppPort: 8787, createdAt: Date())
        try store.save(saved)
        let client = FakePortalHelperClient()
        var opened: [URL] = []
        let controller = PortalController(store: store, helper: client, openURL: { opened.append($0) })
        let transient = URL(string: "https://login.tailscale.com/a/transient")!

        client.send(.authenticationURL(portalID, transient))
        XCTAssertTrue(opened.isEmpty)

        controller.authenticate()
        XCTAssertEqual(client.authenticated, [portalID])
        client.send(.authenticationURL(UUID(), transient))
        XCTAssertTrue(opened.isEmpty)
        client.send(.authenticationURL(portalID, transient))
        XCTAssertEqual(opened, [transient])

        client.send(.authenticationURL(portalID, URL(string: "https://login.tailscale.com/a/stale")!))
        XCTAssertEqual(opened, [transient])
    }

    private func temporaryRoot() -> URL {
        FileManager.default.temporaryDirectory.appendingPathComponent("PorticoControllerTests-\(UUID().uuidString)", isDirectory: true)
    }
}

@MainActor
final class FakePortalHelperClient: PortalHelperClient {
    var availability: HelperAvailability
    var onConnected: (() -> Void)?
    var onEvent: ((PortalHelperEvent) -> Void)?
    private(set) var started: [PortalConfiguration] = []
    private(set) var authenticated: [UUID] = []

    init(availability: HelperAvailability = .connected) {
        self.availability = availability
    }

    func startPortal(_ portal: PortalConfiguration, completion: @escaping (Result<Void, Error>) -> Void) {
        started.append(portal)
        completion(.success(()))
    }

    func authenticatePortal(id: UUID, completion: @escaping (Result<Void, Error>) -> Void) {
        authenticated.append(id)
        completion(.success(()))
    }

    func connect() {
        availability = .connected
        onConnected?()
    }

    func send(_ event: PortalHelperEvent) { onEvent?(event) }
}
