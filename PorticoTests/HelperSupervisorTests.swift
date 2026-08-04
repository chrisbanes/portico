import Foundation
import XCTest
@testable import Portico

@MainActor
final class HelperSupervisorTests: XCTestCase {
    func testRetriesUnexpectedExitWithOneChildAndFixedSharedBudget() {
        let launcher = FakeHelperLauncher()
        let scheduler = FakePorticoScheduler()
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { UUID().uuidString },
            scheduler: scheduler,
            handshakeTimeout: 60
        )

        supervisor.start()
        for expectedDelay in [1.0, 2.0, 4.0, 8.0, 16.0] {
            launcher.exit(status: 1)

            XCTAssertEqual(
                supervisor.availability,
                .retrying(attempt: launcher.processes.count, delay: expectedDelay)
            )
            XCTAssertEqual(scheduler.pendingDelays, [expectedDelay])
            XCTAssertLessThanOrEqual(launcher.processes.filter(\.isRunning).count, 0)

            scheduler.runNext()
            XCTAssertEqual(supervisor.availability, .connecting)
            XCTAssertEqual(launcher.processes.filter(\.isRunning).count, 1)
        }

        launcher.exit(status: 1)

        XCTAssertEqual(supervisor.availability, .failed)
        XCTAssertTrue(scheduler.pendingDelays.isEmpty)
        XCTAssertEqual(scheduler.recordedDelays.filter { $0 < 60 }, [1, 2, 4, 8, 16])
        XCTAssertEqual(launcher.processes.count, 6)

        supervisor.retry()

        XCTAssertEqual(supervisor.availability, .connecting)
        XCTAssertEqual(launcher.processes.count, 7)
        XCTAssertEqual(launcher.processes.filter(\.isRunning).count, 1)
    }

    func testRetryBudgetResetsOnlyAfterLatestConvergenceStaysConnectedForFiveMinutes() {
        let launcher = FakeHelperLauncher()
        let scheduler = FakePorticoScheduler()
        var requestIDs = ["handshake-1", "handshake-2", "reconcile-2", "handshake-3", "reconcile-3"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            scheduler: scheduler,
            handshakeTimeout: 60
        )

        supervisor.start()
        launcher.exit(status: 1)
        scheduler.runNext()
        launcher.receive(line: #"{"version":3,"requestId":"handshake-2","result":{"protocolVersion":3}}"#)
        supervisor.reconcilePortals([]) { _ in }
        launcher.receive(line: #"{"version":3,"requestId":"reconcile-2","result":{"entries":[]}}"#)

        XCTAssertTrue(scheduler.pendingDelays.contains(300))
        launcher.exit(status: 1)
        XCTAssertEqual(supervisor.availability, .retrying(attempt: 2, delay: 2))

        scheduler.run(delay: 2)
        launcher.receive(line: #"{"version":3,"requestId":"handshake-3","result":{"protocolVersion":3}}"#)
        supervisor.reconcilePortals([]) { _ in }
        launcher.receive(line: #"{"version":3,"requestId":"reconcile-3","result":{"entries":[]}}"#)
        scheduler.run(delay: 300)
        launcher.exit(status: 1)

        XCTAssertEqual(supervisor.availability, .retrying(attempt: 1, delay: 1))
    }

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
        XCTAssertEqual(request.version, 3)
        XCTAssertEqual(request.requestId, "request-1")
        XCTAssertEqual(request.command, .handshake)

        launcher.receive(line: #"{"version":3,"requestId":"other","result":{"protocolVersion":3}}"#)
        XCTAssertEqual(supervisor.availability, .connecting)

        launcher.receive(line: #"{"version":3,"requestId":"request-1","result":{"protocolVersion":3}}"#)
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
        launcher.exit(status: 1)

        XCTAssertEqual(supervisor.availability, .retrying(attempt: 1, delay: 1))
        XCTAssertTrue(launcher.process.inputClosed)
        XCTAssertTrue(launcher.process.terminated)
    }

    func testHandshakeFailuresEnterSharedRecoveryBudgetAfterChildExit() {
        let failures: [(String, Bool, (FakeHelperLauncher) -> Void)] = [
            ("malformed line", false, { $0.receive(line: "{") }),
            ("correlated error", false, { $0.receive(line: #"{"version":3,"requestId":"request-1","error":{"code":"unsupportedVersion","message":"unsupported protocol version"}}"#) }),
            ("unsupported response version", false, { $0.receive(line: #"{"version":1,"requestId":"request-1","result":{"protocolVersion":1}}"#) }),
            ("EOF", false, { $0.receiveEOF() }),
            ("nonzero exit", true, { $0.exit(status: 1) }),
        ]

        for (name, alreadyExited, trigger) in failures {
            let launcher = FakeHelperLauncher()
            let supervisor = HelperSupervisor(
                helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
                launcher: launcher,
                requestIDProvider: { "request-1" },
                handshakeTimeout: 1
            )
            supervisor.start()

            trigger(launcher)
            if !alreadyExited {
                launcher.exit(status: 1)
            }

            XCTAssertEqual(supervisor.availability, .retrying(attempt: 1, delay: 1), name)
            if !alreadyExited {
                XCTAssertTrue(launcher.process.inputClosed, name)
            }
        }
    }

    func testRoutesCorrelatedReconciliationResponseAndAsynchronousEvent() throws {
        let launcher = FakeHelperLauncher()
        var requestIDs = ["handshake-1", "reconcile-1"]
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
        launcher.receive(line: #"{"version":3,"requestId":"handshake-1","result":{"protocolVersion":3}}"#)
        let laterPortal = PortalConfiguration(
            id: UUID(uuidString: "9f55ca93-d7b3-4eab-a871-310ea576005a")!,
            name: "hermes",
            localAppPort: 8787,
            createdAt: Date()
        )
        let earlierPortal = PortalConfiguration(
            id: UUID(uuidString: "5ea74329-3144-4ba2-925f-138d14d61fcc")!,
            name: "atlas",
            localAppPort: 8788,
            createdAt: Date(),
            desiredState: .stopped
        )
        var result: Result<ReconcilePortalsResult, Error>?

        supervisor.reconcilePortals([laterPortal, earlierPortal]) { result = $0 }

        let requestData = try XCTUnwrap(launcher.process.sent.last)
        let request = try JSONDecoder().decode(HelperRequest<ReconcilePortalsPayload>.self, from: requestData)
        XCTAssertEqual(request.command, .reconcilePortals)
        XCTAssertEqual(request.requestId, "reconcile-1")
        XCTAssertEqual(
            request.payload.portals,
            [
                ReconcilePortalPayload(
                    portalId: earlierPortal.id,
                    portalName: "atlas",
                    localAppPort: 8788,
                    desiredState: .stopped
                ),
                ReconcilePortalPayload(
                    portalId: laterPortal.id,
                    portalName: "hermes",
                    localAppPort: 8787,
                    desiredState: .enabled
                ),
            ]
        )
        launcher.receive(line: #"{"version":3,"event":"portalStatus","portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A","payload":{"state":"connecting","addresses":[]}}"#)
        XCTAssertEqual(events, [.status(laterPortal.id, PortalStatusPayload(state: .connecting, stableNodeId: nil, assignedName: nil, portalURL: nil, addresses: []))])
        XCTAssertNil(result)
        launcher.receive(line: #"{"version":3,"requestId":"reconcile-1","result":{"entries":[{"portalId":"5EA74329-3144-4BA2-925F-138D14D61FCC","outcome":"converged"},{"portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A","outcome":"startFailed"}]}}"#)
        XCTAssertEqual(
            try result?.get().entries,
            [
                ReconcilePortalEntry(portalId: earlierPortal.id, outcome: .converged),
                ReconcilePortalEntry(portalId: laterPortal.id, outcome: .startFailed),
            ]
        )
    }

    func testConnectionLossFailsUnresolvedReconciliation() {
        let launcher = FakeHelperLauncher()
        var requestIDs = ["handshake-1", "reconcile-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            handshakeTimeout: 1
        )
        supervisor.start()
        launcher.receive(line: #"{"version":3,"requestId":"handshake-1","result":{"protocolVersion":3}}"#)
        var result: Result<ReconcilePortalsResult, Error>?

        supervisor.reconcilePortals([]) { result = $0 }
        launcher.receiveEOF()

        guard case .failure(HelperClientError.protocolFailure) = result else {
            return XCTFail("expected unresolved reconciliation to fail")
        }
    }

    func testRequestsAndCorrelatesLocalAppDiscovery() throws {
        let launcher = FakeHelperLauncher()
        var requestIDs = ["handshake-1", "discover-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            handshakeTimeout: 1
        )
        supervisor.start()
        launcher.receive(line: #"{"version":3,"requestId":"handshake-1","result":{"protocolVersion":3}}"#)
        var result: Result<[LocalAppCandidatePayload], Error>?

        supervisor.discoverLocalApps { result = $0 }

        let requestData = try XCTUnwrap(launcher.process.sent.last)
        let request = try JSONDecoder().decode(HelperRequest<EmptyPayload>.self, from: requestData)
        XCTAssertEqual(request.command, .discoverLocalApps)
        XCTAssertEqual(request.requestId, "discover-1")
        launcher.receive(line: #"{"version":3,"requestId":"discover-1","result":{"candidates":[{"localAppPort":3000,"processLabel":"node","suggestedPortalName":"hermes"}]}}"#)
        XCTAssertEqual(
            try result?.get(),
            [LocalAppCandidatePayload(localAppPort: 3000, processLabel: "node", suggestedPortalName: "hermes")]
        )
    }

    func testRequestsAndCorrelatesRejectedPortalCleanup() throws {
        let launcher = FakeHelperLauncher()
        var requestIDs = ["handshake-1", "cleanup-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            handshakeTimeout: 1
        )
        supervisor.start()
        launcher.receive(line: #"{"version":3,"requestId":"handshake-1","result":{"protocolVersion":3}}"#)
        let portalID = UUID(uuidString: "9f55ca93-d7b3-4eab-a871-310ea576005a")!
        var result: Result<Void, Error>?

        supervisor.cleanupRejectedPortal(id: portalID) { result = $0 }

        let requestData = try XCTUnwrap(launcher.process.sent.last)
        let request = try JSONDecoder().decode(HelperRequest<CleanupRejectedPortalPayload>.self, from: requestData)
        XCTAssertEqual(request.command, .cleanupRejectedPortal)
        XCTAssertEqual(request.payload.portalId, portalID)
        XCTAssertNil(result)
        launcher.receive(line: #"{"version":3,"requestId":"cleanup-1","result":{"accepted":true}}"#)
        XCTAssertNoThrow(try result?.get())
    }

    func testRemovalRequestIsUUIDOnlyAndFailsWhenProcessGenerationIsLost() throws {
        let launcher = FakeHelperLauncher()
        var requestIDs = ["handshake-1", "remove-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            handshakeTimeout: 1
        )
        supervisor.start()
        launcher.receive(line: #"{"version":3,"requestId":"handshake-1","result":{"protocolVersion":3}}"#)
        let portalID = UUID(uuidString: "9f55ca93-d7b3-4eab-a871-310ea576005a")!
        var result: Result<Void, Error>?

        supervisor.removePortal(id: portalID) { result = $0 }

        let requestData = try XCTUnwrap(launcher.process.sent.last)
        let request = try JSONDecoder().decode(HelperRequest<RemovePortalPayload>.self, from: requestData)
        XCTAssertEqual(request.command, .removePortal)
        XCTAssertEqual(request.payload, RemovePortalPayload(portalId: portalID))
        XCTAssertEqual(try JSONSerialization.jsonObject(with: requestData) as? NSDictionary, [
            "version": 3,
            "requestId": "remove-1",
            "command": "removePortal",
            "payload": ["portalId": portalID.uuidString],
        ])
        XCTAssertNil(result)

        launcher.receiveEOF()

        guard case .failure(HelperClientError.protocolFailure) = result else {
            return XCTFail("expected unresolved removal to fail after process loss")
        }
    }

    func testReturnsFixedHelperDiscoveryFailure() throws {
        let launcher = FakeHelperLauncher()
        var requestIDs = ["handshake-1", "discover-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            handshakeTimeout: 1
        )
        supervisor.start()
        launcher.receive(line: #"{"version":3,"requestId":"handshake-1","result":{"protocolVersion":3}}"#)
        var result: Result<[LocalAppCandidatePayload], Error>?

        supervisor.discoverLocalApps { result = $0 }
        launcher.receive(line: #"{"version":3,"requestId":"discover-1","error":{"code":"discoveryFailure","message":"local app discovery failed"}}"#)

        guard case let .failure(HelperClientError.helper(error)) = result else {
            return XCTFail("expected fixed helper discovery failure")
        }
        XCTAssertEqual(error, HelperProtocolError(code: "discoveryFailure", message: "local app discovery failed"))
    }
}

final class FakeHelperLauncher: HelperLaunching {
    private(set) var processes: [FakeHelperProcess] = []
    var process: FakeHelperProcess { processes.last! }
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
        let process = FakeHelperProcess()
        processes.append(process)
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

@MainActor
final class FakePorticoScheduler: PorticoScheduling {
    private struct Entry {
        let delay: TimeInterval
        let task: FakeScheduledTask
        let action: () -> Void
    }

    private var entries: [Entry] = []
    private(set) var recordedDelays: [TimeInterval] = []
    var pendingDelays: [TimeInterval] {
        entries.filter { !$0.task.isCancelled }.map(\.delay)
    }

    func schedule(after delay: TimeInterval, _ action: @escaping () -> Void) -> ScheduledTask {
        let task = FakeScheduledTask()
        recordedDelays.append(delay)
        entries.append(Entry(delay: delay, task: task, action: action))
        return task
    }

    func runNext() {
        while !entries.isEmpty {
            let entry = entries.removeFirst()
            guard !entry.task.isCancelled else { continue }
            entry.action()
            return
        }
    }

    func run(delay: TimeInterval) {
        guard let index = entries.firstIndex(where: { $0.delay == delay && !$0.task.isCancelled }) else {
            return
        }
        let entry = entries.remove(at: index)
        entry.action()
    }
}

final class FakeScheduledTask: ScheduledTask {
    private(set) var isCancelled = false
    func cancel() { isCancelled = true }
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
