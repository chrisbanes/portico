import Foundation

struct PortalPresentation {
    static let prerequisiteGuidance = [
        "Enable MagicDNS for your tailnet.",
        "Enable HTTPS certificates for your tailnet.",
        "Approve the Portal device when your tailnet requires device approval.",
        "Portal names are published to Certificate Transparency logs when certificates are issued.",
    ]

    let portalName: String
    let assignedName: String?
    let desiredState: String
    let tailscaleState: String
    let localAppReachability: String
    let portalURLLabel: String
    let collisionExplanation: String?

    init(
        portal: PortalConfiguration,
        status: PortalStatusPayload?,
        reachability: LocalAppReachabilityState,
        isStale: Bool
    ) {
        portalName = portal.name
        assignedName = status?.assignedName
        desiredState = portal.desiredState.title
        let state = status?.state.title ?? "Unknown"
        tailscaleState = isStale ? "\(state) — Last Known" : state
        localAppReachability = reachability.title
        portalURLLabel = isStale ? "Portal URL — Last Known" : "Portal URL"
        if let assignedName = status?.assignedName, assignedName != portal.name {
            collisionExplanation = "Tailscale assigned “\(assignedName)” because “\(portal.name)” was unavailable. Portal Name remains “\(portal.name)”."
        } else {
            collisionExplanation = nil
        }
    }

    static func showsPrerequisiteGuidance(portalCount: Int) -> Bool {
        portalCount == 0
    }
}

enum PorticoAnnouncementEvent {
    case helperConnected
    case helperTerminalFailure
    case portalOnline
    case removalSucceeded
    case removalFailed
    case preferenceRestartCompleted
    case preferenceRestartFailed
}

enum PorticoAnnouncement {
    static func text(for event: PorticoAnnouncementEvent) -> String {
        switch event {
        case .helperConnected: "Helper connected."
        case .helperTerminalFailure: "Helper unavailable."
        case .portalOnline: "Portal online."
        case .removalSucceeded: "Portal removal completed."
        case .removalFailed: "Portal removal failed."
        case .preferenceRestartCompleted: "Logging preference restart completed."
        case .preferenceRestartFailed: "Logging preference restart failed."
        }
    }
}

extension LocalAppReachabilityState {
    var title: String {
        switch self {
        case .unknown: "Unknown"
        case .reachable: "Reachable"
        case .unavailable: "Unavailable"
        }
    }
}

extension PortalTailscaleState {
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

extension PortalDesiredState {
    var title: String {
        switch self {
        case .enabled: "Enabled"
        case .stopped: "Stopped"
        }
    }
}
