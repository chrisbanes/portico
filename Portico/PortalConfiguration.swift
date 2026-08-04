import Darwin
import Foundation

enum RemoteAppScheme: String, Codable, Equatable {
    case http
    case https
}

enum PortalDestination: Codable, Equatable {
    case localApp(port: UInt16)
    case remoteApp(scheme: RemoteAppScheme, host: String, port: UInt16)

    init?(localAppPort: UInt16) {
        guard localAppPort > 0 else { return nil }
        self = .localApp(port: localAppPort)
    }

    init?(remoteAppScheme: RemoteAppScheme, host: String, port: UInt16) {
        guard port > 0, let host = Self.canonicalRemoteHost(host), !Self.isLoopbackHost(host) else {
            return nil
        }
        self = .remoteApp(scheme: remoteAppScheme, host: host, port: port)
    }

    var localAppPort: UInt16? {
        guard case let .localApp(port) = self else { return nil }
        return port
    }

    var isLocalApp: Bool {
        localAppPort != nil
    }

    private enum CodingKeys: String, CodingKey {
        case kind
        case port
        case scheme
        case host
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        let allKeys = try decoder.container(keyedBy: DynamicCodingKey.self)
        let keys = Set(allKeys.allKeys.map(\.stringValue))
        let kind = try container.decode(String.self, forKey: .kind)
        switch kind {
        case "localApp":
            guard keys == ["kind", "port"],
                  let destination = Self(localAppPort: try container.decode(UInt16.self, forKey: .port))
            else { throw DecodingError.dataCorruptedError(forKey: .kind, in: container, debugDescription: "Invalid Local App destination.") }
            self = destination
        case "remoteApp":
            guard keys == ["kind", "scheme", "host", "port"],
                  let destination = Self(
                    remoteAppScheme: try container.decode(RemoteAppScheme.self, forKey: .scheme),
                    host: try container.decode(String.self, forKey: .host),
                    port: try container.decode(UInt16.self, forKey: .port)
                  )
            else { throw DecodingError.dataCorruptedError(forKey: .kind, in: container, debugDescription: "Invalid Remote App destination.") }
            self = destination
        default:
            throw DecodingError.dataCorruptedError(forKey: .kind, in: container, debugDescription: "Unknown Portal destination kind.")
        }
    }

    func encode(to encoder: Encoder) throws {
        var container = encoder.container(keyedBy: CodingKeys.self)
        switch self {
        case let .localApp(port):
            try container.encode("localApp", forKey: .kind)
            try container.encode(port, forKey: .port)
        case let .remoteApp(scheme, host, port):
            try container.encode("remoteApp", forKey: .kind)
            try container.encode(scheme, forKey: .scheme)
            try container.encode(host, forKey: .host)
            try container.encode(port, forKey: .port)
        }
    }

    private static func canonicalRemoteHost(_ raw: String) -> String? {
        guard !raw.isEmpty, raw == raw.trimmingCharacters(in: .whitespacesAndNewlines), raw.unicodeScalars.allSatisfy(\.isASCII) else {
            return nil
        }
        if raw.contains(":"), !raw.contains("["), !raw.contains("%") {
            return canonicalIPv6(raw)
        }
        if raw.contains("[") || raw.contains("]") || raw.contains("%") {
            return nil
        }
        if let ipv4 = canonicalIPv4(raw) {
            return ipv4
        }
        guard !raw.hasSuffix("."), raw.utf8.count <= 253 else { return nil }
        let host = raw.lowercased()
        let labels = host.split(separator: ".", omittingEmptySubsequences: false)
        guard !labels.isEmpty, labels.allSatisfy({ isValidDNSLabel($0.utf8) }) else { return nil }
        return host
    }

    private static func canonicalIPv4(_ raw: String) -> String? {
        var address = in_addr()
        guard inet_pton(AF_INET, raw, &address) == 1 else { return nil }
        var rendered = [CChar](repeating: 0, count: Int(INET_ADDRSTRLEN))
        guard inet_ntop(AF_INET, &address, &rendered, socklen_t(INET_ADDRSTRLEN)) != nil else { return nil }
        let canonical = String(cString: rendered)
        return canonical == raw ? canonical : nil
    }

    private static func canonicalIPv6(_ raw: String) -> String? {
        var address = in6_addr()
        guard inet_pton(AF_INET6, raw, &address) == 1 else { return nil }
        var rendered = [CChar](repeating: 0, count: Int(INET6_ADDRSTRLEN))
        guard inet_ntop(AF_INET6, &address, &rendered, socklen_t(INET6_ADDRSTRLEN)) != nil else { return nil }
        let canonical = String(cString: rendered).lowercased()
        return canonical == raw ? canonical : nil
    }

    private static func isLoopbackHost(_ host: String) -> Bool {
        if host == "localhost" || host == "::1" || host == "0.0.0.0" || host == "::" { return true }
        if host.hasPrefix("::ffff:") {
            return isLoopbackHost(String(host.dropFirst("::ffff:".count)))
        }
        if let first = host.split(separator: ".").first, Int(first) == 127 { return true }
        return false
    }

}

private struct DynamicCodingKey: CodingKey {
    let stringValue: String
    init?(stringValue: String) { self.stringValue = stringValue }
    let intValue: Int? = nil
    init?(intValue: Int) { return nil }
}

private func isValidDNSLabel<Bytes: BidirectionalCollection>(_ bytes: Bytes) -> Bool where Bytes.Element == UInt8 {
    guard (1...63).contains(bytes.count),
          let first = bytes.first,
          let last = bytes.last,
          isLowercaseAlphanumeric(first), isLowercaseAlphanumeric(last)
    else { return false }
    return bytes.allSatisfy { isLowercaseAlphanumeric($0) || $0 == 45 }
}

private func isLowercaseAlphanumeric(_ byte: UInt8) -> Bool {
    (48...57).contains(byte) || (97...122).contains(byte)
}

struct PortalConfiguration: Codable, Equatable {
    let id: UUID
    let name: String
    var destination: PortalDestination
    let createdAt: Date
    var desiredState: PortalDesiredState = .enabled
    var lifecycle: PortalLifecycle = .active
    var removalAssignedName: String?

    init(
        id: UUID,
        name: String,
        localAppPort: UInt16,
        createdAt: Date,
        desiredState: PortalDesiredState = .enabled,
        lifecycle: PortalLifecycle = .active,
        removalAssignedName: String? = nil
    ) {
        guard let destination = PortalDestination(localAppPort: localAppPort) else {
            preconditionFailure("Portal destinations require a port from 1 through 65535.")
        }
        self.init(
            id: id,
            name: name,
            destination: destination,
            createdAt: createdAt,
            desiredState: desiredState,
            lifecycle: lifecycle,
            removalAssignedName: removalAssignedName
        )
    }

    init(
        id: UUID,
        name: String,
        destination: PortalDestination,
        createdAt: Date,
        desiredState: PortalDesiredState = .enabled,
        lifecycle: PortalLifecycle = .active,
        removalAssignedName: String? = nil
    ) {
        self.id = id
        self.name = name
        self.destination = destination
        self.createdAt = createdAt
        self.desiredState = desiredState
        self.lifecycle = lifecycle
        self.removalAssignedName = removalAssignedName
    }

    var localAppPort: UInt16? {
        get { destination.localAppPort }
        set {
            guard let newValue, let destination = PortalDestination(localAppPort: newValue) else {
                preconditionFailure("Portal destinations require a port from 1 through 65535.")
            }
            self.destination = destination
        }
    }
}

enum PortalDesiredState: String, Codable, Equatable {
    case enabled
    case stopped
}

extension PortalConfiguration {
    private enum CodingKeys: String, CodingKey {
        case id
        case name
        case destination
        case createdAt
        case desiredState
        case lifecycle
        case removalAssignedName
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        id = try container.decode(UUID.self, forKey: .id)
        name = try container.decode(String.self, forKey: .name)
        destination = try container.decode(PortalDestination.self, forKey: .destination)
        createdAt = try container.decode(Date.self, forKey: .createdAt)
        desiredState = try container.contains(.desiredState)
            ? container.decode(PortalDesiredState.self, forKey: .desiredState)
            : .enabled
        lifecycle = try container.decode(PortalLifecycle.self, forKey: .lifecycle)
        removalAssignedName = lifecycle == .pendingRemoval
            ? try container.decodeIfPresent(String.self, forKey: .removalAssignedName)
            : nil
    }

    func encode(to encoder: Encoder) throws {
        var container = encoder.container(keyedBy: CodingKeys.self)
        try container.encode(id, forKey: .id)
        try container.encode(name, forKey: .name)
        try container.encode(destination, forKey: .destination)
        try container.encode(createdAt, forKey: .createdAt)
        try container.encode(desiredState, forKey: .desiredState)
        try container.encode(lifecycle, forKey: .lifecycle)
        if lifecycle == .pendingRemoval {
            try container.encodeIfPresent(removalAssignedName, forKey: .removalAssignedName)
        }
    }
}

enum PortalLifecycle: String, Codable, Equatable {
    case active
    case pendingTailnetRejection
    case pendingRemoval
}

struct TailnetBinding: Codable, Equatable {
    let name: String
    var magicDNSSuffix: String
}

enum InstallationAlertKind: String, Codable, Equatable {
    case crossTailnetRejection
}

struct InstallationAlert: Codable, Equatable, Identifiable {
    let id: UUID
    let kind: InstallationAlertKind
    let portalName: String
    let assignedName: String?
    let expectedMagicDNSSuffix: String?
    let rejectedMagicDNSSuffix: String?
    let createdAt: Date
}

enum OperationalLoggingPreference: String, Codable, Equatable, CaseIterable {
    case undecided
    case enabled
    case disabled
}

enum LaunchAtLoginOfferState: String, Codable, Equatable {
    case notOffered
    case presented
    case declined
    case accepted
}

struct InstallationRecord: Codable, Equatable {
    static let currentVersion = 4

    let version: Int
    var tailnetBinding: TailnetBinding?
    var portals: [PortalConfiguration]
    var alerts: [InstallationAlert]
    var operationalLogging: OperationalLoggingPreference
    var launchAtLoginOffer: LaunchAtLoginOfferState

    init(
        tailnetBinding: TailnetBinding? = nil,
        portals: [PortalConfiguration] = [],
        alerts: [InstallationAlert] = [],
        operationalLogging: OperationalLoggingPreference = .undecided,
        launchAtLoginOffer: LaunchAtLoginOfferState = .notOffered
    ) {
        version = Self.currentVersion
        self.tailnetBinding = tailnetBinding
        self.portals = portals
        self.alerts = alerts
        self.operationalLogging = operationalLogging
        self.launchAtLoginOffer = launchAtLoginOffer
    }
}


enum PortalValidationError: Error, Equatable {
    case invalidName
    case invalidPort
    case invalidRemoteHost
}

enum PortalInputValidator {
    static func validate(name: String, port: String) throws -> UInt16 {
        guard isValidDNSLabel(name.utf8) else {
            throw PortalValidationError.invalidName
        }
        guard !port.isEmpty,
              port.unicodeScalars.allSatisfy({ (48...57).contains($0.value) }),
              let value = UInt16(port),
              value > 0
        else {
            throw PortalValidationError.invalidPort
        }
        return value
    }

}
