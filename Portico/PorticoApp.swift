import AppKit
import SwiftUI

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
                managementRouting: appDelegate.managementRouting,
                windowActivation: appDelegate.windowActivation,
                takeInitialManagementWindowRequest: appDelegate.takeInitialManagementWindowRequest
            )
            .environment(\.openURL, OpenURLAction { url in
                appDelegate.portalController.openExternalURL(url)
                return .handled
            })
        }
        .menuBarExtraStyle(.window)
        .commands {
            PorticoCommands(
                managementRouting: appDelegate.managementRouting,
                windowActivation: appDelegate.windowActivation
            )
        }
        Window("Portico", id: "management") {
            OverviewView(
                controller: appDelegate.portalController,
                supervisor: appDelegate.supervisor,
                launchAtLogin: appDelegate.launchAtLoginController,
                managementRouting: appDelegate.managementRouting
            )
        }
        .defaultSize(width: 720, height: 520)
        Window("Portico Diagnostics", id: "diagnostics") {
            DiagnosticsView(controller: appDelegate.portalController)
        }
        .defaultSize(width: 720, height: 520)
    }

    private func scheduleInitialManagementWindowIfNeeded() {
        guard appDelegate.takeInitialManagementWindowRequest() else { return }
        DispatchQueue.main.async {
            appDelegate.windowActivation.present {
                openWindow(id: "management")
            }
        }
    }
}

public enum PorticoApplication {
    public static func launch() {
        PorticoApp.main()
    }
}

private struct PorticoCommands: Commands {
    @Environment(\.openWindow) private var openWindow
    @ObservedObject var managementRouting: ManagementRouting
    let windowActivation: AppWindowActivation

    var body: some Commands {
        CommandGroup(replacing: .appSettings) {
            Button("Settings…") {
                managementRouting.requestSettings()
                presentWindow(id: "management")
            }
            .keyboardShortcut(",", modifiers: .command)
        }
        CommandGroup(after: .appSettings) {
            Button("Open Portico") { presentWindow(id: "management") }
                .keyboardShortcut("o", modifiers: [.command, .shift])
            Button("Diagnostics") { presentWindow(id: "diagnostics") }
                .keyboardShortcut("d", modifiers: [.command, .shift])
#if DEBUG
            if UITestLaunchConfiguration.current?.scenario == .restarting {
                Button("Complete UI Test Restart") { UITestRestartGate.shared.release() }
                    .keyboardShortcut("r", modifiers: [.command, .shift])
            }
#endif
        }
    }

    private func presentWindow(id: String) {
        windowActivation.present {
            openWindow(id: id)
        }
    }
}

private struct OverviewView: View {
    private enum Destination: Hashable {
        case overview
        case portal(UUID)
        case settings
    }

    private enum AccessibilityFocusTarget: Hashable {
        case removalNotice(UUID)
    }

    @ObservedObject var controller: PortalController
    @ObservedObject var supervisor: HelperSupervisor
    @ObservedObject var launchAtLogin: LaunchAtLoginController
    @ObservedObject var managementRouting: ManagementRouting
    @State private var selection: Destination? = .overview
    @State private var showingAddPortal = false
    @State private var showingResetConfirmation = false
    @State private var removalCandidate: PortalConfiguration?
    @State private var removalCompletionFocusID: UUID?
    @State private var removalCancelFocusPortalID: UUID?
    @State private var authenticationFocusPortalID: UUID?
    @AccessibilityFocusState private var accessibilityFocus: AccessibilityFocusTarget?

    var body: some View {
        NavigationSplitView {
            List(selection: $selection) {
                Label("Overview", systemImage: "door.left.hand.open")
                    .accessibilityIdentifier("management-sidebar-overview")
                    .tag(Destination.overview)
                ForEach(controller.portals, id: \.id) { portal in
                    let presentation = PortalPresentation(
                        portal: portal,
                        status: controller.statuses[portal.id],
                        reachability: controller.reachabilityStates[portal.id] ?? .unknown,
                        isStale: controller.staleStatusIDs.contains(portal.id)
                    )
                    Button {
                        selection = .portal(portal.id)
                    } label: {
                        HStack {
                            Text(presentation.portalName)
                            Spacer()
                            PortalStatusIcons(
                                portal: portal,
                                status: controller.statuses[portal.id],
                                reachability: controller.reachabilityStates[portal.id] ?? .unknown,
                                isStale: controller.staleStatusIDs.contains(portal.id),
                                summary: sidebarState(for: portal, presentation: presentation)
                            )
                        }
                    }
                    .buttonStyle(.plain)
                    .accessibilityElement(children: .combine)
                    .accessibilityIdentifier("management-sidebar-portal-\(portal.name)")
                    .tag(Destination.portal(portal.id))
                }
                Section {
                    Label("Settings", systemImage: "gearshape")
                        .accessibilityIdentifier("management-sidebar-settings")
                        .tag(Destination.settings)
                }
            }
            .navigationTitle("Portico")
        } detail: {
            managementContent
            .navigationTitle(navigationTitle)
            .accessibilityIdentifier("management-overview")
            .toolbar {
                if selection != .settings {
                    Button("Add Portal") { showingAddPortal = true }
                        .disabled(controller.operationalLogging == .undecided)
                        .accessibilityIdentifier("overview-add-portal")
                }
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
                    didPersistPortal: { portal in
                        selection = .portal(portal.id)
                        authenticationFocusPortalID = portal.id
                        showingAddPortal = false
                    }
                )
            }
            .onChange(of: controller.portals) { _, portals in
                guard let currentSelection = selection,
                      case let .portal(id) = currentSelection,
                      !portals.contains(where: { $0.id == id })
                else { return }
                selection = .overview
            }
            .onChange(of: controller.removalNotices) { _, notices in
                guard let id = removalCompletionFocusID,
                      notices.contains(where: { $0.id == id })
                else { return }
                selection = .overview
                removalCompletionFocusID = nil
                DispatchQueue.main.async {
                    accessibilityFocus = .removalNotice(id)
                }
            }
            .onChange(of: managementRouting.requestRevision) { applyManagementRequest() }
            .onChange(of: selection) { _, selection in
                switch selection {
                case .overview: managementRouting.recordVisibleDestination(.overview)
                case .settings: managementRouting.recordVisibleDestination(.settings)
                case .portal, nil: break
                }
            }
            .onAppear { applyManagementRequest() }
            .confirmationDialog(
                "Reset this installation's tailnet binding? This does not remove any remote Tailscale node.",
                isPresented: $showingResetConfirmation,
                titleVisibility: .visible
            ) {
                Button("Reset Tailnet", role: .destructive) {
                    controller.resetTailnet(confirmed: true)
                }
                .accessibilityIdentifier("overview-confirm-reset-tailnet")
                Button("Cancel", role: .cancel) {}
                    .accessibilityIdentifier("overview-cancel-reset-tailnet")
            }
            .sheet(isPresented: Binding(
                get: { removalCandidate != nil },
                set: { if !$0 { cancelRemoval() } }
            )) {
                if let portal = removalCandidate {
                    RemovalConfirmationView(
                        controller: controller,
                        portal: portal,
                        cancel: cancelRemoval,
                        confirm: {
                            removalCompletionFocusID = portal.id
                            controller.removePortal(id: portal.id)
                            removalCandidate = nil
                        }
                    )
                }
            }
        }
    }

    private var selectedPortal: PortalConfiguration? {
        guard let selection, case let .portal(id) = selection else { return nil }
        return controller.portals.first(where: { $0.id == id })
    }

    private var navigationTitle: String {
        switch selection {
        case .settings: "Settings"
        case let .portal(id): controller.portals.first(where: { $0.id == id })?.name ?? "Portal"
        case .overview, nil: "Overview"
        }
    }

    @ViewBuilder
    private var managementContent: some View {
        if selection == .settings {
            SettingsView(
                controller: controller,
                supervisor: supervisor,
                launchAtLogin: launchAtLogin
            )
        } else if let selectedPortal {
            selectedDetail(for: selectedPortal)
                .id(selectedPortal.id)
        } else if controller.portals.isEmpty,
                  controller.operationalLogging != .undecided,
                  !hasRecoveryContent {
            VStack(spacing: 16) {
                ContentUnavailableView {
                    Label("Connect Your Tailnet", systemImage: "door.left.hand.open")
                } description: {
                    Text("Create your first Portal. Next, you’ll sign in with Tailscale in your browser.")
                        .accessibilityIdentifier("overview-first-portal-authentication-guidance")
                }
                if launchAtLogin.isOffering {
                    launchAtLoginOffer
                }
                Button("Create Your First Portal") { showingAddPortal = true }
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
                if launchAtLogin.isOffering {
                    Section("Launch at Login") {
                        launchAtLoginOffer
                    }
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
                        WrappingHStack {
                            Button("Allow Operational-support Logging") {
                                controller.setOperationalLogging(.enabled)
                            }
                            .accessibilityIdentifier("overview-logging-enabled")
                            Button("Disable Operational-support Logging") {
                                controller.setOperationalLogging(.disabled)
                            }
                            .accessibilityIdentifier("overview-logging-disabled")
                        }
                    } else {
                        Text(controller.operationalLogging == .enabled
                            ? "Operational-support logging is allowed."
                            : "Operational-support logging is disabled.")
                    }
                }
                if !controller.portals.isEmpty {
                    Section("Portals") {
                        ForEach(controller.portals, id: \.id) { portal in
                            Text(portal.name)
                                .accessibilityIdentifier("overview-portal-\(portal.name)")
                        }
                    }
                }
                recoverySections
                if let message = controller.message {
                    Text(message)
                        .foregroundStyle(.secondary)
                        .accessibilityIdentifier("overview-message")
                }
            }
            .formStyle(.grouped)
        }
    }

    private var launchAtLoginOffer: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Open Portico at Login?")
            Text("You can change this later in Settings.")
                .foregroundStyle(.secondary)
            HStack {
                Button("Not Now") { launchAtLogin.declineOffer() }
                    .accessibilityIdentifier("overview-login-offer-decline")
                Button("Enable") { launchAtLogin.acceptOffer() }
                    .buttonStyle(.borderedProminent)
                    .accessibilityIdentifier("overview-login-offer-enable")
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("overview-launch-at-login-offer")
    }

    @ViewBuilder
    private func selectedDetail(for portal: PortalConfiguration) -> some View {
        switch portal.lifecycle {
        case .active:
            SelectedPortalView(
                controller: controller,
                portal: portal,
                authenticationFocusRequestID: authenticationFocusPortalID == portal.id &&
                    controller.actionAvailability(for: portal).authenticate ? portal.id : nil,
                removalFocusRequestID: removalCancelFocusPortalID == portal.id
                    ? portal.id
                    : nil,
                onRemove: {
                    removalCancelFocusPortalID = nil
                    removalCandidate = portal
                }
            )
        case .pendingRemoval:
            RemovingPortalView(controller: controller, portal: portal)
        case .pendingTailnetRejection:
            PendingPortalDetailView(controller: controller, portal: portal)
        }
    }

    @ViewBuilder
    private var recoverySections: some View {
        if supervisor.availability == .failed || !controller.pendingPortals.isEmpty ||
            !controller.removalNotices.isEmpty || !controller.alerts.isEmpty || controller.canResetTailnet {
            Section("Recovery") {
                if supervisor.availability == .failed {
                    Button("Retry Helper") { controller.retryHelper() }
                        .buttonStyle(.borderedProminent)
                        .accessibilityIdentifier("overview-retry-helper")
                }
                ForEach(controller.pendingPortals, id: \.id) { portal in
                    PendingPortalWarningView(controller: controller, portal: portal)
                }
                ForEach(controller.removalNotices) { notice in
                    RemovalNoticeView(controller: controller, notice: notice)
                        .accessibilityFocused($accessibilityFocus, equals: .removalNotice(notice.id))
                }
                ForEach(controller.alerts) { alert in
                    CompletedWarningView(controller: controller, alert: alert)
                }
                if controller.canResetTailnet {
                    Button("Reset Tailnet", role: .destructive) {
                        showingResetConfirmation = true
                    }
                    .accessibilityIdentifier("overview-reset-tailnet")
                }
            }
        }
    }

    private var hasRecoveryContent: Bool {
        supervisor.availability == .failed || !controller.pendingPortals.isEmpty ||
            !controller.removalNotices.isEmpty || !controller.alerts.isEmpty ||
            controller.canResetTailnet || controller.message != nil
    }

    private func sidebarState(for portal: PortalConfiguration, presentation: PortalPresentation) -> String {
        lifecycleStatusText(for: portal) ?? presentation.tailscaleState
    }

    private func cancelRemoval() {
        guard let portal = removalCandidate else { return }
        removalCandidate = nil
        removalCancelFocusPortalID = portal.id
    }

    private func applyManagementRequest() {
        switch managementRouting.destination {
        case .overview: selection = .overview
        case .settings: selection = .settings
        }
    }
}

private struct SelectedPortalView: View {
    @Environment(\.openWindow) private var openWindow
    @ObservedObject var controller: PortalController
    let portal: PortalConfiguration
    let authenticationFocusRequestID: UUID?
    let removalFocusRequestID: UUID?
    let onRemove: () -> Void
    @State private var destinationEdit: PortalDestinationEdit

    init(
        controller: PortalController,
        portal: PortalConfiguration,
        authenticationFocusRequestID: UUID?,
        removalFocusRequestID: UUID?,
        onRemove: @escaping () -> Void
    ) {
        self.controller = controller
        self.portal = portal
        self.authenticationFocusRequestID = authenticationFocusRequestID
        self.removalFocusRequestID = removalFocusRequestID
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
                    WrappingHStack {
                        Button {
                            controller.copyPortalURL(id: portal.id)
                        } label: {
                            Label("Copy Portal URL", systemImage: "doc.on.doc")
                        }
                        .labelStyle(.iconOnly)
                        .help("Copy Portal URL")
                        .disabled(!portalActions.copyPortalURL)
                        .accessibilityIdentifier("selected-copy-portal-url")
                        Button {
                            controller.openPortalURL(id: portal.id)
                        } label: {
                            Label("Open Portal URL", systemImage: "arrow.up.right.square")
                        }
                        .labelStyle(.iconOnly)
                        .help("Open Portal URL")
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
                if portalActions.authenticate {
                    Label("Next, sign in with Tailscale in your browser.", systemImage: "key.fill")
                        .font(.headline)
                        .padding(8)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(.tint.opacity(0.12), in: RoundedRectangle(cornerRadius: 8))
                        .accessibilityIdentifier("selected-authentication-guidance")
                }
                WrappingHStack {
                    if status?.state == .authenticating {
                        FocusRestoringButton(
                            title: "Authenticate",
                            isEnabled: portalActions.authenticate,
                            isProminent: true,
                            focusRequestID: authenticationFocusRequestID
                        ) {
                            controller.authenticate(id: portal.id)
                        }
                            .accessibilityIdentifier("selected-authenticate")
                    }
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
                    Button {
                        openWindow(id: "diagnostics")
                    } label: {
                        Label("Diagnostics", systemImage: "stethoscope")
                    }
                    .labelStyle(.iconOnly)
                    .help("Diagnostics")
                    .accessibilityIdentifier("selected-portal-diagnostics")
                    FocusRestoringButton(
                        title: "Remove Portal",
                        isEnabled: portalActions.remove,
                        isDestructive: true,
                        focusRequestID: removalFocusRequestID,
                        action: onRemove
                    )
                        .accessibilityIdentifier("selected-remove-portal")
                }
            }
            if let message = controller.message {
                Text(message)
                    .foregroundStyle(.secondary)
                    .accessibilityIdentifier("selected-portal-message")
            }
        }
        .formStyle(.grouped)
        .accessibilityIdentifier("selected-portal-\(portal.name)")
        .navigationTitle(portal.name)
        .onChange(of: portal.destination) { _, destination in
            destinationEdit = PortalDestinationEdit(destination: destination)
        }
    }

    private func updateDestination() {
        controller.updateDestination(id: portal.id, edit: destinationEdit)
    }
}

private struct RemovalConfirmationView: View {
    @ObservedObject var controller: PortalController
    let portal: PortalConfiguration
    let cancel: () -> Void
    let confirm: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Remove \(portal.name)?").font(.headline)
                .accessibilityIdentifier("remove-confirmation-heading")
            Text(controller.removalWarningText(for: portal))
                .accessibilityIdentifier("remove-warning")
            Link("How to remove a Tailscale device", destination: PortalController.manualRemovalURL)
            HStack {
                Spacer()
                Button("Cancel", action: cancel)
                    .keyboardShortcut(.cancelAction)
                    .accessibilityIdentifier("remove-cancel")
                Button("Remove Portal", role: .destructive, action: confirm)
                    .keyboardShortcut(.defaultAction)
                    .accessibilityIdentifier("remove-confirm")
            }
        }
        .padding()
        .frame(width: 420)
    }
}

private struct RemovingPortalView: View {
    @Environment(\.openWindow) private var openWindow
    @ObservedObject var controller: PortalController
    let portal: PortalConfiguration

    var body: some View {
        Form {
            Section("Removing Portal") {
                Text("Removing Portal")
                    .accessibilityIdentifier("removing-portal")
                Text(controller.pendingRemovalWarningText(for: portal))
                Link("How to remove a Tailscale device", destination: PortalController.manualRemovalURL)
                if controller.removalStates[portal.id] == .failed {
                    Button("Retry Removal") { controller.retryRemoval(id: portal.id) }
                        .disabled(!controller.actionAvailability(for: portal).retryRemoval)
                        .accessibilityIdentifier("removing-retry")
                } else {
                    ProgressView()
                        .accessibilityIdentifier("removing-progress")
                }
                Button("Diagnostics") { openWindow(id: "diagnostics") }
            }
        }
        .formStyle(.grouped)
    }
}

private struct PendingPortalDetailView: View {
    @ObservedObject var controller: PortalController
    let portal: PortalConfiguration

    var body: some View {
        Form {
            Section("Cleanup in progress") {
                Text(controller.pendingWarningText(for: portal))
                    .accessibilityIdentifier("pending-portal-detail")
                Link("How to remove a Tailscale device", destination: PortalController.manualRemovalURL)
            }
        }
        .formStyle(.grouped)
        .accessibilityIdentifier("pending-portal")
    }
}

private struct PendingPortalWarningView: View {
    @ObservedObject var controller: PortalController
    let portal: PortalConfiguration

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Label("Cleanup in progress", systemImage: "exclamationmark.triangle.fill")
                .font(.headline)
            Text(controller.pendingWarningText(for: portal))
                .font(.caption)
            Link("How to remove a Tailscale device", destination: PortalController.manualRemovalURL)
                .font(.caption)
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("pending-portal-warning")
    }
}

private struct CompletedWarningView: View {
    @ObservedObject var controller: PortalController
    let alert: InstallationAlert

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Label("Portal removed from this Mac", systemImage: "exclamationmark.triangle.fill")
                .font(.headline)
            Text(controller.completedWarningText(for: alert))
                .font(.caption)
            Link("How to remove a Tailscale device", destination: PortalController.manualRemovalURL)
                .font(.caption)
            Button("Dismiss") { controller.dismissAlert(id: alert.id) }
                .controlSize(.small)
                .accessibilityIdentifier("completed-warning-dismiss")
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("completed-warning")
    }
}

private struct RemovalNoticeView: View {
    @ObservedObject var controller: PortalController
    let notice: PortalRemovalNotice

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Label("Portal removed from this Mac", systemImage: "exclamationmark.triangle.fill")
                .font(.headline)
            Text(controller.removalNoticeText(for: notice))
                .font(.caption)
            Link("How to remove a Tailscale device", destination: PortalController.manualRemovalURL)
                .font(.caption)
            Button("Dismiss") { controller.dismissRemovalNotice(id: notice.id) }
                .controlSize(.small)
                .accessibilityIdentifier("removal-complete-dismiss")
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("removal-complete")
    }
}

private struct FocusRestoringButton: NSViewRepresentable {
    let title: String
    let isEnabled: Bool
    var isDestructive = false
    var isProminent = false
    var focusRequestID: UUID?
    let action: () -> Void

    func makeCoordinator() -> Coordinator {
        Coordinator(action: action, focusRequestID: focusRequestID)
    }

    func makeNSView(context: Context) -> NSButton {
        let button = NSButton(title: title, target: context.coordinator, action: #selector(Coordinator.performAction))
        button.bezelStyle = .rounded
        button.controlSize = .small
        button.bezelColor = isProminent ? .controlAccentColor : nil
        if context.coordinator.focusRequestID != nil {
            context.coordinator.restoreFocus(afterSheetDismissal: button)
        }
        return button
    }

    func updateNSView(_ button: NSButton, context: Context) {
        let titleChanged = button.title != title
        context.coordinator.action = action
        button.title = title
        button.isEnabled = isEnabled
        button.contentTintColor = isDestructive ? .systemRed : nil
        button.bezelColor = isProminent ? .controlAccentColor : nil
        let focusRequested = context.coordinator.focusRequestID != focusRequestID
        context.coordinator.focusRequestID = focusRequestID
        guard titleChanged || (focusRequested && focusRequestID != nil) else { return }
        context.coordinator.restoreFocus(afterSheetDismissal: button)
    }

    static func dismantleNSView(_ button: NSButton, coordinator: Coordinator) {
        coordinator.removeSheetObserver()
    }

    final class Coordinator: NSObject {
        var action: () -> Void
        var focusRequestID: UUID?
        private var sheetObserver: NSObjectProtocol?

        init(action: @escaping () -> Void, focusRequestID: UUID?) {
            self.action = action
            self.focusRequestID = focusRequestID
        }

        deinit {
            removeSheetObserver()
        }

        func restoreFocus(afterSheetDismissal button: NSButton) {
            DispatchQueue.main.async { [weak self, weak button] in
                guard let self, let button, let window = button.window else { return }
                self.removeSheetObserver()
                guard window.attachedSheet != nil else {
                    button.scrollToVisible(button.bounds)
                    window.makeFirstResponder(button)
                    return
                }
                self.sheetObserver = NotificationCenter.default.addObserver(
                    forName: NSWindow.didEndSheetNotification,
                    object: window,
                    queue: .main
                ) { [weak self, weak button, weak window] _ in
                    self?.removeSheetObserver()
                    guard let button, let window else { return }
                    button.scrollToVisible(button.bounds)
                    window.makeFirstResponder(button)
                }
            }
        }

        func removeSheetObserver() {
            guard let sheetObserver else { return }
            NotificationCenter.default.removeObserver(sheetObserver)
            self.sheetObserver = nil
        }


        @objc func performAction() {
            action()
        }
    }
}

private struct PortalView: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(\.openWindow) private var openWindow
    @ObservedObject var controller: PortalController
    @ObservedObject var supervisor: HelperSupervisor
    @ObservedObject var launchAtLogin: LaunchAtLoginController
    @ObservedObject var managementRouting: ManagementRouting
    let windowActivation: AppWindowActivation
    let takeInitialManagementWindowRequest: () -> Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Label(supervisor.availability.title, systemImage: supervisor.availability.symbolName)
                .accessibilityIdentifier("helper-state")
            LabeledContent("Tailnet", value: controller.tailnetDisplaySuffix ?? "Not connected")
                .accessibilityIdentifier("tailnet-state")
            if requiresAttention {
                VStack(alignment: .leading, spacing: 6) {
                    Text("Portico needs your attention.")
                    Button("Review in Portico") { openOverview() }
                        .accessibilityIdentifier("compact-attention")
                }
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("compact-attention-summary")
            }
            if launchAtLogin.isOffering {
                VStack(alignment: .leading, spacing: 6) {
                    Text("Launch at login is ready to set up.")
                    Button("Set Up Launch at Login") { openOverview() }
                    .accessibilityIdentifier("login-offer-reminder")
                }
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("launch-at-login-offer")
            }
            ForEach(controller.portals, id: \.id) { portal in
                CompactPortalMenuRow(controller: controller, portal: portal)
                Divider()
            }
            Divider()
            HStack {
                Button {
                    openSettings()
                } label: {
                    Label("Settings", systemImage: "gearshape")
                }
                .labelStyle(.iconOnly)
                .help("Settings")
                .keyboardShortcut(",", modifiers: .command)
                .accessibilityIdentifier("settings")
                Button {
                    presentWindow(id: "diagnostics")
                } label: {
                    Label("Diagnostics", systemImage: "stethoscope")
                }
                .labelStyle(.iconOnly)
                .help("Diagnostics")
                .keyboardShortcut("d", modifiers: [.command, .shift])
                .accessibilityIdentifier("diagnostics")
                Button {
                    presentWindow(id: "management")
                    dismiss()
                } label: {
                    Label("Open Portico", systemImage: "rectangle.on.rectangle")
                }
                .labelStyle(.iconOnly)
                .help("Open Portico")
                .accessibilityIdentifier("open-portico")
            }
            Divider()
            Button("Quit Portico") {
                NSApp.terminate(nil)
            }
            .keyboardShortcut("q")
            .accessibilityIdentifier("quit")
        }
        .padding()
        .frame(width: 380)
        .task {
            if takeInitialManagementWindowRequest() {
                presentWindow(id: "management")
            }
        }
    }

    private var requiresAttention: Bool {
        supervisor.availability == .failed
            || !controller.pendingPortals.isEmpty
            || !controller.pendingRemovalPortals.isEmpty
            || !controller.alerts.isEmpty
            || !controller.removalNotices.isEmpty
            || controller.message != nil
    }

    private func openOverview() {
        managementRouting.requestOverview()
        presentWindow(id: "management")
        dismiss()
    }

    private func openSettings() {
        managementRouting.requestSettings()
        presentWindow(id: "management")
        dismiss()
    }

    private func presentWindow(id: String) {
        windowActivation.present {
            openWindow(id: id)
        }
    }
}

private struct AddPortalSheet: View {
    private enum Step: Equatable {
        case destination
        case details

        var title: String {
            switch self {
            case .destination: "Choose a destination"
            case .details: "Name your Portal"
            }
        }

        var number: Int {
            switch self {
            case .destination: 1
            case .details: 2
            }
        }
    }

    private enum FocusTarget: Hashable {
        case portalName
        case localAppPort
        case remoteAppHost
        case remoteAppPort
    }

    @ObservedObject var controller: PortalController
    let cancel: () -> Void
    let didPersistPortal: (PortalConfiguration) -> Void
    @FocusState private var inputFocus: FocusTarget?
    @AccessibilityFocusState private var accessibilityFocus: FocusTarget?
    @State private var step = Step.destination

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Label(step.title, systemImage: "\(step.number).circle.fill")
                    .font(.headline)
                    .accessibilityIdentifier("add-portal-sheet")
                Spacer()
                Text("Step \(step.number) of 2")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            switch step {
            case .destination:
                destinationStep
            case .details:
                detailsStep
            }

            HStack {
                Button("Cancel", action: cancel)
                    .keyboardShortcut(.cancelAction)
                    .accessibilityIdentifier("add-portal-cancel")
                Spacer()
                if step == .details {
                    Button("Back") {
                        step = .destination
                        inputFocus = nil
                    }
                    .accessibilityIdentifier("add-portal-back")
                    Button("Add Portal") { submitPortal() }
                        .buttonStyle(.borderedProminent)
                        .disabled(!controller.actionAvailability().addPortal)
                        .keyboardShortcut(.defaultAction)
                        .accessibilityIdentifier("add-portal")
                } else {
                    Button("Continue") { showDetails() }
                        .buttonStyle(.borderedProminent)
                        .keyboardShortcut(.defaultAction)
                        .accessibilityIdentifier("add-portal-next")
                }
            }
        }
        .padding()
        .frame(width: 480)
    }

    @ViewBuilder
    private var destinationStep: some View {
        if PortalPresentation.showsPrerequisiteGuidance(portalCount: controller.portals.count) {
            DisclosureGroup("First Portal requirements") {
                ForEach(PortalPresentation.prerequisiteGuidance, id: \.self) { guidance in
                    Label(guidance, systemImage: "info.circle")
                        .font(.caption)
                }
            }
            .accessibilityIdentifier("add-guidance")
        }
        Picker("Destination", selection: $controller.creationKind) {
            Text("Local App").tag(PortalCreationKind.localApp)
            Text("Remote App").tag(PortalCreationKind.remoteApp)
        }
        .pickerStyle(.segmented)
        .accessibilityIdentifier("destination-kind")

        if controller.creationKind == .localApp {
            HStack {
                Label("Detected Local Apps", systemImage: "sparkle.magnifyingglass")
                    .font(.subheadline)
                Spacer()
                Button {
                    controller.refreshLocalApps()
                } label: {
                    Label("Refresh Local Apps", systemImage: "arrow.clockwise")
                }
                .labelStyle(.iconOnly)
                .help("Refresh Local Apps")
                .disabled(!controller.actionAvailability().refreshLocalApps)
                .accessibilityIdentifier("refresh-local-apps")
            }
            if controller.isRefreshingLocalApps {
                ProgressView()
                    .controlSize(.small)
            }
            WrappingHStack {
                ForEach(controller.localApps, id: \.localAppPort) { candidate in
                    Button {
                        controller.selectLocalApp(candidate)
                        showDetails()
                    } label: {
                        Text(verbatim: "\(candidate.processLabel) · localhost:\(candidate.localAppPort)")
                    }
                    .accessibilityIdentifier("local-app-\(candidate.localAppPort)")
                }
            }
            if let message = controller.localAppsMessage {
                Text(message)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        } else {
            Label("Connect to an HTTP or HTTPS app reachable from this Mac.", systemImage: "network")
                .foregroundStyle(.secondary)
        }
    }

    @ViewBuilder
    private var detailsStep: some View {
        if let error = controller.portalCreationError {
            Label(error, systemImage: "exclamationmark.circle")
                .font(.caption)
                .foregroundStyle(.secondary)
                .accessibilityIdentifier("add-portal-error")
        }
        TextField("Portal Name", text: $controller.portalName)
            .accessibilityIdentifier("portal-name-field")
            .focused($inputFocus, equals: .portalName)
            .accessibilityFocused($accessibilityFocus, equals: .portalName)
            .onSubmit { validateNameAndAdvance() }
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
    }

    private func showDetails() {
        step = .details
        DispatchQueue.main.async {
            inputFocus = .portalName
        }
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
        case let .persisted(portal):
            didPersistPortal(portal)
        case .persistenceFailure, nil:
            break
        }
    }
}

private struct PortalStatusIcons: View {
    let portal: PortalConfiguration
    let status: PortalStatusPayload?
    let reachability: LocalAppReachabilityState
    let isStale: Bool
    let summary: String

    var body: some View {
        HStack(spacing: 5) {
            Image(systemName: tailscaleSymbol)
                .foregroundStyle(tailscaleColor)
            if portal.lifecycle == .active {
                if status?.state != .stopped || portal.desiredState != .stopped {
                    Image(systemName: portal.desiredState == .enabled ? "play.fill" : "pause.fill")
                        .foregroundStyle(.secondary)
                }
                if portal.localAppPort != nil {
                    Image(systemName: reachabilitySymbol)
                        .foregroundStyle(reachabilityColor)
                }
            }
        }
        .font(.caption)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(summary)
        .help(summary)
    }

    private var tailscaleSymbol: String {
        switch portal.lifecycle {
        case .pendingRemoval:
            return "trash.circle"
        case .pendingTailnetRejection:
            return "exclamationmark.triangle.fill"
        case .active:
            if isStale { return "clock.arrow.circlepath" }
            switch status?.state {
            case .authenticating: return "key.fill"
            case .awaitingApproval: return "clock.badge.exclamationmark"
            case .connecting: return "arrow.triangle.2.circlepath"
            case .online: return "checkmark.circle.fill"
            case .stopped: return "pause.circle.fill"
            case .error: return "exclamationmark.triangle.fill"
            case nil: return "questionmark.circle"
            }
        }
    }

    private var tailscaleColor: Color {
        switch portal.lifecycle {
        case .pendingRemoval, .pendingTailnetRejection:
            return .orange
        case .active:
            if isStale { return .gray }
            switch status?.state {
            case .online: return .green
            case .error: return .red
            case .authenticating, .awaitingApproval: return .orange
            case .connecting, .stopped, nil: return .gray
            }
        }
    }

    private var reachabilitySymbol: String {
        switch reachability {
        case .reachable: "bolt.horizontal.circle.fill"
        case .unavailable: "bolt.slash.circle.fill"
        case .unknown: "questionmark.circle"
        }
    }

    private var reachabilityColor: Color {
        switch reachability {
        case .reachable: .green
        case .unavailable: .red
        case .unknown: .gray
        }
    }
}

private struct CompactPortalMenuRow: View {
    @ObservedObject var controller: PortalController
    let portal: PortalConfiguration

    var body: some View {
        let presentation = PortalPresentation(
            portal: portal,
            status: controller.statuses[portal.id],
            reachability: controller.reachabilityStates[portal.id] ?? .unknown,
            isStale: controller.staleStatusIDs.contains(portal.id)
        )
        HStack(spacing: 10) {
            Text(presentation.portalName).font(.headline)
            PortalStatusIcons(
                portal: portal,
                status: controller.statuses[portal.id],
                reachability: controller.reachabilityStates[portal.id] ?? .unknown,
                isStale: controller.staleStatusIDs.contains(portal.id),
                summary: statusText(for: presentation)
            )
                .accessibilityIdentifier("compact-portal-status-\(portal.name)")
            Spacer()
            contextualAction
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("compact-portal-\(portal.name)")
    }

    @ViewBuilder
    private var contextualAction: some View {
        let availability = controller.actionAvailability(for: portal)
        if availability.authenticate {
            Button {
                controller.authenticate(id: portal.id)
            } label: {
                Label("Authenticate", systemImage: "key.fill")
            }
                .accessibilityIdentifier("compact-authenticate")
        } else if availability.start {
            Button {
                controller.startPortal(id: portal.id)
            } label: {
                Label("Start", systemImage: "play.fill")
            }
                .accessibilityIdentifier("compact-start")
        } else if availability.openPortalURL {
            Button {
                controller.openPortalURL(id: portal.id)
            } label: {
                Label("Open", systemImage: "arrow.up.right.square")
            }
                .accessibilityIdentifier("compact-open-portal-url")
        }
    }

    private func statusText(for presentation: PortalPresentation) -> String {
        if let lifecycleStatus = lifecycleStatusText(for: portal) {
            return lifecycleStatus
        }
        if portal.localAppPort != nil {
            return "\(presentation.tailscaleState) • \(presentation.desiredState) • \(presentation.localAppReachability)"
        }
        return "\(presentation.tailscaleState) • \(presentation.desiredState)"
    }
}

private func lifecycleStatusText(for portal: PortalConfiguration) -> String? {
    switch portal.lifecycle {
    case .active: nil
    case .pendingRemoval: "Removing"
    case .pendingTailnetRejection: "Cleanup in progress"
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
            Section("Privacy") {
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
                Label("Changing this setting safely restarts the helper.", systemImage: "arrow.clockwise")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                LabeledContent("Helper") {
                    Label(supervisor.availability.title, systemImage: supervisor.availability.symbolName)
                }
                .accessibilityIdentifier("settings-helper-state")
                if let error = controller.operationalLoggingError {
                    Label(error, systemImage: "exclamationmark.circle")
                        .foregroundStyle(.secondary)
                        .accessibilityIdentifier("logging-preference-error")
                }
            }
            Section("Startup") {
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
                    Label(error, systemImage: "exclamationmark.circle")
                        .foregroundStyle(.secondary)
                        .accessibilityIdentifier("launch-at-login-error")
                }
            }
        }
        .formStyle(.grouped)
        .accessibilityIdentifier("settings-view")
        .onAppear {
            launchAtLogin.refreshStatusAfterApplicationActivation()
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
                Button {
                    controller.refreshReachability()
                } label: {
                    Label("Refresh Local App Reachability", systemImage: "arrow.clockwise")
                }
                .labelStyle(.iconOnly)
                .help("Refresh Local App Reachability")
                Spacer()
                Button {
                    controller.copyDiagnosticReport()
                } label: {
                    Label("Copy Report", systemImage: "doc.on.doc")
                }
                .labelStyle(.iconOnly)
                .help("Copy Report")
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

private struct WrappingHStack: Layout {
    var spacing: CGFloat = 8
    var rowSpacing: CGFloat = 8

    func sizeThatFits(
        proposal: ProposedViewSize,
        subviews: Subviews,
        cache: inout ()
    ) -> CGSize {
        let availableWidth = proposal.width ?? .greatestFiniteMagnitude
        let rows = makeRows(for: subviews, availableWidth: availableWidth)
        return CGSize(
            width: proposal.width ?? rows.map(\.width).max() ?? 0,
            height: rows.reduce(0) { $0 + $1.height } + rowSpacing * CGFloat(max(rows.count - 1, 0))
        )
    }

    func placeSubviews(
        in bounds: CGRect,
        proposal: ProposedViewSize,
        subviews: Subviews,
        cache: inout ()
    ) {
        var y = bounds.minY
        for row in makeRows(for: subviews, availableWidth: bounds.width) {
            var x = bounds.minX
            for item in row.items {
                item.subview.place(at: CGPoint(x: x, y: y), proposal: .unspecified)
                x += item.size.width + spacing
            }
            y += row.height + rowSpacing
        }
    }

    private func makeRows(for subviews: Subviews, availableWidth: CGFloat) -> [Row] {
        var rows: [Row] = []
        var items: [Item] = []
        var width: CGFloat = 0
        var height: CGFloat = 0

        for subview in subviews {
            let size = subview.sizeThatFits(.unspecified)
            let proposedWidth = items.isEmpty ? size.width : width + spacing + size.width
            if !items.isEmpty && proposedWidth > availableWidth {
                rows.append(Row(items: items, width: width, height: height))
                items = []
                width = 0
                height = 0
            }
            items.append(Item(subview: subview, size: size))
            width = items.count == 1 ? size.width : width + spacing + size.width
            height = max(height, size.height)
        }
        if !items.isEmpty {
            rows.append(Row(items: items, width: width, height: height))
        }
        return rows
    }

    private struct Row {
        let items: [Item]
        let width: CGFloat
        let height: CGFloat
    }

    private struct Item {
        let subview: LayoutSubview
        let size: CGSize
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
