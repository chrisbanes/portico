import Foundation

struct PortalActionContext {
    var loggingPreference: OperationalLoggingPreference
    var inputsValid: Bool
    var helperAvailability: HelperAvailability
    var lifecycle: PortalLifecycle?
    var desiredState: PortalDesiredState?
    var tailscaleState: PortalTailscaleState?
    var hasPortalURL: Bool
    var isTailscaleFactsStale: Bool
    var isAuthenticationPending: Bool
    var removalState: PortalRemovalState?
    var hasTailnetBinding: Bool
    var portalCount: Int
    var isEditedDestinationValid: Bool
    var isRefreshingLocalApps: Bool

    init(
        loggingPreference: OperationalLoggingPreference,
        inputsValid: Bool,
        helperAvailability: HelperAvailability,
        lifecycle: PortalLifecycle? = nil,
        desiredState: PortalDesiredState? = nil,
        tailscaleState: PortalTailscaleState? = nil,
        hasPortalURL: Bool = false,
        isTailscaleFactsStale: Bool = false,
        isAuthenticationPending: Bool = false,
        removalState: PortalRemovalState? = nil,
        hasTailnetBinding: Bool = false,
        portalCount: Int = 0,
        isEditedDestinationValid: Bool = false,
        isRefreshingLocalApps: Bool = false
    ) {
        self.loggingPreference = loggingPreference
        self.inputsValid = inputsValid
        self.helperAvailability = helperAvailability
        self.lifecycle = lifecycle
        self.desiredState = desiredState
        self.tailscaleState = tailscaleState
        self.hasPortalURL = hasPortalURL
        self.isTailscaleFactsStale = isTailscaleFactsStale
        self.isAuthenticationPending = isAuthenticationPending
        self.removalState = removalState
        self.hasTailnetBinding = hasTailnetBinding
        self.portalCount = portalCount
        self.isEditedDestinationValid = isEditedDestinationValid
        self.isRefreshingLocalApps = isRefreshingLocalApps
    }
}

struct PortalActionAvailability: Equatable {
    let addPortal: Bool
    let refreshLocalApps: Bool
    let start: Bool
    let stop: Bool
    let editDestination: Bool
    let authenticate: Bool
    let copyPortalURL: Bool
    let openPortalURL: Bool
    let diagnostics: Bool
    let remove: Bool
    let retryRemoval: Bool
    let resetTailnet: Bool
    let settings: Bool

    init(context: PortalActionContext) {
        let isActive = context.lifecycle == .active
        let helperConnected = context.helperAvailability == .connected
        addPortal = context.loggingPreference != .undecided && context.inputsValid
        refreshLocalApps = helperConnected && !context.isRefreshingLocalApps
        start = isActive && context.desiredState == .stopped
        stop = isActive && context.desiredState == .enabled
        editDestination = isActive && context.isEditedDestinationValid
        authenticate = isActive
            && context.desiredState == .enabled
            && helperConnected
            && context.tailscaleState == .authenticating
            && !context.isTailscaleFactsStale
            && !context.isAuthenticationPending
        copyPortalURL = context.hasPortalURL
        openPortalURL = context.hasPortalURL
            && !context.isTailscaleFactsStale
            && context.tailscaleState == .online
        diagnostics = true
        remove = isActive
        retryRemoval = context.lifecycle == .pendingRemoval
            && context.removalState == .failed
            && helperConnected
        resetTailnet = context.hasTailnetBinding && context.portalCount == 0
        settings = true
    }
}
