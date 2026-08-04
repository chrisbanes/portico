import Foundation
import XCTest
@testable import Portico

@MainActor
final class PortalControllerTests: XCTestCase {
    private let portalID = UUID(uuidString: "9f55ca93-d7b3-4eab-a871-310ea576005a")!

    func testUndecidedLoggingPreventsFirstPortalCreation() {
        let controller = PortalController(
            store: PortalStore(rootURL: temporaryRoot()),
            helper: FakePortalHelperClient(),
            openURL: { _ in }
        )
        controller.portalName = "hermes"
        controller.localAppPort = "8787"

        controller.addPortal()

        XCTAssertTrue(controller.portals.isEmpty)
        XCTAssertEqual(controller.message, "Choose an operational-support logging setting before adding a Portal.")
    }

    func testLoggingPreferenceCommitsBeforeControlledRestartAndSameValueIsNoOp() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        try store.save(InstallationRecord(operationalLogging: .enabled))
        let client = FakePortalHelperClient()
        client.onRestart = { preference in
            XCTAssertEqual(try? store.loadInstallation().operationalLogging, preference)
        }
        let controller = PortalController(store: store, helper: client, openURL: { _ in })

        controller.setOperationalLogging(.enabled)
        controller.setOperationalLogging(.disabled)

        XCTAssertEqual(controller.operationalLogging, .disabled)
        XCTAssertEqual(client.restartedWith, [.disabled])
    }

    func testLoggingPersistenceFailureLeavesPreferenceAndProcessUntouched() throws {
        struct ExpectedFailure: Error {}
        let root = temporaryRoot()
        let initialStore = PortalStore(rootURL: root)
        try initialStore.save(InstallationRecord(operationalLogging: .enabled))
        let failingStore = PortalStore(rootURL: root, writeData: { _, _ in throw ExpectedFailure() })
        let client = FakePortalHelperClient()
        let controller = PortalController(store: failingStore, helper: client, openURL: { _ in })

        controller.setOperationalLogging(.disabled)

        XCTAssertEqual(controller.operationalLogging, .enabled)
        XCTAssertTrue(client.restartedWith.isEmpty)
        XCTAssertEqual(controller.message, "The operational-support logging setting could not be saved.")
        XCTAssertEqual(
            controller.operationalLoggingError,
            "The operational-support logging setting could not be saved."
        )
    }

    func testLoggingRestartRejectsOldAuthenticationURL() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        let portal = PortalConfiguration(id: portalID, name: "hermes", localAppPort: 8787, createdAt: Date())
        try store.save(InstallationRecord(portals: [portal], operationalLogging: .enabled))
        let client = FakePortalHelperClient()
        var opened: [URL] = []
        let controller = PortalController(store: store, helper: client, openURL: { opened.append($0) })
        controller.authenticate(id: portalID)

        controller.setOperationalLogging(.disabled)
        client.send(.authenticationURL(portalID, URL(string: "https://login.example/secret")!))

        XCTAssertTrue(opened.isEmpty)
    }

    func testCurrentPortalURLCanCopyAndOpenButStaleURLCanOnlyCopy() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        let portal = PortalConfiguration(id: portalID, name: "hermes", localAppPort: 8787, createdAt: Date())
        try store.save(InstallationRecord(portals: [portal], operationalLogging: .enabled))
        let client = FakePortalHelperClient()
        var copied: [String] = []
        var opened: [URL] = []
        let controller = PortalController(
            store: store,
            helper: client,
            copyText: { copied.append($0) },
            openURL: { opened.append($0) }
        )
        let url = URL(string: "https://hermes.example.ts.net/")!
        client.send(.status(portalID, PortalStatusPayload(
            state: .online,
            stableNodeId: nil,
            assignedName: "hermes",
            portalURL: url,
            addresses: [],
            magicDNSSuffix: "example.ts.net"
        )))

        controller.copyPortalURL(id: portalID)
        controller.openPortalURL(id: portalID)
        client.disconnect(as: .retrying(attempt: 1, delay: 1))
        controller.copyPortalURL(id: portalID)
        controller.openPortalURL(id: portalID)

        XCTAssertEqual(copied, [url.absoluteString, url.absoluteString])
        XCTAssertEqual(opened, [url])
    }

    func testAddValidatesBeforeCreatingOnePortalUUID() throws {
        let client = FakePortalHelperClient()
        let store = PortalStore(rootURL: temporaryRoot())
        try store.save(InstallationRecord(operationalLogging: .enabled))
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
        XCTAssertEqual(controller.addPortal(), .invalidName)
        XCTAssertNil(controller.portal)
        XCTAssertEqual(UUIDCreationCount, 0)
        XCTAssertTrue(client.started.isEmpty)

        controller.portalName = "hermes"
        controller.localAppPort = "0"
        XCTAssertEqual(controller.addPortal(), .invalidPort)
        XCTAssertNil(controller.portal)
        XCTAssertEqual(UUIDCreationCount, 0)

        controller.localAppPort = "8787"
        XCTAssertNil(controller.addPortal())
        let portal = try XCTUnwrap(controller.portal)
        XCTAssertEqual(portal.id, portalID)
        XCTAssertEqual(UUIDCreationCount, 1)
        XCTAssertEqual(client.started, [portal])
        XCTAssertEqual(try store.load(), portal)

        controller.addPortal()
        XCTAssertEqual(UUIDCreationCount, 1)
        XCTAssertEqual(client.started.count, 1)
    }

    func testInitialDiscoveryAfterConnectionDoesNotChangeManualFields() {
        let client = FakePortalHelperClient(availability: .connecting)
        let controller = PortalController(store: PortalStore(rootURL: temporaryRoot()), helper: client, openURL: { _ in })
        controller.portalName = "manual-name"
        controller.localAppPort = "4321"

        XCTAssertTrue(client.discoveryCompletions.isEmpty)
        client.connect()
        XCTAssertEqual(client.discoveryCompletions.count, 1)
        XCTAssertTrue(controller.isRefreshingLocalApps)
        let candidates = [
            LocalAppCandidatePayload(localAppPort: 3000, processLabel: "node", suggestedPortalName: "detected")
        ]
        client.completeDiscovery(.success(candidates))

        XCTAssertEqual(controller.localApps, candidates)
        XCTAssertFalse(controller.isRefreshingLocalApps)
        XCTAssertEqual(controller.portalName, "manual-name")
        XCTAssertEqual(controller.localAppPort, "4321")
    }

    func testSelectingLocalAppUpdatesPortAndOnlyFillsBlankName() {
        let client = FakePortalHelperClient()
        let controller = PortalController(store: PortalStore(rootURL: temporaryRoot()), helper: client, openURL: { _ in })
        let hinted = LocalAppCandidatePayload(localAppPort: 3000, processLabel: "node", suggestedPortalName: "hermes")
        let generic = LocalAppCandidatePayload(localAppPort: 8787, processLabel: "Port 8787", suggestedPortalName: nil)
        client.completeDiscovery(.success([hinted, generic]))

        controller.selectLocalApp(hinted)
        XCTAssertEqual(controller.localAppPort, "3000")
        XCTAssertEqual(controller.portalName, "hermes")

        controller.portalName = "manual-name"
        controller.selectLocalApp(generic)
        XCTAssertEqual(controller.localAppPort, "8787")
        XCTAssertEqual(controller.portalName, "manual-name")

        controller.selectLocalApp(hinted)
        XCTAssertEqual(controller.portalName, "manual-name")
    }

    func testExplicitRefreshReplacesCandidatesAndPresentsOnlyFixedFailure() {
        let client = FakePortalHelperClient()
        let controller = PortalController(store: PortalStore(rootURL: temporaryRoot()), helper: client, openURL: { _ in })
        let first = LocalAppCandidatePayload(localAppPort: 3000, processLabel: "node", suggestedPortalName: "first")
        let replacement = LocalAppCandidatePayload(localAppPort: 8787, processLabel: "python3", suggestedPortalName: "replacement")
        client.completeDiscovery(.success([first]))
        controller.portalName = "manual-name"
        controller.localAppPort = "4321"

        controller.refreshLocalApps()
        XCTAssertTrue(controller.isRefreshingLocalApps)
        XCTAssertEqual(controller.localApps, [first])
        client.completeDiscovery(.success([replacement]), at: 1)
        XCTAssertEqual(controller.localApps, [replacement])
        XCTAssertNil(controller.localAppsMessage)

        controller.refreshLocalApps()
        client.completeDiscovery(.failure(NSError(domain: "secret=do-not-copy", code: 1)), at: 2)
        XCTAssertEqual(controller.localApps, [replacement])
        XCTAssertEqual(controller.localAppsMessage, "Local Apps could not be refreshed.")
        XCTAssertFalse(controller.localAppsMessage?.contains("do-not-copy") ?? true)
        XCTAssertEqual(controller.portalName, "manual-name")
        XCTAssertEqual(controller.localAppPort, "4321")
    }

    func testAddPersistsFinalEditableFieldsAndIgnoresStaleDiscovery() throws {
        let client = FakePortalHelperClient()
        let store = PortalStore(rootURL: temporaryRoot())
        try store.save(InstallationRecord(operationalLogging: .enabled))
        let controller = PortalController(
            store: store,
            helper: client,
            uuidProvider: { self.portalID },
            dateProvider: { Date(timeIntervalSince1970: 1_786_000_000) },
            openURL: { _ in }
        )
        let candidate = LocalAppCandidatePayload(localAppPort: 3000, processLabel: "node", suggestedPortalName: "hint")
        client.completeDiscovery(.success([candidate]))
        controller.selectLocalApp(candidate)
        controller.portalName = "final-name"
        controller.localAppPort = "4321"
        controller.refreshLocalApps()

        controller.addPortal()

        let portal = try XCTUnwrap(controller.portal)
        XCTAssertEqual(portal.name, "final-name")
        XCTAssertEqual(portal.localAppPort, 4321)
        XCTAssertEqual(try store.load(), portal)
        XCTAssertTrue(controller.localApps.isEmpty)
        XCTAssertFalse(controller.isRefreshingLocalApps)
        XCTAssertNil(controller.localAppsMessage)

        client.completeDiscovery(.success([
            LocalAppCandidatePayload(localAppPort: 9999, processLabel: "stale", suggestedPortalName: "stale")
        ]), at: 1)
        XCTAssertTrue(controller.localApps.isEmpty)
        XCTAssertNil(controller.localAppsMessage)
    }

    func testStartsSavedPortalAfterHandshakeWithSameDefinition() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        let saved = PortalConfiguration(id: portalID, name: "hermes", localAppPort: 8787, createdAt: Date(timeIntervalSince1970: 1_786_000_000))
        try store.save(InstallationRecord(portals: [saved], operationalLogging: .enabled))
        let client = FakePortalHelperClient(availability: .connecting)
        let controller = PortalController(store: store, helper: client, openURL: { _ in })

        XCTAssertEqual(controller.portal, saved)
        XCTAssertTrue(client.started.isEmpty)

        client.connect()
        XCTAssertEqual(client.started, [saved])
        XCTAssertEqual(client.discoveryCompletions.count, 1)
    }

    func testConnectionReconcilesOneFullActiveSnapshotAndOmitsPendingCleanup() throws {
        let stoppedID = UUID(uuidString: "5ea74329-3144-4ba2-925f-138d14d61fcc")!
        let pendingID = UUID(uuidString: "1f93e456-69ec-445a-8374-d7fc5558d0c7")!
        let enabled = PortalConfiguration(
            id: portalID,
            name: "hermes",
            localAppPort: 8787,
            createdAt: Date(),
            desiredState: .enabled
        )
        let stopped = PortalConfiguration(
            id: stoppedID,
            name: "atlas",
            localAppPort: 8788,
            createdAt: Date(),
            desiredState: .stopped
        )
        let pending = PortalConfiguration(
            id: pendingID,
            name: "rejected",
            localAppPort: 8789,
            createdAt: Date(),
            lifecycle: .pendingTailnetRejection
        )
        let store = PortalStore(rootURL: temporaryRoot())
        try store.save(InstallationRecord(portals: [enabled, pending, stopped]))
        let client = FakePortalHelperClient(availability: .connecting)
        let controller = PortalController(store: store, helper: client, openURL: { _ in })

        client.connect()

        _ = controller
        XCTAssertEqual(client.reconciliations, [[enabled, stopped]])
        XCTAssertEqual(client.cleaned, [pendingID])
    }

    func testDesiredStateAndPortPersistBeforeReconciliation() throws {
        let saved = PortalConfiguration(
            id: portalID,
            name: "hermes",
            localAppPort: 8787,
            createdAt: Date()
        )
        let store = PortalStore(rootURL: temporaryRoot())
        try store.save(saved)
        let client = FakePortalHelperClient()
        let controller = PortalController(store: store, helper: client, openURL: { _ in })
        client.onReconcile = { snapshot in
            XCTAssertEqual(try? store.loadInstallation().portals, snapshot)
        }

        controller.stopPortal(id: portalID)
        controller.updateLocalAppPort(id: portalID, port: "4321")

        XCTAssertEqual(client.reconciliations.last?.first?.desiredState, .stopped)
        XCTAssertEqual(client.reconciliations.last?.first?.localAppPort, 4321)
        XCTAssertEqual(try store.loadInstallation().portals.first?.desiredState, .stopped)
        XCTAssertEqual(try store.loadInstallation().portals.first?.localAppPort, 4321)
    }

    func testSingleFlightCoalescesLatestSnapshotAndIgnoresStaleFailure() throws {
        let saved = PortalConfiguration(
            id: portalID,
            name: "hermes",
            localAppPort: 8787,
            createdAt: Date()
        )
        let store = PortalStore(rootURL: temporaryRoot())
        try store.save(saved)
        let client = FakePortalHelperClient()
        client.completeReconciliationImmediately = false
        let controller = PortalController(store: store, helper: client, openURL: { _ in })

        controller.stopPortal(id: portalID)
        controller.startPortal(id: portalID)
        XCTAssertEqual(client.reconciliations.count, 1)

        client.completeReconciliation(.success(ReconcilePortalsResult(entries: [
            ReconcilePortalEntry(portalId: portalID, outcome: .startFailed)
        ])))

        XCTAssertNil(controller.message)
        XCTAssertEqual(client.reconciliations.count, 2)
        XCTAssertEqual(client.reconciliations.last?.first?.desiredState, .enabled)
        client.completeReconciliation(.success(ReconcilePortalsResult(entries: [
            ReconcilePortalEntry(portalId: portalID, outcome: .converged)
        ])), at: 1)
        XCTAssertNil(controller.message)
    }

    func testInvalidPortEditDoesNotPersistOrReconcile() throws {
        let saved = PortalConfiguration(
            id: portalID,
            name: "hermes",
            localAppPort: 8787,
            createdAt: Date(),
            desiredState: .stopped
        )
        let store = PortalStore(rootURL: temporaryRoot())
        try store.save(saved)
        let client = FakePortalHelperClient()
        let controller = PortalController(store: store, helper: client, openURL: { _ in })
        let originalReconciliationCount = client.reconciliations.count

        controller.updateLocalAppPort(id: portalID, port: "0")

        XCTAssertEqual(try store.loadInstallation().portals, [saved])
        XCTAssertEqual(client.reconciliations.count, originalReconciliationCount)
        XCTAssertEqual(controller.message, "Enter a port from 1 through 65535.")
    }

    func testAlreadySatisfiedStartAndStopAreHarmless() throws {
        let saved = PortalConfiguration(
            id: portalID,
            name: "hermes",
            localAppPort: 8787,
            createdAt: Date()
        )
        let store = PortalStore(rootURL: temporaryRoot())
        try store.save(saved)
        let client = FakePortalHelperClient()
        let controller = PortalController(store: store, helper: client, openURL: { _ in })

        controller.startPortal(id: portalID)
        XCTAssertEqual(client.reconciliations.count, 1)
        controller.stopPortal(id: portalID)
        XCTAssertEqual(client.reconciliations.count, 2)
        controller.stopPortal(id: portalID)

        XCTAssertEqual(client.reconciliations.count, 2)
        XCTAssertEqual(try store.loadInstallation().portals.first?.desiredState, .stopped)
    }

    func testCommittedStartAndStopRecordDesiredStateWithoutReachabilityProbe() throws {
        let saved = PortalConfiguration(
            id: portalID,
            name: "hermes",
            localAppPort: 8787,
            createdAt: Date()
        )
        let store = PortalStore(rootURL: temporaryRoot())
        try store.save(saved)
        let history = DiagnosticHistory()
        let probe = FakeLocalAppProbe()
        let reachability = LocalAppReachability(
            probe: probe,
            scheduler: FakePorticoScheduler()
        )
        let controller = PortalController(
            store: store,
            helper: FakePortalHelperClient(),
            reachability: reachability,
            history: history,
            openURL: { _ in }
        )

        controller.stopPortal(id: portalID)
        controller.startPortal(id: portalID)

        XCTAssertEqual(history.entries.map(\.event), [
            .portal(
                name: "hermes",
                desired: .enabled,
                tailscale: nil,
                reachability: .unknown,
                stale: false
            ),
            .portal(
                name: "hermes",
                desired: .stopped,
                tailscale: nil,
                reachability: .unknown,
                stale: false
            ),
            .portal(
                name: "hermes",
                desired: .enabled,
                tailscale: nil,
                reachability: .unknown,
                stale: false
            ),
        ])
        XCTAssertEqual(probe.requests.count, 1)
    }

    func testCurrentPartialFailureKeepsCommittedDesiredState() throws {
        let saved = PortalConfiguration(
            id: portalID,
            name: "hermes",
            localAppPort: 8787,
            createdAt: Date()
        )
        let store = PortalStore(rootURL: temporaryRoot())
        try store.save(saved)
        let client = FakePortalHelperClient(availability: .connecting)
        client.completeReconciliationImmediately = false
        let controller = PortalController(store: store, helper: client, openURL: { _ in })

        controller.stopPortal(id: portalID)
        client.connect()
        client.completeReconciliation(.success(ReconcilePortalsResult(entries: [
            ReconcilePortalEntry(portalId: portalID, outcome: .closeFailed)
        ])))

        XCTAssertEqual(controller.portals.first?.desiredState, .stopped)
        XCTAssertEqual(try store.loadInstallation().portals.first?.desiredState, .stopped)
        XCTAssertEqual(controller.message, "Some Portals could not be reconciled.")
    }

    func testRemovalCommitsBeforePublishingAndOmitsPortalBeforeDeletingState() throws {
        let otherID = UUID(uuidString: "5ea74329-3144-4ba2-925f-138d14d61fcc")!
        let removedPortal = PortalConfiguration(
            id: portalID,
            name: "hermes",
            localAppPort: 8787,
            createdAt: Date()
        )
        let otherPortal = PortalConfiguration(
            id: otherID,
            name: "atlas",
            localAppPort: 8788,
            createdAt: Date()
        )
        let store = PortalStore(rootURL: temporaryRoot())
        try store.save(InstallationRecord(portals: [removedPortal, otherPortal]))
        let client = FakePortalHelperClient(availability: .connecting)
        client.completeReconciliationImmediately = false
        client.completeRemovalImmediately = false
        let controller = PortalController(store: store, helper: client, openURL: { _ in })
        client.send(.status(portalID, PortalStatusPayload(
            state: .connecting,
            stableNodeId: "secret-node",
            assignedName: "hermes-1",
            portalURL: nil,
            addresses: []
        )))

        XCTAssertEqual(
            controller.removalWarningText(for: removedPortal),
            "Remove Portal “hermes” from this Mac? This permanently deletes its local configuration and local Tailscale identity state. The Tailscale device “hermes-1” may remain in the tailnet and may require manual removal."
        )
        controller.removePortal(id: portalID)

        let persisted = try XCTUnwrap(store.loadInstallation().portals.first(where: { $0.id == portalID }))
        XCTAssertEqual(persisted.lifecycle, .pendingRemoval)
        XCTAssertEqual(persisted.removalAssignedName, "hermes-1")
        XCTAssertEqual(controller.pendingRemovalPortals.map(\.id), [portalID])
        XCTAssertEqual(
            controller.pendingRemovalWarningText(for: persisted),
            "Removing Portal “hermes” from this Mac permanently deletes its local configuration and local Tailscale identity state. The Tailscale device “hermes-1” may remain in the tailnet and may require manual removal."
        )
        XCTAssertNil(controller.statuses[portalID])
        XCTAssertTrue(client.removed.isEmpty)

        client.connect()

        XCTAssertEqual(client.reconciliations, [[otherPortal]])
        XCTAssertTrue(client.removed.isEmpty)
        client.completeReconciliation(.success(ReconcilePortalsResult(entries: [])))
        XCTAssertEqual(client.removed, [portalID])
        XCTAssertEqual(controller.removalStates[portalID], .removing)

        controller.startPortal(id: portalID)
        controller.updateLocalAppPort(id: portalID, port: "9999")
        controller.authenticate(id: portalID)
        XCTAssertEqual(client.authenticated, [])
        XCTAssertEqual(try store.loadInstallation().portals.first(where: { $0.id == portalID }), persisted)
    }

    func testPendingRemovalGetsOneAutomaticCycleAcrossReconnectsAndExplicitRetryAddsOne() throws {
        let pending = PortalConfiguration(
            id: portalID,
            name: "hermes",
            localAppPort: 8787,
            createdAt: Date(),
            lifecycle: .pendingRemoval,
            removalAssignedName: "hermes-1"
        )
        let store = PortalStore(rootURL: temporaryRoot())
        try store.save(InstallationRecord(portals: [pending]))
        let client = FakePortalHelperClient(availability: .connecting)
        client.completeRemovalImmediately = false
        let controller = PortalController(store: store, helper: client, openURL: { _ in })

        client.connect()
        XCTAssertEqual(client.reconciliations, [[]])
        XCTAssertEqual(client.removed, [portalID])
        client.completeRemoval(.failure(NSError(domain: "secret", code: 1)))
        XCTAssertEqual(controller.removalStates[portalID], .failed)

        client.disconnect(as: .retrying(attempt: 1, delay: 1))
        client.connect()
        XCTAssertEqual(client.reconciliations, [[], []])
        XCTAssertEqual(client.removed, [portalID])

        controller.retryRemoval(id: portalID)
        XCTAssertEqual(client.reconciliations, [[], [], []])
        XCTAssertEqual(client.removed, [portalID, portalID])
    }

    func testRemovalWaitsForNewestActiveOnlyReconciliationAndSuppressesStaleEvents() throws {
        let pending = PortalConfiguration(
            id: portalID,
            name: "hermes",
            localAppPort: 8787,
            createdAt: Date(),
            lifecycle: .pendingRemoval,
            removalAssignedName: "hermes-1"
        )
        let store = PortalStore(rootURL: temporaryRoot())
        try store.save(InstallationRecord(portals: [pending]))
        let client = FakePortalHelperClient(availability: .connecting)
        client.completeReconciliationImmediately = false
        let controller = PortalController(store: store, helper: client, openURL: { _ in })

        client.connect()
        client.send(.status(portalID, PortalStatusPayload(
            state: .online,
            stableNodeId: "stale-node",
            assignedName: "stale-name",
            portalURL: URL(string: "https://stale.example.ts.net/"),
            addresses: ["100.64.0.1"]
        )))
        client.send(.authenticationURL(portalID, URL(string: "https://login.tailscale.com/a/stale")!))

        XCTAssertNil(controller.statuses[portalID])
        client.completeReconciliation(.success(ReconcilePortalsResult(entries: [
            ReconcilePortalEntry(portalId: portalID, outcome: .closeFailed)
        ])))
        XCTAssertTrue(client.removed.isEmpty)
        XCTAssertEqual(controller.removalStates[portalID], .failed)

        controller.retryRemoval(id: portalID)
        client.completeReconciliation(.success(ReconcilePortalsResult(entries: [])), at: 1)
        XCTAssertEqual(client.removed, [portalID])
    }

    func testRemovalCompletionRetainsPendingRecordUntilAtomicSaveSucceeds() throws {
        let root = temporaryRoot()
        let seedStore = PortalStore(rootURL: root)
        let pending = PortalConfiguration(
            id: portalID,
            name: "hermes",
            localAppPort: 8787,
            createdAt: Date(),
            lifecycle: .pendingRemoval,
            removalAssignedName: "hermes-1"
        )
        try seedStore.save(InstallationRecord(
            tailnetBinding: TailnetBinding(name: "opaque-tailnet", magicDNSSuffix: "example.ts.net"),
            portals: [pending]
        ))
        var failNextWrite = true
        let store = PortalStore(rootURL: root, writeData: { data, url in
            if failNextWrite {
                failNextWrite = false
                throw NSError(domain: "secret", code: 1)
            }
            try data.write(to: url, options: .atomic)
        })
        let client = FakePortalHelperClient()
        let controller = PortalController(store: store, helper: client, openURL: { _ in })

        XCTAssertEqual(client.removed, [portalID])
        XCTAssertEqual(try store.loadInstallation().portals, [pending])
        XCTAssertEqual(controller.pendingRemovalPortals, [pending])
        XCTAssertEqual(controller.removalStates[portalID], .failed)
        XCTAssertTrue(controller.removalNotices.isEmpty)

        controller.retryRemoval(id: portalID)

        XCTAssertEqual(client.removed, [portalID, portalID])
        XCTAssertTrue(try store.loadInstallation().portals.isEmpty)
        XCTAssertTrue(controller.portals.isEmpty)
        XCTAssertEqual(controller.tailnetDisplaySuffix, "example.ts.net")
        XCTAssertEqual(
            controller.removalNotices,
            [PortalRemovalNotice(id: portalID, portalName: "hermes", assignedName: "hermes-1")]
        )
        XCTAssertEqual(
            controller.removalNoticeText(for: controller.removalNotices[0]),
            "Removed Portal “hermes” from this Mac. The Tailscale device “hermes-1” may remain in the tailnet and may require manual removal."
        )
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
            addresses: ["100.64.0.1"],
            magicDNSSuffix: "example.ts.net"
        )
        let displayStatus = PortalStatusPayload(
            state: .online,
            stableNodeId: nil,
            assignedName: "hermes-1",
            portalURL: URL(string: "https://hermes-1.example.ts.net/"),
            addresses: ["100.64.0.1"],
            magicDNSSuffix: "example.ts.net"
        )

        client.send(.status(UUID(), status))
        XCTAssertNil(controller.status)
        client.send(.status(portalID, status))
        XCTAssertEqual(controller.status, displayStatus)
    }

    func testOpensAuthenticationURLOnlyForPendingExplicitAction() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        let saved = PortalConfiguration(id: portalID, name: "hermes", localAppPort: 8787, createdAt: Date())
        try store.save(saved)
        let client = FakePortalHelperClient()
        var opened: [URL] = []
        let controller = PortalController(store: store, helper: client, openURL: { opened.append($0) })
        let transient = URL(string: "https://login.tailscale.com/a/transient")!
        client.send(.status(portalID, PortalStatusPayload(
            state: .authenticating,
            stableNodeId: nil,
            assignedName: nil,
            portalURL: nil,
            addresses: []
        )))

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

    func testAddsAndStartsSecondPortalWhileFirstRemainsEnabled() throws {
        let firstID = portalID
        let secondID = UUID(uuidString: "5ea74329-3144-4ba2-925f-138d14d61fcc")!
        let first = PortalConfiguration(id: firstID, name: "hermes", localAppPort: 8787, createdAt: Date())
        let store = PortalStore(rootURL: temporaryRoot())
        try store.save(InstallationRecord(portals: [first], operationalLogging: .enabled))
        let client = FakePortalHelperClient()
        let controller = PortalController(
            store: store,
            helper: client,
            uuidProvider: { secondID },
            dateProvider: { Date(timeIntervalSince1970: 1_786_000_100) },
            openURL: { _ in }
        )

        controller.portalName = "atlas"
        controller.localAppPort = "8788"
        controller.addPortal()

        XCTAssertEqual(controller.portals.map(\.id), [firstID, secondID])
        XCTAssertEqual(client.reconciliations, [[first], try store.loadInstallation().portals])
        XCTAssertEqual(try store.loadInstallation().portals.map(\.id), [firstID, secondID])
    }

    func testFirstSuccessfulOnlineSaveWinsAndExactMatchRefreshesSuffix() throws {
        let secondID = UUID(uuidString: "5ea74329-3144-4ba2-925f-138d14d61fcc")!
        let root = temporaryRoot()
        let seed = PortalStore(rootURL: root)
        try seed.save(InstallationRecord(portals: [
            PortalConfiguration(id: portalID, name: "hermes", localAppPort: 8787, createdAt: Date()),
            PortalConfiguration(id: secondID, name: "atlas", localAppPort: 8788, createdAt: Date()),
        ]))
        var failNextWrite = true
        let store = PortalStore(rootURL: root, writeData: { data, url in
            if failNextWrite {
                failNextWrite = false
                throw NSError(domain: "expected", code: 1)
            }
            try data.write(to: url, options: .atomic)
        })
        let client = FakePortalHelperClient()
        let controller = PortalController(store: store, helper: client, openURL: { _ in })

        client.send(.status(portalID, onlineStatus(tailnetName: "opaque-first", suffix: "first.ts.net")))
        XCTAssertNil(try store.loadInstallation().tailnetBinding)
        client.send(.status(secondID, onlineStatus(tailnetName: "opaque-winner", suffix: "winner.ts.net")))
        XCTAssertEqual(
            try store.loadInstallation().tailnetBinding,
            TailnetBinding(name: "opaque-winner", magicDNSSuffix: "winner.ts.net")
        )
        XCTAssertNil(controller.statuses[secondID]?.tailnetName)
        client.send(.status(portalID, onlineStatus(tailnetName: "opaque-winner", suffix: "refreshed.ts.net")))
        XCTAssertEqual(controller.tailnetDisplaySuffix, "refreshed.ts.net")
        XCTAssertEqual(try store.loadInstallation().tailnetBinding?.magicDNSSuffix, "refreshed.ts.net")
        XCTAssertFalse(controller.message?.contains("opaque") ?? false)
    }

    func testEmptyTailnetNameDoesNotBindInstallation() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        try store.save(InstallationRecord(portals: [
            PortalConfiguration(id: portalID, name: "hermes", localAppPort: 8787, createdAt: Date())
        ]))
        let client = FakePortalHelperClient()
        let controller = PortalController(store: store, helper: client, openURL: { _ in })

        client.send(.status(portalID, onlineStatus(tailnetName: "", suffix: "example.ts.net")))

        XCTAssertNil(try store.loadInstallation().tailnetBinding)
        XCTAssertNil(controller.statuses[portalID]?.tailnetName)
    }

    func testDifferentTailnetNameWithSameSuffixRejectsOnlyAddressedPortal() throws {
        let secondID = UUID(uuidString: "5ea74329-3144-4ba2-925f-138d14d61fcc")!
        let first = PortalConfiguration(id: portalID, name: "hermes", localAppPort: 8787, createdAt: Date())
        let second = PortalConfiguration(id: secondID, name: "atlas", localAppPort: 8788, createdAt: Date())
        let store = PortalStore(rootURL: temporaryRoot())
        try store.save(InstallationRecord(
            tailnetBinding: TailnetBinding(name: "opaque-expected", magicDNSSuffix: "shared.ts.net"),
            portals: [first, second]
        ))
        let client = FakePortalHelperClient()
        client.completeCleanupImmediately = false
        let controller = PortalController(store: store, helper: client, openURL: { _ in })

        client.send(.status(portalID, onlineStatus(
            tailnetName: "opaque-different",
            suffix: "shared.ts.net"
        )))
        client.send(.status(secondID, onlineStatus(
            tailnetName: "opaque-expected",
            suffix: "shared.ts.net",
            assignedName: "atlas-1"
        )))
        client.completeCleanup(.failure(NSError(domain: "expected", code: 1)))

        XCTAssertEqual(client.cleaned, [portalID])
        XCTAssertEqual(controller.pendingPortals.map(\.id), [portalID])
        XCTAssertEqual(controller.portals.first(where: { $0.id == secondID })?.lifecycle, .active)
        XCTAssertEqual(controller.statuses[secondID]?.state, .online)
        XCTAssertEqual(client.reconciliations.first?.map(\.id), [portalID, secondID])
        XCTAssertEqual(client.reconciliations.last?.map(\.id), [secondID])
    }

    func testCompletedCleanupPersistenceFailureKeepsPendingAndUnrelatedPortalEnabled() throws {
        struct ExpectedFailure: Error {}
        let secondID = UUID(uuidString: "5ea74329-3144-4ba2-925f-138d14d61fcc")!
        let root = temporaryRoot()
        let seed = PortalStore(rootURL: root)
        try seed.save(InstallationRecord(
            tailnetBinding: TailnetBinding(name: "opaque-expected", magicDNSSuffix: "expected.ts.net"),
            portals: [
                PortalConfiguration(id: portalID, name: "hermes", localAppPort: 8787, createdAt: Date()),
                PortalConfiguration(id: secondID, name: "atlas", localAppPort: 8788, createdAt: Date()),
            ]
        ))
        var writeCount = 0
        let store = PortalStore(rootURL: root, writeData: { data, url in
            writeCount += 1
            if writeCount == 2 { throw ExpectedFailure() }
            try data.write(to: url, options: .atomic)
        })
        let client = FakePortalHelperClient()
        let controller = PortalController(store: store, helper: client, openURL: { _ in })

        client.send(.status(portalID, onlineStatus(
            tailnetName: "opaque-different",
            suffix: "rejected.ts.net"
        )))

        let persisted = try store.loadInstallation()
        XCTAssertEqual(persisted.portals.first(where: { $0.id == portalID })?.lifecycle, .pendingTailnetRejection)
        XCTAssertEqual(persisted.portals.first(where: { $0.id == secondID })?.lifecycle, .active)
        XCTAssertEqual(controller.pendingPortals.map(\.id), [portalID])
        XCTAssertEqual(controller.portals.first(where: { $0.id == secondID })?.lifecycle, .active)
        XCTAssertEqual(client.reconciliations.first?.map(\.id), [portalID, secondID])
        XCTAssertEqual(client.reconciliations.last?.map(\.id), [secondID])
    }

    func testMismatchPersistsTombstoneBeforeCleanupAndRelaunchNeverRestartsIt() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        let saved = PortalConfiguration(id: portalID, name: "hermes", localAppPort: 8787, createdAt: Date())
        try store.save(InstallationRecord(
            tailnetBinding: TailnetBinding(name: "opaque-expected", magicDNSSuffix: "expected.ts.net"),
            portals: [saved]
        ))
        let client = FakePortalHelperClient()
        client.completeCleanupImmediately = false
        client.onCleanup = { id in
            XCTAssertEqual(id, self.portalID)
            XCTAssertEqual(try? store.loadInstallation().portals.first?.lifecycle, .pendingTailnetRejection)
        }
        let controller = PortalController(store: store, helper: client, openURL: { _ in })

        client.send(.status(portalID, onlineStatus(
            tailnetName: "opaque-rejected",
            suffix: "rejected.ts.net",
            assignedName: "hermes-rejected"
        )))

        XCTAssertEqual(controller.pendingPortals.map(\.id), [portalID])
        XCTAssertEqual(client.cleaned, [portalID])
        XCTAssertFalse(controller.pendingWarningText(for: controller.pendingPortals[0]).contains("opaque"))

        let relaunchedClient = FakePortalHelperClient()
        relaunchedClient.completeCleanupImmediately = false
        _ = PortalController(store: store, helper: relaunchedClient, openURL: { _ in })
        XCTAssertTrue(relaunchedClient.started.isEmpty)
        XCTAssertEqual(relaunchedClient.cleaned, [portalID])
    }

    func testCompletedCleanupRemovesPortalPersistsSafeAlertAndCanBeDismissed() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        let saved = PortalConfiguration(id: portalID, name: "hermes", localAppPort: 8787, createdAt: Date())
        try store.save(InstallationRecord(
            tailnetBinding: TailnetBinding(name: "opaque-expected", magicDNSSuffix: "expected.ts.net"),
            portals: [saved]
        ))
        let alertID = UUID(uuidString: "1f93e456-69ec-445a-8374-d7fc5558d0c7")!
        let client = FakePortalHelperClient()
        client.completeCleanupImmediately = false
        let controller = PortalController(
            store: store,
            helper: client,
            uuidProvider: { alertID },
            dateProvider: { Date(timeIntervalSince1970: 1_786_000_100) },
            openURL: { _ in }
        )
        client.send(.status(portalID, onlineStatus(
            tailnetName: "opaque-rejected",
            suffix: "rejected.ts.net",
            assignedName: "hermes-rejected"
        )))

        client.completeCleanup(.success(()))

        XCTAssertTrue(controller.portals.isEmpty)
        let alert = try XCTUnwrap(controller.alerts.first)
        XCTAssertEqual(alert.id, alertID)
        XCTAssertEqual(alert.assignedName, "hermes-rejected")
        XCTAssertEqual(alert.expectedMagicDNSSuffix, "expected.ts.net")
        XCTAssertEqual(alert.rejectedMagicDNSSuffix, "rejected.ts.net")
        XCTAssertFalse(controller.completedWarningText(for: alert).contains("opaque"))
        XCTAssertEqual(PortalController.manualRemovalURL.host, "tailscale.com")

        controller.dismissAlert(id: alertID)
        XCTAssertTrue(controller.alerts.isEmpty)
        XCTAssertEqual(try store.loadInstallation().tailnetBinding?.name, "opaque-expected")
    }

    func testResetRequiresEmptyInstallationAndExplicitConfirmation() throws {
        let root = temporaryRoot()
        let store = PortalStore(rootURL: root)
        let saved = PortalConfiguration(id: portalID, name: "hermes", localAppPort: 8787, createdAt: Date())
        try store.save(InstallationRecord(
            tailnetBinding: TailnetBinding(name: "opaque-expected", magicDNSSuffix: "expected.ts.net"),
            portals: [saved]
        ))
        var controller = PortalController(store: store, helper: FakePortalHelperClient(), openURL: { _ in })
        controller.resetTailnet(confirmed: true)
        XCTAssertEqual(try store.loadInstallation().tailnetBinding?.name, "opaque-expected")

        try store.save(InstallationRecord(
            tailnetBinding: TailnetBinding(name: "opaque-expected", magicDNSSuffix: "expected.ts.net")
        ))
        controller = PortalController(store: store, helper: FakePortalHelperClient(), openURL: { _ in })
        XCTAssertTrue(controller.canResetTailnet)
        controller.resetTailnet(confirmed: false)
        XCTAssertNotNil(try store.loadInstallation().tailnetBinding)
        controller.resetTailnet(confirmed: true)
        XCTAssertNil(try store.loadInstallation().tailnetBinding)
    }

    func testRecoveryMarksFactsStaleAndReconcilesNewestCommittedSnapshot() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        let saved = PortalConfiguration(id: portalID, name: "hermes", localAppPort: 8787, createdAt: Date())
        try store.save(InstallationRecord(portals: [saved]))
        let client = FakePortalHelperClient(availability: .connecting)
        let controller = PortalController(store: store, helper: client, openURL: { _ in })
        client.connect()
        client.send(.status(portalID, onlineStatus(tailnetName: "opaque", suffix: "example.ts.net")))

        XCTAssertEqual(client.reconciliations, [[saved]])
        XCTAssertFalse(controller.staleStatusIDs.contains(portalID))

        client.disconnect(as: .retrying(attempt: 1, delay: 1))
        controller.stopPortal(id: portalID)
        XCTAssertTrue(controller.staleStatusIDs.contains(portalID))
        XCTAssertEqual(client.reconciliations.count, 1)

        client.connect()

        XCTAssertEqual(client.reconciliations.count, 2)
        XCTAssertEqual(client.reconciliations.last?.first?.desiredState, .stopped)
        XCTAssertTrue(controller.staleStatusIDs.contains(portalID))
        client.send(.status(portalID, PortalStatusPayload(
            state: .stopped,
            stableNodeId: nil,
            assignedName: "hermes-1",
            portalURL: URL(string: "https://hermes-1.example.ts.net/"),
            addresses: ["100.64.0.1"],
            magicDNSSuffix: "example.ts.net"
        )))
        XCTAssertFalse(controller.staleStatusIDs.contains(portalID))

        controller.retryHelper()
        XCTAssertEqual(client.retryCount, 1)
    }

    func testConnectionLossCannotStrandPendingNewestReconciliation() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        let saved = PortalConfiguration(id: portalID, name: "hermes", localAppPort: 8787, createdAt: Date())
        try store.save(InstallationRecord(portals: [saved]))
        let launcher = FakeHelperLauncher()
        let scheduler = FakePorticoScheduler()
        var requestIDs = [
            "handshake-1", "discover-1", "reconcile-1",
            "handshake-2", "discover-2", "reconcile-2",
        ]
        let supervisor = HelperSupervisor(
            helperURL: URL(fileURLWithPath: "/unused/portico-helper"),
            launcher: launcher,
            requestIDProvider: { requestIDs.removeFirst() },
            scheduler: scheduler,
            handshakeTimeout: 60
        )
        let controller = PortalController(store: store, helper: supervisor, openURL: { _ in })
        supervisor.start(loggingPreference: .enabled)
        launcher.receive(line: #"{"version":3,"requestId":"handshake-1","result":{"protocolVersion":3}}"#)
        controller.stopPortal(id: portalID)

        launcher.exit(status: 1)
        scheduler.run(delay: 1)
        launcher.receive(line: #"{"version":3,"requestId":"handshake-2","result":{"protocolVersion":3}}"#)

        let request = try JSONDecoder().decode(
            HelperRequest<ReconcilePortalsPayload>.self,
            from: XCTUnwrap(launcher.process.sent.last)
        )
        XCTAssertEqual(request.command, .reconcilePortals)
        XCTAssertEqual(request.payload.portals.first?.desiredState, .stopped)
    }

    func testHostileHelperFactsAreExcludedFromDisplayAndReport() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        let saved = PortalConfiguration(id: portalID, name: "hermes", localAppPort: 8787, createdAt: Date())
        try store.save(InstallationRecord(portals: [saved]))
        let client = FakePortalHelperClient()
        let controller = PortalController(store: store, helper: client, openURL: { _ in })

        client.send(.status(portalID, PortalStatusPayload(
            state: .connecting,
            stableNodeId: "secret-stable-node",
            assignedName: "login",
            portalURL: URL(string: "file:///Users/chris/private/auth"),
            addresses: ["Authorization: Bearer secret"],
            magicDNSSuffix: "tailscale.com"
        )))

        XCTAssertNil(controller.statuses[portalID]?.stableNodeId)
        XCTAssertEqual(controller.statuses[portalID]?.assignedName, "login")
        XCTAssertNil(controller.statuses[portalID]?.portalURL)
        XCTAssertTrue(controller.statuses[portalID]?.addresses.isEmpty == true)
        XCTAssertNil(controller.statuses[portalID]?.magicDNSSuffix)
        let report = controller.diagnosticReport()
        for excluded in ["secret-stable-node", "file:///Users/chris/private/auth", "Bearer secret", "tailscale.com"] {
            XCTAssertFalse(report.contains(excluded), excluded)
        }
    }

    func testOnlineHostileFactsCannotBindOrPersistRejectionEvidence() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        let saved = PortalConfiguration(id: portalID, name: "hermes", localAppPort: 8787, createdAt: Date())
        try store.save(InstallationRecord(portals: [saved]))
        let client = FakePortalHelperClient()
        let controller = PortalController(store: store, helper: client, openURL: { _ in })
        let unsafeSuffix = "file:///Users/chris/private"

        client.send(.status(portalID, PortalStatusPayload(
            state: .online,
            stableNodeId: "secret-node",
            assignedName: "bad\nname",
            portalURL: URL(string: "file:///Users/chris/private/auth"),
            addresses: ["Authorization: Bearer secret"],
            tailnetName: "opaque-first",
            magicDNSSuffix: unsafeSuffix
        )))

        XCTAssertNil(try store.loadInstallation().tailnetBinding)
        XCTAssertNil(controller.tailnetDisplaySuffix)

        try store.save(InstallationRecord(
            tailnetBinding: TailnetBinding(name: "opaque-expected", magicDNSSuffix: "expected.ts.net"),
            portals: [saved]
        ))
        let rebound = PortalController(store: store, helper: client, openURL: { _ in })
        client.send(.status(portalID, PortalStatusPayload(
            state: .online,
            stableNodeId: "secret-node",
            assignedName: "bad\nname",
            portalURL: URL(string: "file:///Users/chris/private/auth"),
            addresses: ["Authorization: Bearer secret"],
            tailnetName: "opaque-different",
            magicDNSSuffix: unsafeSuffix
        )))

        let alert = try XCTUnwrap(rebound.alerts.first)
        XCTAssertNil(alert.assignedName)
        XCTAssertNil(alert.rejectedMagicDNSSuffix)
        XCTAssertFalse(rebound.completedWarningText(for: alert).contains(unsafeSuffix))
    }

    private func onlineStatus(
        tailnetName: String,
        suffix: String,
        assignedName: String = "hermes-1"
    ) -> PortalStatusPayload {
        PortalStatusPayload(
            state: .online,
            stableNodeId: "node-1",
            assignedName: assignedName,
            portalURL: URL(string: "https://\(assignedName).\(suffix)/"),
            addresses: ["100.64.0.1"],
            tailnetName: tailnetName,
            magicDNSSuffix: suffix
        )
    }

    private func temporaryRoot() -> URL {
        FileManager.default.temporaryDirectory.appendingPathComponent("PorticoControllerTests-\(UUID().uuidString)", isDirectory: true)
    }
}

@MainActor
final class FakePortalHelperClient: PortalHelperClient {
    var availability: HelperAvailability
    var onConnected: (() -> Void)?
    var onAvailabilityChange: ((HelperAvailability) -> Void)?
    var onEvent: ((PortalHelperEvent) -> Void)?
    private(set) var reconciliations: [[PortalConfiguration]] = []
    var started: [PortalConfiguration] { reconciliations.flatMap { $0 } }
    private(set) var authenticated: [UUID] = []
    private(set) var cleaned: [UUID] = []
    private(set) var removed: [UUID] = []
    private(set) var discoveryCompletions: [(Result<[LocalAppCandidatePayload], Error>) -> Void] = []
    private(set) var cleanupCompletions: [(Result<Void, Error>) -> Void] = []
    private(set) var removalCompletions: [(Result<Void, Error>) -> Void] = []
    private(set) var reconciliationCompletions: [(Result<ReconcilePortalsResult, Error>) -> Void] = []
    var completeCleanupImmediately = true
    var completeRemovalImmediately = true
    var completeReconciliationImmediately = true
    var onCleanup: ((UUID) -> Void)?
    var onReconcile: (([PortalConfiguration]) -> Void)?
    private(set) var retryCount = 0
    private(set) var restartedWith: [OperationalLoggingPreference] = []
    var onRestart: ((OperationalLoggingPreference) -> Void)?

    init(availability: HelperAvailability = .connected) {
        self.availability = availability
    }

    func reconcilePortals(
        _ portals: [PortalConfiguration],
        completion: @escaping (Result<ReconcilePortalsResult, Error>) -> Void
    ) {
        reconciliations.append(portals)
        onReconcile?(portals)
        if completeReconciliationImmediately {
            completion(.success(ReconcilePortalsResult(entries: portals.map {
                ReconcilePortalEntry(portalId: $0.id, outcome: .converged)
            })))
        } else {
            reconciliationCompletions.append(completion)
        }
    }

    func retry() { retryCount += 1 }

    func restart(loggingPreference: OperationalLoggingPreference) {
        restartedWith.append(loggingPreference)
        onRestart?(loggingPreference)
    }

    func authenticatePortal(id: UUID, completion: @escaping (Result<Void, Error>) -> Void) {
        authenticated.append(id)
        completion(.success(()))
    }

    func cleanupRejectedPortal(id: UUID, completion: @escaping (Result<Void, Error>) -> Void) {
        cleaned.append(id)
        onCleanup?(id)
        if completeCleanupImmediately {
            completion(.success(()))
        } else {
            cleanupCompletions.append(completion)
        }
    }

    func removePortal(id: UUID, completion: @escaping (Result<Void, Error>) -> Void) {
        removed.append(id)
        if completeRemovalImmediately {
            completion(.success(()))
        } else {
            removalCompletions.append(completion)
        }
    }

    func discoverLocalApps(completion: @escaping (Result<[LocalAppCandidatePayload], Error>) -> Void) {
        discoveryCompletions.append(completion)
    }

    func connect() {
        availability = .connected
        onAvailabilityChange?(.connected)
        onConnected?()
    }

    func disconnect(as availability: HelperAvailability) {
        self.availability = availability
        onAvailabilityChange?(availability)
    }

    func send(_ event: PortalHelperEvent) { onEvent?(event) }

    func completeDiscovery(_ result: Result<[LocalAppCandidatePayload], Error>, at index: Int = 0) {
        discoveryCompletions[index](result)
    }

    func completeCleanup(_ result: Result<Void, Error>, at index: Int = 0) {
        cleanupCompletions[index](result)
    }

    func completeRemoval(_ result: Result<Void, Error>, at index: Int = 0) {
        removalCompletions[index](result)
    }

    func completeReconciliation(
        _ result: Result<ReconcilePortalsResult, Error>,
        at index: Int = 0
    ) {
        reconciliationCompletions[index](result)
    }
}
