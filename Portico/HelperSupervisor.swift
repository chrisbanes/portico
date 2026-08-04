import Foundation

enum HelperAvailability: Equatable {
    case connecting
    case retrying(attempt: Int, delay: TimeInterval)
    case connected
    case failed
    case shuttingDown
}

enum PortalHelperEvent: Equatable {
    case status(UUID, PortalStatusPayload)
    case authenticationURL(UUID, URL)
}

enum HelperClientError: Error {
    case unavailable
    case protocolFailure
    case helper(HelperProtocolError)
}

@MainActor
protocol PortalHelperClient: AnyObject {
    var availability: HelperAvailability { get }
    var onConnected: (() -> Void)? { get set }
    var onAvailabilityChange: ((HelperAvailability) -> Void)? { get set }
    var onEvent: ((PortalHelperEvent) -> Void)? { get set }

    func retry()

    func reconcilePortals(
        _ portals: [PortalConfiguration],
        completion: @escaping (Result<ReconcilePortalsResult, Error>) -> Void
    )
    func authenticatePortal(id: UUID, completion: @escaping (Result<Void, Error>) -> Void)
    func cleanupRejectedPortal(id: UUID, completion: @escaping (Result<Void, Error>) -> Void)
    func discoverLocalApps(completion: @escaping (Result<[LocalAppCandidatePayload], Error>) -> Void)
}

protocol HelperProcess: AnyObject {
    var isRunning: Bool { get }

    func send(_ data: Data) throws
    func closeInput()
    func terminate()
}

protocol HelperLaunching {
    func launch(
        at executableURL: URL,
        arguments: [String],
        onLine: @escaping (Data) -> Void,
        onEOF: @escaping () -> Void,
        onExit: @escaping (Int32) -> Void
    ) throws -> HelperProcess
}

@MainActor
final class HelperSupervisor: ObservableObject, PortalHelperClient {
    @Published private(set) var availability: HelperAvailability = .connecting {
        didSet {
            guard oldValue != availability else { return }
            history?.record(.helper(availability))
            onAvailabilityChange?(availability)
        }
    }
    var onConnected: (() -> Void)?
    var onAvailabilityChange: ((HelperAvailability) -> Void)?
    var onEvent: ((PortalHelperEvent) -> Void)?

    private let helperURL: URL
    private let stateRootURL: URL
    private let launcher: HelperLaunching
    private let requestIDProvider: () -> String
    private let scheduler: PorticoScheduling
    private let history: DiagnosticHistory?
    private let handshakeTimeout: TimeInterval
    private let shutdownGraceInterval: TimeInterval
    private var process: HelperProcess?
    private var pendingResponses: [String: (Data?) -> Bool] = [:]
    private var handshakeTimeoutTask: ScheduledTask?
    private var shutdownTimeoutTask: ScheduledTask?
    private var retryTask: ScheduledTask?
    private var stabilityTask: ScheduledTask?
    private var processGeneration = 0
    private var reconciliationGeneration = 0
    private var retryDelayIndex = 0
    private var failureHandled = false
    private var isShuttingDown = false
    private var isShutdownComplete = false
    private var shutdownCompletions: [() -> Void] = []

    init(
        helperURL: URL,
        stateRootURL: URL = HelperSupervisor.defaultStateRootURL(),
        launcher: HelperLaunching,
        requestIDProvider: @escaping () -> String = { UUID().uuidString },
        scheduler: PorticoScheduling? = nil,
        history: DiagnosticHistory? = nil,
        handshakeTimeout: TimeInterval = 3,
        shutdownGraceInterval: TimeInterval = 1
    ) {
        self.helperURL = helperURL
        self.stateRootURL = stateRootURL
        self.launcher = launcher
        self.requestIDProvider = requestIDProvider
        self.scheduler = scheduler ?? MainQueueScheduler()
        self.history = history
        self.handshakeTimeout = handshakeTimeout
        self.shutdownGraceInterval = shutdownGraceInterval
    }

    func start() {
        guard process == nil, retryTask == nil, !isShuttingDown else { return }
        launch()
    }

    func retry() {
        guard availability == .failed, !isShuttingDown else { return }
        retryTask?.cancel()
        retryTask = nil
        stabilityTask?.cancel()
        stabilityTask = nil
        handshakeTimeoutTask?.cancel()
        handshakeTimeoutTask = nil
        processGeneration += 1
        reconciliationGeneration += 1
        failPendingResponses()
        retryDelayIndex = 0
        failureHandled = false
        availability = .connecting
        guard let process else {
            launch()
            return
        }
        process.closeInput()
        if process.isRunning {
            process.terminate()
        } else {
            self.process = nil
            launch()
        }
    }

    private func launch() {
        guard process == nil, !isShuttingDown else { return }
        processGeneration += 1
        let generation = processGeneration
        failureHandled = false
        availability = .connecting
        do {
            process = try launcher.launch(
                at: helperURL,
                arguments: ["--state-root", stateRootURL.path],
                onLine: { [weak self] data in self?.receive(line: data, generation: generation) },
                onEOF: { [weak self] in self?.handleFailure(generation: generation) },
                onExit: { [weak self] _ in self?.processExited(generation: generation) }
            )
            try sendRequest(command: .handshake, payload: EmptyPayload()) { [weak self] (result: Result<HandshakeResult, Error>) in
                guard let self else { return }
                guard generation == self.processGeneration, !self.failureHandled else { return }
                guard case let .success(handshake) = result,
                      handshake.protocolVersion == helperProtocolVersion
                else {
                    self.handleFailure(generation: generation)
                    return
                }
                self.handshakeTimeoutTask?.cancel()
                self.handshakeTimeoutTask = nil
                self.availability = .connected
                self.onConnected?()
            }
            handshakeTimeoutTask = scheduler.schedule(after: handshakeTimeout) { [weak self] in
                self?.handleFailure(generation: generation)
            }
        } catch {
            process = nil
            handleFailure(generation: generation)
        }
    }

    func reconcilePortals(
        _ portals: [PortalConfiguration],
        completion: @escaping (Result<ReconcilePortalsResult, Error>) -> Void
    ) {
        guard availability == .connected else {
            completion(.failure(HelperClientError.unavailable))
            return
        }
        let payload = ReconcilePortalsPayload(
            portals: portals
                .sorted { $0.id.uuidString.lowercased() < $1.id.uuidString.lowercased() }
                .map {
                    ReconcilePortalPayload(
                        portalId: $0.id,
                        portalName: $0.name,
                        localAppPort: $0.localAppPort,
                        desiredState: $0.desiredState
                    )
                }
        )
        reconciliationGeneration += 1
        let requestedReconciliation = reconciliationGeneration
        let requestedProcess = processGeneration
        stabilityTask?.cancel()
        stabilityTask = nil
        do {
            try sendRequest(command: .reconcilePortals, payload: payload) { [weak self] result in
                completion(result)
                guard let self,
                      requestedProcess == self.processGeneration,
                      requestedReconciliation == self.reconciliationGeneration,
                      self.availability == .connected,
                      case let .success(response) = result,
                      response.entries.allSatisfy({ $0.outcome == .converged })
                else { return }
                self.stabilityTask = self.scheduler.schedule(after: 300) { [weak self] in
                    guard let self,
                          requestedProcess == self.processGeneration,
                          requestedReconciliation == self.reconciliationGeneration,
                          self.availability == .connected
                    else { return }
                    self.retryDelayIndex = 0
                    self.stabilityTask = nil
                }
            }
        } catch {
            completion(.failure(error))
            handleFailure(generation: requestedProcess)
        }
    }

    func authenticatePortal(id: UUID, completion: @escaping (Result<Void, Error>) -> Void) {
        guard availability == .connected else {
            completion(.failure(HelperClientError.unavailable))
            return
        }
        do {
            try sendRequest(command: .authenticatePortal, payload: AuthenticatePortalPayload(portalId: id)) { (result: Result<AuthenticatePortalResult, Error>) in
                completion(result.map { _ in () })
            }
        } catch {
            completion(.failure(error))
            handleFailure(generation: processGeneration)
        }
    }

    func cleanupRejectedPortal(id: UUID, completion: @escaping (Result<Void, Error>) -> Void) {
        guard availability == .connected else {
            completion(.failure(HelperClientError.unavailable))
            return
        }
        do {
            try sendRequest(command: .cleanupRejectedPortal, payload: CleanupRejectedPortalPayload(portalId: id)) { (result: Result<CleanupRejectedPortalResult, Error>) in
                completion(result.map { _ in () })
            }
        } catch {
            completion(.failure(error))
            handleFailure(generation: processGeneration)
        }
    }

    func discoverLocalApps(completion: @escaping (Result<[LocalAppCandidatePayload], Error>) -> Void) {
        guard availability == .connected else {
            completion(.failure(HelperClientError.unavailable))
            return
        }
        do {
            try sendRequest(command: .discoverLocalApps, payload: EmptyPayload()) { (result: Result<DiscoverLocalAppsResult, Error>) in
                completion(result.map(\.candidates))
            }
        } catch {
            completion(.failure(error))
            handleFailure(generation: processGeneration)
        }
    }

    func shutdown(completion: @escaping () -> Void) {
        if isShutdownComplete {
            completion()
            return
        }
        shutdownCompletions.append(completion)
        guard !isShuttingDown else { return }
        isShuttingDown = true
        availability = .shuttingDown
        handshakeTimeoutTask?.cancel()
        handshakeTimeoutTask = nil
        retryTask?.cancel()
        retryTask = nil
        stabilityTask?.cancel()
        stabilityTask = nil

        guard process?.isRunning == true else {
            finishShutdown()
            return
        }
        do {
            try sendWithoutResponse(command: .shutdown, requestID: requestIDProvider(), payload: EmptyPayload())
            process?.closeInput()
            shutdownTimeoutTask = scheduler.schedule(after: shutdownGraceInterval) { [weak self] in
                self?.forceShutdown()
            }
        } catch {
            process?.closeInput()
            process?.terminate()
            finishShutdown()
        }
    }

    private func receive(line: Data, generation: Int) {
        guard generation == processGeneration, !failureHandled else { return }
        guard !isShuttingDown else { return }
        guard let envelope = try? JSONDecoder().decode(IncomingHelperEnvelope.self, from: line),
              envelope.version == helperProtocolVersion
        else {
            handleFailure(generation: generation)
            return
        }
        if let requestID = envelope.requestId {
            if pendingResponses.removeValue(forKey: requestID)?(line) == false {
                handleFailure(generation: generation)
            }
            return
        }
        guard let event = envelope.event else {
            handleFailure(generation: generation)
            return
        }
        switch event {
        case .portalStatus:
            guard let message = try? JSONDecoder().decode(HelperEvent<PortalStatusPayload>.self, from: line),
                  message.event == .portalStatus
            else {
                handleFailure(generation: generation)
                return
            }
            onEvent?(.status(message.portalId, message.payload))
        case .authenticationURL:
            guard let message = try? JSONDecoder().decode(HelperEvent<AuthenticationURLPayload>.self, from: line),
                  message.event == .authenticationURL
            else {
                handleFailure(generation: generation)
                return
            }
            onEvent?(.authenticationURL(message.portalId, message.payload.url))
        }
    }

    private func sendRequest<Payload: Codable, Response: Codable>(
        command: HelperCommand,
        payload: Payload,
        completion: @escaping (Result<Response, Error>) -> Void
    ) throws {
        let requestID = requestIDProvider()
        pendingResponses[requestID] = { data in
            guard let data,
                  let response = try? JSONDecoder().decode(HelperResponse<Response>.self, from: data),
                  response.version == helperProtocolVersion,
                  response.requestId == requestID
            else {
                completion(.failure(HelperClientError.protocolFailure))
                return false
            }
            if let error = response.error {
                completion(.failure(HelperClientError.helper(error)))
            } else if let result = response.result {
                completion(.success(result))
            } else {
                completion(.failure(HelperClientError.protocolFailure))
                return false
            }
            return true
        }
        do {
            try sendWithoutResponse(command: command, requestID: requestID, payload: payload)
        } catch {
            pendingResponses.removeValue(forKey: requestID)
            throw error
        }
    }

    private func sendWithoutResponse<Payload: Codable>(command: HelperCommand, requestID: String, payload: Payload) throws {
        var data = try JSONEncoder().encode(
            HelperRequest(version: helperProtocolVersion, requestId: requestID, command: command, payload: payload)
        )
        data.append(0x0A)
        guard let process else { throw HelperClientError.unavailable }
        try process.send(data)
    }

    private func handleFailure(generation: Int) {
        guard generation == processGeneration, !failureHandled, !isShuttingDown else { return }
        failureHandled = true
        handshakeTimeoutTask?.cancel()
        handshakeTimeoutTask = nil
        stabilityTask?.cancel()
        stabilityTask = nil
        reconciliationGeneration += 1
        availability = .connecting
        failPendingResponses()
        guard let process else {
            scheduleRetry(generation: generation)
            return
        }
        process.closeInput()
        if process.isRunning {
            process.terminate()
        } else {
            self.process = nil
            scheduleRetry(generation: generation)
        }
    }

    private func failPendingResponses() {
        let pending = pendingResponses.values
        pendingResponses.removeAll()
        pending.forEach { _ = $0(nil) }
    }

    private func scheduleRetry(generation: Int) {
        guard generation == processGeneration, !isShuttingDown else { return }
        let delays: [TimeInterval] = [1, 2, 4, 8, 16]
        guard retryDelayIndex < delays.count else {
            availability = .failed
            return
        }
        let delay = delays[retryDelayIndex]
        retryDelayIndex += 1
        availability = .retrying(attempt: retryDelayIndex, delay: delay)
        retryTask = scheduler.schedule(after: delay) { [weak self] in
            guard let self, generation == self.processGeneration, !self.isShuttingDown else { return }
            self.retryTask = nil
            self.launch()
        }
    }

    private func processExited(generation: Int) {
        guard generation == processGeneration, process != nil else { return }
        process = nil
        if isShuttingDown {
            finishShutdown()
        } else if failureHandled {
            scheduleRetry(generation: generation)
        } else {
            handleFailure(generation: generation)
        }
    }

    private func finishShutdown() {
        guard !isShutdownComplete else { return }
        shutdownTimeoutTask?.cancel()
        shutdownTimeoutTask = nil
        isShutdownComplete = true
        let completions = shutdownCompletions
        shutdownCompletions.removeAll()
        completions.forEach { $0() }
    }

    private func forceShutdown() {
        guard isShuttingDown, !isShutdownComplete else { return }
        if process?.isRunning == true { process?.terminate() }
        finishShutdown()
    }

    nonisolated private static func defaultStateRootURL() -> URL {
        FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("Portico", isDirectory: true)
            .appendingPathComponent("tsnet", isDirectory: true)
    }
}

private struct IncomingHelperEnvelope: Decodable {
    let version: Int
    let requestId: String?
    let event: HelperEventType?
}
