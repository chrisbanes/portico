import Foundation

let helperProtocolVersion = 4

enum HelperCommand: String, Codable {
    case handshake
    case shutdown
    case reconcilePortals
    case authenticatePortal
    case cleanupRejectedPortal
    case removePortal
    case discoverLocalApps
}

struct EmptyPayload: Codable, Equatable {}

struct HelperRequest<Payload: Codable>: Codable {
    let version: Int
    let requestId: String
    let command: HelperCommand
    let payload: Payload
}

struct HelperResponse<Result: Codable>: Codable {
    let version: Int
    let requestId: String
    let result: Result?
    let error: HelperProtocolError?
}

struct HandshakeResult: Codable, Equatable {
    let protocolVersion: Int
}

struct ShutdownResult: Codable, Equatable {
    let accepted: Bool
}

struct ReconcilePortalPayload: Codable, Equatable {
    let portalId: UUID
    let portalName: String
    let destination: PortalDestination
    let desiredState: PortalDesiredState

}

struct ReconcilePortalsPayload: Codable, Equatable {
    let portals: [ReconcilePortalPayload]
}

enum ReconcilePortalOutcome: String, Codable, Equatable {
    case converged
    case startFailed
    case closeFailed
}

struct ReconcilePortalEntry: Codable, Equatable {
    let portalId: UUID
    let outcome: ReconcilePortalOutcome
}

struct ReconcilePortalsResult: Codable, Equatable {
    let entries: [ReconcilePortalEntry]
}

struct AuthenticatePortalPayload: Codable, Equatable {
    let portalId: UUID
}

struct AuthenticatePortalResult: Codable, Equatable {
    let accepted: Bool
}

struct CleanupRejectedPortalPayload: Codable, Equatable {
    let portalId: UUID
}

struct CleanupRejectedPortalResult: Codable, Equatable {
    let accepted: Bool
}

struct RemovePortalPayload: Codable, Equatable {
    let portalId: UUID
}

struct RemovePortalResult: Codable, Equatable {
    let accepted: Bool
}

struct LocalAppCandidatePayload: Codable, Equatable {
    let localAppPort: UInt16
    let processLabel: String
    let suggestedPortalName: String?
}

struct DiscoverLocalAppsResult: Codable, Equatable {
    let candidates: [LocalAppCandidatePayload]
}

enum HelperEventType: String, Codable {
    case portalStatus
    case authenticationURL
}

struct HelperEvent<Payload: Codable>: Codable {
    let version: Int
    let event: HelperEventType
    let portalId: UUID
    let payload: Payload
}

enum PortalTailscaleState: String, Codable, Equatable {
    case authenticating
    case awaitingApproval
    case connecting
    case online
    case stopped
    case error
}

struct PortalStatusPayload: Codable, Equatable {
    let state: PortalTailscaleState
    let stableNodeId: String?
    let assignedName: String?
    let portalURL: URL?
    let addresses: [String]
    let tailnetName: String?
    let magicDNSSuffix: String?

    init(
        state: PortalTailscaleState,
        stableNodeId: String?,
        assignedName: String?,
        portalURL: URL?,
        addresses: [String],
        tailnetName: String? = nil,
        magicDNSSuffix: String? = nil
    ) {
        self.state = state
        self.stableNodeId = stableNodeId
        self.assignedName = assignedName
        self.portalURL = portalURL
        self.addresses = addresses
        self.tailnetName = tailnetName
        self.magicDNSSuffix = magicDNSSuffix
    }
}

struct AuthenticationURLPayload: Codable, Equatable {
    let url: URL
}

struct HelperProtocolError: Codable, Equatable {
    let code: String
    let message: String
}
