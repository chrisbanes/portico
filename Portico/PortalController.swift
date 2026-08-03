import Foundation

@MainActor
final class PortalController: ObservableObject {
    @Published var portalName = ""
    @Published var localAppPort = ""
    @Published private(set) var portal: PortalConfiguration?
    @Published private(set) var status: PortalStatusPayload?
    @Published private(set) var message: String?
    @Published private(set) var localApps: [LocalAppCandidatePayload] = []
    @Published private(set) var isRefreshingLocalApps = false
    @Published private(set) var localAppsMessage: String?

    private let store: PortalStore
    private let helper: PortalHelperClient
    private let uuidProvider: () -> UUID
    private let dateProvider: () -> Date
    private let openURL: (URL) -> Void
    private var authenticationPending = false
    private var discoveryGeneration = 0

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
            portal = try store.load()
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
        guard portal == nil else { return }
        discoveryGeneration += 1
        let generation = discoveryGeneration
        isRefreshingLocalApps = true
        localAppsMessage = nil
        helper.discoverLocalApps { [weak self] result in
            guard let self, self.portal == nil, self.discoveryGeneration == generation else { return }
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
        guard portal == nil, localApps.contains(candidate) else { return }
        localAppPort = String(candidate.localAppPort)
        if portalName.isEmpty, let suggestion = candidate.suggestedPortalName {
            portalName = suggestion
        }
    }

    func addPortal() {
        guard portal == nil else { return }
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
        do {
            try store.save(configuration)
            portal = configuration
            message = nil
            discoveryGeneration += 1
            localApps = []
            isRefreshingLocalApps = false
            localAppsMessage = nil
            startSavedPortal()
        } catch {
            message = "The Portal could not be saved."
        }
    }

    func authenticate() {
        guard let portal, !authenticationPending else { return }
        authenticationPending = true
        helper.authenticatePortal(id: portal.id) { [weak self] result in
            if case .failure = result {
                self?.authenticationPending = false
                self?.message = "Authentication could not be started."
            }
        }
    }

    private func startSavedPortal() {
        guard let portal, helper.availability == .connected else { return }
        helper.startPortal(portal) { [weak self] result in
            if case .failure = result {
                self?.message = "The Portal could not be started."
            }
        }
    }

    private func helperConnected() {
        if portal == nil {
            refreshLocalApps()
        } else {
            startSavedPortal()
        }
    }

    private func receive(_ event: PortalHelperEvent) {
        guard let portal else { return }
        switch event {
        case let .status(id, status) where id == portal.id:
            self.status = status
        case let .authenticationURL(id, url) where id == portal.id && authenticationPending:
            authenticationPending = false
            openURL(url)
        default:
            break
        }
    }
}
