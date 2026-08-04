import Foundation

enum HelperAvailability: Equatable {
    case connecting
    case connected
    case failed
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
    var onEvent: ((PortalHelperEvent) -> Void)? { get set }

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
    @Published private(set) var availability: HelperAvailability = .connecting
    var onConnected: (() -> Void)?
    var onEvent: ((PortalHelperEvent) -> Void)?

    private let helperURL: URL
    private let stateRootURL: URL
    private let launcher: HelperLaunching
    private let requestIDProvider: () -> String
    private let handshakeTimeout: TimeInterval
    private let shutdownGraceInterval: TimeInterval
    private var process: HelperProcess?
    private var pendingResponses: [String: (Data?) -> Void] = [:]
    private var handshakeTimeoutWorkItem: DispatchWorkItem?
    private var shutdownTimeoutWorkItem: DispatchWorkItem?
    private var isShuttingDown = false
    private var isShutdownComplete = false
    private var shutdownCompletions: [() -> Void] = []

    init(
        helperURL: URL,
        stateRootURL: URL = HelperSupervisor.defaultStateRootURL(),
        launcher: HelperLaunching,
        requestIDProvider: @escaping () -> String = { UUID().uuidString },
        handshakeTimeout: TimeInterval = 3,
        shutdownGraceInterval: TimeInterval = 1
    ) {
        self.helperURL = helperURL
        self.stateRootURL = stateRootURL
        self.launcher = launcher
        self.requestIDProvider = requestIDProvider
        self.handshakeTimeout = handshakeTimeout
        self.shutdownGraceInterval = shutdownGraceInterval
    }

    func start() {
        availability = .connecting
        do {
            process = try launcher.launch(
                at: helperURL,
                arguments: ["--state-root", stateRootURL.path],
                onLine: { [weak self] data in self?.receive(line: data) },
                onEOF: { [weak self] in self?.processExited() },
                onExit: { [weak self] _ in self?.processExited() }
            )
            try sendRequest(command: .handshake, payload: EmptyPayload()) { [weak self] (result: Result<HandshakeResult, Error>) in
                guard let self else { return }
                guard case let .success(handshake) = result,
                      handshake.protocolVersion == helperProtocolVersion
                else {
                    self.fail()
                    return
                }
                self.handshakeTimeoutWorkItem?.cancel()
                self.handshakeTimeoutWorkItem = nil
                self.availability = .connected
                self.onConnected?()
            }
            let workItem = DispatchWorkItem { [weak self] in self?.fail() }
            handshakeTimeoutWorkItem = workItem
            DispatchQueue.main.asyncAfter(deadline: .now() + handshakeTimeout, execute: workItem)
        } catch {
            fail()
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
        do {
            try sendRequest(command: .reconcilePortals, payload: payload, completion: completion)
        } catch {
            completion(.failure(error))
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
        handshakeTimeoutWorkItem?.cancel()
        handshakeTimeoutWorkItem = nil

        guard process?.isRunning == true else {
            finishShutdown()
            return
        }
        do {
            try sendWithoutResponse(command: .shutdown, requestID: requestIDProvider(), payload: EmptyPayload())
            process?.closeInput()
            let workItem = DispatchWorkItem { [weak self] in self?.forceShutdown() }
            shutdownTimeoutWorkItem = workItem
            DispatchQueue.main.asyncAfter(deadline: .now() + shutdownGraceInterval, execute: workItem)
        } catch {
            process?.closeInput()
            process?.terminate()
            finishShutdown()
        }
    }

    private func receive(line: Data) {
        guard !isShuttingDown else { return }
        guard let envelope = try? JSONDecoder().decode(IncomingHelperEnvelope.self, from: line),
              envelope.version == helperProtocolVersion
        else {
            fail()
            return
        }
        if let requestID = envelope.requestId {
            pendingResponses.removeValue(forKey: requestID)?(line)
            return
        }
        guard let event = envelope.event else {
            fail()
            return
        }
        switch event {
        case .portalStatus:
            guard let message = try? JSONDecoder().decode(HelperEvent<PortalStatusPayload>.self, from: line),
                  message.event == .portalStatus
            else {
                fail()
                return
            }
            onEvent?(.status(message.portalId, message.payload))
        case .authenticationURL:
            guard let message = try? JSONDecoder().decode(HelperEvent<AuthenticationURLPayload>.self, from: line),
                  message.event == .authenticationURL
            else {
                fail()
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
                return
            }
            if let error = response.error {
                completion(.failure(HelperClientError.helper(error)))
            } else if let result = response.result {
                completion(.success(result))
            } else {
                completion(.failure(HelperClientError.protocolFailure))
            }
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

    private func fail() {
        guard availability != .failed else { return }
        handshakeTimeoutWorkItem?.cancel()
        handshakeTimeoutWorkItem = nil
        availability = .failed
        let pending = pendingResponses.values
        pendingResponses.removeAll()
        pending.forEach { $0(nil) }
        process?.closeInput()
        if process?.isRunning == true {
            process?.terminate()
        }
    }

    private func processExited() {
        if isShuttingDown { finishShutdown() } else { fail() }
    }

    private func finishShutdown() {
        guard !isShutdownComplete else { return }
        shutdownTimeoutWorkItem?.cancel()
        shutdownTimeoutWorkItem = nil
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
