import Foundation
import XCTest
@testable import Portico

@MainActor
final class HelperShutdownTests: XCTestCase {
    func testWaitsForGracefulHelperExitBeforeCompleting() throws {
        let launcher = FakeHelperLauncher()
        var requestIDs = ["handshake-1", "shutdown-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            handshakeTimeout: 1,
            shutdownGraceInterval: 1
        )
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":3,"requestId":"handshake-1","result":{"protocolVersion":3}}"#)
        var completionCount = 0

        supervisor.shutdown { completionCount += 1 }

        XCTAssertEqual(completionCount, 0)
        XCTAssertTrue(launcher.process.inputClosed)
        XCTAssertFalse(launcher.process.terminated)
        let requestData = try XCTUnwrap(launcher.process.sent.last)
        let request = try JSONDecoder().decode(HelperRequest<EmptyPayload>.self, from: requestData)
        XCTAssertEqual(request.requestId, "shutdown-1")
        XCTAssertEqual(request.command, .shutdown)

        launcher.receive(line: #"{"version":3,"requestId":"shutdown-1","result":{"accepted":true}}"#)
        XCTAssertEqual(completionCount, 0)
        launcher.exit(status: 0)

        XCTAssertEqual(completionCount, 1)
        XCTAssertFalse(launcher.process.terminated)
    }

    func testTerminatesOwnedProcessAfterGraceTimeout() async throws {
        let launcher = FakeHelperLauncher()
        var requestIDs = ["handshake-1", "shutdown-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            handshakeTimeout: 1,
            shutdownGraceInterval: 0.01
        )
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":3,"requestId":"handshake-1","result":{"protocolVersion":3}}"#)
        var completionCount = 0

        supervisor.shutdown { completionCount += 1 }
        try await Task.sleep(nanoseconds: 50_000_000)

        XCTAssertTrue(launcher.process.terminated)
        XCTAssertEqual(completionCount, 1)
    }

    func testAlreadyExitedChildCompletesImmediately() {
        let launcher = FakeHelperLauncher()
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { "request-1" }
        )
        supervisor.start(loggingPreference: .enabled)
        launcher.exit(status: 1)
        var completionCount = 0

        supervisor.shutdown { completionCount += 1 }

        XCTAssertEqual(completionCount, 1)
        XCTAssertEqual(launcher.process.sent.count, 1)
    }

    func testRepeatedTerminationRequestsShareOneShutdown() {
        let launcher = FakeHelperLauncher()
        var requestIDs = ["handshake-1", "shutdown-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            shutdownGraceInterval: 1
        )
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":3,"requestId":"handshake-1","result":{"protocolVersion":3}}"#)
        var completionCount = 0

        supervisor.shutdown { completionCount += 1 }
        supervisor.shutdown { completionCount += 1 }

        XCTAssertEqual(launcher.process.sent.count, 2)
        XCTAssertEqual(completionCount, 0)
        launcher.exit(status: 0)
        XCTAssertEqual(completionCount, 2)

        supervisor.shutdown { completionCount += 1 }
        XCTAssertEqual(completionCount, 3)
        XCTAssertEqual(launcher.process.sent.count, 2)
    }
}
