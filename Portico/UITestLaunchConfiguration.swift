import Foundation

#if DEBUG

enum UITestScenario: String {
    case firstRun = "first-run"
    case online
    case remoteOnline = "remote-online"
    case authenticating
    case stale
    case restarting
    case terminalFailure = "terminal-failure"
    case removing
    case removingFailure = "removing-failure"
    case loginApproval = "login-approval"
    case loginError = "login-error"
    case loginOffer = "login-offer"
    case migrated
    case initialSaveFailure = "initial-save-failure"
}

struct UITestLaunchConfiguration {
    static let portalID = UUID(uuidString: "11111111-1111-4111-8111-111111111111")!

    let scenario: UITestScenario
    let rootURL: URL

    static var current: UITestLaunchConfiguration? {
#if DEBUG
        let process = ProcessInfo.processInfo
        guard process.arguments.contains("--ui-testing"),
              let rawScenario = process.environment["PORTICO_UI_TEST_SCENARIO"],
              let scenario = UITestScenario(rawValue: rawScenario),
              let root = process.environment["PORTICO_UI_TEST_ROOT"]
        else { return nil }
        let rootURL = URL(fileURLWithPath: root, isDirectory: true).standardizedFileURL
        guard rootURL.deletingLastPathComponent().path == "/private/tmp",
              rootURL.lastPathComponent.hasPrefix("PorticoUITests-")
        else { return nil }
        return UITestLaunchConfiguration(scenario: scenario, rootURL: rootURL)
#else
        return nil
#endif
    }

    func prepareInstallationIfNeeded() throws {
        let store = PortalStore(rootURL: rootURL)
        guard !FileManager.default.fileExists(atPath: store.installationURL.path) else { return }
        switch scenario {
        case .firstRun:
            break
        case .migrated:
            try FileManager.default.createDirectory(at: rootURL, withIntermediateDirectories: true)
            try Data(#"{"version":2,"portals":[],"alerts":[]}"#.utf8).write(to: store.versionTwoInstallationURL)
        case .initialSaveFailure:
            try Data("fixture".utf8).write(to: rootURL)
        case .loginApproval, .loginError:
            try store.save(InstallationRecord(operationalLogging: .enabled))
        case .removing, .removingFailure:
            try store.save(InstallationRecord(
                tailnetBinding: Self.tailnetBinding,
                portals: [Self.portal(lifecycle: .pendingRemoval, removalAssignedName: "portal-one-1")],
                operationalLogging: .enabled,
                launchAtLoginOffer: .declined
            ))
        case .loginOffer:
            try store.save(InstallationRecord(
                tailnetBinding: Self.tailnetBinding,
                portals: [Self.portal()],
                operationalLogging: .enabled,
                launchAtLoginOffer: .notOffered
            ))
        case .remoteOnline:
            try store.save(InstallationRecord(
                tailnetBinding: Self.tailnetBinding,
                portals: [Self.portal(destination: .remoteApp(
                    scheme: .https,
                    host: "app.example.com",
                    port: 443
                ))],
                operationalLogging: .enabled,
                launchAtLoginOffer: .declined
            ))
        case .online, .authenticating, .stale, .restarting, .terminalFailure:
            try store.save(InstallationRecord(
                tailnetBinding: Self.tailnetBinding,
                portals: [Self.portal()],
                operationalLogging: .enabled,
                launchAtLoginOffer: .declined
            ))
        }
    }

    var launchAtLoginStatus: LaunchAtLoginStatus {
        scenario == .loginApproval ? .requiresApproval : .notRegistered
    }

    var registrationFails: Bool { scenario == .loginError }
    var reachabilityResult: Bool { scenario != .terminalFailure }
    var supervisorSchedulerScale: Double {
        switch scenario {
        case .terminalFailure: 0.01
        case .restarting: 5
        default: 1
        }
    }

    private static let tailnetBinding = TailnetBinding(
        name: "test-tailnet",
        magicDNSSuffix: "example.ts.net"
    )

    private static func portal(
        destination: PortalDestination = .localApp(port: 8080),
        lifecycle: PortalLifecycle = .active,
        removalAssignedName: String? = nil
    ) -> PortalConfiguration {
        PortalConfiguration(
            id: portalID,
            name: "portal-one",
            destination: destination,
            createdAt: Date(timeIntervalSince1970: 1_700_000_000),
            desiredState: .enabled,
            lifecycle: lifecycle,
            removalAssignedName: removalAssignedName
        )
    }
}

private struct UITestHelperLaunchBlocked: Error {}
private struct UITestServiceError: Error {}

final class UITestRestartGate {
    static let shared = UITestRestartGate()

    private var completion: (() -> Void)?

    private init() {}

    func hold(_ completion: @escaping () -> Void) {
        self.completion = completion
    }

    func release() {
        let completion = completion
        self.completion = nil
        completion?()
    }
}

final class UITestHelperLauncher: HelperLaunching {
    private let scenario: UITestScenario
    private var launchCount = 0

    init(scenario: UITestScenario) {
        self.scenario = scenario
    }

    func launch(
        at executableURL: URL,
        arguments: [String],
        loggingPreference: OperationalLoggingPreference,
        onLine: @escaping (Data) -> Void,
        onEOF: @escaping () -> Void,
        onExit: @escaping (Int32) -> Void
    ) throws -> HelperProcess {
        if scenario == .terminalFailure || (scenario == .stale && launchCount > 0) {
            throw UITestHelperLaunchBlocked()
        }
        launchCount += 1
        return UITestHelperProcess(
            scenario: scenario,
            onLine: onLine,
            onEOF: onEOF,
            onExit: onExit
        )
    }
}

private final class UITestHelperProcess: HelperProcess {
    private struct RequestEnvelope: Decodable {
        let requestId: String
        let command: HelperCommand
    }

    private let scenario: UITestScenario
    private let onLine: (Data) -> Void
    private let onEOF: () -> Void
    private let onExit: (Int32) -> Void
    private var shutdownRequested = false
    private var staleFailureScheduled = false
    private(set) var isRunning = true

    init(
        scenario: UITestScenario,
        onLine: @escaping (Data) -> Void,
        onEOF: @escaping () -> Void,
        onExit: @escaping (Int32) -> Void
    ) {
        self.scenario = scenario
        self.onLine = onLine
        self.onEOF = onEOF
        self.onExit = onExit
    }

    func send(_ data: Data) throws {
        let envelope = try JSONDecoder().decode(RequestEnvelope.self, from: data)
        switch envelope.command {
        case .handshake:
            respond(HandshakeResult(protocolVersion: helperProtocolVersion), requestID: envelope.requestId)
        case .shutdown:
            shutdownRequested = true
        case .reconcilePortals:
            let request = try JSONDecoder().decode(HelperRequest<ReconcilePortalsPayload>.self, from: data)
            respond(
                ReconcilePortalsResult(entries: request.payload.portals.map {
                    ReconcilePortalEntry(portalId: $0.portalId, outcome: .converged)
                }),
                requestID: envelope.requestId
            )
            emitStatuses(for: request.payload.portals)
        case .authenticatePortal:
            respond(AuthenticatePortalResult(accepted: true), requestID: envelope.requestId)
        case .cleanupRejectedPortal:
            respond(CleanupRejectedPortalResult(accepted: true), requestID: envelope.requestId)
        case .removePortal:
            if scenario == .removing { return }
            if scenario == .removingFailure {
                respondError(code: "remove_failed", requestID: envelope.requestId)
            } else {
                respond(RemovePortalResult(accepted: true), requestID: envelope.requestId)
            }
        case .discoverLocalApps:
            respond(DiscoverLocalAppsResult(candidates: []), requestID: envelope.requestId)
        }
    }

    func closeInput() {
        guard shutdownRequested else { return }
        if scenario == .restarting {
            UITestRestartGate.shared.hold { [weak self] in
                self?.finish(exitCode: 0)
            }
        } else {
            finish(exitCode: 0)
        }
    }

    func terminate() {
        finish(exitCode: -15)
    }

    private func respond<Result: Codable>(_ result: Result, requestID: String) {
        let response = HelperResponse(
            version: helperProtocolVersion,
            requestId: requestID,
            result: result,
            error: nil
        )
        deliver(response)
    }

    private func respondError(code: String, requestID: String) {
        let response = HelperResponse<RemovePortalResult>(
            version: helperProtocolVersion,
            requestId: requestID,
            result: nil,
            error: HelperProtocolError(code: code, message: "UI test failure")
        )
        deliver(response)
    }

    private func deliver<Message: Encodable>(_ message: Message, delay: TimeInterval = 0) {
        guard let data = try? JSONEncoder().encode(message) else { return }
        DispatchQueue.main.asyncAfter(deadline: .now() + delay) { [weak self] in
            guard self?.isRunning == true else { return }
            self?.onLine(data)
        }
    }

    private func emitStatuses(for portals: [ReconcilePortalPayload]) {
        guard [.online, .remoteOnline, .authenticating, .stale, .restarting, .loginOffer].contains(scenario) else { return }
        for portal in portals {
            let state: PortalTailscaleState = scenario == .authenticating ? .authenticating : .online
            deliver(HelperEvent(
                version: helperProtocolVersion,
                event: .portalStatus,
                portalId: portal.portalId,
                payload: PortalStatusPayload(
                    state: state,
                    stableNodeId: "node-1",
                    assignedName: "portal-one-1",
                    portalURL: URL(string: "https://portal-one-1.example.ts.net"),
                    addresses: ["100.64.0.10"],
                    tailnetName: "test-tailnet",
                    magicDNSSuffix: "example.ts.net"
                )
            ), delay: 0.01)
        }
        guard scenario == .stale, !staleFailureScheduled else { return }
        staleFailureScheduled = true
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.02) { [weak self] in
            guard self?.isRunning == true else { return }
            self?.onEOF()
        }
    }

    private func finish(exitCode: Int32) {
        guard isRunning else { return }
        isRunning = false
        onExit(exitCode)
    }
}

final class UITestLaunchAtLoginService: LaunchAtLoginServicing {
    private(set) var status: LaunchAtLoginStatus
    private let registrationFails: Bool

    init(status: LaunchAtLoginStatus, registrationFails: Bool) {
        self.status = status
        self.registrationFails = registrationFails
    }

    func register() throws {
        if registrationFails { throw UITestServiceError() }
        status = .enabled
    }

    func unregister() throws { status = .notRegistered }
    func openSystemSettingsLoginItems() {}
}

final class UITestLocalAppProbe: LocalAppProbing {
    private let result: Bool

    init(result: Bool) {
        self.result = result
    }

    func probe(port: UInt16, timeout: TimeInterval, completion: @escaping (Bool) -> Void) {
        DispatchQueue.main.async { [result] in completion(result) }
    }
}

@MainActor
final class UITestScaledScheduler: PorticoScheduling {
    private let scale: Double

    init(scale: Double) {
        self.scale = scale
    }

    @discardableResult
    func schedule(after delay: TimeInterval, _ action: @escaping () -> Void) -> ScheduledTask {
        let workItem = DispatchWorkItem(block: action)
        DispatchQueue.main.asyncAfter(deadline: .now() + max(0.01, delay * scale), execute: workItem)
        return UITestScheduledTask(workItem: workItem)
    }
}

private final class UITestScheduledTask: ScheduledTask {
    private let workItem: DispatchWorkItem

    init(workItem: DispatchWorkItem) {
        self.workItem = workItem
    }

    func cancel() { workItem.cancel() }
}
#endif
