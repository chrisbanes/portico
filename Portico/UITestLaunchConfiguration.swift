import Foundation

#if DEBUG

enum UITestScenario: String {
    case firstRun = "first-run"
    case creation
    case management
    case durableManagement = "durable-management"
    case recovery
    case resetEligible = "reset-eligible"
    case online
    case stopped
    case remoteOnline = "remote-online"
    case authenticating
    case awaitingApproval = "awaiting-approval"
    case stale
    case staleAuthenticating = "stale-authenticating"
    case restarting
    case terminalFailure = "terminal-failure"
    case removing
    case removingFailure = "removing-failure"
    case loginApproval = "login-approval"
    case loginError = "login-error"
    case loginOffer = "login-offer"
    case loginOfferApproval = "login-offer-approval"
    case loginOfferError = "login-offer-error"
    case loginOfferEmpty = "login-offer-empty"
    case migrated
    case initialSaveFailure = "initial-save-failure"
    case configuredMessage = "configured-message"
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
        case .creation:
            try store.save(InstallationRecord(operationalLogging: .enabled))
        case .migrated:
            try FileManager.default.createDirectory(at: rootURL, withIntermediateDirectories: true)
            try Data(#"{"version":2,"portals":[],"alerts":[]}"#.utf8).write(to: store.versionTwoInstallationURL)
        case .initialSaveFailure:
            try Data("fixture".utf8).write(to: rootURL)
        case .configuredMessage:
            try store.save(InstallationRecord(operationalLogging: .enabled))
        case .loginApproval, .loginError:
            try store.save(InstallationRecord(operationalLogging: .enabled))
        case .removing, .removingFailure:
            try store.save(InstallationRecord(
                tailnetBinding: Self.tailnetBinding,
                portals: [Self.portal(lifecycle: .pendingRemoval, removalAssignedName: "portal-one-1")],
                operationalLogging: .enabled,
                launchAtLoginOffer: .declined
            ))
        case .loginOffer, .loginOfferApproval, .loginOfferError:
            try store.save(InstallationRecord(
                tailnetBinding: Self.tailnetBinding,
                portals: [Self.portal()],
                operationalLogging: .enabled,
                launchAtLoginOffer: .notOffered
            ))
        case .loginOfferEmpty:
            try store.save(InstallationRecord(
                operationalLogging: .enabled,
                launchAtLoginOffer: .presented
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
        case .management:
            try store.save(InstallationRecord(
                tailnetBinding: Self.tailnetBinding,
                portals: [
                    Self.portal(
                        id: Self.firstPortalID,
                        name: "first-portal",
                        createdAt: Date(timeIntervalSince1970: 1_700_000_000)
                    ),
                    Self.portal(
                        id: Self.secondPortalID,
                        name: "second-portal",
                        createdAt: Date(timeIntervalSince1970: 1_700_000_001)
                    ),
                ],
                operationalLogging: .enabled,
                launchAtLoginOffer: .declined
            ))
        case .durableManagement:
            try store.save(InstallationRecord(
                tailnetBinding: Self.tailnetBinding,
                portals: [
                    Self.portal(
                        id: Self.firstPortalID,
                        name: "durable-active",
                        createdAt: Date(timeIntervalSince1970: 1_700_000_000)
                    ),
                    Self.portal(
                        id: Self.secondPortalID,
                        name: "durable-removing",
                        createdAt: Date(timeIntervalSince1970: 1_700_000_001),
                        lifecycle: .pendingRemoval,
                        removalAssignedName: "durable-removing-1"
                    ),
                    Self.portal(
                        id: Self.thirdPortalID,
                        name: "durable-rejected",
                        createdAt: Date(timeIntervalSince1970: 1_700_000_002),
                        lifecycle: .pendingTailnetRejection,
                        removalAssignedName: "durable-rejected-1"
                    ),
                ],
                operationalLogging: .enabled,
                launchAtLoginOffer: .declined
            ))
        case .recovery:
            try store.save(InstallationRecord(
                tailnetBinding: Self.tailnetBinding,
                portals: [
                    Self.portal(
                        id: Self.thirdPortalID,
                        name: "recovery-rejected",
                        lifecycle: .pendingTailnetRejection,
                        removalAssignedName: "recovery-rejected-1"
                    ),
                ],
                alerts: [Self.recoveryAlert],
                operationalLogging: .enabled,
                launchAtLoginOffer: .declined
            ))
        case .resetEligible:
            try store.save(InstallationRecord(
                tailnetBinding: Self.tailnetBinding,
                operationalLogging: .enabled,
                launchAtLoginOffer: .declined
            ))
        case .online, .stopped, .authenticating, .awaitingApproval, .stale, .staleAuthenticating, .restarting, .terminalFailure:
            try store.save(InstallationRecord(
                tailnetBinding: Self.tailnetBinding,
                portals: [Self.portal(desiredState: scenario == .stopped ? .stopped : .enabled)],
                operationalLogging: .enabled,
                launchAtLoginOffer: .declined
            ))
        }
    }

    var launchAtLoginStatus: LaunchAtLoginStatus { scenario == .loginApproval ? .requiresApproval : .notRegistered }
    var registrationOutcome: UITestLaunchAtLoginRegistrationOutcome {
        switch scenario {
        case .loginOfferApproval: .requiresApproval
        case .loginOfferError, .loginError: .failure
        default: .enabled
        }
    }
    var reachabilityResult: Bool { scenario != .terminalFailure }
    var reportsInitialPersistenceFailure: Bool { scenario == .configuredMessage }
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

    private static let firstPortalID = UUID(uuidString: "22222222-2222-4222-8222-222222222222")!
    private static let secondPortalID = UUID(uuidString: "33333333-3333-4333-8333-333333333333")!
    private static let thirdPortalID = UUID(uuidString: "44444444-4444-4444-8444-444444444444")!
    private static let recoveryAlert = InstallationAlert(
        id: UUID(uuidString: "55555555-5555-4555-8555-555555555555")!,
        kind: .crossTailnetRejection,
        portalName: "recovery-rejected",
        assignedName: "recovery-rejected-1",
        expectedMagicDNSSuffix: "example.ts.net",
        rejectedMagicDNSSuffix: "other.ts.net",
        createdAt: Date(timeIntervalSince1970: 1_700_000_003)
    )

    private static func portal(
        id: UUID = portalID,
        name: String = "portal-one",
        destination: PortalDestination = .localApp(port: 8080),
        createdAt: Date = Date(timeIntervalSince1970: 1_700_000_000),
        lifecycle: PortalLifecycle = .active,
        desiredState: PortalDesiredState = .enabled,
        removalAssignedName: String? = nil
    ) -> PortalConfiguration {
        PortalConfiguration(
            id: id,
            name: name,
            destination: destination,
            createdAt: createdAt,
            desiredState: desiredState,
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
    private let rootURL: URL
    private var launchCount = 0

    init(configuration: UITestLaunchConfiguration) {
        scenario = configuration.scenario
        rootURL = configuration.rootURL
    }

    func launch(
        at executableURL: URL,
        arguments: [String],
        loggingPreference: OperationalLoggingPreference,
        onLine: @escaping (Data) -> Void,
        onEOF: @escaping () -> Void,
        onExit: @escaping (Int32) -> Void
    ) throws -> HelperProcess {
        if scenario == .terminalFailure || ([.stale, .staleAuthenticating].contains(scenario) && launchCount > 0) {
            throw UITestHelperLaunchBlocked()
        }
        launchCount += 1
        return UITestHelperProcess(
            scenario: scenario,
            rootURL: rootURL,
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
    private let rootURL: URL
    private let onLine: (Data) -> Void
    private let onEOF: () -> Void
    private let onExit: (Int32) -> Void
    private var shutdownRequested = false
    private var staleFailureScheduled = false
    private(set) var isRunning = true

    init(
        scenario: UITestScenario,
        rootURL: URL,
        onLine: @escaping (Data) -> Void,
        onEOF: @escaping () -> Void,
        onExit: @escaping (Int32) -> Void
    ) {
        self.scenario = scenario
        self.rootURL = rootURL
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
            guard scenario != .configuredMessage else { return }
            let request = try JSONDecoder().decode(HelperRequest<ReconcilePortalsPayload>.self, from: data)
            recordEnrollmentIfNeeded(for: request.payload.portals)
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
            if scenario == .durableManagement || scenario == .recovery { return }
            respond(CleanupRejectedPortalResult(accepted: true), requestID: envelope.requestId)
        case .removePortal:
            if scenario == .removing || scenario == .durableManagement { return }
            if scenario == .removingFailure {
                respondError(code: "remove_failed", requestID: envelope.requestId)
            } else {
                respond(RemovePortalResult(accepted: true), requestID: envelope.requestId)
            }
        case .discoverLocalApps:
            let candidates: [LocalAppCandidatePayload] = scenario == .creation ? [
                LocalAppCandidatePayload(
                    localAppPort: 9342,
                    processLabel: "node",
                    suggestedPortalName: "detected-portal"
                ),
            ] : []
            respond(DiscoverLocalAppsResult(candidates: candidates), requestID: envelope.requestId)
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

    private func recordEnrollmentIfNeeded(for portals: [ReconcilePortalPayload]) {
        guard !portals.isEmpty else { return }
        try? Data("enrolled\n".utf8).write(
            to: rootURL.appendingPathComponent("helper-enrollment.txt"),
            options: .atomic
        )
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
        guard [.online, .stopped, .remoteOnline, .authenticating, .awaitingApproval, .stale, .staleAuthenticating, .restarting, .loginOffer, .loginOfferApproval, .loginOfferError, .management, .durableManagement, .creation].contains(scenario) else { return }
        for portal in portals {
            let state: PortalTailscaleState
            if scenario == .stopped {
                state = .stopped
            } else if [.authenticating, .creation, .staleAuthenticating].contains(scenario) {
                state = .authenticating
            } else if scenario == .awaitingApproval {
                state = .awaitingApproval
            } else if scenario == .management, portal.portalName == "second-portal" {
                state = .connecting
            } else {
                state = .online
            }
            deliver(HelperEvent(
                version: helperProtocolVersion,
                event: .portalStatus,
                portalId: portal.portalId,
                payload: PortalStatusPayload(
                    state: state,
                    stableNodeId: "node-\(portal.portalName)",
                    assignedName: "\(portal.portalName)-1",
                    portalURL: URL(string: "https://\(portal.portalName)-1.example.ts.net"),
                    addresses: portal.portalName == "second-portal" ? ["100.64.0.11"] : ["100.64.0.10"],
                    tailnetName: "test-tailnet",
                    magicDNSSuffix: "example.ts.net"
                )
            ), delay: 0.01)
        }
        guard [.stale, .staleAuthenticating].contains(scenario), !staleFailureScheduled else { return }
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
    private let registrationOutcome: UITestLaunchAtLoginRegistrationOutcome

    init(status: LaunchAtLoginStatus, registrationOutcome: UITestLaunchAtLoginRegistrationOutcome) {
        self.status = status
        self.registrationOutcome = registrationOutcome
    }

    func register() throws {
        switch registrationOutcome {
        case .enabled: status = .enabled
        case .requiresApproval: status = .requiresApproval
        case .failure: throw UITestServiceError()
        }
    }

    func unregister() throws { status = .notRegistered }
    func openSystemSettingsLoginItems() {}
}

enum UITestLaunchAtLoginRegistrationOutcome {
    case enabled
    case requiresApproval
    case failure
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
