import AppKit
import Combine

@MainActor
final class AppWindowActivation {
    private let application: NSApplication

    init() {
        application = .shared
    }

    init(application: NSApplication) {
        self.application = application
    }

    func present(_ openWindow: () -> Void) {
        application.setActivationPolicy(.regular)
        openWindow()
        DispatchQueue.main.async { [application] in
            application.activate(ignoringOtherApps: true)
        }
    }

    func windowDidClose() {
        DispatchQueue.main.async { [application] in
            let hasVisibleApplicationWindow = application.windows.contains { window in
                window.isVisible && window.styleMask.contains(.titled) && !(window is NSPanel)
            }
            if !hasVisibleApplicationWindow {
                application.setActivationPolicy(.accessory)
            }
        }
    }
}

@MainActor
final class ManagementRouting: ObservableObject {
    enum Destination {
        case overview
        case settings
    }

    @Published private(set) var destination = Destination.overview
    @Published private(set) var requestRevision = 0

    func requestOverview() {
        destination = .overview
        requestRevision += 1
    }

    func requestSettings() {
        destination = .settings
        requestRevision += 1
    }

    func recordVisibleDestination(_ destination: Destination) {
        self.destination = destination
    }
}

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    let appVariant: AppVariant
    let supervisor: HelperSupervisor
    let portalController: PortalController
    let launchAtLoginController: LaunchAtLoginController?
    let managementRouting = ManagementRouting()
    let windowActivation = AppWindowActivation()
    private var requestsInitialManagementWindow = false

    override init() {
        let appVariant: AppVariant
        do {
            appVariant = try AppVariant.current()
        } catch {
            fatalError("Portico app variant metadata is invalid.")
        }
        let foundationSupportDirectory = FileManager.default.urls(
            for: .applicationSupportDirectory,
            in: .userDomainMask
        )[0]
        let selectedRoot = foundationSupportDirectory.appendingPathComponent(
            appVariant.supportDirectoryName,
            isDirectory: true
        )
        let scheduler = MainQueueScheduler()

#if DEBUG
        let testConfiguration = UITestLaunchConfiguration.current
        let smokeConfiguration: SmokeLaunchConfiguration?
        do {
            smokeConfiguration = testConfiguration == nil
                ? try SmokeLaunchConfiguration.current(variant: appVariant)
                : nil
        } catch {
            fatalError("The real-helper smoke configuration is invalid.")
        }
        let reportsInitialPersistenceFailure = testConfiguration?.reportsInitialPersistenceFailure ?? false
        if let testConfiguration {
            do {
                try testConfiguration.prepareInstallationIfNeeded()
            } catch {
                fatalError("The UI test installation fixture could not be prepared.")
            }
        }
        let applicationRoot = testConfiguration?.rootURL ?? selectedRoot
        let supervisorScheduler: PorticoScheduling
        if let scale = testConfiguration?.supervisorSchedulerScale, scale != 1 {
            supervisorScheduler = UITestScaledScheduler(scale: scale)
        } else {
            supervisorScheduler = scheduler
        }
        let launcher: HelperLaunching = testConfiguration.map {
            UITestHelperLauncher(configuration: $0)
        } ?? ProcessHelperLauncher()
        let reachabilityProbe: LocalAppProbing = testConfiguration.map {
            UITestLocalAppProbe(result: $0.reachabilityResult)
        } ?? LoopbackTCPProbe()
        let copyText: (String) -> Void
        let openURL: (URL) -> Void
        if testConfiguration == nil {
            copyText = Self.copyToPasteboard
            openURL = Self.openWorkspaceURL
        } else {
            copyText = { _ in }
            openURL = { _ in }
        }
#else
        let reportsInitialPersistenceFailure = false
        let applicationRoot = selectedRoot
        let supervisorScheduler: PorticoScheduling = scheduler
        let launcher: HelperLaunching = ProcessHelperLauncher()
        let reachabilityProbe: LocalAppProbing = LoopbackTCPProbe()
        let copyText: (String) -> Void = Self.copyToPasteboard
        let openURL: (URL) -> Void = Self.openWorkspaceURL
#endif

        var admittedStore: PortalStore?
#if DEBUG
        if let smokeConfiguration {
            guard SmokeRootAdmission.run(
                configuration: smokeConfiguration,
                resolvedRootURL: selectedRoot,
                prepareStore: { admittedStore = PortalStore(rootURL: applicationRoot) }
            ) else {
                fatalError("The real-helper smoke root does not match Foundation resolution.")
            }
        }
#endif
        let store = admittedStore ?? PortalStore(rootURL: applicationRoot)
        var startupResult: PortalStoreStartupResult?
        do {
            startupResult = try store.prepareForStartup()
        } catch {
            startupResult = nil
        }
#if DEBUG
        if let smokeConfiguration, startupResult != nil {
            do {
                var installation = try store.loadInstallation()
                installation.operationalLogging = .enabled
                try store.save(installation)
            } catch {
                startupResult = nil
            }
        }
#endif

        let helperURL = Bundle.main.bundleURL
            .appendingPathComponent("Contents/Helpers/portico-helper", isDirectory: false)
        let history = DiagnosticHistory()
        let supervisor = HelperSupervisor(
            helperURL: helperURL,
            stateRootURL: applicationRoot.appendingPathComponent("tsnet", isDirectory: true),
            launcher: launcher,
            scheduler: supervisorScheduler,
            history: history
        )
        let portalController = PortalController(
            store: store,
            helper: supervisor,
            reachability: LocalAppReachability(probe: reachabilityProbe, scheduler: scheduler),
            history: history,
            diagnosticVersions: .current(appName: appVariant.displayName),
            copyText: copyText,
            announce: { message in
                NSAccessibility.post(
                    element: NSApp as Any,
                    notification: .announcementRequested,
                    userInfo: [
                        .announcement: message,
                        .priority: NSAccessibilityPriorityLevel.medium.rawValue,
                    ]
                )
            },
            openURL: openURL
        )
        let launchAtLoginController: LaunchAtLoginController?
        let launchAtLoginComposition: LaunchAtLoginComposition = appVariant.isLaunchAtLoginAvailable
            ? .available
            : .unavailable
        if let controller = launchAtLoginComposition.makeController(
            service: ServiceManagementLaunchAtLoginService(),
            offerState: { portalController.launchAtLoginOffer },
            saveOfferState: { portalController.commitLaunchAtLoginOffer($0) }
        ) {
            controller.restorePresentedOffer()
            portalController.onFreshPortalOnline = {
                controller.considerOfferAfterFreshOnline()
            }
            launchAtLoginController = controller
        } else {
            launchAtLoginController = nil
        }
        self.appVariant = appVariant
        self.supervisor = supervisor
        self.portalController = portalController
        self.launchAtLoginController = launchAtLoginController
#if DEBUG
        if let smokeConfiguration {
            let existingOnConnected = supervisor.onConnected
            supervisor.onConnected = {
                existingOnConnected?()
                try? smokeConfiguration.recordAcceptedHandshake()
            }
        }
#endif
        self.requestsInitialManagementWindow = startupResult == .freshInstallation
        if startupResult == nil {
            portalController.reportInitialPersistenceFailure()
        }
        if reportsInitialPersistenceFailure {
            DispatchQueue.main.async {
                portalController.reportInitialPersistenceFailure()
            }
        }
        super.init()
    }

    private static func copyToPasteboard(_ text: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
    }

    private static func openWorkspaceURL(_ url: URL) {
        NSWorkspace.shared.open(url)
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        NotificationCenter.default.addObserver(
            self,
            selector: #selector(applicationWindowDidClose),
            name: NSWindow.willCloseNotification,
            object: nil
        )
        guard portalController.isInstallationAvailable else { return }
        supervisor.start(loggingPreference: portalController.operationalLogging)
    }

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        supervisor.shutdown {
            sender.reply(toApplicationShouldTerminate: true)
        }
        return .terminateLater
    }

    func applicationDidBecomeActive(_ notification: Notification) {
        launchAtLoginController?.refreshStatusAfterApplicationActivation()
    }

    func takeInitialManagementWindowRequest() -> Bool {
        defer { requestsInitialManagementWindow = false }
        return requestsInitialManagementWindow
    }

    @objc private func applicationWindowDidClose(_ notification: Notification) {
        windowActivation.windowDidClose()
    }
}
