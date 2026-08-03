import Foundation

@MainActor
final class PortalController: ObservableObject {
    @Published var portalName = ""
    @Published var localAppPort = ""
    @Published private(set) var portal: PortalConfiguration?
    @Published private(set) var status: PortalStatusPayload?
    @Published private(set) var message: String?

    private let store: PortalStore
    private let helper: PortalHelperClient
    private let uuidProvider: () -> UUID
    private let dateProvider: () -> Date
    private let openURL: (URL) -> Void
    private var authenticationPending = false

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
        helper.onConnected = { [weak self] in self?.startSavedPortal() }
        helper.onEvent = { [weak self] event in self?.receive(event) }
        if helper.availability == .connected {
            startSavedPortal()
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
