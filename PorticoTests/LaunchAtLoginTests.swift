import XCTest
@testable import PorticoApplication

@MainActor
final class LaunchAtLoginTests: XCTestCase {
    func testFirstFreshOnlineOfferPersistsPresentedBeforePresentation() {
        let service = FakeLaunchAtLoginService(status: .notRegistered)
        let store = FakeLaunchAtLoginOfferStore()
        let controller = makeController(service: service, store: store)

        controller.considerOfferAfterFreshOnline()

        XCTAssertEqual(store.state, .presented)
        XCTAssertEqual(store.saved, [.presented])
        XCTAssertTrue(controller.isOffering)
        controller.considerOfferAfterFreshOnline()
        XCTAssertEqual(store.saved, [.presented])
    }

    func testAlreadyEnabledAcceptsWithoutPrompt() {
        let service = FakeLaunchAtLoginService(status: .enabled)
        let store = FakeLaunchAtLoginOfferStore()
        let controller = makeController(service: service, store: store)

        controller.considerOfferAfterFreshOnline()

        XCTAssertEqual(store.state, .accepted)
        XCTAssertFalse(controller.isOffering)
        XCTAssertEqual(service.registerCount, 0)
    }

    func testPersistenceFailurePreventsPresentationAndRegistration() {
        let service = FakeLaunchAtLoginService(status: .notRegistered)
        let store = FakeLaunchAtLoginOfferStore(shouldSave: false)
        let controller = makeController(service: service, store: store)

        controller.considerOfferAfterFreshOnline()
        controller.acceptOffer()

        XCTAssertFalse(controller.isOffering)
        XCTAssertEqual(service.registerCount, 0)
        XCTAssertEqual(controller.errorMessage, "The launch at login choice could not be saved.")
    }

    func testPresentedStateRestoresWithoutSavingOrRegisteringAndDeclineIsDurable() {
        let service = FakeLaunchAtLoginService(status: .notRegistered)
        let presentedStore = FakeLaunchAtLoginOfferStore(state: .presented)
        let relaunched = makeController(service: service, store: presentedStore)

        relaunched.restorePresentedOffer()
        XCTAssertTrue(relaunched.isOffering)
        XCTAssertTrue(presentedStore.saved.isEmpty)
        XCTAssertEqual(service.registerCount, 0)

        let enabledService = FakeLaunchAtLoginService(status: .enabled)
        let enabledStore = FakeLaunchAtLoginOfferStore(state: .presented)
        let alreadyEnabled = makeController(service: enabledService, store: enabledStore)
        alreadyEnabled.restorePresentedOffer()
        XCTAssertFalse(alreadyEnabled.isOffering)
        XCTAssertEqual(enabledStore.state, .accepted)
        XCTAssertEqual(enabledStore.saved, [.accepted])
        XCTAssertEqual(enabledService.registerCount, 0)

        let declined = makeController(
            service: service,
            store: FakeLaunchAtLoginOfferStore(state: .declined)
        )
        declined.restorePresentedOffer()
        XCTAssertFalse(declined.isOffering)

        let accepted = makeController(
            service: service,
            store: FakeLaunchAtLoginOfferStore(state: .accepted)
        )
        accepted.restorePresentedOffer()
        XCTAssertFalse(accepted.isOffering)

        let offeredStore = FakeLaunchAtLoginOfferStore()
        let offered = makeController(service: service, store: offeredStore)
        offered.considerOfferAfterFreshOnline()
        offered.declineOffer()
        XCTAssertEqual(offeredStore.state, .declined)
        XCTAssertFalse(offered.isOffering)
    }

    func testAcceptancePersistsBeforeRegistrationAndFailureHasExplicitRetry() {
        struct ExpectedFailure: Error {}
        let service = FakeLaunchAtLoginService(status: .notRegistered)
        service.registerError = ExpectedFailure()
        let store = FakeLaunchAtLoginOfferStore()
        var events: [String] = []
        store.onSave = { events.append("saved-\($0.rawValue)") }
        service.onRegister = { events.append("register") }
        let controller = makeController(service: service, store: store)
        controller.considerOfferAfterFreshOnline()

        controller.acceptOffer()

        XCTAssertEqual(events.suffix(2), ["saved-accepted", "register"])
        XCTAssertEqual(store.state, .accepted)
        XCTAssertEqual(service.status, .notRegistered)
        XCTAssertEqual(controller.status, .notRegistered)
        XCTAssertEqual(controller.errorMessage, "Launch at login could not be enabled.")
        service.registerError = nil
        controller.retryRegistration()
        XCTAssertEqual(service.registerCount, 2)
    }

    func testApprovalAndUnavailableStatesExposeOnlyTheirFixedActions() {
        let service = FakeLaunchAtLoginService(status: .requiresApproval)
        let store = FakeLaunchAtLoginOfferStore(state: .accepted)
        let controller = makeController(service: service, store: store)

        controller.refreshStatus()
        controller.openLoginItemsSettings()

        XCTAssertEqual(controller.status, .requiresApproval)
        XCTAssertEqual(service.openSettingsCount, 1)

        service.status = .notFound
        controller.refreshStatus()
        controller.retryRegistration()
        XCTAssertEqual(controller.status, .notFound)
        XCTAssertEqual(service.registerCount, 0)
    }

    func testApplicationActivationRefreshPreservesRegistrationFailure() {
        struct ExpectedFailure: Error {}
        let service = FakeLaunchAtLoginService(status: .notRegistered)
        service.registerError = ExpectedFailure()
        let store = FakeLaunchAtLoginOfferStore()
        let controller = makeController(service: service, store: store)
        controller.considerOfferAfterFreshOnline()
        controller.acceptOffer()

        controller.refreshStatusAfterApplicationActivation()

        XCTAssertEqual(controller.status, .notRegistered)
        XCTAssertEqual(controller.errorMessage, "Launch at login could not be enabled.")
    }

    func testApplicationActivationRefreshClearsRegistrationFailureAfterExternalStatusChange() {
        struct ExpectedFailure: Error {}
        let service = FakeLaunchAtLoginService(status: .notRegistered)
        service.registerError = ExpectedFailure()
        let controller = makeController(service: service, store: FakeLaunchAtLoginOfferStore())
        controller.considerOfferAfterFreshOnline()
        controller.acceptOffer()

        service.status = .enabled
        controller.refreshStatusAfterApplicationActivation()

        XCTAssertEqual(controller.status, .enabled)
        XCTAssertNil(controller.errorMessage)
    }

    func testDisableFailureKeepsLiveStatusAndExternalRefreshDoesNotMutateOfferHistory() {
        struct ExpectedFailure: Error {}
        let service = FakeLaunchAtLoginService(status: .enabled)
        service.unregisterError = ExpectedFailure()
        let store = FakeLaunchAtLoginOfferStore(state: .accepted)
        let controller = makeController(service: service, store: store)

        controller.setEnabled(false)

        XCTAssertEqual(controller.status, .enabled)
        XCTAssertEqual(controller.errorMessage, "Launch at login could not be disabled.")
        service.status = .notRegistered
        controller.refreshStatus()
        XCTAssertEqual(controller.status, .notRegistered)
        XCTAssertTrue(store.saved.isEmpty)
    }

    private func makeController(
        service: FakeLaunchAtLoginService,
        store: FakeLaunchAtLoginOfferStore
    ) -> LaunchAtLoginController {
        LaunchAtLoginController(
            service: service,
            offerState: { store.state },
            saveOfferState: { store.save($0) }
        )
    }
}

private final class FakeLaunchAtLoginOfferStore {
    var state: LaunchAtLoginOfferState
    var shouldSave: Bool
    var saved: [LaunchAtLoginOfferState] = []
    var onSave: ((LaunchAtLoginOfferState) -> Void)?

    init(state: LaunchAtLoginOfferState = .notOffered, shouldSave: Bool = true) {
        self.state = state
        self.shouldSave = shouldSave
    }

    func save(_ state: LaunchAtLoginOfferState) -> Bool {
        guard shouldSave else { return false }
        self.state = state
        saved.append(state)
        onSave?(state)
        return true
    }
}

private final class FakeLaunchAtLoginService: LaunchAtLoginServicing {
    var status: LaunchAtLoginStatus
    var registerError: Error?
    var unregisterError: Error?
    var onRegister: (() -> Void)?
    private(set) var registerCount = 0
    private(set) var unregisterCount = 0
    private(set) var openSettingsCount = 0

    init(status: LaunchAtLoginStatus) {
        self.status = status
    }

    func register() throws {
        registerCount += 1
        onRegister?()
        if let registerError { throw registerError }
        status = .enabled
    }

    func unregister() throws {
        unregisterCount += 1
        if let unregisterError { throw unregisterError }
        status = .notRegistered
    }

    func openSystemSettingsLoginItems() {
        openSettingsCount += 1
    }
}
