import Foundation
import XCTest
@testable import PorticoApplication

@MainActor
final class HelperSupervisorTests: XCTestCase {
    func testUndecidedLoggingWaitsWithoutLaunchingChild() {
        let launcher = FakeHelperLauncher()
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher
        )

        supervisor.start(loggingPreference: .undecided)

        XCTAssertEqual(supervisor.availability, .awaitingLoggingChoice)
        XCTAssertTrue(launcher.processes.isEmpty)
    }

    func testExposesCurrentProcessGenerationThroughClientSeam() {
        let launcher = FakeHelperLauncher()
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { "handshake-1" }
        )

        XCTAssertEqual(supervisor.generation, 0)
        supervisor.start(loggingPreference: .enabled)
        XCTAssertEqual(supervisor.generation, 1)
    }

    func testControlledRestartWaitsForOldOwnershipBeforeLaunchingWithNewPreference() throws {
        let launcher = FakeHelperLauncher()
        let scheduler = FakePorticoScheduler()
        var requestIDs = ["handshake-1", "shutdown-1", "handshake-2"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            scheduler: scheduler,
            handshakeTimeout: 60
        )
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}"#)

        supervisor.restart(loggingPreference: .disabled)

        XCTAssertEqual(supervisor.availability, .restarting)
        XCTAssertEqual(launcher.processes.count, 1)
        XCTAssertTrue(launcher.process.inputClosed)
        let shutdown = try JSONDecoder().decode(
            HelperRequest<EmptyPayload>.self,
            from: XCTUnwrap(launcher.process.sent.last)
        )
        XCTAssertEqual(shutdown.command, .shutdown)

        launcher.exit(status: 0)

        XCTAssertEqual(launcher.processes.count, 2)
        XCTAssertEqual(launcher.loggingPreferences, [.enabled, .disabled])
        XCTAssertEqual(launcher.processes.filter(\.isRunning).count, 1)
        launcher.receive(line: #"{"version":4,"requestId":"handshake-2","result":{"protocolVersion":4}}"#)
        XCTAssertEqual(supervisor.availability, .connected)
    }

    func testControlledRestartForcesOldChildButStillWaitsForExitOwnership() {
        let launcher = FakeHelperLauncher()
        let scheduler = FakePorticoScheduler()
        var requestIDs = ["handshake-1", "shutdown-1", "handshake-2"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            scheduler: scheduler,
            handshakeTimeout: 60,
            shutdownGraceInterval: 1
        )
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}"#)

        supervisor.restart(loggingPreference: .disabled)
        scheduler.run(delay: 1)

        XCTAssertTrue(launcher.process.terminated)
        XCTAssertEqual(launcher.processes.count, 1)

        launcher.exit(status: 0)
        XCTAssertEqual(launcher.processes.count, 2)
    }

    func testControlledRestartReportsOwnershipFailureAfterUnconfirmedKill() {
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
        launcher.receive(line: #"{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}"#)

        supervisor.restart(loggingPreference: .disabled)
        scheduler.run(delay: 5)
        scheduler.run(delay: 2)
        scheduler.run(delay: 1)

        XCTAssertTrue(launcher.process.terminated)
        XCTAssertTrue(launcher.process.killed)
        XCTAssertEqual(supervisor.availability, .ownershipFailure)
        XCTAssertEqual(launcher.processes.count, 1)
    }

    func testControlledRestartRejectsOldEventsAndStartsFreshRecoveryBudget() {
        let launcher = FakeHelperLauncher()
        let scheduler = FakePorticoScheduler()
        var requestIDs = ["handshake-1", "shutdown-1", "handshake-2"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            scheduler: scheduler,
            handshakeTimeout: 60
        )
        var events: [PortalHelperEvent] = []
        supervisor.onEvent = { events.append($0) }
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}"#)

        supervisor.restart(loggingPreference: .disabled)
        launcher.receive(line: #"{"version":4,"event":"portalStatus","portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A","payload":{"state":"online","addresses":[]}}"#)
        XCTAssertTrue(events.isEmpty)
        launcher.exit(status: 0)
        launcher.exit(status: 1)

        XCTAssertEqual(supervisor.availability, .retrying(attempt: 1, delay: 1))
    }

    func testRestartFencesAvailabilityBeforeFailingOldGenerationCallbacks() {
        let launcher = FakeHelperLauncher()
        var requestIDs = ["handshake-1", "authenticate-1", "discover-1", "shutdown-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            handshakeTimeout: 60
        )
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}"#)

        supervisor.authenticatePortal(id: UUID()) { _ in
            supervisor.discoverLocalApps { _ in }
        }
        supervisor.restart(loggingPreference: .disabled)

        XCTAssertEqual(launcher.process.sent.count, 3)
        let lastRequest = try? JSONDecoder().decode(
            HelperRequest<EmptyPayload>.self,
            from: launcher.process.sent.last ?? Data()
        )
        XCTAssertEqual(lastRequest?.command, .shutdown)
    }

    func testLateExitAfterTerminalOwnershipFailureCannotScheduleReplacement() {
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

        launcher.exit(status: 1)
        scheduler.runNext()

        XCTAssertEqual(supervisor.availability, .ownershipFailure)
        XCTAssertEqual(launcher.processes.count, 1)
    }

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

        supervisor.start(loggingPreference: .enabled)
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

        supervisor.start(loggingPreference: .enabled)
        launcher.exit(status: 1)
        scheduler.runNext()
        launcher.receive(line: #"{"version":4,"requestId":"handshake-2","result":{"protocolVersion":4}}"#)
        supervisor.reconcilePortals([]) { _ in }
        launcher.receive(line: #"{"version":4,"requestId":"reconcile-2","result":{"entries":[]}}"#)

        XCTAssertTrue(scheduler.pendingDelays.contains(300))
        launcher.exit(status: 1)
        XCTAssertEqual(supervisor.availability, .retrying(attempt: 2, delay: 2))

        scheduler.run(delay: 2)
        launcher.receive(line: #"{"version":4,"requestId":"handshake-3","result":{"protocolVersion":4}}"#)
        supervisor.reconcilePortals([]) { _ in }
        launcher.receive(line: #"{"version":4,"requestId":"reconcile-3","result":{"entries":[]}}"#)
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

        supervisor.start(loggingPreference: .enabled)

        XCTAssertEqual(supervisor.availability, .connecting)
        let requestData = try XCTUnwrap(launcher.process.sent.first)
        let request = try JSONDecoder().decode(HelperRequest<EmptyPayload>.self, from: requestData)
        XCTAssertEqual(request.version, 4)
        XCTAssertEqual(request.requestId, "request-1")
        XCTAssertEqual(request.command, .handshake)

        launcher.receive(line: #"{"version":4,"requestId":"other","result":{"protocolVersion":4}}"#)
        XCTAssertEqual(supervisor.availability, .connecting)

        launcher.receive(line: #"{"version":4,"requestId":"request-1","result":{"protocolVersion":4}}"#)
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

        supervisor.start(loggingPreference: .enabled)
        try await Task.sleep(nanoseconds: 50_000_000)
        launcher.exit(status: 1)

        XCTAssertEqual(supervisor.availability, .retrying(attempt: 1, delay: 1))
        XCTAssertTrue(launcher.process.inputClosed)
        XCTAssertTrue(launcher.process.terminated)
    }

    func testExactHandshakeProtocolMismatchIsTerminalWithoutRetry() {
        let launcher = FakeHelperLauncher()
        let scheduler = FakePorticoScheduler()
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { "request-1" },
            scheduler: scheduler,
            handshakeTimeout: 60
        )

        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":4,"requestId":"request-1","result":{"protocolVersion":5}}"#)
        XCTAssertTrue(launcher.process.terminated)
        launcher.exit(status: 1)

        XCTAssertEqual(supervisor.availability, .protocolMismatch)
        XCTAssertTrue(scheduler.pendingDelays.isEmpty)
    }

    func testExactEnvelopeProtocolMismatchIsTerminalForHandshakeAndLaterResponse() {
        for line in [
            #"{"version":5,"requestId":"request-1","result":{"protocolVersion":4}}"#,
            #"{"version":5,"requestId":"reconcile-1","result":{"entries":[]}}"#,
        ] {
            let launcher = FakeHelperLauncher()
            let scheduler = FakePorticoScheduler()
            var requestIDs = ["request-1", "reconcile-1"]
            let supervisor = HelperSupervisor(
                helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
                launcher: launcher,
                requestIDProvider: { requestIDs.removeFirst() },
                scheduler: scheduler,
                handshakeTimeout: 60
            )
            supervisor.start(loggingPreference: .enabled)
            if line.contains("reconcile") {
                launcher.receive(line: #"{"version":4,"requestId":"request-1","result":{"protocolVersion":4}}"#)
                supervisor.reconcilePortals([]) { _ in }
            }
            launcher.receive(line: line)
            launcher.exit(status: 1)

            XCTAssertEqual(supervisor.availability, .protocolMismatch)
            XCTAssertTrue(scheduler.pendingDelays.isEmpty)
        }
    }

    func testHandshakeFailuresEnterSharedRecoveryBudgetAfterChildExit() {
        let failures: [(String, Bool, (FakeHelperLauncher) -> Void)] = [
            ("malformed line", false, { $0.receive(line: "{") }),
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
            supervisor.start(loggingPreference: .enabled)

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
        supervisor.start(loggingPreference: .enabled)
        XCTAssertEqual(launcher.arguments, ["--state-root", "/trusted/tsnet"])
        launcher.receive(line: #"{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}"#)
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
                    destination: .localApp(port: 8788),
                    desiredState: .stopped
                ),
                ReconcilePortalPayload(
                    portalId: laterPortal.id,
                    portalName: "hermes",
                    destination: .localApp(port: 8787),
                    desiredState: .enabled
                ),
            ]
        )
        launcher.receive(line: #"{"version":4,"event":"portalStatus","portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A","payload":{"state":"connecting","addresses":[]}}"#)
        XCTAssertEqual(events, [.status(laterPortal.id, PortalStatusPayload(state: .connecting, stableNodeId: nil, assignedName: nil, portalURL: nil, addresses: []), generation: 1)])
        XCTAssertNil(result)
        launcher.receive(line: #"{"version":4,"requestId":"reconcile-1","result":{"entries":[{"portalId":"5EA74329-3144-4BA2-925F-138D14D61FCC","outcome":"converged"},{"portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A","outcome":"startFailed"}]}}"#)
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
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}"#)
        var result: Result<ReconcilePortalsResult, Error>?

        supervisor.reconcilePortals([]) { result = $0 }
        launcher.receiveEOF()

        guard case .failure(HelperClientError.generationLost) = result else {
            return XCTFail("expected unresolved reconciliation generation loss")
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
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}"#)
        var result: Result<[LocalAppCandidatePayload], Error>?

        supervisor.discoverLocalApps { result = $0 }

        let requestData = try XCTUnwrap(launcher.process.sent.last)
        let request = try JSONDecoder().decode(HelperRequest<EmptyPayload>.self, from: requestData)
        XCTAssertEqual(request.command, .discoverLocalApps)
        XCTAssertEqual(request.requestId, "discover-1")
        launcher.receive(line: #"{"version":4,"requestId":"discover-1","result":{"candidates":[{"localAppPort":3000,"processLabel":"node","suggestedPortalName":"hermes"}]}}"#)
        XCTAssertEqual(
            try result?.get(),
            [LocalAppCandidatePayload(localAppPort: 3000, processLabel: "node", suggestedPortalName: "hermes")]
        )
    }

    func testSilentDiscoveryCompletesOnceAtItsDeadline() {
        let launcher = FakeHelperLauncher()
        let scheduler = FakePorticoScheduler()
        var requestIDs = ["handshake-1", "discover-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            scheduler: scheduler,
            handshakeTimeout: 60
        )
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}"#)
        var completions: [Result<[LocalAppCandidatePayload], Error>] = []

        supervisor.discoverLocalApps { completions.append($0) }
        scheduler.run(delay: 5)

        XCTAssertEqual(completions.count, 1)
        guard case .failure = completions.first else {
            return XCTFail("expected silent discovery deadline failure")
        }
        launcher.receive(line: #"{"version":4,"requestId":"discover-1","result":{"candidates":[]}}"#)
        XCTAssertEqual(completions.count, 1)
    }

    func testDispatchedCommandsUseTheirSpecifiedDeadlines() {
        let launcher = FakeHelperLauncher()
        let scheduler = FakePorticoScheduler()
        var requestIDs = ["handshake-1", "authenticate-1", "discover-1", "cleanup-1", "remove-1", "reconcile-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            scheduler: scheduler,
            handshakeTimeout: 60
        )
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}"#)

        supervisor.authenticatePortal(id: UUID()) { _ in }
        supervisor.discoverLocalApps { _ in }
        supervisor.cleanupRejectedPortal(id: UUID()) { _ in }
        supervisor.removePortal(id: UUID()) { _ in }
        supervisor.reconcilePortals([]) { _ in }

        XCTAssertEqual(scheduler.pendingDelays.filter { $0 < 60 }, [5])
        launcher.receive(line: #"{"version":4,"requestId":"authenticate-1","result":{"accepted":true}}"#)

        XCTAssertEqual(scheduler.pendingDelays.filter { $0 < 60 }, [5])
        launcher.receive(line: #"{"version":4,"requestId":"discover-1","result":{"candidates":[]}}"#)

        XCTAssertEqual(scheduler.pendingDelays.filter { $0 < 60 }, [15])
        launcher.receive(line: #"{"version":4,"requestId":"cleanup-1","result":{"accepted":true}}"#)

        XCTAssertEqual(scheduler.pendingDelays.filter { $0 < 60 }, [15])
        launcher.receive(line: #"{"version":4,"requestId":"remove-1","result":{"accepted":true}}"#)

        XCTAssertEqual(scheduler.pendingDelays.filter { $0 < 60 }, [10])
    }

    func testQueuesAuthenticationAndCleanupUntilReconciliationResponds() throws {
        let launcher = FakeHelperLauncher()
        let scheduler = FakePorticoScheduler()
        var requestIDs = ["handshake-1", "reconcile-1", "authenticate-1", "cleanup-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            scheduler: scheduler,
            handshakeTimeout: 60
        )
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}"#)

        supervisor.reconcilePortals([]) { _ in }
        supervisor.authenticatePortal(id: UUID()) { _ in }
        supervisor.cleanupRejectedPortal(id: UUID()) { _ in }

        XCTAssertEqual(launcher.process.sent.count, 2)
        XCTAssertEqual(scheduler.pendingDelays, [10])
        let reconciliation = try XCTUnwrap(launcher.process.sent.last)
        XCTAssertEqual(
            try JSONDecoder().decode(HelperRequest<ReconcilePortalsPayload>.self, from: reconciliation).command,
            .reconcilePortals
        )

        launcher.receive(line: #"{"version":4,"requestId":"reconcile-1","result":{"entries":[]}}"#)

        XCTAssertEqual(launcher.process.sent.count, 3)
        XCTAssertEqual(scheduler.pendingDelays.filter { $0 < 60 }, [5])
        let authentication = try XCTUnwrap(launcher.process.sent.last)
        XCTAssertEqual(
            try JSONDecoder().decode(HelperRequest<AuthenticatePortalPayload>.self, from: authentication).command,
            .authenticatePortal
        )

        launcher.receive(line: #"{"version":4,"requestId":"authenticate-1","result":{"accepted":true}}"#)

        XCTAssertEqual(launcher.process.sent.count, 4)
        XCTAssertEqual(scheduler.pendingDelays.filter { $0 < 60 }, [15])
        let cleanup = try XCTUnwrap(launcher.process.sent.last)
        XCTAssertEqual(
            try JSONDecoder().decode(HelperRequest<CleanupRejectedPortalPayload>.self, from: cleanup).command,
            .cleanupRejectedPortal
        )
    }

    func testGenerationLossFailsQueuedRequestsWithoutDispatchingThem() {
        let launcher = FakeHelperLauncher()
        var requestIDs = ["handshake-1", "reconcile-1", "authenticate-1", "cleanup-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            handshakeTimeout: 60
        )
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}"#)
        var authenticationResults: [Result<Void, Error>] = []
        var cleanupResults: [Result<Void, Error>] = []

        supervisor.reconcilePortals([]) { _ in }
        supervisor.authenticatePortal(id: UUID()) { authenticationResults.append($0) }
        supervisor.cleanupRejectedPortal(id: UUID()) { cleanupResults.append($0) }
        launcher.receiveEOF()

        XCTAssertEqual(launcher.process.sent.count, 2)
        guard authenticationResults.count == 1,
              case .failure(HelperClientError.generationLost) = authenticationResults[0]
        else {
            return XCTFail("expected queued authentication to lose its generation")
        }
        guard cleanupResults.count == 1,
              case .failure(HelperClientError.generationLost) = cleanupResults[0]
        else {
            return XCTFail("expected queued cleanup to lose its generation")
        }
    }

    func testShutdownBypassesQueuedRequestsAndFailsThemOnce() throws {
        let launcher = FakeHelperLauncher()
        var requestIDs = ["handshake-1", "reconcile-1", "authenticate-1", "shutdown-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            handshakeTimeout: 60
        )
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}"#)
        var authenticationResults: [Result<Void, Error>] = []

        supervisor.reconcilePortals([]) { _ in }
        supervisor.authenticatePortal(id: UUID()) { authenticationResults.append($0) }
        supervisor.shutdown {}

        XCTAssertEqual(launcher.process.sent.count, 3)
        let shutdown = try XCTUnwrap(launcher.process.sent.last)
        XCTAssertEqual(
            try JSONDecoder().decode(HelperRequest<EmptyPayload>.self, from: shutdown).command,
            .shutdown
        )
        guard authenticationResults.count == 1,
              case .failure(HelperClientError.generationLost) = authenticationResults[0]
        else {
            return XCTFail("expected queued authentication to finish once during shutdown")
        }
        XCTAssertTrue(launcher.process.inputClosed)
    }

    func testRestartFailsQueuedRequestsWithoutDispatchingThem() throws {
        let launcher = FakeHelperLauncher()
        var requestIDs = ["handshake-1", "reconcile-1", "authenticate-1", "shutdown-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            handshakeTimeout: 60
        )
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}"#)
        var authenticationResults: [Result<Void, Error>] = []

        supervisor.reconcilePortals([]) { _ in }
        supervisor.authenticatePortal(id: UUID()) { authenticationResults.append($0) }
        supervisor.restart(loggingPreference: .disabled)

        XCTAssertEqual(launcher.process.sent.count, 3)
        let shutdown = try XCTUnwrap(launcher.process.sent.last)
        XCTAssertEqual(
            try JSONDecoder().decode(HelperRequest<EmptyPayload>.self, from: shutdown).command,
            .shutdown
        )
        guard authenticationResults.count == 1,
              case .failure(HelperClientError.generationLost) = authenticationResults[0]
        else {
            return XCTFail("expected queued authentication to finish once during restart")
        }
    }

    func testResponseCallbackQueuesAfterPreviouslyAcceptedOrdinaryRequest() throws {
        let launcher = FakeHelperLauncher()
        var requestIDs = ["handshake-1", "reconcile-1", "cleanup-1", "authenticate-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            handshakeTimeout: 60
        )
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}"#)

        supervisor.reconcilePortals([]) { _ in
            supervisor.authenticatePortal(id: UUID()) { _ in }
        }
        supervisor.cleanupRejectedPortal(id: UUID()) { _ in }
        launcher.receive(line: #"{"version":4,"requestId":"reconcile-1","result":{"entries":[]}}"#)

        XCTAssertEqual(launcher.process.sent.count, 3)
        let cleanup = try XCTUnwrap(launcher.process.sent.last)
        XCTAssertEqual(
            try JSONDecoder().decode(HelperRequest<CleanupRejectedPortalPayload>.self, from: cleanup).command,
            .cleanupRejectedPortal
        )

        launcher.receive(line: #"{"version":4,"requestId":"cleanup-1","result":{"accepted":true}}"#)

        XCTAssertEqual(launcher.process.sent.count, 4)
        let authentication = try XCTUnwrap(launcher.process.sent.last)
        XCTAssertEqual(
            try JSONDecoder().decode(HelperRequest<AuthenticatePortalPayload>.self, from: authentication).command,
            .authenticatePortal
        )
    }

    func testUnmatchedResponseDoesNotAdvanceOrdinaryQueue() throws {
        let launcher = FakeHelperLauncher()
        var requestIDs = ["handshake-1", "reconcile-1", "authenticate-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            handshakeTimeout: 60
        )
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}"#)
        var authenticationResults: [Result<Void, Error>] = []

        supervisor.reconcilePortals([]) { _ in }
        supervisor.authenticatePortal(id: UUID()) { authenticationResults.append($0) }
        launcher.receive(line: #"{"version":4,"requestId":"authenticate-1","result":{"accepted":true}}"#)

        XCTAssertEqual(launcher.process.sent.count, 2)
        XCTAssertTrue(authenticationResults.isEmpty)
        launcher.receive(line: #"{"version":4,"requestId":"reconcile-1","result":{"entries":[]}}"#)

        XCTAssertEqual(launcher.process.sent.count, 3)
        let authentication = try XCTUnwrap(launcher.process.sent.last)
        XCTAssertEqual(
            try JSONDecoder().decode(HelperRequest<AuthenticatePortalPayload>.self, from: authentication).command,
            .authenticatePortal
        )
        launcher.receive(line: #"{"version":4,"requestId":"authenticate-1","result":{"accepted":true}}"#)
        XCTAssertEqual(authenticationResults.count, 1)
    }

    func testReconciliationDeadlineUsesOnlyThePreviousSentSnapshotCount() {
        let launcher = FakeHelperLauncher()
        let scheduler = FakePorticoScheduler()
        var requestIDs = ["handshake-1", "reconcile-1", "reconcile-2", "reconcile-3"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            scheduler: scheduler,
            handshakeTimeout: 60
        )
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}"#)
        let portals = (0..<5).map {
            PortalConfiguration(id: UUID(), name: "portal-\($0)", localAppPort: 8000 + $0, createdAt: Date())
        }

        supervisor.reconcilePortals(portals) { _ in }
        XCTAssertTrue(scheduler.pendingDelays.contains(60))
        launcher.receive(line: #"{"version":4,"requestId":"reconcile-1","error":{"code":"expected","message":"expected"}}"#)
        supervisor.reconcilePortals([]) { _ in }
        XCTAssertTrue(scheduler.pendingDelays.contains(60))
        launcher.receive(line: #"{"version":4,"requestId":"reconcile-2","error":{"code":"expected","message":"expected"}}"#)
        supervisor.reconcilePortals([]) { _ in }

        XCTAssertEqual(scheduler.pendingDelays, [10])
    }

    func testDeadlineFailsItsCommandAndOtherPendingCommandsWithGenerationLoss() {
        let launcher = FakeHelperLauncher()
        let scheduler = FakePorticoScheduler()
        var requestIDs = ["handshake-1", "authenticate-1", "cleanup-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            scheduler: scheduler,
            handshakeTimeout: 60
        )
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}"#)
        var authenticationResult: Result<Void, Error>?
        var cleanupResult: Result<Void, Error>?

        supervisor.authenticatePortal(id: UUID()) { authenticationResult = $0 }
        supervisor.cleanupRejectedPortal(id: UUID()) { cleanupResult = $0 }
        scheduler.run(delay: 5)

        guard case .failure(HelperClientError.deadline) = authenticationResult else {
            return XCTFail("expected authentication deadline")
        }
        guard case .failure(HelperClientError.generationLost) = cleanupResult else {
            return XCTFail("expected cleanup generation loss")
        }
        XCTAssertTrue(launcher.process.terminated)
    }

    func testDeadlineChangesAvailabilityBeforeCompletionCanReenter() {
        let launcher = FakeHelperLauncher()
        let scheduler = FakePorticoScheduler()
        var requestIDs = ["handshake-1", "discover-1", "authenticate-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            scheduler: scheduler,
            handshakeTimeout: 60
        )
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}"#)

        supervisor.discoverLocalApps { _ in
            supervisor.authenticatePortal(id: UUID()) { _ in }
        }
        scheduler.run(delay: 5)

        XCTAssertEqual(launcher.process.sent.count, 2)
        XCTAssertEqual(supervisor.availability, .requestDeadline)
    }

    func testUnhealthyGenerationEscalatesToKILLAndReportsOwnershipFailureWithoutExit() {
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
        XCTAssertEqual(supervisor.availability, .requestDeadline)
        XCTAssertTrue(launcher.process.terminated)
        scheduler.run(delay: 2)
        XCTAssertTrue(launcher.process.killed)
        scheduler.run(delay: 1)

        XCTAssertEqual(supervisor.availability, .ownershipFailure)
        XCTAssertEqual(launcher.processes.count, 1)
    }

    func testLateFailedWriteInvalidatesGenerationAfterItsResponseAlreadyCompleted() {
        let launcher = FakeHelperLauncher()
        var requestIDs = ["handshake-1", "discover-1", "authenticate-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            handshakeTimeout: 60
        )
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}"#)
        launcher.process.completesWritesImmediately = false
        var discoveryResults: [Result<[LocalAppCandidatePayload], Error>] = []
        var authenticationResults: [Result<Void, Error>] = []

        supervisor.discoverLocalApps { discoveryResults.append($0) }
        supervisor.authenticatePortal(id: UUID()) { authenticationResults.append($0) }
        launcher.receive(line: #"{"version":4,"requestId":"discover-1","result":{"candidates":[]}}"#)
        launcher.process.completeNextWrite(.failure(FakeHelperWriteError.failed))

        XCTAssertEqual(discoveryResults.count, 1)
        guard authenticationResults.count == 1,
              case .failure(HelperClientError.generationLost) = authenticationResults[0]
        else {
            return XCTFail("expected the remaining request to lose its generation")
        }
        XCTAssertEqual(supervisor.availability, .generationLost)
    }

    func testMalformedResponseFencesGenerationBeforeItsCallbackCanReenter() {
        let launcher = FakeHelperLauncher()
        var requestIDs = ["handshake-1", "discover-1", "authenticate-1"]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            handshakeTimeout: 60
        )
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}"#)

        supervisor.discoverLocalApps { _ in
            supervisor.authenticatePortal(id: UUID()) { _ in }
        }
        launcher.receive(line: #"{"version":4,"requestId":"discover-1","result":{"unexpected":true}}"#)

        XCTAssertEqual(launcher.process.sent.count, 2)
        XCTAssertEqual(supervisor.availability, .generationLost)
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
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}"#)
        let portalID = UUID(uuidString: "9f55ca93-d7b3-4eab-a871-310ea576005a")!
        var result: Result<Void, Error>?

        supervisor.cleanupRejectedPortal(id: portalID) { result = $0 }

        let requestData = try XCTUnwrap(launcher.process.sent.last)
        let request = try JSONDecoder().decode(HelperRequest<CleanupRejectedPortalPayload>.self, from: requestData)
        XCTAssertEqual(request.command, .cleanupRejectedPortal)
        XCTAssertEqual(request.payload.portalId, portalID)
        XCTAssertNil(result)
        launcher.receive(line: #"{"version":4,"requestId":"cleanup-1","result":{"accepted":true}}"#)
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
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}"#)
        let portalID = UUID(uuidString: "9f55ca93-d7b3-4eab-a871-310ea576005a")!
        var result: Result<Void, Error>?

        supervisor.removePortal(id: portalID) { result = $0 }

        let requestData = try XCTUnwrap(launcher.process.sent.last)
        let request = try JSONDecoder().decode(HelperRequest<RemovePortalPayload>.self, from: requestData)
        XCTAssertEqual(request.command, .removePortal)
        XCTAssertEqual(request.payload, RemovePortalPayload(portalId: portalID))
        XCTAssertEqual(try JSONSerialization.jsonObject(with: requestData) as? NSDictionary, [
            "version": 4,
            "requestId": "remove-1",
            "command": "removePortal",
            "payload": ["portalId": portalID.uuidString],
        ])
        XCTAssertNil(result)

        launcher.receiveEOF()

        guard case .failure(HelperClientError.generationLost) = result else {
            return XCTFail("expected unresolved removal generation loss after process loss")
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
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}"#)
        var result: Result<[LocalAppCandidatePayload], Error>?

        supervisor.discoverLocalApps { result = $0 }
        launcher.receive(line: #"{"version":4,"requestId":"discover-1","error":{"code":"discoveryFailure","message":"local app discovery failed"}}"#)

        guard case let .failure(HelperClientError.helper(error)) = result else {
            return XCTFail("expected fixed helper discovery failure")
        }
        XCTAssertEqual(error, HelperProtocolError(code: "discoveryFailure", message: "local app discovery failed"))
    }

    func testRealSilentHelperDeadlineLosesOtherPendingRequestAndEscalatesTERMToKILL() throws {
        let readyURL = temporaryFixtureURL(suffix: "ready")
        let scriptURL = try makeHelperFixture("""
        trap '' TERM
        IFS= read -r handshake
        printf '%s\\n' '{"version":4,"requestId":"handshake-1","result":{"protocolVersion":4}}'
        IFS= read -r discovery
        : > '\(readyURL.path)'
        while :; do :; done
        """)
        defer {
            try? FileManager.default.removeItem(at: scriptURL)
            try? FileManager.default.removeItem(at: readyURL)
        }

        let scheduler = FakePorticoScheduler()
        let connected = expectation(description: "real helper connects")
        let retried = expectation(description: "killed helper exits and schedules retry")
        let exited = expectation(description: "TERM-resistant child exits after KILL")
        var childExited = false
        let launcher = RecordingHelperLauncher { _ in
            childExited = true
            exited.fulfill()
        }
        let supervisor = HelperSupervisor(
            helperURL: scriptURL,
            launcher: launcher,
            requestIDProvider: sequenceProvider(["handshake-1", "discover-1", "authenticate-1", "shutdown-1"]),
            scheduler: scheduler,
            handshakeTimeout: 60
        )
        supervisor.onConnected = { connected.fulfill() }
        supervisor.onAvailabilityChange = {
            if case .retrying = $0 { retried.fulfill() }
        }
        defer {
            if !childExited {
                supervisor.shutdown {}
                scheduler.run(delay: 5)
                scheduler.run(delay: 2)
                launcher.forceKill()
                waitForProcessToStop(launcher)
            }
        }
        supervisor.start(loggingPreference: .enabled)
        wait(for: [connected], timeout: 2)

        var discovery: Result<[LocalAppCandidatePayload], Error>?
        var authentication: Result<Void, Error>?
        supervisor.discoverLocalApps { discovery = $0 }
        supervisor.authenticatePortal(id: UUID()) { authentication = $0 }
        waitForFixture(at: readyURL)

        scheduler.run(delay: 5)
        guard case .failure(HelperClientError.deadline) = discovery else {
            return XCTFail("expected silent discovery deadline")
        }
        guard case .failure(HelperClientError.generationLost) = authentication else {
            return XCTFail("expected other pending request to lose its generation")
        }
        XCTAssertEqual(supervisor.availability, .requestDeadline)

        scheduler.run(delay: 2)
        wait(for: [exited, retried], timeout: 2)
        XCTAssertEqual(supervisor.availability, .retrying(attempt: 1, delay: 1))
    }

    func testRealProtocolMismatchIsPermanentAfterTERMIgnoresUntilKILL() throws {
        let scriptURL = try makeHelperFixture("""
        trap '' TERM
        IFS= read -r handshake
        printf '%s\\n' '{"version":4,"requestId":"handshake-1","result":{"protocolVersion":5}}'
        while :; do :; done
        """)
        defer { try? FileManager.default.removeItem(at: scriptURL) }

        let scheduler = FakePorticoScheduler()
        let mismatch = expectation(description: "protocol mismatch")
        let exited = expectation(description: "TERM-resistant mismatch child exits after KILL")
        var childExited = false
        let launcher = RecordingHelperLauncher { _ in
            childExited = true
            exited.fulfill()
        }
        let supervisor = HelperSupervisor(
            helperURL: scriptURL,
            launcher: launcher,
            requestIDProvider: { "handshake-1" },
            scheduler: scheduler,
            handshakeTimeout: 60
        )
        supervisor.onAvailabilityChange = {
            if $0 == .protocolMismatch { mismatch.fulfill() }
        }
        defer {
            if !childExited {
                supervisor.shutdown {}
                scheduler.run(delay: 5)
                scheduler.run(delay: 2)
                launcher.forceKill()
                waitForProcessToStop(launcher)
            }
        }
        supervisor.start(loggingPreference: .enabled)
        wait(for: [mismatch], timeout: 2)

        scheduler.run(delay: 2)
        wait(for: [exited], timeout: 2)

        XCTAssertEqual(supervisor.availability, .protocolMismatch)
        XCTAssertFalse(scheduler.pendingDelays.contains { $0 == 1 || $0 == 2 || $0 == 4 || $0 == 8 || $0 == 16 })
    }

    func testRealLauncherFailureEntersGenerationLossRecovery() {
        let scheduler = FakePorticoScheduler()
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/missing/portico-helper"),
            launcher: ProcessHelperLauncher(),
            requestIDProvider: { "handshake-1" },
            scheduler: scheduler,
            handshakeTimeout: 60
        )

        supervisor.start(loggingPreference: .enabled)

        XCTAssertEqual(supervisor.availability, .retrying(attempt: 1, delay: 1))
        XCTAssertEqual(scheduler.pendingDelays, [1])
    }
}

private func makeHelperFixture(_ body: String) throws -> URL {
    let url = temporaryFixtureURL(suffix: "sh")
    try "#!/bin/sh\n\(body)\n".write(to: url, atomically: true, encoding: .utf8)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
    return url
}

private func temporaryFixtureURL(suffix: String) -> URL {
    FileManager.default.temporaryDirectory
        .appendingPathComponent("portico-helper-\(UUID().uuidString)")
        .appendingPathExtension(suffix)
}

private func sequenceProvider(_ values: [String]) -> () -> String {
    var values = values
    return { values.removeFirst() }
}

private final class RecordingHelperLauncher: HelperLaunching {
    private let launcher = ProcessHelperLauncher()
    private let childDidExit: (Int32) -> Void
    private var process: HelperProcess?

    var isRunning: Bool { process?.isRunning ?? false }

    init(childDidExit: @escaping (Int32) -> Void) {
        self.childDidExit = childDidExit
    }

    func launch(
        at executableURL: URL,
        arguments: [String],
        loggingPreference: OperationalLoggingPreference,
        onLine: @escaping (Data) -> Void,
        onEOF: @escaping () -> Void,
        onExit: @escaping (Int32) -> Void
    ) throws -> HelperProcess {
        let process = try launcher.launch(
            at: executableURL,
            arguments: arguments,
            loggingPreference: loggingPreference,
            onLine: onLine,
            onEOF: onEOF,
            onExit: { [childDidExit] status in
                childDidExit(status)
                onExit(status)
            }
        )
        self.process = process
        return process
    }

    func forceKill() {
        process?.closeInput()
        process?.terminate()
        process?.kill()
    }
}

@MainActor
private func waitForFixture(at url: URL) {
    let deadline = Date().addingTimeInterval(2)
    while !FileManager.default.fileExists(atPath: url.path), Date() < deadline {
        RunLoop.current.run(until: Date().addingTimeInterval(0.01))
    }
    XCTAssertTrue(FileManager.default.fileExists(atPath: url.path), "fixture did not receive the request")
}

@MainActor
private func waitForProcessToStop(_ launcher: RecordingHelperLauncher) {
    let deadline = Date().addingTimeInterval(2)
    while launcher.isRunning, Date() < deadline {
        RunLoop.current.run(until: Date().addingTimeInterval(0.01))
    }
    XCTAssertFalse(launcher.isRunning, "fixture child remained running after cleanup")
}

final class FakeHelperLauncher: HelperLaunching {
    private(set) var processes: [FakeHelperProcess] = []
    var process: FakeHelperProcess { processes.last! }
    private var onLine: ((Data) -> Void)?
    private var onEOF: (() -> Void)?
    private var onExit: ((Int32) -> Void)?
    private(set) var arguments: [String] = []
    private(set) var loggingPreferences: [OperationalLoggingPreference] = []

    func launch(
        at executableURL: URL,
        arguments: [String],
        loggingPreference: OperationalLoggingPreference,
        onLine: @escaping (Data) -> Void,
        onEOF: @escaping () -> Void,
        onExit: @escaping (Int32) -> Void
    ) throws -> HelperProcess {
        let process = FakeHelperProcess()
        processes.append(process)
        self.arguments = arguments
        loggingPreferences.append(loggingPreference)
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
    private(set) var killed = false
    var completesWritesImmediately = true
    private var pendingWriteCompletions: [(Result<Void, Error>) -> Void] = []

    func send(_ data: Data, completion: @escaping (Result<Void, Error>) -> Void) {
        sent.append(data)
        if completesWritesImmediately {
            completion(.success(()))
        } else {
            pendingWriteCompletions.append(completion)
        }
    }

    func completeNextWrite(_ result: Result<Void, Error>) {
        pendingWriteCompletions.removeFirst()(result)
    }

    func closeInput() {
        inputClosed = true
    }

    func terminate() {
        terminated = true
    }

    func kill() {
        killed = true
    }

}

private enum FakeHelperWriteError: Error {
    case failed
}
