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
        Window("Portico Diagnostics", id: "diagnostics") {
            DiagnosticsView(controller: appDelegate.portalController)
        }
        .defaultSize(width: 720, height: 520)
    }
}

private struct PortalView: View {
    @Environment(\.openWindow) private var openWindow
    @ObservedObject var controller: PortalController
    @ObservedObject var supervisor: HelperSupervisor
    @State private var showingResetConfirmation = false

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Label(supervisor.availability.title, systemImage: supervisor.availability.symbolName)
            if supervisor.availability == .failed {
                Button("Retry Helper") { controller.retryHelper() }
                    .buttonStyle(.borderedProminent)
            }
            if let suffix = controller.tailnetDisplaySuffix {
                LabeledContent("Tailnet", value: suffix)
            }
            Button("Diagnostics") { openWindow(id: "diagnostics") }
            Divider()
            ForEach(controller.pendingPortals, id: \.id) { portal in
                pendingWarning(portal)
            }
            ForEach(controller.alerts) { alert in
                completedWarning(alert)
            }
            ForEach(controller.portals.filter { $0.lifecycle == .active }, id: \.id) { portal in
                PortalStatusView(controller: controller, portal: portal)
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

private struct PortalStatusView: View {
    @Environment(\.openWindow) private var openWindow
    @ObservedObject var controller: PortalController
    let portal: PortalConfiguration
    @State private var localAppPort: String

    init(controller: PortalController, portal: PortalConfiguration) {
        self.controller = controller
        self.portal = portal
        _localAppPort = State(initialValue: String(portal.localAppPort))
    }

    var body: some View {
        Text(portal.name).font(.headline)
        LabeledContent("Desired State", value: portal.desiredState.title)
        HStack {
            Text("Local App Port")
            Spacer()
            TextField("Local App Port", text: $localAppPort)
                .frame(width: 80)
                .onSubmit(updatePort)
            Button("Update") { updatePort() }
                .controlSize(.small)
            Button(portal.desiredState == .enabled ? "Stop" : "Start") {
                if portal.desiredState == .enabled {
                    controller.stopPortal(id: portal.id)
                } else {
                    controller.startPortal(id: portal.id)
                }
            }
            .controlSize(.small)
        }
        if let status = controller.statuses[portal.id] {
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
            if controller.staleStatusIDs.contains(portal.id) {
                Text("Last known Tailscale facts — stale")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        } else {
            Text("Waiting for Portal status…")
                .foregroundStyle(.secondary)
        }
        LabeledContent(
            "Local App",
            value: (controller.reachabilityStates[portal.id] ?? .unknown).title
        )
        Button("Diagnostics") { openWindow(id: "diagnostics") }
            .controlSize(.small)
    }

    private func updatePort() {
        controller.updateLocalAppPort(id: portal.id, port: localAppPort)
    }
}

private struct DiagnosticsView: View {
    @ObservedObject var controller: PortalController

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Portico Diagnostics").font(.title2)
            HStack {
                Button("Refresh Local App Reachability") { controller.refreshReachability() }
                Spacer()
                Button("Copy Report") {
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(controller.diagnosticReport(), forType: .string)
                }
            }
            ScrollView {
                Text(controller.diagnosticReport())
                    .font(.system(.body, design: .monospaced))
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .padding()
    }
}

private extension LocalAppReachabilityState {
    var title: String {
        switch self {
        case .unknown: "Unknown"
        case .reachable: "Reachable"
        case .unavailable: "Unavailable"
        }
    }
}

private extension HelperAvailability {
    var title: String {
        switch self {
        case .connecting: "Connecting"
        case let .retrying(attempt, delay): "Retry \(attempt) in \(Int(delay))s"
        case .connected: "Connected"
        case .failed: "Helper unavailable"
        case .shuttingDown: "Shutting down"
        }
    }

    var symbolName: String {
        switch self {
        case .connecting, .retrying: "ellipsis.circle"
        case .connected: "checkmark.circle"
        case .failed: "exclamationmark.triangle"
        case .shuttingDown: "stop.circle"
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

private extension PortalDesiredState {
    var title: String {
        switch self {
        case .enabled: "Enabled"
        case .stopped: "Stopped"
        }
    }
}
