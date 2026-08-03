import Foundation

struct PortalConfiguration: Codable, Equatable {
    let id: UUID
    let name: String
    let localAppPort: UInt16
    let createdAt: Date
    var lifecycle: PortalLifecycle = .active
}

enum PortalLifecycle: String, Codable, Equatable {
    case active
    case pendingTailnetRejection
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

struct InstallationRecord: Codable, Equatable {
    static let currentVersion = 2

    let version: Int
    var tailnetBinding: TailnetBinding?
    var portals: [PortalConfiguration]
    var alerts: [InstallationAlert]

    init(
        tailnetBinding: TailnetBinding? = nil,
        portals: [PortalConfiguration] = [],
        alerts: [InstallationAlert] = []
    ) {
        version = Self.currentVersion
        self.tailnetBinding = tailnetBinding
        self.portals = portals
        self.alerts = alerts
    }
}


enum PortalValidationError: Error, Equatable {
    case invalidName
    case invalidPort
}

enum PortalInputValidator {
    static func validate(name: String, port: String) throws -> UInt16 {
        guard isValidDNSLabel(name) else {
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

    private static func isValidDNSLabel(_ value: String) -> Bool {
        guard (1...63).contains(value.utf8.count),
              value.unicodeScalars.allSatisfy({ $0.isASCII })
        else {
            return false
        }
        let bytes = Array(value.utf8)
        guard isLowercaseAlphanumeric(bytes[0]),
              isLowercaseAlphanumeric(bytes[bytes.count - 1])
        else {
            return false
        }
        return bytes.allSatisfy { isLowercaseAlphanumeric($0) || $0 == 45 }
    }

    private static func isLowercaseAlphanumeric(_ byte: UInt8) -> Bool {
        (48...57).contains(byte) || (97...122).contains(byte)
    }
}
