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
    @State private var showingResetConfirmation = false

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Label(supervisor.availability.title, systemImage: supervisor.availability.symbolName)
            if let suffix = controller.tailnetDisplaySuffix {
                LabeledContent("Tailnet", value: suffix)
            }
            Divider()
            ForEach(controller.pendingPortals, id: \.id) { portal in
                pendingWarning(portal)
            }
            ForEach(controller.alerts) { alert in
                completedWarning(alert)
            }
            ForEach(controller.portals, id: \.id) { portal in
                portalStatus(portal)
                Divider()
            }
            addPortalForm
            if controller.canResetTailnet {
                Divider()
                Button("Reset Tailnet", role: .destructive) {
                    showingResetConfirmation = true
                }
            }
            if let message = controller.message {
                Text(message)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .padding()
        .frame(width: 380)
        .confirmationDialog(
            "Reset this installation's tailnet binding? This does not remove any remote Tailscale node.",
            isPresented: $showingResetConfirmation,
            titleVisibility: .visible
        ) {
            Button("Reset Tailnet", role: .destructive) {
                controller.resetTailnet(confirmed: true)
            }
            Button("Cancel", role: .cancel) {}
        }
        Divider()
        Button("Quit Portico") {
            NSApp.terminate(nil)
        }
        .keyboardShortcut("q")
    }

    private var addPortalForm: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Add Portal").font(.headline)
            HStack {
                Text("Detected Local Apps").font(.subheadline)
                Spacer()
                Button("Refresh") { controller.refreshLocalApps() }
                    .disabled(controller.isRefreshingLocalApps)
            }
            if controller.isRefreshingLocalApps {
                ProgressView()
                    .controlSize(.small)
            }
            ForEach(controller.localApps, id: \.localAppPort) { candidate in
                Button {
                    controller.selectLocalApp(candidate)
                } label: {
                    HStack {
                        Text(candidate.processLabel)
                        Spacer()
                        Text(String(candidate.localAppPort))
                            .foregroundStyle(.secondary)
                    }
                }
                .buttonStyle(.plain)
            }
            if let message = controller.localAppsMessage {
                Text(message)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
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
        if portal.lifecycle == .pendingTailnetRejection {
            Text("Cleanup pending")
                .foregroundStyle(.secondary)
        } else if let status = controller.statuses[portal.id] {
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
                Button("Authenticate") { controller.authenticate(id: portal.id) }
                    .buttonStyle(.borderedProminent)
            }
        } else {
            Text("Waiting for Portal status…")
                .foregroundStyle(.secondary)
        }
    }

    private func pendingWarning(_ portal: PortalConfiguration) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Label("Cleanup in progress", systemImage: "exclamationmark.triangle.fill")
                .font(.headline)
            Text(controller.pendingWarningText(for: portal))
                .font(.caption)
            Link("How to remove a Tailscale device", destination: PortalController.manualRemovalURL)
                .font(.caption)
        }
    }

    private func completedWarning(_ alert: InstallationAlert) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Label("Portal removed from this Mac", systemImage: "exclamationmark.triangle.fill")
                .font(.headline)
            Text(controller.completedWarningText(for: alert))
                .font(.caption)
            Link("How to remove a Tailscale device", destination: PortalController.manualRemovalURL)
                .font(.caption)
            Button("Dismiss") { controller.dismissAlert(id: alert.id) }
                .controlSize(.small)
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
