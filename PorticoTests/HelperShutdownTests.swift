import Foundation
import XCTest
@testable import PorticoApplication

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
        launcher.receive(line: #"{"version":5,"requestId":"handshake-1","result":{"protocolVersion":5}}"#)
        var completionCount = 0

        supervisor.shutdown { completionCount += 1 }

        XCTAssertEqual(completionCount, 0)
        XCTAssertTrue(launcher.process.inputClosed)
        XCTAssertFalse(launcher.process.terminated)
        let requestData = try XCTUnwrap(launcher.process.sent.last)
        let request = try JSONDecoder().decode(HelperRequest<EmptyPayload>.self, from: requestData)
        XCTAssertEqual(request.requestId, "shutdown-1")
        XCTAssertEqual(request.command, .shutdown)

        launcher.receive(line: #"{"version":5,"requestId":"shutdown-1","result":{"accepted":true}}"#)
        XCTAssertEqual(completionCount, 0)
        launcher.exit(status: 0)

        XCTAssertEqual(completionCount, 1)
        XCTAssertFalse(launcher.process.terminated)
    }

    func testEscalatesGracefulShutdownToTERMThenKILLAndWaitsForConfirmedExit() throws {
        let launcher = FakeHelperLauncher()
        let scheduler = FakePorticoScheduler()
        var requestIDs = ["handshake-1", "shutdown-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            scheduler: scheduler,
            handshakeTimeout: 60
        )
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":5,"requestId":"handshake-1","result":{"protocolVersion":5}}"#)
        var completionCount = 0

        supervisor.shutdown { completionCount += 1 }
        scheduler.run(delay: 5)

        XCTAssertTrue(launcher.process.terminated)
        XCTAssertFalse(launcher.process.killed)
        XCTAssertEqual(completionCount, 0)
        scheduler.run(delay: 2)

        XCTAssertTrue(launcher.process.killed)
        XCTAssertEqual(completionCount, 0)
        launcher.exit(status: 0)

        XCTAssertEqual(completionCount, 1)
    }

    func testUnconfirmedKillReportsTerminalOwnershipFailureWithoutReplacingChild() throws {
        let launcher = FakeHelperLauncher()
        let scheduler = FakePorticoScheduler()
        var requestIDs = ["handshake-1", "shutdown-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            scheduler: scheduler,
            handshakeTimeout: 60
        )
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":5,"requestId":"handshake-1","result":{"protocolVersion":5}}"#)
        var completionCount = 0

        supervisor.shutdown { completionCount += 1 }
        scheduler.run(delay: 5)
        scheduler.run(delay: 2)
        scheduler.run(delay: 1)

        XCTAssertEqual(supervisor.availability, .ownershipFailure)
        XCTAssertEqual(completionCount, 1)
        XCTAssertEqual(launcher.processes.count, 1)
        supervisor.restart(loggingPreference: .disabled)
        XCTAssertEqual(launcher.processes.count, 1)
    }

    func testShutdownCompletesImmediatelyFromExistingTerminalOwnershipFailure() {
        let launcher = FakeHelperLauncher()
        let scheduler = FakePorticoScheduler()
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { "handshake-1" },
            scheduler: scheduler,
            handshakeTimeout: 3
        )
        supervisor.start(loggingPreference: .enabled)
        scheduler.run(delay: 3)
        scheduler.run(delay: 2)
        scheduler.run(delay: 1)
        XCTAssertEqual(supervisor.availability, .ownershipFailure)

        var completionCount = 0
        supervisor.shutdown { completionCount += 1 }

        XCTAssertEqual(completionCount, 1)
        XCTAssertEqual(supervisor.availability, .ownershipFailure)
        XCTAssertEqual(launcher.processes.count, 1)

        supervisor.shutdown { completionCount += 1 }

        XCTAssertEqual(completionCount, 2)
        XCTAssertEqual(launcher.processes.count, 1)
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
        launcher.receive(line: #"{"version":5,"requestId":"handshake-1","result":{"protocolVersion":5}}"#)
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

    func testShutdownCompletesPendingRequestsWithGenerationLossExactlyOnce() {
        let launcher = FakeHelperLauncher()
        var requestIDs = ["handshake-1", "discover-1", "shutdown-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            handshakeTimeout: 60
        )
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":5,"requestId":"handshake-1","result":{"protocolVersion":5}}"#)
        var results: [Result<[LocalAppCandidatePayload], Error>] = []

        supervisor.discoverLocalApps { results.append($0) }
        supervisor.shutdown {}
        launcher.exit(status: 0)

        XCTAssertEqual(results.count, 1)
        guard case .failure(HelperClientError.generationLost) = results[0] else {
            return XCTFail("expected pending request generation loss")
        }
    }

    func testShutdownCompletesAtObservedExitDuringControlledRestart() {
        let launcher = FakeHelperLauncher()
        var requestIDs = ["handshake-1", "shutdown-1", "shutdown-2"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            handshakeTimeout: 60
        )
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":5,"requestId":"handshake-1","result":{"protocolVersion":5}}"#)
        supervisor.restart(loggingPreference: .disabled)
        var completionCount = 0

        supervisor.shutdown { completionCount += 1 }
        launcher.exit(status: 0)

        XCTAssertEqual(completionCount, 1)
        XCTAssertEqual(launcher.processes.count, 1)
    }
}
