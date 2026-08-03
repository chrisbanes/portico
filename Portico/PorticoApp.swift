import AppKit
import SwiftUI

@main
struct PorticoApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate

    var body: some Scene {
        MenuBarExtra("Portico", systemImage: "door.left.hand.open") {
            HelperStatusView(supervisor: appDelegate.supervisor)
        }
    }
}

private struct HelperStatusView: View {
    @ObservedObject var supervisor: HelperSupervisor

    var body: some View {
        Label(supervisor.availability.title, systemImage: supervisor.availability.symbolName)
        Divider()
        Button("Quit Portico") {
            NSApp.terminate(nil)
        }
        .keyboardShortcut("q")
    }
}

private extension HelperAvailability {
    var title: String {
        switch self {
        case .connecting: "Connecting"
        case .connected: "Connected"
        case .failed: "Helper unavailable"
        }
    }

    var symbolName: String {
        switch self {
        case .connecting: "ellipsis.circle"
        case .connected: "checkmark.circle"
        case .failed: "exclamationmark.triangle"
        }
    }
}
