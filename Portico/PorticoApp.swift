import AppKit
import SwiftUI

@main
struct PorticoApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate

    var body: some Scene {
        MenuBarExtra("Portico", systemImage: "door.left.hand.open") {
            PortalView(
                controller: appDelegate.portalController,
                supervisor: appDelegate.supervisor,
                launchAtLogin: appDelegate.launchAtLoginController
            )
            .environment(\.openURL, OpenURLAction { url in
                appDelegate.portalController.openExternalURL(url)
                return .handled
            })
        }
        .menuBarExtraStyle(.window)
        .commands { PorticoCommands() }
        Window("Portico Diagnostics", id: "diagnostics") {
            DiagnosticsView(controller: appDelegate.portalController)
        }
        .defaultSize(width: 720, height: 520)
        Settings {
            SettingsView(
                controller: appDelegate.portalController,
                supervisor: appDelegate.supervisor,
                launchAtLogin: appDelegate.launchAtLoginController
            )
        }
    }
}

private struct PorticoCommands: Commands {
    @Environment(\.openWindow) private var openWindow

    var body: some Commands {
        CommandGroup(after: .appSettings) {
            Button("Diagnostics") { openWindow(id: "diagnostics") }
                .keyboardShortcut("d", modifiers: [.command, .shift])
#if DEBUG
            if UITestLaunchConfiguration.current?.scenario == .restarting {
                Button("Complete UI Test Restart") { UITestRestartGate.shared.release() }
                    .keyboardShortcut("r", modifiers: [.command, .shift])
            }
#endif
        }
    }
}

private struct PortalView: View {
    private enum AccessibilityFocusTarget: Hashable {
        case portalNameField
        case localAppPortField
        case portal(UUID)
        case removalNotice(UUID)
    }

    @Environment(\.openWindow) private var openWindow
    @ObservedObject var controller: PortalController
    @ObservedObject var supervisor: HelperSupervisor
    @ObservedObject var launchAtLogin: LaunchAtLoginController
    @State private var showingResetConfirmation = false
    @State private var removalCandidate: PortalConfiguration?
    @State private var removalCompletionFocusID: UUID?
    @FocusState private var inputFocus: AccessibilityFocusTarget?
    @AccessibilityFocusState private var accessibilityFocus: AccessibilityFocusTarget?

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Label(supervisor.availability.title, systemImage: supervisor.availability.symbolName)
                .accessibilityIdentifier("helper-state")
            if supervisor.availability == .failed {
                Button("Retry Helper") { controller.retryHelper() }
                    .buttonStyle(.borderedProminent)
                    .accessibilityIdentifier("retry-helper")
            }
            if let suffix = controller.tailnetDisplaySuffix {
                LabeledContent("Tailnet", value: suffix)
            }
            if launchAtLogin.isOffering {
                VStack(alignment: .leading, spacing: 6) {
                    Text("Open Portico at Login?").font(.headline)
                    Text("You can change this later in Settings.")
                    HStack {
                        Button("Not Now") { launchAtLogin.declineOffer() }
                            .accessibilityIdentifier("login-offer-decline")
                        Button("Enable") { launchAtLogin.acceptOffer() }
                            .buttonStyle(.borderedProminent)
                            .accessibilityIdentifier("login-offer-enable")
                    }
                }
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("launch-at-login-offer")
            }
            Divider()
            ForEach(controller.pendingPortals, id: \.id) { portal in
                pendingWarning(portal)
            }
            ForEach(controller.pendingRemovalPortals, id: \.id) { portal in
                pendingRemoval(portal)
            }
            ForEach(controller.removalNotices) { notice in
                removalNotice(notice)
                    .accessibilityFocused($accessibilityFocus, equals: .removalNotice(notice.id))
            }
            ForEach(controller.alerts) { alert in
                completedWarning(alert)
            }
            ForEach(controller.portals.filter { $0.lifecycle == .active }, id: \.id) { portal in
                PortalStatusView(
                    controller: controller,
                    portal: portal,
                    onRemove: { removalCandidate = portal }
                )
                .accessibilityFocused($accessibilityFocus, equals: .portal(portal.id))
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
            Divider()
            SettingsLink()
                .keyboardShortcut(",", modifiers: .command)
                .accessibilityIdentifier("settings")
            Button("Diagnostics") { openWindow(id: "diagnostics") }
                .keyboardShortcut("d", modifiers: [.command, .shift])
                .accessibilityIdentifier("diagnostics")
        }
        .padding()
        .frame(width: 380)
        .onDisappear {
            if launchAtLogin.isOffering {
                launchAtLogin.declineOffer()
            }
        }
        .onChange(of: controller.removalNotices) { _, notices in
            guard let id = removalCompletionFocusID,
                  notices.contains(where: { $0.id == id })
            else { return }
            removalCompletionFocusID = nil
            accessibilityFocus = .removalNotice(id)
        }
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
        .sheet(isPresented: Binding(
            get: { removalCandidate != nil },
            set: { if !$0 { cancelRemoval() } }
        )) {
            if let portal = removalCandidate {
                VStack(alignment: .leading, spacing: 12) {
                    Text("Remove Portal?").font(.headline)
                        .accessibilityIdentifier("remove-confirmation-heading")
                    Text(controller.removalWarningText(for: portal))
                    Link(
                        "How to remove a Tailscale device",
                        destination: PortalController.manualRemovalURL
                    )
                    HStack {
                        Spacer()
                        Button("Cancel") { cancelRemoval() }
                            .keyboardShortcut(.cancelAction)
                            .accessibilityIdentifier("remove-cancel")
                        Button("Remove Portal", role: .destructive) {
                            removalCompletionFocusID = portal.id
                            controller.removePortal(id: portal.id)
                            removalCandidate = nil
                        }
                        .keyboardShortcut(.defaultAction)
                        .accessibilityIdentifier("remove-confirm")
                    }
                }
                .padding()
                .frame(width: 420)
            }
        }
        Divider()
        Button("Quit Portico") {
            NSApp.terminate(nil)
        }
        .keyboardShortcut("q")
        .accessibilityIdentifier("quit")
    }

    private var addPortalForm: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Add Portal").font(.headline)
                .accessibilityIdentifier("add-form")
            if PortalPresentation.showsPrerequisiteGuidance(portalCount: controller.portals.count) {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Before creating your first Portal").font(.subheadline)
                    ForEach(PortalPresentation.prerequisiteGuidance, id: \.self) { guidance in
                        Label(guidance, systemImage: "info.circle")
                            .font(.caption)
                    }
                }
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("add-guidance")
            }
            if controller.operationalLogging == .undecided {
                VStack(alignment: .leading, spacing: 6) {
                    Text("Choose operational-support logging before adding a Portal.")
                        .font(.caption)
                    HStack {
                        Button("Allow Operational-support Logging") {
                            controller.setOperationalLogging(.enabled)
                        }
                        .accessibilityIdentifier("logging-enabled")
                        Button("Disable Operational-support Logging") {
                            controller.setOperationalLogging(.disabled)
                        }
                        .accessibilityIdentifier("logging-disabled")
                    }
                }
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("logging-choice")
            }
            HStack {
                Text("Detected Local Apps").font(.subheadline)
                Spacer()
                Button("Refresh") { controller.refreshLocalApps() }
                    .disabled(!controller.actionAvailability().refreshLocalApps)
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
                .accessibilityIdentifier("portal-name-field")
                .focused($inputFocus, equals: .portalNameField)
                .accessibilityFocused($accessibilityFocus, equals: .portalNameField)
                .onSubmit { validateNameAndAdvance() }
            TextField("Local App Port", text: $controller.localAppPort)
                .accessibilityIdentifier("local-app-port-field")
                .focused($inputFocus, equals: .localAppPortField)
                .accessibilityFocused($accessibilityFocus, equals: .localAppPortField)
                .onSubmit { submitPortal() }
            Button("Add Portal") { submitPortal() }
                .buttonStyle(.borderedProminent)
                .disabled(!controller.actionAvailability().addPortal)
        }
    }

    private func validateNameAndAdvance() {
        do {
            _ = try PortalInputValidator.validate(name: controller.portalName, port: "1")
            inputFocus = .localAppPortField
            accessibilityFocus = .localAppPortField
        } catch {
            inputFocus = .portalNameField
            accessibilityFocus = .portalNameField
        }
    }

    private func submitPortal() {
        switch controller.addPortal() {
        case .invalidName:
            inputFocus = .portalNameField
            accessibilityFocus = .portalNameField
        case .invalidPort:
            inputFocus = .localAppPortField
            accessibilityFocus = .localAppPortField
        case nil:
            break
        }
    }

    private func cancelRemoval() {
        guard let portal = removalCandidate else { return }
        removalCandidate = nil
        DispatchQueue.main.async {
            accessibilityFocus = .portal(portal.id)
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

    private func pendingRemoval(_ portal: PortalConfiguration) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Label("Removing Portal", systemImage: "trash")
                .font(.headline)
            Text(controller.pendingRemovalWarningText(for: portal))
                .font(.caption)
            Link("How to remove a Tailscale device", destination: PortalController.manualRemovalURL)
                .font(.caption)
            if controller.removalStates[portal.id] == .failed {
                Button("Retry Removal") { controller.retryRemoval(id: portal.id) }
                    .controlSize(.small)
                    .disabled(!controller.actionAvailability(for: portal).retryRemoval)
            } else {
                ProgressView()
                    .controlSize(.small)
            }
            Button("Diagnostics") { openWindow(id: "diagnostics") }
                .controlSize(.small)
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("removing-portal")
    }

    private func removalNotice(_ notice: PortalRemovalNotice) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Label("Portal removed from this Mac", systemImage: "exclamationmark.triangle.fill")
                .font(.headline)
            Text(controller.removalNoticeText(for: notice))
                .font(.caption)
            Link("How to remove a Tailscale device", destination: PortalController.manualRemovalURL)
                .font(.caption)
            Button("Dismiss") { controller.dismissRemovalNotice(id: notice.id) }
                .controlSize(.small)
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("removal-complete")
    }
}

private struct PortalStatusView: View {
    @Environment(\.openWindow) private var openWindow
    @ObservedObject var controller: PortalController
    let portal: PortalConfiguration
    let onRemove: () -> Void
    @State private var localAppPort: String
    @FocusState private var startStopFocused: Bool

    init(controller: PortalController, portal: PortalConfiguration, onRemove: @escaping () -> Void) {
        self.controller = controller
        self.portal = portal
        self.onRemove = onRemove
        _localAppPort = State(initialValue: String(portal.localAppPort))
    }

    var body: some View {
        let isStale = controller.staleStatusIDs.contains(portal.id)
        let status = controller.statuses[portal.id]
        let presentation = PortalPresentation(
            portal: portal,
            status: status,
            reachability: controller.reachabilityStates[portal.id] ?? .unknown,
            isStale: isStale
        )
        let rowActions = controller.actionAvailability(for: portal)
        let editedPortActions = controller.actionAvailability(for: portal, editedPort: localAppPort)
        Text(presentation.portalName).font(.headline)
            .accessibilityLabel("Portal Name, \(presentation.portalName)")
            .accessibilityIdentifier("portal-name")
        if let assignedName = presentation.assignedName {
            LabeledContent("Assigned Name", value: assignedName)
                .accessibilityIdentifier("assigned-name")
        }
        if let explanation = presentation.collisionExplanation {
            Text(explanation).font(.caption).foregroundStyle(.secondary)
        }
        LabeledContent("Desired State", value: presentation.desiredState)
            .accessibilityIdentifier("desired-state")
        LabeledContent("Tailscale", value: presentation.tailscaleState)
            .accessibilityIdentifier("tailscale-state")
        LabeledContent("Local App", value: presentation.localAppReachability)
            .accessibilityIdentifier("local-app-state")
        if let status = controller.statuses[portal.id] {
            if let portalURL = status.portalURL {
                LabeledContent(
                    presentation.portalURLLabel,
                    value: portalURL.absoluteString
                )
                .accessibilityIdentifier("portal-url")
                HStack {
                    Button("Copy Portal URL") { controller.copyPortalURL(id: portal.id) }
                        .disabled(!rowActions.copyPortalURL)
                        .accessibilityIdentifier("copy-portal-url")
                    Button("Open Portal URL") { controller.openPortalURL(id: portal.id) }
                        .disabled(!rowActions.openPortalURL)
                        .accessibilityIdentifier("open-portal-url")
                }
            }
            if !status.addresses.isEmpty {
                LabeledContent("Addresses", value: status.addresses.joined(separator: ", "))
            }
            if status.state == .authenticating {
                Button("Authenticate") { controller.authenticate(id: portal.id) }
                    .buttonStyle(.borderedProminent)
                    .disabled(!rowActions.authenticate)
                    .accessibilityIdentifier("authenticate")
            }
        }
        HStack {
            Text("Local App Port")
            Spacer()
            TextField("Local App Port", text: $localAppPort)
                .frame(width: 80)
                .onSubmit(updatePort)
                .accessibilityIdentifier("edit-local-app-port")
            Button("Update") { updatePort() }
                .controlSize(.small)
                .disabled(!editedPortActions.editPort)
                .accessibilityIdentifier("update-local-app-port")
            Button(portal.desiredState == .enabled ? "Stop" : "Start") {
                if portal.desiredState == .enabled {
                    controller.stopPortal(id: portal.id)
                } else {
                    controller.startPortal(id: portal.id)
                }
                DispatchQueue.main.async {
                    startStopFocused = true
                }
            }
            .controlSize(.small)
            .focusable()
            .focused($startStopFocused)
            .accessibilityIdentifier("start-stop")
            .disabled(portal.desiredState == .enabled
                ? !rowActions.stop
                : !rowActions.start)
        }
        Button("Diagnostics") { openWindow(id: "diagnostics") }
            .controlSize(.small)
            .accessibilityIdentifier("portal-diagnostics")
        Button("Remove Portal", role: .destructive, action: onRemove)
            .controlSize(.small)
            .accessibilityIdentifier("remove-portal")
    }

    private func updatePort() {
        controller.updateLocalAppPort(id: portal.id, port: localAppPort)
    }
}

private struct SettingsView: View {
    @ObservedObject var controller: PortalController
    @ObservedObject var supervisor: HelperSupervisor
    @ObservedObject var launchAtLogin: LaunchAtLoginController
    @AccessibilityFocusState private var headingFocused: Bool

    var body: some View {
        Form {
            Text("Portico Settings")
                .font(.title2)
                .accessibilityAddTraits(.isHeader)
                .accessibilityFocused($headingFocused)
                .accessibilityIdentifier("settings-heading")
            Picker(
                "Operational-support logging",
                selection: Binding(
                    get: { controller.operationalLogging },
                    set: { controller.setOperationalLogging($0) }
                )
            ) {
                Text("Allow operational-support logging").tag(OperationalLoggingPreference.enabled)
                Text("Disable operational-support logging").tag(OperationalLoggingPreference.disabled)
            }
            .pickerStyle(.radioGroup)
            .accessibilityIdentifier("logging-preference")
            Text("Changing this setting safely restarts the helper.")
                .font(.caption)
            LabeledContent("Helper", value: supervisor.availability.title)
                .accessibilityIdentifier("settings-helper-state")
            if let error = controller.operationalLoggingError {
                Text(error).foregroundStyle(.secondary)
                    .accessibilityIdentifier("logging-preference-error")
            }
            LabeledContent("Launch at login", value: launchAtLogin.status.title)
                .accessibilityIdentifier("launch-at-login-status")
            if launchAtLogin.status == .notRegistered {
                Button("Enable Launch at Login") { launchAtLogin.setEnabled(true) }
                    .accessibilityIdentifier("enable-launch-at-login")
            } else if launchAtLogin.status == .enabled {
                Button("Disable Launch at Login") { launchAtLogin.setEnabled(false) }
                    .accessibilityIdentifier("disable-launch-at-login")
            } else if launchAtLogin.status == .requiresApproval {
                Button("Open Login Items Settings") { launchAtLogin.openLoginItemsSettings() }
                    .accessibilityIdentifier("open-login-items-settings")
            }
            if controller.launchAtLoginOffer == .accepted,
               launchAtLogin.status == .notRegistered,
               launchAtLogin.errorMessage != nil {
                Button("Retry Launch at Login") { launchAtLogin.retryRegistration() }
                    .accessibilityIdentifier("retry-launch-at-login")
            }
            if let error = launchAtLogin.errorMessage {
                Text(error).foregroundStyle(.secondary)
                    .accessibilityIdentifier("launch-at-login-error")
            }
        }
        .padding()
        .frame(width: 480)
        .onAppear {
            launchAtLogin.refreshStatus()
            headingFocused = true
        }
    }
}

private struct DiagnosticsView: View {
    @ObservedObject var controller: PortalController
    @AccessibilityFocusState private var headingFocused: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Portico Diagnostics").font(.title2)
                .accessibilityAddTraits(.isHeader)
                .accessibilityFocused($headingFocused)
                .accessibilityIdentifier("diagnostics-heading")
            HStack {
                Button("Refresh Local App Reachability") { controller.refreshReachability() }
                Spacer()
                Button("Copy Report") {
                    controller.copyDiagnosticReport()
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
        .onAppear { headingFocused = true }
    }
}

private extension LaunchAtLoginStatus {
    var title: String {
        switch self {
        case .notRegistered: "Off"
        case .enabled: "On"
        case .requiresApproval: "Approval required"
        case .notFound: "Unavailable"
        }
    }
}

private extension HelperAvailability {
    var title: String {
        switch self {
        case .awaitingLoggingChoice: "Awaiting logging choice"
        case .restarting: "Restarting"
        case .connecting: "Connecting"
        case let .retrying(attempt, delay): "Retry \(attempt) in \(Int(delay))s"
        case .connected: "Connected"
        case .failed: "Helper unavailable"
        case .shuttingDown: "Shutting down"
        }
    }

    var symbolName: String {
        switch self {
        case .awaitingLoggingChoice, .restarting, .connecting, .retrying: "ellipsis.circle"
        case .connected: "checkmark.circle"
        case .failed: "exclamationmark.triangle"
        case .shuttingDown: "stop.circle"
        }
    }
}
