import Foundation

enum HelperAvailability: Equatable {
    case awaitingLoggingChoice
    case restarting
    case connecting
    case retrying(attempt: Int, delay: TimeInterval)
    case connected
    case failed
    case requestDeadline
    case generationLost
    case protocolMismatch
    case ownershipFailure
    case shuttingDown
}

enum PortalHelperEvent: Equatable {
    case status(UUID, PortalStatusPayload, generation: Int)
    case authenticationURL(UUID, URL, generation: Int)
}

enum HelperClientError: Error {
    case unavailable
    case deadline
    case generationLost
    case protocolFailure
    case ownershipFailure
    case helper(HelperProtocolError)
}

@MainActor
protocol PortalHelperClient: AnyObject {
    var availability: HelperAvailability { get }
    var generation: Int { get }
    var onConnected: (() -> Void)? { get set }
    var onAvailabilityChange: ((HelperAvailability) -> Void)? { get set }
    var onEvent: ((PortalHelperEvent) -> Void)? { get set }

    func retry()
    func restart(loggingPreference: OperationalLoggingPreference)

    func reconcilePortals(
        _ portals: [PortalConfiguration],
        completion: @escaping (Result<ReconcilePortalsResult, Error>) -> Void
    )
    func authenticatePortal(id: UUID, completion: @escaping (Result<Void, Error>) -> Void)
    func cleanupRejectedPortal(id: UUID, completion: @escaping (Result<Void, Error>) -> Void)
    func removePortal(id: UUID, completion: @escaping (Result<Void, Error>) -> Void)
    func discoverLocalApps(completion: @escaping (Result<[LocalAppCandidatePayload], Error>) -> Void)
}

protocol HelperProcess: AnyObject {
    var isRunning: Bool { get }

    func send(_ data: Data, completion: @escaping (Result<Void, Error>) -> Void)
    func closeInput()
    func terminate()
    func kill()
}

protocol HelperLaunching {
    func launch(
        at executableURL: URL,
        arguments: [String],
        loggingPreference: OperationalLoggingPreference,
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
    var generation: Int { processGeneration }

    private let helperURL: URL
    private let stateRootURL: URL
    private let launcher: HelperLaunching
    private let requestIDProvider: () -> String
    private let scheduler: PorticoScheduling
    private let history: DiagnosticHistory?
    private let handshakeTimeout: TimeInterval
    private let shutdownGraceInterval: TimeInterval
    private let shutdownTerminationInterval: TimeInterval
    private let shutdownKillObservationInterval: TimeInterval
    private var process: HelperProcess?
    private var pendingResponses: [String: PendingResponse] = [:]
    private var queuedOrdinaryRequests: [QueuedRequest] = []
    private var activeOrdinaryRequestID: String?
    private var isCompletingOrdinaryRequest = false
    private var shutdownTimeoutTask: ScheduledTask?
    private var retryTask: ScheduledTask?
    private var stabilityTask: ScheduledTask?
    private var restartTimeoutTask: ScheduledTask?
    private var failureTerminationTask: ScheduledTask?
    private var processGeneration = 0
    private var lastReconciliationCount = 0
    private var reconciliationGeneration = 0
    private var retryDelayIndex = 0
    private var failureHandled = false
    private var permanentProtocolMismatch = false
    private var isShuttingDown = false
    private var isShutdownComplete = false
    private var shutdownCompletions: [() -> Void] = []
    private var loggingPreference: OperationalLoggingPreference = .undecided
    private var restartingGeneration: Int?
    private var terminalOwnershipFailure = false

    init(
        helperURL: URL,
        stateRootURL: URL = HelperSupervisor.defaultStateRootURL(),
        launcher: HelperLaunching,
        requestIDProvider: @escaping () -> String = { UUID().uuidString },
        scheduler: PorticoScheduling? = nil,
        history: DiagnosticHistory? = nil,
        handshakeTimeout: TimeInterval = 3,
        shutdownGraceInterval: TimeInterval = 5,
        shutdownTerminationInterval: TimeInterval = 2,
        shutdownKillObservationInterval: TimeInterval = 1
    ) {
        self.helperURL = helperURL
        self.stateRootURL = stateRootURL
        self.launcher = launcher
        self.requestIDProvider = requestIDProvider
        self.scheduler = scheduler ?? MainQueueScheduler()
        self.history = history
        self.handshakeTimeout = handshakeTimeout
        self.shutdownGraceInterval = shutdownGraceInterval
        self.shutdownTerminationInterval = shutdownTerminationInterval
        self.shutdownKillObservationInterval = shutdownKillObservationInterval
    }

    func start(loggingPreference: OperationalLoggingPreference) {
        guard process == nil,
              retryTask == nil,
              !isShuttingDown,
              !terminalOwnershipFailure,
              !permanentProtocolMismatch
        else { return }
        self.loggingPreference = loggingPreference
        guard loggingPreference != .undecided else {
            availability = .awaitingLoggingChoice
            return
        }
        launch()
    }

    func retry() {
        guard availability == .failed, !isShuttingDown, process == nil else { return }
        retryTask?.cancel()
        retryTask = nil
        stabilityTask?.cancel()
        stabilityTask = nil
        processGeneration += 1
        reconciliationGeneration += 1
        failPendingResponses(with: HelperClientError.generationLost)
        retryDelayIndex = 0
        failureHandled = false
        availability = .connecting
        launch()
    }

    func restart(loggingPreference: OperationalLoggingPreference) {
        guard loggingPreference != .undecided,
              loggingPreference != self.loggingPreference,
              !isShuttingDown,
              !terminalOwnershipFailure,
              !permanentProtocolMismatch
        else { return }
        self.loggingPreference = loggingPreference
        retryTask?.cancel()
        retryTask = nil
        stabilityTask?.cancel()
        stabilityTask = nil
        restartTimeoutTask?.cancel()
        restartTimeoutTask = nil
        reconciliationGeneration += 1
        availability = .restarting
        failPendingResponses(with: HelperClientError.generationLost)
        retryDelayIndex = 0
        failureHandled = false

        guard let process else {
            launch()
            return
        }
        let generation = processGeneration
        restartingGeneration = generation
        do {
            try sendWithoutResponse(
                command: .shutdown,
                requestID: requestIDProvider(),
                payload: EmptyPayload()
            )
        } catch {
            process.terminate()
        }
        process.closeInput()
        restartTimeoutTask = scheduler.schedule(after: shutdownGraceInterval) { [weak self] in
            self?.forceRestart(generation: generation)
        }
    }

    private func launch() {
        guard process == nil,
              !isShuttingDown,
              !terminalOwnershipFailure,
              !permanentProtocolMismatch
        else { return }
        processGeneration += 1
        let generation = processGeneration
        lastReconciliationCount = 0
        failureHandled = false
        availability = .connecting
        do {
            process = try launcher.launch(
                at: helperURL,
                arguments: ["--state-root", stateRootURL.path],
                loggingPreference: loggingPreference,
                onLine: { [weak self] data in self?.receive(line: data, generation: generation) },
                onEOF: { [weak self] in self?.handleFailure(generation: generation) },
                onExit: { [weak self] _ in self?.processExited(generation: generation) }
            )
            try sendRequest(
                command: .handshake,
                payload: EmptyPayload(),
                deadline: handshakeTimeout,
                dispatch: .control
            ) { [weak self] (result: Result<HandshakeResult, Error>) in
                guard let self else { return }
                guard generation == self.processGeneration, !self.failureHandled else { return }
                guard case let .success(handshake) = result else {
                    self.handleFailure(generation: generation)
                    return
                }
                guard handshake.protocolVersion == helperProtocolVersion else {
                    self.handleProtocolMismatch(generation: generation)
                    return
                }
                self.availability = .connected
                self.onConnected?()
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
                        destination: $0.destination,
                        desiredState: $0.desiredState
                    )
                }
        )
        reconciliationGeneration += 1
        let requestedReconciliation = reconciliationGeneration
        let requestedProcess = processGeneration
        let requestCount = max(portals.count, lastReconciliationCount)
        lastReconciliationCount = portals.count
        stabilityTask?.cancel()
        stabilityTask = nil
        do {
            try sendRequest(
                command: .reconcilePortals,
                payload: payload,
                deadline: 10 + 10 * TimeInterval(requestCount)
            ) { [weak self] result in
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
            try sendRequest(command: .authenticatePortal, payload: AuthenticatePortalPayload(portalId: id), deadline: 5) { (result: Result<AuthenticatePortalResult, Error>) in
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
            try sendRequest(command: .cleanupRejectedPortal, payload: CleanupRejectedPortalPayload(portalId: id), deadline: 15) { (result: Result<CleanupRejectedPortalResult, Error>) in
                completion(result.map { _ in () })
            }
        } catch {
            completion(.failure(error))
            handleFailure(generation: processGeneration)
        }
    }

    func removePortal(id: UUID, completion: @escaping (Result<Void, Error>) -> Void) {
        guard availability == .connected else {
            completion(.failure(HelperClientError.unavailable))
            return
        }
        do {
            try sendRequest(command: .removePortal, payload: RemovePortalPayload(portalId: id), deadline: 15) { (result: Result<RemovePortalResult, Error>) in
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
            try sendRequest(command: .discoverLocalApps, payload: EmptyPayload(), deadline: 5) { (result: Result<DiscoverLocalAppsResult, Error>) in
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
        if terminalOwnershipFailure {
            finishShutdown()
            return
        }
        guard !isShuttingDown else { return }
        isShuttingDown = true
        availability = .shuttingDown
        retryTask?.cancel()
        retryTask = nil
        stabilityTask?.cancel()
        stabilityTask = nil
        failPendingResponses(with: HelperClientError.generationLost)

        guard process != nil else {
            finishShutdown()
            return
        }
        _ = try? sendWithoutResponse(command: .shutdown, requestID: requestIDProvider(), payload: EmptyPayload())
        process?.closeInput()
        shutdownTimeoutTask = scheduler.schedule(after: shutdownGraceInterval) { [weak self] in
            self?.forceShutdown()
        }
    }

    private func receive(line: Data, generation: Int) {
        guard generation == processGeneration,
              restartingGeneration == nil,
              !failureHandled
        else { return }
        guard !isShuttingDown else { return }
        guard let envelope = try? JSONDecoder().decode(IncomingHelperEnvelope.self, from: line) else {
            handleFailure(generation: generation, pendingError: HelperClientError.protocolFailure)
            return
        }
        guard envelope.version == helperProtocolVersion else {
            handleProtocolMismatch(generation: generation)
            return
        }
        if let requestID = envelope.requestId {
            if let pending = pendingResponses.removeValue(forKey: requestID) {
                pending.deadlineTask?.cancel()
                let completesOrdinaryRequest = activeOrdinaryRequestID == requestID
                if completesOrdinaryRequest {
                    activeOrdinaryRequestID = nil
                    isCompletingOrdinaryRequest = true
                }
                let consumed = pending.consume(line)
                if completesOrdinaryRequest {
                    isCompletingOrdinaryRequest = false
                }
                if !consumed {
                    handleFailure(generation: generation)
                    pending.fail(HelperClientError.protocolFailure)
                } else if completesOrdinaryRequest {
                    dispatchNextOrdinaryRequest()
                }
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
            onEvent?(.status(message.portalId, message.payload, generation: generation))
        case .authenticationURL:
            guard let message = try? JSONDecoder().decode(HelperEvent<AuthenticationURLPayload>.self, from: line),
                  message.event == .authenticationURL
            else {
                handleFailure(generation: generation)
                return
            }
            onEvent?(.authenticationURL(message.portalId, message.payload.url, generation: generation))
        }
    }

    private func sendRequest<Payload: Codable, Response: Codable>(
        command: HelperCommand,
        payload: Payload,
        deadline: TimeInterval? = nil,
        dispatch: RequestDispatch = .ordinary,
        completion: @escaping (Result<Response, Error>) -> Void
    ) throws {
        let requestID = requestIDProvider()
        var data = try JSONEncoder().encode(
            HelperRequest(version: helperProtocolVersion, requestId: requestID, command: command, payload: payload)
        )
        data.append(0x0A)
        guard process != nil else { throw HelperClientError.unavailable }
        let pending = PendingResponse(
            consume: { data in
                guard let response = try? JSONDecoder().decode(HelperResponse<Response>.self, from: data),
                  response.version == helperProtocolVersion,
                  response.requestId == requestID
                else {
                    return false
                }
                if let error = response.error {
                    completion(.failure(HelperClientError.helper(error)))
                } else if let result = response.result {
                    completion(.success(result))
                } else {
                    return false
                }
                return true
            },
            fail: { error in completion(.failure(error)) }
        )
        let generation = processGeneration
        switch dispatch {
        case .control:
            dispatchRequest(
                requestID: requestID,
                data: data,
                deadline: deadline,
                generation: generation,
                pending: pending
            )
        case .ordinary:
            queuedOrdinaryRequests.append(
                QueuedRequest(
                    requestID: requestID,
                    data: data,
                    deadline: deadline,
                    generation: generation,
                    pending: pending
                )
            )
            dispatchNextOrdinaryRequest()
        }
    }

    private func dispatchNextOrdinaryRequest() {
        guard activeOrdinaryRequestID == nil,
              !isCompletingOrdinaryRequest,
              availability == .connected,
              !failureHandled,
              !isShuttingDown,
              restartingGeneration == nil
        else { return }
        while !queuedOrdinaryRequests.isEmpty {
            let request = queuedOrdinaryRequests.removeFirst()
            guard request.generation == processGeneration else {
                request.pending.fail(HelperClientError.generationLost)
                continue
            }
            activeOrdinaryRequestID = request.requestID
            dispatchRequest(
                requestID: request.requestID,
                data: request.data,
                deadline: request.deadline,
                generation: request.generation,
                pending: request.pending
            )
            return
        }
    }

    private func dispatchRequest(
        requestID: String,
        data: Data,
        deadline: TimeInterval?,
        generation: Int,
        pending: PendingResponse
    ) {
        pendingResponses[requestID] = pending
        if let deadline {
            pending.deadlineTask = scheduler.schedule(after: deadline) { [weak self] in
                self?.requestTimedOut(requestID: requestID, generation: generation)
            }
        }
        guard let process else {
            pendingResponses.removeValue(forKey: requestID)?.deadlineTask?.cancel()
            if activeOrdinaryRequestID == requestID {
                activeOrdinaryRequestID = nil
            }
            handleFailure(generation: generation)
            pending.fail(HelperClientError.generationLost)
            return
        }
        process.send(data) { [weak self] result in
            guard case .failure = result else { return }
            self?.requestWriteFailed(requestID: requestID, generation: generation)
        }
    }

    private func requestWriteFailed(requestID: String, generation: Int) {
        guard generation == processGeneration else { return }
        let failedPending = pendingResponses.removeValue(forKey: requestID)
        failedPending?.deadlineTask?.cancel()
        if activeOrdinaryRequestID == requestID {
            activeOrdinaryRequestID = nil
        }
        handleFailure(generation: generation)
        failedPending?.fail(HelperClientError.generationLost)
    }

    private func requestTimedOut(requestID: String, generation: Int) {
        guard generation == processGeneration,
              let pending = pendingResponses.removeValue(forKey: requestID)
        else { return }
        handleFailure(
            generation: generation,
            pendingError: HelperClientError.generationLost,
            availability: .requestDeadline
        )
        pending.fail(HelperClientError.deadline)
    }

    private func sendWithoutResponse<Payload: Codable>(
        command: HelperCommand,
        requestID: String,
        payload: Payload,
        onWriteFailure: @escaping () -> Void = {}
    ) throws {
        var data = try JSONEncoder().encode(
            HelperRequest(version: helperProtocolVersion, requestId: requestID, command: command, payload: payload)
        )
        data.append(0x0A)
        guard let process else { throw HelperClientError.unavailable }
        process.send(data) { result in
            if case .failure = result {
                onWriteFailure()
            }
        }
    }

    private func handleFailure(
        generation: Int,
        pendingError: Error = HelperClientError.generationLost,
        availability failureAvailability: HelperAvailability = .generationLost
    ) {
        guard generation == processGeneration,
              restartingGeneration == nil,
              !failureHandled,
              !isShuttingDown
        else { return }
        failureHandled = true
        stabilityTask?.cancel()
        stabilityTask = nil
        reconciliationGeneration += 1
        availability = failureAvailability
        failPendingResponses(with: pendingError)
        guard let process else {
            scheduleRetry(generation: generation)
            return
        }
        process.closeInput()
        process.terminate()
        failureTerminationTask = scheduler.schedule(after: shutdownTerminationInterval) { [weak self] in
            self?.killFailedProcess(generation: generation)
        }
    }

    private func handleProtocolMismatch(generation: Int) {
        guard generation == processGeneration,
              restartingGeneration == nil,
              !failureHandled,
              !isShuttingDown
        else { return }
        permanentProtocolMismatch = true
        failureHandled = true
        stabilityTask?.cancel()
        stabilityTask = nil
        reconciliationGeneration += 1
        availability = .protocolMismatch
        failPendingResponses(with: HelperClientError.protocolFailure)
        guard let process else { return }
        process.closeInput()
        process.terminate()
        failureTerminationTask = scheduler.schedule(after: shutdownTerminationInterval) { [weak self] in
            self?.killFailedProcess(generation: generation)
        }
    }

    private func failPendingResponses(with error: Error = HelperClientError.protocolFailure) {
        let pending = pendingResponses.values
        pendingResponses.removeAll()
        activeOrdinaryRequestID = nil
        let queued = queuedOrdinaryRequests
        queuedOrdinaryRequests.removeAll()
        pending.forEach {
            $0.deadlineTask?.cancel()
            $0.fail(error)
        }
        queued.forEach { $0.pending.fail(error) }
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
        if restartingGeneration == generation {
            process = nil
            restartTimeoutTask?.cancel()
            restartTimeoutTask = nil
            restartingGeneration = nil
            if isShuttingDown {
                finishShutdown()
                return
            }
            guard !terminalOwnershipFailure else { return }
            launch()
            return
        }
        guard generation == processGeneration, process != nil else { return }
        process = nil
        failureTerminationTask?.cancel()
        failureTerminationTask = nil
        if isShuttingDown {
            finishShutdown()
        } else if terminalOwnershipFailure {
            return
        } else if permanentProtocolMismatch {
            return
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
        process?.terminate()
        shutdownTimeoutTask = scheduler.schedule(after: shutdownTerminationInterval) { [weak self] in
            self?.killShutdownProcess()
        }
    }

    private func killShutdownProcess() {
        guard isShuttingDown, !isShutdownComplete else { return }
        process?.kill()
        shutdownTimeoutTask = scheduler.schedule(after: shutdownKillObservationInterval) { [weak self] in
            self?.recordTerminalOwnershipFailure()
        }
    }

    private func forceRestart(generation: Int) {
        guard restartingGeneration == generation else { return }
        process?.terminate()
        restartTimeoutTask = scheduler.schedule(after: shutdownTerminationInterval) { [weak self] in
            self?.killRestartProcess(generation: generation)
        }
    }

    private func killRestartProcess(generation: Int) {
        guard restartingGeneration == generation else { return }
        process?.kill()
        restartTimeoutTask = scheduler.schedule(after: shutdownKillObservationInterval) { [weak self] in
            guard self?.restartingGeneration == generation else { return }
            self?.recordTerminalOwnershipFailure()
        }
    }

    private func killFailedProcess(generation: Int) {
        guard generation == processGeneration,
              failureHandled,
              !isShuttingDown,
              restartingGeneration == nil,
              !terminalOwnershipFailure
        else { return }
        process?.kill()
        failureTerminationTask = scheduler.schedule(after: shutdownKillObservationInterval) { [weak self] in
            guard let self,
                  generation == self.processGeneration,
                  self.failureHandled,
                  !self.isShuttingDown,
                  self.restartingGeneration == nil
            else { return }
            self.recordTerminalOwnershipFailure()
        }
    }

    private func recordTerminalOwnershipFailure() {
        guard !terminalOwnershipFailure, process?.isRunning == true else { return }
        terminalOwnershipFailure = true
        failureHandled = true
        stabilityTask?.cancel()
        stabilityTask = nil
        retryTask?.cancel()
        retryTask = nil
        failureTerminationTask?.cancel()
        failureTerminationTask = nil
        failPendingResponses(with: HelperClientError.ownershipFailure)
        availability = .ownershipFailure
        if isShuttingDown {
            finishShutdown()
        }
    }

    nonisolated private static func defaultStateRootURL() -> URL {
        FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("Portico", isDirectory: true)
            .appendingPathComponent("tsnet", isDirectory: true)
    }
}

private final class PendingResponse {
    var deadlineTask: ScheduledTask?
    let consume: (Data) -> Bool
    let fail: (Error) -> Void

    init(consume: @escaping (Data) -> Bool, fail: @escaping (Error) -> Void) {
        self.consume = consume
        self.fail = fail
    }
}

private enum RequestDispatch {
    case control
    case ordinary
}

private struct QueuedRequest {
    let requestID: String
    let data: Data
    let deadline: TimeInterval?
    let generation: Int
    let pending: PendingResponse
}

private struct IncomingHelperEnvelope: Decodable {
    let version: Int
    let requestId: String?
    let event: HelperEventType?
}
