import Foundation
import ServiceManagement

enum LaunchAtLoginStatus: Equatable {
    case notRegistered
    case enabled
    case requiresApproval
    case notFound
}

protocol LaunchAtLoginServicing: AnyObject {
    var status: LaunchAtLoginStatus { get }

    func register() throws
    func unregister() throws
    func openSystemSettingsLoginItems()
}

final class ServiceManagementLaunchAtLoginService: LaunchAtLoginServicing {
    private let service: SMAppService

    init(service: SMAppService = .mainApp) {
        self.service = service
    }

    var status: LaunchAtLoginStatus {
        switch service.status {
        case .notRegistered: .notRegistered
        case .enabled: .enabled
        case .requiresApproval: .requiresApproval
        case .notFound: .notFound
        @unknown default: .notFound
        }
    }

    func register() throws {
        try service.register()
    }

    func unregister() throws {
        try service.unregister()
    }

    func openSystemSettingsLoginItems() {
        SMAppService.openSystemSettingsLoginItems()
    }
}

@MainActor
final class LaunchAtLoginController: ObservableObject {
    @Published private(set) var status: LaunchAtLoginStatus
    @Published private(set) var isOffering = false
    @Published private(set) var errorMessage: String?

    private let service: LaunchAtLoginServicing
    private let offerState: () -> LaunchAtLoginOfferState
    private let saveOfferState: (LaunchAtLoginOfferState) -> Bool

    init(
        service: LaunchAtLoginServicing,
        offerState: @escaping () -> LaunchAtLoginOfferState,
        saveOfferState: @escaping (LaunchAtLoginOfferState) -> Bool
    ) {
        self.service = service
        self.offerState = offerState
        self.saveOfferState = saveOfferState
        status = service.status
    }

    func considerOfferAfterFreshOnline() {
        guard offerState() == .notOffered else { return }
        refreshStatus()
        if status == .enabled {
            _ = commit(.accepted)
            return
        }
        guard commit(.presented) else { return }
        isOffering = true
    }

    func restorePresentedOffer() {
        guard offerState() == .presented else { return }
        isOffering = true
    }

    func acceptOffer() {
        guard isOffering, commit(.accepted) else { return }
        isOffering = false
        register()
    }

    func declineOffer() {
        guard isOffering, commit(.declined) else { return }
        isOffering = false
    }

    func retryRegistration() {
        refreshStatus()
        guard status == .notRegistered else { return }
        register()
    }

    func setEnabled(_ enabled: Bool) {
        refreshStatus()
        if enabled {
            guard status == .notRegistered else { return }
            if offerState() != .accepted, !commit(.accepted) { return }
            register()
        } else {
            guard status == .enabled || status == .requiresApproval else { return }
            do {
                try service.unregister()
                errorMessage = nil
            } catch {
                errorMessage = "Launch at login could not be disabled."
            }
            refreshStatus(preservingError: true)
        }
    }

    func openLoginItemsSettings() {
        refreshStatus()
        guard status == .requiresApproval else { return }
        service.openSystemSettingsLoginItems()
    }

    func refreshStatus() {
        refreshStatus(preservingError: false)
    }

    func refreshStatusAfterApplicationActivation() {
        refreshStatus(preservingError: true)
    }

    private func register() {
        do {
            try service.register()
            errorMessage = nil
        } catch {
            errorMessage = "Launch at login could not be enabled."
        }
        refreshStatus(preservingError: true)
    }

    private func commit(_ state: LaunchAtLoginOfferState) -> Bool {
        guard saveOfferState(state) else {
            errorMessage = "The launch at login choice could not be saved."
            return false
        }
        errorMessage = nil
        return true
    }

    private func refreshStatus(preservingError: Bool) {
        status = service.status
        if !preservingError {
            errorMessage = nil
        }
    }
}
