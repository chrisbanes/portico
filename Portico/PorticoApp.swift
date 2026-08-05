import AppKit
import SwiftUI

@main
struct PorticoApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @Environment(\.openWindow) private var openWindow

    var body: some Scene {
        let _ = scheduleInitialManagementWindowIfNeeded()
        MenuBarExtra("Portico", systemImage: "door.left.hand.open") {
            PortalView(
                controller: appDelegate.portalController,
                supervisor: appDelegate.supervisor,
                launchAtLogin: appDelegate.launchAtLoginController,
                takeInitialManagementWindowRequest: appDelegate.takeInitialManagementWindowRequest
            )
            .environment(\.openURL, OpenURLAction { url in
                appDelegate.portalController.openExternalURL(url)
                return .handled
            })
        }
        .menuBarExtraStyle(.window)
        .commands { PorticoCommands() }
        Window("Portico", id: "management") {
            OverviewView(
                controller: appDelegate.portalController,
                supervisor: appDelegate.supervisor
            )
        }
        .defaultSize(width: 720, height: 520)
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

    private func scheduleInitialManagementWindowIfNeeded() {
        guard appDelegate.takeInitialManagementWindowRequest() else { return }
        DispatchQueue.main.async {
            openWindow(id: "management")
        }
    }
}

private struct PorticoCommands: Commands {
    @Environment(\.openWindow) private var openWindow

    var body: some Commands {
        CommandGroup(after: .appSettings) {
            Button("Open Portico") { openWindow(id: "management") }
                .keyboardShortcut("o", modifiers: [.command, .shift])
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

private struct OverviewView: View {
    private enum Destination: Hashable {
        case overview
        case portal(UUID)
    }

    @ObservedObject var controller: PortalController
    @ObservedObject var supervisor: HelperSupervisor
    @State private var selection: Destination? = .overview
    @State private var showingAddPortal = false

    var body: some View {
        NavigationSplitView {
            List(selection: $selection) {
                Label("Overview", systemImage: "door.left.hand.open")
                    .accessibilityIdentifier("management-sidebar-overview")
                    .tag(Destination.overview)
                ForEach(controller.portals.filter { $0.lifecycle == .active }, id: \.id) { portal in
                    let presentation = PortalPresentation(
                        portal: portal,
                        status: controller.statuses[portal.id],
                        reachability: controller.reachabilityStates[portal.id] ?? .unknown,
                        isStale: controller.staleStatusIDs.contains(portal.id)
                    )
                    Button {
                        selection = .portal(portal.id)
                    } label: {
                        VStack(alignment: .leading) {
                            Text(presentation.portalName)
                            Text(presentation.tailscaleState)
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                    }
                    .buttonStyle(.plain)
                    .accessibilityElement(children: .combine)
                    .accessibilityIdentifier("management-sidebar-portal-\(portal.name)")
                    .tag(Destination.portal(portal.id))
                }
            }
            .navigationTitle("Portico")
        } detail: {
            managementContent
            .navigationTitle(selectedPortal?.name ?? "Overview")
            .accessibilityIdentifier("management-overview")
            .toolbar {
                Button("Add Portal") { showingAddPortal = true }
                    .disabled(controller.operationalLogging == .undecided)
                    .accessibilityIdentifier("overview-add-portal")
            }
            .sheet(isPresented: Binding(
                get: { showingAddPortal },
                set: { isPresented in
                    if !isPresented { controller.discardPortalDraft() }
                    showingAddPortal = isPresented
                }
            )) {
                AddPortalSheet(
                    controller: controller,
                    cancel: {
                        controller.discardPortalDraft()
                        showingAddPortal = false
                    },
                    dismissAfterPersistence: { showingAddPortal = false }
                )
            }
            .onChange(of: controller.portals) { _, portals in
                guard let currentSelection = selection,
                      case let .portal(id) = currentSelection,
                      !portals.contains(where: { $0.id == id && $0.lifecycle == .active })
                else { return }
                selection = .overview
            }
        }
    }

    private var selectedPortal: PortalConfiguration? {
        guard let selection, case let .portal(id) = selection else { return nil }
        return controller.portals.first(where: { $0.id == id && $0.lifecycle == .active })
    }

    @ViewBuilder
    private var managementContent: some View {
        if let selectedPortal {
            SelectedPortalView(controller: controller, portal: selectedPortal)
                .id(selectedPortal.id)
        } else if controller.portals.isEmpty, controller.operationalLogging != .undecided {
            VStack(spacing: 16) {
                ContentUnavailableView {
                    Label("No Portals", systemImage: "door.left.hand.open")
                } description: {
                    Text("Add a Portal to give a Local App a private tailnet doorway.")
                }
                Button("Add Portal") { showingAddPortal = true }
                    .buttonStyle(.borderedProminent)
                    .accessibilityIdentifier("overview-empty-add-portal")
            }
            .accessibilityElement(children: .contain)
        } else {
            Form {
                Section("Overview") {
                    LabeledContent("Helper", value: supervisor.availability.title)
                        .accessibilityIdentifier("overview-helper-state")
                    LabeledContent("Tailnet", value: controller.tailnetDisplaySuffix ?? "Not connected")
                        .accessibilityIdentifier("overview-tailnet")
                }
                if PortalPresentation.showsPrerequisiteGuidance(portalCount: controller.portals.count) {
                    Section("Before creating your first Portal") {
                        ForEach(PortalPresentation.prerequisiteGuidance, id: \.self) { guidance in
                            Label(guidance, systemImage: "info.circle")
                        }
                    }
                    .accessibilityIdentifier("overview-first-portal-guidance")
                }
                Section("Operational-support logging") {
                    if controller.operationalLogging == .undecided {
                        Button("Allow Operational-support Logging") {
                            controller.setOperationalLogging(.enabled)
                        }
                        .accessibilityIdentifier("overview-logging-enabled")
                        Button("Disable Operational-support Logging") {
                            controller.setOperationalLogging(.disabled)
                        }
                        .accessibilityIdentifier("overview-logging-disabled")
                    } else {
                        Text(controller.operationalLogging == .enabled
                            ? "Operational-support logging is allowed."
                            : "Operational-support logging is disabled.")
                    }
                }
                if !controller.portals.isEmpty {
                    Section("Portals") {
                        ForEach(controller.portals.filter { $0.lifecycle == .active }, id: \.id) { portal in
                            Text(portal.name)
                                .accessibilityIdentifier("overview-portal-\(portal.name)")
                        }
                    }
                }
                if let message = controller.message {
                    Text(message)
                        .foregroundStyle(.secondary)
                        .accessibilityIdentifier("overview-message")
                }
            }
            .formStyle(.grouped)
        }
    }
}

private struct SelectedPortalView: View {
    @Environment(\.openWindow) private var openWindow
    @ObservedObject var controller: PortalController
    let portal: PortalConfiguration
    @State private var destinationEdit: PortalDestinationEdit

    init(controller: PortalController, portal: PortalConfiguration) {
        self.controller = controller
        self.portal = portal
        _destinationEdit = State(initialValue: PortalDestinationEdit(destination: portal.destination))
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
        let portalActions = controller.actionAvailability(for: portal)
        let destinationActions = controller.actionAvailability(
            for: portal,
            editedDestination: destinationEdit
        )
        Form {
            Section("Identity") {
                LabeledContent("Portal Name", value: presentation.portalName)
                    .accessibilityIdentifier("selected-portal-name")
                LabeledContent("Assigned Name", value: presentation.assignedName ?? "Not assigned")
                    .accessibilityIdentifier("selected-assigned-name")
                if let explanation = presentation.collisionExplanation {
                    Text(explanation)
                        .foregroundStyle(.secondary)
                }
            }
            Section("Portal State") {
                LabeledContent("Desired State", value: presentation.desiredState)
                    .accessibilityIdentifier("selected-desired-state")
                LabeledContent("Tailscale", value: presentation.tailscaleState)
                    .accessibilityIdentifier("selected-tailscale-state")
                if portal.localAppPort != nil {
                    LabeledContent("Local App", value: presentation.localAppReachability)
                        .accessibilityIdentifier("selected-local-app-state")
                }
            }
            Section("Portal URL and addresses") {
                if let portalURL = status?.portalURL {
                    LabeledContent(presentation.portalURLLabel, value: portalURL.absoluteString)
                        .accessibilityIdentifier("selected-portal-url")
                    HStack {
                        Button("Copy Portal URL") { controller.copyPortalURL(id: portal.id) }
                            .disabled(!portalActions.copyPortalURL)
                            .accessibilityIdentifier("selected-copy-portal-url")
                        Button("Open Portal URL") { controller.openPortalURL(id: portal.id) }
                            .disabled(!portalActions.openPortalURL)
                            .accessibilityIdentifier("selected-open-portal-url")
                    }
                } else {
                    LabeledContent("Portal URL", value: "Unavailable")
                        .accessibilityIdentifier("selected-portal-url")
                }
                LabeledContent("Addresses", value: status?.addresses.joined(separator: ", ") ?? "Unavailable")
                    .accessibilityIdentifier("selected-addresses")
            }
            Section("Portal Destination") {
                HStack {
                    Button("Local App") { destinationEdit.kind = .localApp }
                        .disabled(destinationEdit.kind == .localApp)
                        .accessibilityIdentifier("selected-edit-destination-local")
                    Button("Remote App") { destinationEdit.kind = .remoteApp }
                        .disabled(destinationEdit.kind == .remoteApp)
                        .accessibilityIdentifier("selected-edit-destination-remote")
                }
                .buttonStyle(.bordered)
                if destinationEdit.kind == .localApp {
                    HStack {
                        TextField("Local App Port", text: $destinationEdit.localAppPort)
                            .onSubmit { updateDestination() }
                            .accessibilityIdentifier("selected-edit-local-app-port")
                        Button("Update Destination") { updateDestination() }
                            .disabled(!destinationActions.editDestination)
                            .accessibilityIdentifier("selected-update-destination")
                    }
                } else {
                    Picker("Scheme", selection: $destinationEdit.remoteAppScheme) {
                        Text("HTTP").tag(RemoteAppScheme.http)
                        Text("HTTPS").tag(RemoteAppScheme.https)
                    }
                    .accessibilityIdentifier("selected-edit-remote-app-scheme")
                    TextField("Remote App Host", text: $destinationEdit.remoteAppHost)
                        .accessibilityIdentifier("selected-edit-remote-app-host")
                    HStack {
                        TextField("Remote App Port", text: $destinationEdit.remoteAppPort)
                            .onSubmit { updateDestination() }
                            .accessibilityIdentifier("selected-edit-remote-app-port")
                        Button("Update Destination") { updateDestination() }
                            .disabled(!destinationActions.editDestination)
                            .accessibilityIdentifier("selected-update-destination")
                    }
                }
            }
            Section("Actions") {
                if status?.state == .authenticating {
                    Button("Authenticate") { controller.authenticate(id: portal.id) }
                        .buttonStyle(.borderedProminent)
                        .disabled(!portalActions.authenticate)
                        .accessibilityIdentifier("selected-authenticate")
                }
                HStack {
                    FocusRestoringButton(
                        title: portal.desiredState == .enabled ? "Stop" : "Start",
                        isEnabled: portal.desiredState == .enabled ? portalActions.stop : portalActions.start
                    ) {
                        if portal.desiredState == .enabled {
                            controller.stopPortal(id: portal.id)
                        } else {
                            controller.startPortal(id: portal.id)
                        }
                    }
                    .accessibilityIdentifier("selected-start-stop")
                }
                Button("Diagnostics") { openWindow(id: "diagnostics") }
                    .accessibilityIdentifier("selected-portal-diagnostics")
            }
        }
        .formStyle(.grouped)
        .accessibilityIdentifier("selected-portal-\(portal.name)")
        .navigationTitle(portal.name)
    }

    private func updateDestination() {
        controller.updateDestination(id: portal.id, edit: destinationEdit)
    }
}

private struct FocusRestoringButton: NSViewRepresentable {
    let title: String
    let isEnabled: Bool
    let action: () -> Void

    func makeCoordinator() -> Coordinator {
        Coordinator(action: action)
    }

    func makeNSView(context: Context) -> NSButton {
        let button = NSButton(title: title, target: context.coordinator, action: #selector(Coordinator.performAction))
        button.bezelStyle = .rounded
        button.controlSize = .small
        return button
    }

    func updateNSView(_ button: NSButton, context: Context) {
        let titleChanged = button.title != title
        context.coordinator.action = action
        button.title = title
        button.isEnabled = isEnabled
        guard titleChanged else { return }
        DispatchQueue.main.async {
            button.window?.makeFirstResponder(button)
        }
    }

    final class Coordinator: NSObject {
        var action: () -> Void

        init(action: @escaping () -> Void) {
            self.action = action
        }

        @objc func performAction() {
            action()
        }
    }
}

private struct PortalView: View {
    private enum AccessibilityFocusTarget: Hashable {
        case portal(UUID)
        case removalNotice(UUID)
    }

    @Environment(\.openWindow) private var openWindow
    @ObservedObject var controller: PortalController
    @ObservedObject var supervisor: HelperSupervisor
    @ObservedObject var launchAtLogin: LaunchAtLoginController
    let takeInitialManagementWindowRequest: () -> Bool
    @State private var showingResetConfirmation = false
    @State private var removalCandidate: PortalConfiguration?
    @State private var removalCompletionFocusID: UUID?
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
            Button("Open Portico") { openWindow(id: "management") }
                .accessibilityIdentifier("open-portico")
        }
        .padding()
        .frame(width: 380)
        .task {
            if takeInitialManagementWindowRequest() {
                openWindow(id: "management")
            }
        }
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
}

private struct AddPortalSheet: View {
    private enum FocusTarget: Hashable {
        case portalName
        case localAppPort
        case remoteAppHost
        case remoteAppPort
    }

    @ObservedObject var controller: PortalController
    let cancel: () -> Void
    let dismissAfterPersistence: () -> Void
    @FocusState private var inputFocus: FocusTarget?
    @AccessibilityFocusState private var accessibilityFocus: FocusTarget?

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Add Portal").font(.headline)
                .accessibilityIdentifier("add-portal-sheet")
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
            HStack {
                Text("Detected Local Apps").font(.subheadline)
                Spacer()
                Button("Refresh") { controller.refreshLocalApps() }
                    .disabled(!controller.actionAvailability().refreshLocalApps)
                    .accessibilityIdentifier("refresh-local-apps")
            }
            if controller.isRefreshingLocalApps {
                ProgressView()
                    .controlSize(.small)
            }
            ForEach(controller.localApps, id: \.localAppPort) { candidate in
                Button("Use \(candidate.processLabel) on port \(candidate.localAppPort)") {
                    controller.selectLocalApp(candidate)
                }
                .accessibilityIdentifier("local-app-\(candidate.localAppPort)")
            }
            if let message = controller.localAppsMessage {
                Text(message)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            if let error = controller.portalCreationError {
                Text(error)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .accessibilityIdentifier("add-portal-error")
            }
            TextField("Portal Name", text: $controller.portalName)
                .accessibilityIdentifier("portal-name-field")
                .focused($inputFocus, equals: .portalName)
                .accessibilityFocused($accessibilityFocus, equals: .portalName)
                .onSubmit { validateNameAndAdvance() }
            Picker("Destination", selection: $controller.creationKind) {
                Text("Local App").tag(PortalCreationKind.localApp)
                Text("Remote App").tag(PortalCreationKind.remoteApp)
            }
            .pickerStyle(.segmented)
            .accessibilityIdentifier("destination-kind")
            if controller.creationKind == .localApp {
                TextField("Local App Port", text: $controller.localAppPort)
                    .accessibilityIdentifier("local-app-port-field")
                    .focused($inputFocus, equals: .localAppPort)
                    .accessibilityFocused($accessibilityFocus, equals: .localAppPort)
                    .onSubmit { submitPortal() }
            } else {
                Picker("Scheme", selection: $controller.remoteAppScheme) {
                    Text("HTTP").tag(RemoteAppScheme.http)
                    Text("HTTPS").tag(RemoteAppScheme.https)
                }
                .accessibilityIdentifier("remote-app-scheme")
                TextField("Remote App Host", text: $controller.remoteAppHost)
                    .accessibilityIdentifier("remote-app-host-field")
                    .focused($inputFocus, equals: .remoteAppHost)
                    .accessibilityFocused($accessibilityFocus, equals: .remoteAppHost)
                TextField("Remote App Port", text: $controller.remoteAppPort)
                    .accessibilityIdentifier("remote-app-port-field")
                    .focused($inputFocus, equals: .remoteAppPort)
                    .accessibilityFocused($accessibilityFocus, equals: .remoteAppPort)
                    .onSubmit { submitPortal() }
            }
            HStack {
                Spacer()
                Button("Cancel", action: cancel)
                    .keyboardShortcut(.cancelAction)
                    .accessibilityIdentifier("add-portal-cancel")
                Button("Add Portal") { submitPortal() }
                    .buttonStyle(.borderedProminent)
                    .disabled(!controller.actionAvailability().addPortal)
                    .keyboardShortcut(.defaultAction)
                    .accessibilityIdentifier("add-portal")
            }
        }
        .padding()
        .frame(width: 480)
        .onAppear { inputFocus = .portalName }
    }

    private func validateNameAndAdvance() {
        do {
            _ = try PortalInputValidator.validate(name: controller.portalName, port: "1")
            let target: FocusTarget = controller.creationKind == .localApp
                ? .localAppPort
                : .remoteAppHost
            inputFocus = target
            accessibilityFocus = target
        } catch {
            inputFocus = .portalName
            accessibilityFocus = .portalName
        }
    }

    private func submitPortal() {
        switch controller.addPortal() {
        case .validationError(.invalidName):
            inputFocus = .portalName
            accessibilityFocus = .portalName
        case .validationError(.invalidPort):
            let target: FocusTarget = controller.creationKind == .localApp
                ? .localAppPort
                : .remoteAppPort
            inputFocus = target
            accessibilityFocus = target
        case .validationError(.invalidRemoteHost):
            inputFocus = .remoteAppHost
            accessibilityFocus = .remoteAppHost
        case .persisted:
            dismissAfterPersistence()
        case .persistenceFailure, nil:
            break
        }
    }

}

private extension PortalView {
    func cancelRemoval() {
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
    @State private var destinationEdit: PortalDestinationEdit
    @FocusState private var startStopFocused: Bool

    init(controller: PortalController, portal: PortalConfiguration, onRemove: @escaping () -> Void) {
        self.controller = controller
        self.portal = portal
        self.onRemove = onRemove
        _destinationEdit = State(initialValue: PortalDestinationEdit(destination: portal.destination))
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
        let editedDestinationActions = controller.actionAvailability(
            for: portal,
            editedDestination: destinationEdit
        )
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
        if portal.localAppPort != nil {
            LabeledContent("Local App", value: presentation.localAppReachability)
                .accessibilityIdentifier("local-app-state")
        }
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
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Button("Local App") { destinationEdit.kind = .localApp }
                    .accessibilityIdentifier("edit-destination-local")
                    .disabled(destinationEdit.kind == .localApp)
                Button("Remote App") { destinationEdit.kind = .remoteApp }
                    .accessibilityIdentifier("edit-destination-remote")
                    .disabled(destinationEdit.kind == .remoteApp)
            }
            .buttonStyle(.bordered)
            if destinationEdit.kind == .localApp {
                HStack {
                    TextField("Local App Port", text: $destinationEdit.localAppPort)
                        .frame(width: 80)
                        .onSubmit { updateDestination() }
                        .accessibilityIdentifier("edit-local-app-port")
                    Button("Update Destination") { updateDestination() }
                        .controlSize(.small)
                        .disabled(!editedDestinationActions.editDestination)
                        .accessibilityIdentifier("update-destination")
                }
            } else {
                HStack {
                    Picker("Scheme", selection: $destinationEdit.remoteAppScheme) {
                        Text("HTTP").tag(RemoteAppScheme.http)
                        Text("HTTPS").tag(RemoteAppScheme.https)
                    }
                    .accessibilityIdentifier("edit-remote-app-scheme")
                    TextField("Remote App Host", text: $destinationEdit.remoteAppHost)
                        .accessibilityIdentifier("edit-remote-app-host")
                }
                HStack {
                    TextField("Remote App Port", text: $destinationEdit.remoteAppPort)
                        .frame(width: 80)
                        .onSubmit { updateDestination() }
                        .accessibilityIdentifier("edit-remote-app-port")
                    Button("Update Destination") { updateDestination() }
                        .controlSize(.small)
                        .disabled(!editedDestinationActions.editDestination)
                        .accessibilityIdentifier("update-destination")
                }
            }
            HStack {
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
        }
        Button("Diagnostics") { openWindow(id: "diagnostics") }
            .controlSize(.small)
            .accessibilityIdentifier("portal-diagnostics")
        Button("Remove Portal", role: .destructive, action: onRemove)
            .controlSize(.small)
            .accessibilityIdentifier("remove-portal")
    }

    private func updateDestination() {
        controller.updateDestination(id: portal.id, edit: destinationEdit)
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
