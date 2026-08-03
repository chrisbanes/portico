import Foundation
import XCTest
@testable import Portico

@MainActor
final class HelperSupervisorTests: XCTestCase {
    func testConnectsOnlyForCorrelatedHandshake() throws {
        let launcher = FakeHelperLauncher()
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { "request-1" },
            handshakeTimeout: 1
        )

        supervisor.start()

        XCTAssertEqual(supervisor.availability, .connecting)
        let requestData = try XCTUnwrap(launcher.process.sent.first)
        let request = try JSONDecoder().decode(HelperRequest<EmptyPayload>.self, from: requestData)
        XCTAssertEqual(request.version, 1)
        XCTAssertEqual(request.requestId, "request-1")
        XCTAssertEqual(request.command, .handshake)

        launcher.receive(line: #"{"version":1,"requestId":"other","result":{"protocolVersion":1}}"#)
        XCTAssertEqual(supervisor.availability, .connecting)

        launcher.receive(line: #"{"version":1,"requestId":"request-1","result":{"protocolVersion":1}}"#)
        XCTAssertEqual(supervisor.availability, .connected)
    }

    func testHandshakeTimeoutFailsAndTerminatesOwnedProcess() async throws {
        let launcher = FakeHelperLauncher()
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { "request-1" },
            handshakeTimeout: 0.01
        )

        supervisor.start()
        try await Task.sleep(nanoseconds: 50_000_000)

        XCTAssertEqual(supervisor.availability, .failed)
        XCTAssertTrue(launcher.process.inputClosed)
        XCTAssertTrue(launcher.process.terminated)
    }

    func testHandshakeFailuresBecomeUnavailable() {
        let failures: [(String, (FakeHelperLauncher) -> Void)] = [
            ("malformed line", { $0.receive(line: "{") }),
            ("correlated error", { $0.receive(line: #"{"version":1,"requestId":"request-1","error":{"code":"unsupportedVersion","message":"unsupported protocol version"}}"#) }),
            ("unsupported response version", { $0.receive(line: #"{"version":2,"requestId":"request-1","result":{"protocolVersion":1}}"#) }),
            ("EOF", { $0.receiveEOF() }),
            ("nonzero exit", { $0.exit(status: 1) }),
        ]

        for (name, trigger) in failures {
            let launcher = FakeHelperLauncher()
            let supervisor = HelperSupervisor(
                helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
                launcher: launcher,
                requestIDProvider: { "request-1" },
                handshakeTimeout: 1
            )
            supervisor.start()

            trigger(launcher)

            XCTAssertEqual(supervisor.availability, .failed, name)
            XCTAssertTrue(launcher.process.inputClosed, name)
        }
    }

    func testRoutesCorrelatedPortalResponseAndAsynchronousEvent() throws {
        let launcher = FakeHelperLauncher()
        var requestIDs = ["handshake-1", "start-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            stateRootURL: URL(fileURLWithPath: "/trusted/tsnet"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            handshakeTimeout: 1
        )
        var events: [PortalHelperEvent] = []
        supervisor.onEvent = { events.append($0) }
        supervisor.start()
        XCTAssertEqual(launcher.arguments, ["--state-root", "/trusted/tsnet"])
        launcher.receive(line: #"{"version":1,"requestId":"handshake-1","result":{"protocolVersion":1}}"#)
        let portal = PortalConfiguration(
            id: UUID(uuidString: "9f55ca93-d7b3-4eab-a871-310ea576005a")!,
            name: "hermes",
            localAppPort: 8787,
            createdAt: Date()
        )
        var result: Result<Void, Error>?

        supervisor.startPortal(portal) { result = $0 }
        launcher.receive(line: #"{"version":1,"event":"portalStatus","portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A","payload":{"state":"connecting","addresses":[]}}"#)
        XCTAssertEqual(events, [.status(portal.id, PortalStatusPayload(state: .connecting, stableNodeId: nil, assignedName: nil, portalURL: nil, addresses: []))])
        XCTAssertNil(result)
        launcher.receive(line: #"{"version":1,"requestId":"start-1","result":{"accepted":true}}"#)
        XCTAssertNoThrow(try result?.get())
    }
}

final class FakeHelperLauncher: HelperLaunching {
    let process = FakeHelperProcess()
    private var onLine: ((Data) -> Void)?
    private var onEOF: (() -> Void)?
    private var onExit: ((Int32) -> Void)?
    private(set) var arguments: [String] = []

    func launch(
        at executableURL: URL,
        arguments: [String],
        onLine: @escaping (Data) -> Void,
        onEOF: @escaping () -> Void,
        onExit: @escaping (Int32) -> Void
    ) throws -> HelperProcess {
        self.arguments = arguments
        self.onLine = onLine
        self.onEOF = onEOF
        self.onExit = onExit
        return process
    }

    func receive(line: String) {
        onLine?(Data(line.utf8))
    }

    func receiveEOF() {
        onEOF?()
    }

    func exit(status: Int32) {
        process.isRunning = false
        onExit?(status)
    }
}

final class FakeHelperProcess: HelperProcess {
    var isRunning = true
    var sent: [Data] = []
    private(set) var inputClosed = false
    private(set) var terminated = false

    func send(_ data: Data) throws {
        sent.append(data)
    }

    func closeInput() {
        inputClosed = true
    }

    func terminate() {
        terminated = true
        isRunning = false
    }
}
