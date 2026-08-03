import Foundation

let helperProtocolVersion = 1

enum HelperCommand: String, Codable {
    case handshake
    case shutdown
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

struct HelperProtocolError: Codable, Equatable {
    let code: String
    let message: String
}
