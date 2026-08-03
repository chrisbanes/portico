import Foundation

let helperProtocolVersion = 1

enum HelperCommand: String, Codable {
    case handshake
    case shutdown
    case startPortal
    case authenticatePortal
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

struct StartPortalPayload: Codable, Equatable {
    let portalId: UUID
    let portalName: String
    let localAppPort: UInt16
}

struct StartPortalResult: Codable, Equatable {
    let accepted: Bool
}

struct AuthenticatePortalPayload: Codable, Equatable {
    let portalId: UUID
}

struct AuthenticatePortalResult: Codable, Equatable {
    let accepted: Bool
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
}

struct AuthenticationURLPayload: Codable, Equatable {
    let url: URL
}

struct HelperProtocolError: Codable, Equatable {
    let code: String
    let message: String
}
