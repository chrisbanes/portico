import AppKit
import Combine

@MainActor
final class ManagementRouting: ObservableObject {
    @Published private(set) var overviewRequest = 0

    func requestOverview() {
        overviewRequest += 1
    }
}

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    let supervisor: HelperSupervisor
    let portalController: PortalController
    let launchAtLoginController: LaunchAtLoginController
    let managementRouting = ManagementRouting()
    private var requestsInitialManagementWindow = false

    override init() {
        let productionRoot = FileManager.default.urls(
            for: .applicationSupportDirectory,
            in: .userDomainMask
        )[0].appendingPathComponent("Portico", isDirectory: true)
        let scheduler = MainQueueScheduler()

#if DEBUG
        let testConfiguration = UITestLaunchConfiguration.current
        if let testConfiguration {
            do {
                try testConfiguration.prepareInstallationIfNeeded()
            } catch {
                fatalError("The UI test installation fixture could not be prepared.")
            }
        }
        let applicationRoot = testConfiguration?.rootURL ?? productionRoot
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
        let launchAtLoginService: LaunchAtLoginServicing = testConfiguration.map {
            UITestLaunchAtLoginService(
                status: $0.launchAtLoginStatus,
                registrationOutcome: $0.registrationOutcome
            )
        } ?? ServiceManagementLaunchAtLoginService()
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
        let applicationRoot = productionRoot
        let supervisorScheduler: PorticoScheduling = scheduler
        let launcher: HelperLaunching = ProcessHelperLauncher()
        let reachabilityProbe: LocalAppProbing = LoopbackTCPProbe()
        let launchAtLoginService: LaunchAtLoginServicing = ServiceManagementLaunchAtLoginService()
        let copyText: (String) -> Void = Self.copyToPasteboard
        let openURL: (URL) -> Void = Self.openWorkspaceURL
#endif

        let store = PortalStore(rootURL: applicationRoot)
        let startupResult: PortalStoreStartupResult?
        do {
            startupResult = try store.prepareForStartup()
        } catch {
            startupResult = nil
        }

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
        let launchAtLoginController = LaunchAtLoginController(
            service: launchAtLoginService,
            offerState: { portalController.launchAtLoginOffer },
            saveOfferState: { portalController.commitLaunchAtLoginOffer($0) }
        )
        launchAtLoginController.restorePresentedOffer()
        portalController.onFreshPortalOnline = {
            launchAtLoginController.considerOfferAfterFreshOnline()
        }
        self.supervisor = supervisor
        self.portalController = portalController
        self.launchAtLoginController = launchAtLoginController
        self.requestsInitialManagementWindow = startupResult == .freshInstallation
        if startupResult == nil {
            portalController.reportInitialPersistenceFailure()
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
        supervisor.start(loggingPreference: portalController.operationalLogging)
    }

    func applicationShouldRestoreApplicationState(_ app: NSApplication) -> Bool {
        false
    }

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        supervisor.shutdown {
            sender.reply(toApplicationShouldTerminate: true)
        }
        return .terminateLater
    }

    func applicationDidBecomeActive(_ notification: Notification) {
        launchAtLoginController.refreshStatusAfterApplicationActivation()
    }

    func takeInitialManagementWindowRequest() -> Bool {
        defer { requestsInitialManagementWindow = false }
        return requestsInitialManagementWindow
    }
}
