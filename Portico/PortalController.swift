import Foundation

enum PortalRemovalState: Equatable {
    case waitingForHelper
    case reconciling
    case removing
    case failed
}

enum PortalCreationKind: String, CaseIterable, Identifiable {
    case localApp
    case remoteApp

    var id: Self { self }
}

enum PortalCreationOutcome: Equatable {
    case validationError(PortalValidationError)
    case persistenceFailure
    case persisted(PortalConfiguration)
}

struct PortalDestinationEdit: Equatable {
    var kind: PortalCreationKind
    var localAppPort: String
    var remoteAppScheme: RemoteAppScheme
    var remoteAppHost: String
    var remoteAppPort: String

    init(
        kind: PortalCreationKind,
        localAppPort: String = "",
        remoteAppScheme: RemoteAppScheme = .https,
        remoteAppHost: String = "",
        remoteAppPort: String = ""
    ) {
        self.kind = kind
        self.localAppPort = localAppPort
        self.remoteAppScheme = remoteAppScheme
        self.remoteAppHost = remoteAppHost
        self.remoteAppPort = remoteAppPort
    }

    init(destination: PortalDestination) {
        switch destination {
        case let .localApp(port):
            self.init(kind: .localApp, localAppPort: String(port))
        case let .remoteApp(scheme, host, port):
            self.init(
                kind: .remoteApp,
                remoteAppScheme: scheme,
                remoteAppHost: host,
                remoteAppPort: String(port)
            )
        }
    }

    func validated(name: String) throws -> PortalDestination {
        switch kind {
        case .localApp:
            let port = try PortalInputValidator.validate(name: name, port: localAppPort)
            guard let destination = PortalDestination(localAppPort: port) else {
                throw PortalValidationError.invalidPort
            }
            return destination
        case .remoteApp:
            let port = try PortalInputValidator.validate(name: name, port: remoteAppPort)
            guard let destination = PortalDestination(
                remoteAppScheme: remoteAppScheme,
                host: remoteAppHost,
                port: port
            ) else {
                throw PortalValidationError.invalidRemoteHost
            }
            return destination
        }
    }
}

struct PortalRemovalNotice: Equatable, Identifiable {
    let id: UUID
    let portalName: String
    let assignedName: String?
}

@MainActor
final class PortalController: ObservableObject {
    static let manualRemovalURL = URL(
        string: "https://tailscale.com/docs/features/access-control/device-management/how-to/remove"
    )!

    @Published var portalName = ""
    @Published var localAppPort = ""
    @Published var creationKind: PortalCreationKind = .localApp
    @Published var remoteAppScheme: RemoteAppScheme = .https
    @Published var remoteAppHost = ""
    @Published var remoteAppPort = ""
    @Published private(set) var portals: [PortalConfiguration] = []
    @Published private(set) var statuses: [UUID: PortalStatusPayload] = [:]
    @Published private(set) var alerts: [InstallationAlert] = []
    @Published private(set) var tailnetDisplaySuffix: String?
    @Published private(set) var message: String?
    @Published private(set) var portalCreationError: String?
    @Published private(set) var localApps: [LocalAppCandidatePayload] = []
    @Published private(set) var isRefreshingLocalApps = false
    @Published private(set) var localAppsMessage: String?
    @Published private(set) var reachabilityStates: [UUID: LocalAppReachabilityState] = [:]
    @Published private(set) var staleStatusIDs: Set<UUID> = []
    @Published private(set) var diagnosticEntries: [DiagnosticEntry] = []
    @Published private(set) var removalStates: [UUID: PortalRemovalState] = [:]
    @Published private(set) var removalNotices: [PortalRemovalNotice] = []
    @Published private(set) var operationalLogging: OperationalLoggingPreference = .undecided
    @Published private(set) var operationalLoggingError: String?
    @Published private(set) var launchAtLoginOffer: LaunchAtLoginOfferState = .notOffered

    var onFreshPortalOnline: (() -> Void)?

    var portal: PortalConfiguration? { portals.first }
    var status: PortalStatusPayload? { portal.flatMap { statuses[$0.id] } }
    var canResetTailnet: Bool {
        isInstallationAvailable && portals.isEmpty && installation.tailnetBinding != nil
    }
    var pendingPortals: [PortalConfiguration] {
        portals.filter { $0.lifecycle == .pendingTailnetRejection }
    }
    var pendingRemovalPortals: [PortalConfiguration] {
        portals.filter { $0.lifecycle == .pendingRemoval }
    }

    private let store: PortalStore
    private let helper: PortalHelperClient
    private let uuidProvider: () -> UUID
    private let dateProvider: () -> Date
    private let openURL: (URL) -> Void
    private let copyText: (String) -> Void
    private let announce: (String) -> Void
    private let reachability: LocalAppReachability?
    private let history: DiagnosticHistory
    private let diagnosticVersions: DiagnosticVersions
    private var installation = InstallationRecord()
    private(set) var isInstallationAvailable = false
    private var authenticationPending: [UUID: AuthenticationRequest] = [:]
    private var authenticationRequestIdentity = 0
    private var cleanupInFlight: Set<UUID> = []
    private var discoveryGeneration = 0
    private var reconciliationGeneration = 0
    private var reconciliationInFlight = false
    private var pendingReconciliation: PortalReconciliation?
    private var removalAttemptTokens: [UUID: Int] = [:]
    private var removalsAwaitingReconciliation: [UUID: Int] = [:]
    private var loggingRestartPending = false

    init(
        store: PortalStore,
        helper: PortalHelperClient,
        uuidProvider: @escaping () -> UUID = UUID.init,
        dateProvider: @escaping () -> Date = Date.init,
        reachability: LocalAppReachability? = nil,
        history: DiagnosticHistory? = nil,
        diagnosticVersions: DiagnosticVersions = .current,
        copyText: @escaping (String) -> Void = { _ in },
        announce: @escaping (String) -> Void = { _ in },
        openURL: @escaping (URL) -> Void
    ) {
        self.store = store
        self.helper = helper
        self.uuidProvider = uuidProvider
        self.dateProvider = dateProvider
        self.reachability = reachability
        self.history = history ?? DiagnosticHistory(dateProvider: dateProvider)
        self.diagnosticVersions = diagnosticVersions
        self.copyText = copyText
        self.announce = announce
        self.openURL = openURL
        self.history.onChange = { [weak self] entries in self?.diagnosticEntries = entries }
        self.reachability?.onChange = { [weak self] states in
            guard let self else { return }
            guard self.isInstallationAvailable else { return }
            self.reachabilityStates = states
            for portal in self.installation.portals where portal.lifecycle == .active {
                self.recordPortalState(portal)
            }
        }
        do {
            installation = try store.loadInstallation()
            isInstallationAvailable = true
            publishInstallation()
            for portal in installation.portals where portal.lifecycle == .pendingRemoval {
                queueRemovalAttempt(portal.id, schedule: false)
            }
        } catch {
            message = "Saved Portal configuration could not be loaded."
        }
        helper.onConnected = { [weak self] in self?.helperConnected() }
        helper.onAvailabilityChange = { [weak self] in self?.helperAvailabilityChanged($0) }
        helper.onEvent = { [weak self] event in self?.receive(event) }
        helperAvailabilityChanged(helper.availability)
        if helper.availability == .connected {
            helperConnected()
        }
    }

    func refreshLocalApps() {
        guard requireInstallation(), actionAvailability().refreshLocalApps else { return }
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

    func refreshReachability() {
        reachability?.refresh()
    }

    func retryHelper() {
        guard requireInstallation() else { return }
        helper.retry()
    }

    func reportInitialPersistenceFailure() {
        message = "Saved Portal configuration could not be loaded."
    }

    func setOperationalLogging(_ preference: OperationalLoggingPreference) {
        guard requireInstallation(),
              preference != .undecided,
              preference != installation.operationalLogging
        else { return }
        var updated = installation
        updated.operationalLogging = preference
        do {
            try store.save(updated)
        } catch {
            let errorMessage = "The operational-support logging setting could not be saved."
            operationalLoggingError = errorMessage
            message = errorMessage
            return
        }

        installation = updated
        publishInstallation()
        operationalLoggingError = nil
        message = nil
        discoveryGeneration += 1
        isRefreshingLocalApps = false
        authenticationPending.removeAll()
        reconciliationGeneration += 1
        pendingReconciliation = nil
        loggingRestartPending = true
        helper.restart(loggingPreference: preference)
    }

    func commitLaunchAtLoginOffer(_ state: LaunchAtLoginOfferState) -> Bool {
        guard requireInstallation() else { return false }
        guard state != installation.launchAtLoginOffer else { return true }
        var updated = installation
        updated.launchAtLoginOffer = state
        do {
            try store.save(updated)
            installation = updated
            publishInstallation()
            return true
        } catch {
            return false
        }
    }

    func diagnosticReport() -> String {
        DiagnosticReportRenderer.render(
            versions: diagnosticVersions,
            helper: helper.availability,
            isInstallationAvailable: isInstallationAvailable,
            portals: installation.portals
                .filter { $0.lifecycle == .active }
                .map {
                    let status = statuses[$0.id]
                    return PortalDiagnosticFacts(
                        portalName: $0.name,
                        assignedName: status?.assignedName,
                        portalURL: status?.portalURL,
                        addresses: status?.addresses ?? [],
                        magicDNSSuffix: status?.magicDNSSuffix,
                        desiredState: $0.desiredState,
                        tailscaleState: status?.state,
                        reachability: reachabilityStates[$0.id] ?? .unknown,
                        isStale: staleStatusIDs.contains($0.id)
                    )
                },
            history: history.entries
        )
    }

    func copyDiagnosticReport() {
        copyText(diagnosticReport())
    }

    func openExternalURL(_ url: URL) {
        openURL(url)
    }

    func selectLocalApp(_ candidate: LocalAppCandidatePayload) {
        guard creationKind == .localApp, localApps.contains(candidate) else { return }
        localAppPort = String(candidate.localAppPort)
        if portalName.isEmpty, let suggestion = candidate.suggestedPortalName {
            portalName = suggestion
        }
    }

    @discardableResult
    func addPortal() -> PortalCreationOutcome? {
        guard requireInstallation() else { return nil }
        guard operationalLogging != .undecided else {
            message = "Choose an operational-support logging setting before adding a Portal."
            return nil
        }
        let destination: PortalDestination
        do {
            destination = try newPortalDestination()
        } catch let error as PortalValidationError {
            portalCreationError = "Enter a lower-case DNS label and valid destination details."
            return .validationError(error)
        } catch {
            return nil
        }
        let configuration = PortalConfiguration(
            id: uuidProvider(),
            name: portalName,
            destination: destination,
            createdAt: dateProvider()
        )
        var updated = installation
        updated.portals.append(configuration)
        do {
            try store.save(updated)
            installation = updated
            publishInstallation()
            portalCreationError = nil
            discardPortalDraft()
            scheduleReconciliation()
            return .persisted(configuration)
        } catch {
            portalCreationError = "The Portal could not be saved."
            return .persistenceFailure
        }
    }

    func discardPortalDraft() {
        portalName = ""
        localAppPort = ""
        creationKind = .localApp
        remoteAppScheme = .https
        remoteAppHost = ""
        remoteAppPort = ""
        discoveryGeneration += 1
        localApps = []
        isRefreshingLocalApps = false
        localAppsMessage = nil
        portalCreationError = nil
    }

    func startPortal(id: UUID) {
        updateDesiredState(id: id, desiredState: .enabled)
    }

    func stopPortal(id: UUID) {
        updateDesiredState(id: id, desiredState: .stopped)
    }

    func removalWarningText(for portal: PortalConfiguration) -> String {
        let assignedName = statuses[portal.id]?.assignedName ?? portal.removalAssignedName
        let device = assignedName.map { " The Tailscale device “\($0)”" }
            ?? " Its remote Tailscale device"
        return "Remove Portal “\(portal.name)” from this Mac? This permanently deletes its local configuration and local Tailscale identity state.\(device) may remain in the tailnet and may require manual removal."
    }

    func removalNoticeText(for notice: PortalRemovalNotice) -> String {
        let device = notice.assignedName.map { " The Tailscale device “\($0)”" }
            ?? " Its remote Tailscale device"
        return "Removed Portal “\(notice.portalName)” from this Mac.\(device) may remain in the tailnet and may require manual removal."
    }

    func pendingRemovalWarningText(for portal: PortalConfiguration) -> String {
        let device = portal.removalAssignedName.map { " The Tailscale device “\($0)”" }
            ?? " Its remote Tailscale device"
        return "Removing Portal “\(portal.name)” from this Mac permanently deletes its local configuration and local Tailscale identity state.\(device) may remain in the tailnet and may require manual removal."
    }

    func removePortal(id: UUID) {
        guard requireInstallation() else { return }
        guard let index = installation.portals.firstIndex(where: {
            $0.id == id && $0.lifecycle == .active
        }) else { return }
        var updated = installation
        updated.portals[index].lifecycle = .pendingRemoval
        updated.portals[index].removalAssignedName = statuses[id]?.assignedName
        do {
            try store.save(updated)
            installation = updated
            authenticationPending.removeValue(forKey: id)
            statuses.removeValue(forKey: id)
            staleStatusIDs.remove(id)
            reachabilityStates.removeValue(forKey: id)
            publishInstallation()
            message = nil
            queueRemovalAttempt(id)
        } catch {
            message = "The Portal could not be saved for removal."
        }
    }

    func retryRemoval(id: UUID) {
        guard requireInstallation() else { return }
        guard installation.portals.contains(where: {
            $0.id == id && $0.lifecycle == .pendingRemoval
        }), actionAvailability(for: installation.portals.first(where: { $0.id == id })).retryRemoval else { return }
        queueRemovalAttempt(id)
    }

    func dismissRemovalNotice(id: UUID) {
        removalNotices.removeAll { $0.id == id }
    }

    func updateDestination(id: UUID, edit: PortalDestinationEdit) {
        guard requireInstallation() else { return }
        guard let index = installation.portals.firstIndex(where: {
            $0.id == id && $0.lifecycle == .active
        }) else { return }
        let destination: PortalDestination
        do {
            destination = try edit.validated(name: installation.portals[index].name)
        } catch {
            message = "Enter valid destination details."
            return
        }
        guard installation.portals[index].destination != destination else { return }
        var updated = installation
        updated.portals[index].destination = destination
        _ = savePortalOperation(updated)
    }

    func authenticate() {
        guard let portal else { return }
        authenticate(id: portal.id)
    }

    func authenticate(id: UUID) {
        guard requireInstallation(),
              let portal = portals.first(where: { $0.id == id && $0.lifecycle == .active }),
              actionAvailability(for: portal).authenticate,
              authenticationPending[id] == nil
        else { return }
        authenticationRequestIdentity += 1
        let request = AuthenticationRequest(
            generation: helper.generation,
            identity: authenticationRequestIdentity
        )
        authenticationPending[id] = request
        helper.authenticatePortal(id: id) { [weak self] result in
            if case .failure = result {
                guard self?.authenticationPending[id] == request else { return }
                self?.authenticationPending.removeValue(forKey: id)
                self?.message = "Authentication could not be started."
            }
        }
    }

    func dismissAlert(id: UUID) {
        guard requireInstallation() else { return }
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
        guard requireInstallation() else { return }
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

    func actionAvailability(
        for portal: PortalConfiguration? = nil,
        editedDestination: PortalDestinationEdit? = nil
    ) -> PortalActionAvailability {
        guard isInstallationAvailable else {
            return PortalActionAvailability(context: PortalActionContext(
                loggingPreference: .undecided,
                inputsValid: false,
                helperAvailability: .failed
            ))
        }
        let addInputsValid = portal == nil && (try? newPortalDestination()) != nil
        let editedDestinationValid: Bool
        if let portal, let editedDestination,
           let destination = try? editedDestination.validated(name: portal.name) {
            editedDestinationValid = destination != portal.destination
        } else {
            editedDestinationValid = false
        }
        let status = portal.flatMap { statuses[$0.id] }
        return PortalActionAvailability(context: PortalActionContext(
            loggingPreference: installation.operationalLogging,
            inputsValid: addInputsValid,
            helperAvailability: helper.availability,
            lifecycle: portal?.lifecycle,
            desiredState: portal?.desiredState,
            tailscaleState: status?.state,
            hasPortalURL: status?.portalURL != nil,
            isTailscaleFactsStale: portal.map { staleStatusIDs.contains($0.id) } ?? false,
            isAuthenticationPending: portal.map { authenticationPending[$0.id] != nil } ?? false,
            removalState: portal.flatMap { removalStates[$0.id] },
            hasTailnetBinding: installation.tailnetBinding != nil,
            portalCount: installation.portals.count,
            isEditedDestinationValid: editedDestinationValid,
            isRefreshingLocalApps: isRefreshingLocalApps
        ))
    }

    private func newPortalDestination() throws -> PortalDestination {
        try PortalDestinationEdit(
            kind: creationKind,
            localAppPort: localAppPort,
            remoteAppScheme: remoteAppScheme,
            remoteAppHost: remoteAppHost,
            remoteAppPort: remoteAppPort
        ).validated(name: portalName)
    }

    func copyPortalURL(id: UUID) {
        guard let portal = installation.portals.first(where: { $0.id == id }),
              actionAvailability(for: portal).copyPortalURL,
              let url = statuses[id]?.portalURL
        else { return }
        copyText(url.absoluteString)
    }

    func openPortalURL(id: UUID) {
        guard let portal = installation.portals.first(where: { $0.id == id }),
              actionAvailability(for: portal).openPortalURL,
              let url = statuses[id]?.portalURL
        else { return }
        openURL(url)
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
        guard isInstallationAvailable else { return }
        reachability?.helperRecovered()
        refreshLocalApps()
        scheduleReconciliation()
        for portal in installation.portals {
            if portal.lifecycle == .pendingTailnetRejection {
                cleanupRejectedPortal(portal, evidence: nil)
            }
        }
    }

    private func helperAvailabilityChanged(_ availability: HelperAvailability) {
        guard isInstallationAvailable else { return }
        if availability == .connected {
            let event: PorticoAnnouncementEvent = loggingRestartPending
                ? .preferenceRestartCompleted
                : .helperConnected
            loggingRestartPending = false
            announce(PorticoAnnouncement.text(for: event))
        } else if availability.isTerminalFailure {
            let event: PorticoAnnouncementEvent = loggingRestartPending
                ? .preferenceRestartFailed
                : .helperTerminalFailure
            loggingRestartPending = false
            announce(PorticoAnnouncement.text(for: event))
        }
        guard availability != .connected else { return }
        authenticationPending.removeAll()
        staleStatusIDs.formUnion(statuses.keys)
        for portal in installation.portals where portal.lifecycle == .active {
            recordPortalState(portal)
        }
    }

    private func updateDesiredState(id: UUID, desiredState: PortalDesiredState) {
        guard requireInstallation() else { return }
        guard let index = installation.portals.firstIndex(where: {
            $0.id == id && $0.lifecycle == .active
        }), installation.portals[index].desiredState != desiredState else { return }
        var updated = installation
        updated.portals[index].desiredState = desiredState
        if savePortalOperation(updated) {
            recordPortalState(updated.portals[index])
        }
    }

    private func savePortalOperation(_ updated: InstallationRecord) -> Bool {
        guard requireInstallation() else { return false }
        do {
            try store.save(updated)
            installation = updated
            publishInstallation()
            message = nil
            scheduleReconciliation()
            return true
        } catch {
            message = "The Portal could not be saved."
            return false
        }
    }

    private func queueRemovalAttempt(_ id: UUID, schedule: Bool = true) {
        let token = (removalAttemptTokens[id] ?? 0) + 1
        removalAttemptTokens[id] = token
        removalsAwaitingReconciliation[id] = token
        removalStates[id] = .waitingForHelper
        if schedule {
            scheduleReconciliation()
        }
    }

    private func scheduleReconciliation() {
        guard isInstallationAvailable else { return }
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
        guard isInstallationAvailable else { return }
        reconciliationInFlight = true
        let removalAttempts = removalsAwaitingReconciliation.filter { id, token in
            removalAttemptTokens[id] == token && installation.portals.contains(where: {
                $0.id == id && $0.lifecycle == .pendingRemoval
            })
        }
        for (id, _) in removalAttempts {
            removalsAwaitingReconciliation.removeValue(forKey: id)
            removalStates[id] = .reconciling
        }
        helper.reconcilePortals(reconciliation.portals) { [weak self] result in
            guard let self else { return }
            self.reconciliationInFlight = false
            if reconciliation.generation == self.reconciliationGeneration {
                switch result {
                case let .success(response):
                    self.message = response.entries.contains { $0.outcome != .converged }
                        ? "Some Portals could not be reconciled."
                        : nil
                    self.continueRemovalAttempts(removalAttempts, after: response)
                case .failure:
                    self.message = "Portals could not be reconciled."
                    for (id, token) in removalAttempts {
                        self.failRemovalAttempt(id: id, token: token)
                    }
                }
            } else {
                for (id, token) in removalAttempts where self.removalAttemptTokens[id] == token {
                    self.removalsAwaitingReconciliation[id] = token
                }
            }
            if let pending = self.pendingReconciliation {
                self.pendingReconciliation = nil
                guard self.helper.availability == .connected else { return }
                self.send(pending)
            }
        }
    }

    private func continueRemovalAttempts(
        _ attempts: [UUID: Int],
        after response: ReconcilePortalsResult
    ) {
        for (id, token) in attempts {
            guard removalAttemptTokens[id] == token,
                  installation.portals.contains(where: {
                      $0.id == id && $0.lifecycle == .pendingRemoval
                  })
            else { continue }
            if let entry = response.entries.first(where: { $0.portalId == id }),
               entry.outcome != .converged {
                failRemovalAttempt(id: id, token: token)
                continue
            }
            removalStates[id] = .removing
            helper.removePortal(id: id) { [weak self] result in
                guard let self, self.removalAttemptTokens[id] == token else { return }
                switch result {
                case .success:
                    self.finishRemoval(id: id, token: token)
                case .failure:
                    self.failRemovalAttempt(id: id, token: token)
                }
            }
        }
    }

    private func failRemovalAttempt(id: UUID, token: Int) {
        guard removalAttemptTokens[id] == token else { return }
        removalStates[id] = .failed
        message = "Portal removal could not be completed. Retry when ready."
        announce(PorticoAnnouncement.text(for: .removalFailed))
    }

    private func finishRemoval(id: UUID, token: Int) {
        guard removalAttemptTokens[id] == token,
              let portal = installation.portals.first(where: {
                  $0.id == id && $0.lifecycle == .pendingRemoval
              })
        else { return }
        var updated = installation
        updated.portals.removeAll { $0.id == id }
        do {
            try store.save(updated)
            installation = updated
            statuses.removeValue(forKey: id)
            authenticationPending.removeValue(forKey: id)
            staleStatusIDs.remove(id)
            reachabilityStates.removeValue(forKey: id)
            removalAttemptTokens.removeValue(forKey: id)
            removalsAwaitingReconciliation.removeValue(forKey: id)
            removalStates.removeValue(forKey: id)
            removalNotices.removeAll { $0.id == id }
            removalNotices.append(PortalRemovalNotice(
                id: id,
                portalName: portal.name,
                assignedName: portal.removalAssignedName
            ))
            publishInstallation()
            message = nil
            announce(PorticoAnnouncement.text(for: .removalSucceeded))
        } catch {
            failRemovalAttempt(id: id, token: token)
            message = "Local cleanup completed, but the Portal record could not be removed. Retry when ready."
        }
    }

    private func receive(_ event: PortalHelperEvent) {
        guard isInstallationAvailable else { return }
        switch event {
        case let .status(id, status, generation):
            guard generation == helper.generation else { return }
            receiveStatus(id: id, status: status)
        case let .authenticationURL(id, url, generation):
            receiveAuthenticationURL(id: id, url: url, generation: generation)
        }
    }

    private func receiveStatus(id: UUID, status: PortalStatusPayload) {
        guard let portal = installation.portals.first(where: {
            $0.id == id && $0.lifecycle == .active
        }) else { return }
        let wasFreshOnline = statuses[id]?.state == .online && !staleStatusIDs.contains(id)
        let displayStatus = status.sanitizedForDisplay()
        statuses[id] = displayStatus
        staleStatusIDs.remove(id)
        recordPortalState(portal)
        if status.state == .online, !wasFreshOnline {
            announce(PorticoAnnouncement.text(for: .portalOnline))
            onFreshPortalOnline?()
        }
        if portal.lifecycle == .active, status.state == .online,
           let tailnetName = status.tailnetName, !tailnetName.isEmpty {
            receiveOnlineStatus(for: portal, displayStatus: displayStatus, tailnetName: tailnetName)
        }
    }

    private func receiveAuthenticationURL(id: UUID, url: URL, generation: Int) {
        guard generation == helper.generation,
              authenticationPending[id]?.generation == generation,
              installation.portals.contains(where: { $0.id == id && $0.lifecycle == .active })
        else { return }
        authenticationPending.removeValue(forKey: id)
        guard url.scheme?.lowercased() == "https",
              let host = url.host,
              !host.isEmpty
        else { return }
        openURL(url)
    }

    private func receiveOnlineStatus(
        for portal: PortalConfiguration,
        displayStatus: PortalStatusPayload,
        tailnetName: String
    ) {
        guard isInstallationAvailable else { return }
        guard let binding = installation.tailnetBinding else {
            guard let suffix = displayStatus.magicDNSSuffix else { return }
            var updated = installation
            updated.tailnetBinding = TailnetBinding(
                name: tailnetName,
                magicDNSSuffix: suffix
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
            reject(
                portal,
                evidence: RejectionEvidence(
                    assignedName: displayStatus.assignedName,
                    rejectedMagicDNSSuffix: displayStatus.magicDNSSuffix
                )
            )
            return
        }
        guard let suffix = displayStatus.magicDNSSuffix,
              suffix != binding.magicDNSSuffix
        else { return }
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

    private func reject(_ portal: PortalConfiguration, evidence: RejectionEvidence) {
        guard isInstallationAvailable else { return }
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
            cleanupRejectedPortal(updated.portals[index], evidence: evidence)
        } catch {
            message = "The rejected Portal could not be saved for cleanup."
        }
    }

    private func cleanupRejectedPortal(_ portal: PortalConfiguration, evidence: RejectionEvidence?) {
        guard isInstallationAvailable else { return }
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
        guard isInstallationAvailable else { return }
        portals = installation.portals
        alerts = installation.alerts
        operationalLogging = installation.operationalLogging
        launchAtLoginOffer = installation.launchAtLoginOffer
        tailnetDisplaySuffix = installation.tailnetBinding?.magicDNSSuffix.nonEmpty
        reachability?.update(portals: installation.portals.filter { $0.lifecycle == .active })
    }

    private func recordPortalState(_ portal: PortalConfiguration) {
        guard isInstallationAvailable else { return }
        history.record(.portal(
            name: portal.name,
            desired: portal.desiredState,
            tailscale: statuses[portal.id]?.state,
            reachability: reachabilityStates[portal.id] ?? .unknown,
            stale: staleStatusIDs.contains(portal.id)
        ))
    }

    @discardableResult
    private func requireInstallation() -> Bool {
        guard isInstallationAvailable else {
            message = "Saved Portal configuration could not be loaded."
            return false
        }
        return true
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

private struct AuthenticationRequest: Equatable {
    let generation: Int
    let identity: Int
}

private extension String {
    var nonEmpty: String? { isEmpty ? nil : self }
}
