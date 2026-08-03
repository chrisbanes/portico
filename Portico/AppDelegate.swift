import AppKit

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    let supervisor: HelperSupervisor
    let portalController: PortalController

    override init() {
        let applicationRoot = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("Portico", isDirectory: true)
        let helperURL = Bundle.main.bundleURL
            .appendingPathComponent("Contents/Helpers/portico-helper", isDirectory: false)
        let supervisor = HelperSupervisor(
            helperURL: helperURL,
            stateRootURL: applicationRoot.appendingPathComponent("tsnet", isDirectory: true),
            launcher: ProcessHelperLauncher()
        )
        self.supervisor = supervisor
        self.portalController = PortalController(
            store: PortalStore(rootURL: applicationRoot),
            helper: supervisor,
            openURL: { NSWorkspace.shared.open($0) }
        )
        super.init()
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        supervisor.start()
    }

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        supervisor.shutdown {
            sender.reply(toApplicationShouldTerminate: true)
        }
        return .terminateLater
    }
}
