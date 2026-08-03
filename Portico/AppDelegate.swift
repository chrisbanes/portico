import AppKit

final class AppDelegate: NSObject, NSApplicationDelegate {
    let supervisor: HelperSupervisor

    override init() {
        let helperURL = Bundle.main.bundleURL
            .appendingPathComponent("Contents/Helpers/portico-helper", isDirectory: false)
        let supervisor = HelperSupervisor(
            helperURL: helperURL,
            launcher: ProcessHelperLauncher()
        )
        self.supervisor = supervisor
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
