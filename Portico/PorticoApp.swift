import AppKit
import SwiftUI

@main
struct PorticoApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate

    var body: some Scene {
        MenuBarExtra("Portico", systemImage: "door.left.hand.open") {
            PortalView(controller: appDelegate.portalController, supervisor: appDelegate.supervisor)
        }
        .menuBarExtraStyle(.window)
    }
}

private struct PortalView: View {
    @ObservedObject var controller: PortalController
    @ObservedObject var supervisor: HelperSupervisor

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Label(supervisor.availability.title, systemImage: supervisor.availability.symbolName)
            Divider()
            if let portal = controller.portal {
                portalStatus(portal)
            } else {
                addPortalForm
            }
            if let message = controller.message {
                Text(message)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .padding()
        .frame(width: 320)
        Divider()
        Button("Quit Portico") {
            NSApp.terminate(nil)
        }
        .keyboardShortcut("q")
    }

    private var addPortalForm: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Add Portal").font(.headline)
            TextField("Portal Name", text: $controller.portalName)
            TextField("Local App Port", text: $controller.localAppPort)
            Button("Add Portal") { controller.addPortal() }
                .buttonStyle(.borderedProminent)
        }
    }

    @ViewBuilder
    private func portalStatus(_ portal: PortalConfiguration) -> some View {
        Text(portal.name).font(.headline)
        LabeledContent("Local App Port", value: String(portal.localAppPort))
        if let status = controller.status {
            LabeledContent("Tailscale", value: status.state.title)
            if let assignedName = status.assignedName {
                LabeledContent("Assigned Name", value: assignedName)
            }
            if let portalURL = status.portalURL {
                LabeledContent("Portal URL", value: portalURL.absoluteString)
            }
            if !status.addresses.isEmpty {
                LabeledContent("Addresses", value: status.addresses.joined(separator: ", "))
            }
            if status.state == .authenticating {
                Button("Authenticate") { controller.authenticate() }
                    .buttonStyle(.borderedProminent)
            }
        } else {
            Text("Waiting for Portal status…")
                .foregroundStyle(.secondary)
        }
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

private extension PortalTailscaleState {
    var title: String {
        switch self {
        case .authenticating: "Authentication required"
        case .awaitingApproval: "Awaiting approval"
        case .connecting: "Connecting"
        case .online: "Online"
        case .stopped: "Stopped"
        case .error: "Error"
        }
    }
}
