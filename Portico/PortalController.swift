import Foundation

@MainActor
final class PortalController: ObservableObject {
    static let manualRemovalURL = URL(
        string: "https://tailscale.com/docs/features/access-control/device-management/how-to/remove"
    )!

    @Published var portalName = ""
    @Published var localAppPort = ""
    @Published private(set) var portals: [PortalConfiguration] = []
    @Published private(set) var statuses: [UUID: PortalStatusPayload] = [:]
    @Published private(set) var alerts: [InstallationAlert] = []
    @Published private(set) var tailnetDisplaySuffix: String?
    @Published private(set) var message: String?
    @Published private(set) var localApps: [LocalAppCandidatePayload] = []
    @Published private(set) var isRefreshingLocalApps = false
    @Published private(set) var localAppsMessage: String?

    var portal: PortalConfiguration? { portals.first }
    var status: PortalStatusPayload? { portal.flatMap { statuses[$0.id] } }
    var canResetTailnet: Bool { portals.isEmpty && installation.tailnetBinding != nil }
    var pendingPortals: [PortalConfiguration] {
        portals.filter { $0.lifecycle == .pendingTailnetRejection }
    }

    private let store: PortalStore
    private let helper: PortalHelperClient
    private let uuidProvider: () -> UUID
    private let dateProvider: () -> Date
    private let openURL: (URL) -> Void
    private var installation = InstallationRecord()
    private var authenticationPending: Set<UUID> = []
    private var cleanupInFlight: Set<UUID> = []
    private var discoveryGeneration = 0
    private var reconciliationGeneration = 0
    private var reconciliationInFlight = false
    private var pendingReconciliation: PortalReconciliation?

    init(
        store: PortalStore,
        helper: PortalHelperClient,
        uuidProvider: @escaping () -> UUID = UUID.init,
        dateProvider: @escaping () -> Date = Date.init,
        openURL: @escaping (URL) -> Void
    ) {
        self.store = store
        self.helper = helper
        self.uuidProvider = uuidProvider
        self.dateProvider = dateProvider
        self.openURL = openURL
        do {
            installation = try store.loadInstallation()
            publishInstallation()
        } catch {
            message = "Saved Portal configuration could not be loaded."
        }
        helper.onConnected = { [weak self] in self?.helperConnected() }
        helper.onEvent = { [weak self] event in self?.receive(event) }
        if helper.availability == .connected {
            helperConnected()
        }
    }

    func refreshLocalApps() {
        discoveryGeneration += 1
        let generation = discoveryGeneration
        isRefreshingLocalApps = true
        localAppsMessage = nil
        helper.discoverLocalApps { [weak self] result in
            guard let self, self.discoveryGeneration == generation else { return }
            self.isRefreshingLocalApps = false
            switch result {
            case let .success(candidates):
                self.localApps = candidates
            case .failure:
                self.localAppsMessage = "Local Apps could not be refreshed."
            }
        }
    }

    func selectLocalApp(_ candidate: LocalAppCandidatePayload) {
        guard localApps.contains(candidate) else { return }
        localAppPort = String(candidate.localAppPort)
        if portalName.isEmpty, let suggestion = candidate.suggestedPortalName {
            portalName = suggestion
        }
    }

    func addPortal() {
        let port: UInt16
        do {
            port = try PortalInputValidator.validate(name: portalName, port: localAppPort)
        } catch {
            message = "Enter a lower-case DNS label and a port from 1 through 65535."
            return
        }
        let configuration = PortalConfiguration(
            id: uuidProvider(),
            name: portalName,
            localAppPort: port,
            createdAt: dateProvider()
        )
        var updated = installation
        updated.portals.append(configuration)
        do {
            try store.save(updated)
            installation = updated
            publishInstallation()
            message = nil
            portalName = ""
            localAppPort = ""
            discoveryGeneration += 1
            localApps = []
            isRefreshingLocalApps = false
            localAppsMessage = nil
            scheduleReconciliation()
        } catch {
            message = "The Portal could not be saved."
        }
    }

    func startPortal(id: UUID) {
        updateDesiredState(id: id, desiredState: .enabled)
    }

    func stopPortal(id: UUID) {
        updateDesiredState(id: id, desiredState: .stopped)
    }

    func updateLocalAppPort(id: UUID, port: String) {
        guard let index = installation.portals.firstIndex(where: {
            $0.id == id && $0.lifecycle == .active
        }) else { return }
        let localAppPort: UInt16
        do {
            localAppPort = try PortalInputValidator.validate(
                name: installation.portals[index].name,
                port: port
            )
        } catch {
            message = "Enter a port from 1 through 65535."
            return
        }
        guard installation.portals[index].localAppPort != localAppPort else { return }
        var updated = installation
        updated.portals[index].localAppPort = localAppPort
        savePortalOperation(updated)
    }

    func authenticate() {
        guard let portal else { return }
        authenticate(id: portal.id)
    }

    func authenticate(id: UUID) {
        guard portals.contains(where: { $0.id == id && $0.lifecycle == .active }),
              authenticationPending.insert(id).inserted
        else { return }
        helper.authenticatePortal(id: id) { [weak self] result in
            if case .failure = result {
                self?.authenticationPending.remove(id)
                self?.message = "Authentication could not be started."
            }
        }
    }

    func dismissAlert(id: UUID) {
        guard installation.alerts.contains(where: { $0.id == id }) else { return }
        var updated = installation
        updated.alerts.removeAll { $0.id == id }
        do {
            try store.save(updated)
            installation = updated
            publishInstallation()
        } catch {
            message = "The warning could not be dismissed."
        }
    }

    func resetTailnet(confirmed: Bool) {
        guard confirmed else { return }
        guard portals.isEmpty else {
            message = "Reset Tailnet is available only when no Portals remain."
            return
        }
        guard installation.tailnetBinding != nil else { return }
        var updated = installation
        updated.tailnetBinding = nil
        do {
            try store.save(updated)
            installation = updated
            publishInstallation()
            message = nil
        } catch {
            message = "The tailnet binding could not be reset."
        }
    }

    func pendingWarningText(for portal: PortalConfiguration) -> String {
        "Removing \(portal.name) from a different tailnet. Local cleanup is in progress; its remote node may still require manual removal."
    }

    func completedWarningText(for alert: InstallationAlert) -> String {
        let node = alert.assignedName.map { " \($0)" } ?? ""
        let expected = alert.expectedMagicDNSSuffix.map { " Expected tailnet: \($0)." } ?? ""
        let rejected = alert.rejectedMagicDNSSuffix.map { " Rejected tailnet: \($0)." } ?? ""
        return "Portico removed \(alert.portalName)'s local configuration and identity state after it joined a different tailnet. The remote node\(node) may remain and may require manual removal.\(expected)\(rejected)"
    }

    private func helperConnected() {
        refreshLocalApps()
        scheduleReconciliation()
        for portal in installation.portals {
            if portal.lifecycle == .pendingTailnetRejection {
                cleanupRejectedPortal(portal, evidence: nil)
            }
        }
    }

    private func updateDesiredState(id: UUID, desiredState: PortalDesiredState) {
        guard let index = installation.portals.firstIndex(where: {
            $0.id == id && $0.lifecycle == .active
        }), installation.portals[index].desiredState != desiredState else { return }
        var updated = installation
        updated.portals[index].desiredState = desiredState
        savePortalOperation(updated)
    }

    private func savePortalOperation(_ updated: InstallationRecord) {
        do {
            try store.save(updated)
            installation = updated
            publishInstallation()
            message = nil
            scheduleReconciliation()
        } catch {
            message = "The Portal could not be saved."
        }
    }

    private func scheduleReconciliation() {
        reconciliationGeneration += 1
        let reconciliation = PortalReconciliation(
            generation: reconciliationGeneration,
            portals: installation.portals.filter { $0.lifecycle == .active }
        )
        guard helper.availability == .connected else { return }
        guard !reconciliationInFlight else {
            pendingReconciliation = reconciliation
            return
        }
        send(reconciliation)
    }

    private func send(_ reconciliation: PortalReconciliation) {
        reconciliationInFlight = true
        helper.reconcilePortals(reconciliation.portals) { [weak self] result in
            guard let self else { return }
            self.reconciliationInFlight = false
            if reconciliation.generation == self.reconciliationGeneration {
                switch result {
                case let .success(response):
                    self.message = response.entries.contains { $0.outcome != .converged }
                        ? "Some Portals could not be reconciled."
                        : nil
                case .failure:
                    self.message = "Portals could not be reconciled."
                }
            }
            if let pending = self.pendingReconciliation {
                self.pendingReconciliation = nil
                guard self.helper.availability == .connected else { return }
                self.send(pending)
            }
        }
    }

    private func receive(_ event: PortalHelperEvent) {
        switch event {
        case let .status(id, status):
            guard let portal = installation.portals.first(where: { $0.id == id }) else { return }
            statuses[id] = status.redactingTailnetName()
            if portal.lifecycle == .active, status.state == .online,
               let tailnetName = status.tailnetName, !tailnetName.isEmpty {
                receiveOnlineStatus(for: portal, status: status, tailnetName: tailnetName)
            }
        case let .authenticationURL(id, url):
            guard authenticationPending.remove(id) != nil,
                  installation.portals.contains(where: { $0.id == id && $0.lifecycle == .active })
            else { return }
            openURL(url)
        }
    }

    private func receiveOnlineStatus(
        for portal: PortalConfiguration,
        status: PortalStatusPayload,
        tailnetName: String
    ) {
        guard let binding = installation.tailnetBinding else {
            var updated = installation
            updated.tailnetBinding = TailnetBinding(
                name: tailnetName,
                magicDNSSuffix: status.magicDNSSuffix ?? ""
            )
            do {
                try store.save(updated)
                installation = updated
                publishInstallation()
                message = nil
            } catch {
                message = "The installation tailnet could not be saved."
            }
            return
        }
        guard binding.name == tailnetName else {
            reject(portal, status: status)
            return
        }
        let suffix = status.magicDNSSuffix ?? ""
        guard suffix != binding.magicDNSSuffix else { return }
        var updated = installation
        updated.tailnetBinding?.magicDNSSuffix = suffix
        do {
            try store.save(updated)
            installation = updated
            publishInstallation()
        } catch {
            message = "The tailnet display name could not be refreshed."
        }
    }

    private func reject(_ portal: PortalConfiguration, status: PortalStatusPayload) {
        guard let index = installation.portals.firstIndex(where: { $0.id == portal.id && $0.lifecycle == .active }) else {
            return
        }
        var updated = installation
        updated.portals[index].lifecycle = .pendingTailnetRejection
        do {
            try store.save(updated)
            installation = updated
            publishInstallation()
            scheduleReconciliation()
            cleanupRejectedPortal(
                updated.portals[index],
                evidence: RejectionEvidence(
                    assignedName: status.assignedName,
                    rejectedMagicDNSSuffix: status.magicDNSSuffix
                )
            )
        } catch {
            message = "The rejected Portal could not be saved for cleanup."
        }
    }

    private func cleanupRejectedPortal(_ portal: PortalConfiguration, evidence: RejectionEvidence?) {
        guard helper.availability == .connected, cleanupInFlight.insert(portal.id).inserted else { return }
        helper.cleanupRejectedPortal(id: portal.id) { [weak self] result in
            guard let self else { return }
            self.cleanupInFlight.remove(portal.id)
            guard case .success = result else {
                self.message = "Local cleanup will be retried the next time Portico starts."
                return
            }
            guard self.installation.portals.contains(where: {
                $0.id == portal.id && $0.lifecycle == .pendingTailnetRejection
            }) else { return }
            var updated = self.installation
            updated.portals.removeAll { $0.id == portal.id }
            updated.alerts.append(InstallationAlert(
                id: self.uuidProvider(),
                kind: .crossTailnetRejection,
                portalName: portal.name,
                assignedName: evidence?.assignedName,
                expectedMagicDNSSuffix: self.installation.tailnetBinding?.magicDNSSuffix.nonEmpty,
                rejectedMagicDNSSuffix: evidence?.rejectedMagicDNSSuffix?.nonEmpty,
                createdAt: self.dateProvider()
            ))
            do {
                try self.store.save(updated)
                self.installation = updated
                self.statuses.removeValue(forKey: portal.id)
                self.publishInstallation()
                self.message = nil
            } catch {
                self.message = "Local cleanup completed, but its warning could not be saved; Portico will retry."
            }
        }
    }

    private func publishInstallation() {
        portals = installation.portals
        alerts = installation.alerts
        tailnetDisplaySuffix = installation.tailnetBinding?.magicDNSSuffix.nonEmpty
    }
}

private struct RejectionEvidence {
    let assignedName: String?
    let rejectedMagicDNSSuffix: String?
}

private struct PortalReconciliation {
    let generation: Int
    let portals: [PortalConfiguration]
}

private extension String {
    var nonEmpty: String? { isEmpty ? nil : self }
}

private extension PortalStatusPayload {
    func redactingTailnetName() -> PortalStatusPayload {
        PortalStatusPayload(
            state: state,
            stableNodeId: stableNodeId,
            assignedName: assignedName,
            portalURL: portalURL,
            addresses: addresses,
            magicDNSSuffix: magicDNSSuffix
        )
    }
}
