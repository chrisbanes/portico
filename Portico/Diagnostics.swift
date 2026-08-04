import Foundation
import Darwin

enum DiagnosticEvent: Equatable {
    case helper(HelperAvailability)
    case portal(
        name: String,
        desired: PortalDesiredState,
        tailscale: PortalTailscaleState?,
        reachability: LocalAppReachabilityState,
        stale: Bool
    )
}

struct DiagnosticEntry: Equatable {
    let timestamp: Date
    let event: DiagnosticEvent
}

final class DiagnosticHistory {
    private(set) var entries: [DiagnosticEntry] = []
    var onChange: (([DiagnosticEntry]) -> Void)?

    private let dateProvider: () -> Date

    init(dateProvider: @escaping () -> Date = Date.init) {
        self.dateProvider = dateProvider
    }

    func record(_ event: DiagnosticEvent) {
        entries.append(DiagnosticEntry(timestamp: dateProvider(), event: event))
        if entries.count > 200 {
            entries.removeFirst(entries.count - 200)
        }
        onChange?(entries)
    }
}

struct PortalDiagnosticFacts: Equatable {
    let portalName: String
    let assignedName: String?
    let portalURL: URL?
    let addresses: [String]
    let magicDNSSuffix: String?
    let desiredState: PortalDesiredState
    let tailscaleState: PortalTailscaleState?
    let reachability: LocalAppReachabilityState
    let isStale: Bool

}

struct DiagnosticVersions: Equatable {
    let porticoShort: String?
    let porticoBuild: String?
    let helperProtocol: Int

    static var current: DiagnosticVersions {
        DiagnosticVersions(
            porticoShort: Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String,
            porticoBuild: Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String,
            helperProtocol: helperProtocolVersion
        )
    }
}

enum DiagnosticReportRenderer {
    static func render(
        versions: DiagnosticVersions,
        helper: HelperAvailability,
        portals: [PortalDiagnosticFacts],
        history: [DiagnosticEntry]
    ) -> String {
        var lines: [String] = []
        if let short = versions.porticoShort {
            let build = versions.porticoBuild.map { " (\($0))" } ?? ""
            lines.append("Portico \(short)\(build)")
        }
        lines.append("Helper protocol \(versions.helperProtocol)")
        lines.append("Helper: \(describeHelper(helper))")
        lines.append("")
        lines.append("Portals")
        for portal in portals.sorted(by: { $0.portalName < $1.portalName }) {
            lines.append("- Portal Name: \(portal.portalName)")
            append("  Assigned Name", portal.assignedName, to: &lines)
            append("  Portal URL", portal.portalURL?.absoluteString, to: &lines)
            if !portal.addresses.isEmpty {
                lines.append("  Tailscale IPs: \(portal.addresses.joined(separator: ", "))")
            }
            append("  MagicDNS suffix", portal.magicDNSSuffix, to: &lines)
            lines.append("  Desired: \(portal.desiredState.rawValue)")
            lines.append("  Tailscale: \(portal.tailscaleState?.rawValue ?? "unknown")")
            lines.append("  Reachability: \(portal.reachability.rawValue)")
            lines.append("  Facts: \(portal.isStale ? "stale" : "current")")
        }
        lines.append("")
        lines.append("History")
        let formatter = ISO8601DateFormatter()
        for entry in history {
            lines.append("- \(formatter.string(from: entry.timestamp)) \(describe(entry.event))")
        }
        return lines.joined(separator: "\n")
    }

    private static func append(_ label: String, _ value: String?, to lines: inout [String]) {
        if let value, !value.isEmpty {
            lines.append("\(label): \(value)")
        }
    }

    private static func describe(_ event: DiagnosticEvent) -> String {
        switch event {
        case let .helper(availability):
            switch availability {
            case .connecting: return "Helper connecting"
            case let .retrying(attempt, delay): return "Helper retry \(attempt) in \(Int(delay))s"
            case .connected: return "Helper connected"
            case .failed: return "Helper unavailable"
            case .shuttingDown: return "Helper shutting down"
            }
        case let .portal(name, desired, tailscale, reachability, stale):
            return "Portal \(name): desired \(desired.rawValue), Tailscale \(tailscale?.rawValue ?? "unknown"), reachability \(reachability.rawValue), facts \(stale ? "stale" : "current")"
        }
    }

    private static func describeHelper(_ availability: HelperAvailability) -> String {
        switch availability {
        case .connecting: return "connecting"
        case let .retrying(attempt, delay): return "retry \(attempt) in \(Int(delay))s"
        case .connected: return "connected"
        case .failed: return "unavailable"
        case .shuttingDown: return "shutting down"
        }
    }
}

extension PortalStatusPayload {
    func sanitizedForDisplay() -> PortalStatusPayload {
        let safeAssignedName = SafePortalFact.assignedName(assignedName)
        let safeSuffix = SafePortalFact.magicDNSSuffix(magicDNSSuffix)
        return PortalStatusPayload(
            state: state,
            stableNodeId: nil,
            assignedName: safeAssignedName,
            portalURL: SafePortalFact.portalURL(
                portalURL,
                assignedName: safeAssignedName,
                magicDNSSuffix: safeSuffix
            ),
            addresses: addresses.filter(SafePortalFact.isIPAddress),
            magicDNSSuffix: safeSuffix
        )
    }
}

private enum SafePortalFact {
    static func assignedName(_ value: String?) -> String? {
        guard let value, isDNSLabel(value) else { return nil }
        return value
    }

    static func magicDNSSuffix(_ value: String?) -> String? {
        guard let value,
              value.hasSuffix(".ts.net"),
              value.split(separator: ".", omittingEmptySubsequences: false).allSatisfy({ isDNSLabel(String($0)) })
        else { return nil }
        return value
    }

    static func portalURL(_ value: URL?, assignedName: String?, magicDNSSuffix: String?) -> URL? {
        guard let value, let assignedName, let magicDNSSuffix,
              let components = URLComponents(url: value, resolvingAgainstBaseURL: false),
              components.scheme == "https",
              components.user == nil,
              components.password == nil,
              components.port == nil,
              components.query == nil,
              components.fragment == nil,
              components.path.isEmpty || components.path == "/",
              components.host?.lowercased() == "\(assignedName).\(magicDNSSuffix)"
        else { return nil }
        return value
    }

    static func isIPAddress(_ value: String) -> Bool {
        var ipv4 = in_addr()
        if value.withCString({ inet_pton(AF_INET, $0, &ipv4) }) == 1 {
            return true
        }
        var ipv6 = in6_addr()
        return value.withCString({ inet_pton(AF_INET6, $0, &ipv6) }) == 1
    }

    private static func isDNSLabel(_ value: String) -> Bool {
        guard (1...63).contains(value.utf8.count),
              value.unicodeScalars.allSatisfy({ $0.isASCII })
        else { return false }
        let bytes = Array(value.utf8)
        let isAlphanumeric: (UInt8) -> Bool = { (48...57).contains($0) || (97...122).contains($0) }
        return isAlphanumeric(bytes[0])
            && isAlphanumeric(bytes[bytes.count - 1])
            && bytes.allSatisfy { isAlphanumeric($0) || $0 == 45 }
    }
}
