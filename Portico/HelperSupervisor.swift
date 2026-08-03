import Foundation

enum HelperAvailability: Equatable {
    case connecting
    case connected
    case failed
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
        onLine: @escaping (Data) -> Void,
        onEOF: @escaping () -> Void,
        onExit: @escaping (Int32) -> Void
    ) throws -> HelperProcess
}

final class HelperSupervisor: ObservableObject {
    @Published private(set) var availability: HelperAvailability = .connecting

    private let helperURL: URL
    private let launcher: HelperLaunching
    private let requestIDProvider: () -> String
    private let handshakeTimeout: TimeInterval
    private let shutdownGraceInterval: TimeInterval
    private var process: HelperProcess?
    private var handshakeRequestID: String?
    private var handshakeTimeoutWorkItem: DispatchWorkItem?
    private var shutdownTimeoutWorkItem: DispatchWorkItem?
    private var isShuttingDown = false
    private var isShutdownComplete = false
    private var shutdownCompletions: [() -> Void] = []

    init(
        helperURL: URL,
        launcher: HelperLaunching,
        requestIDProvider: @escaping () -> String = { UUID().uuidString },
        handshakeTimeout: TimeInterval = 3,
        shutdownGraceInterval: TimeInterval = 1
    ) {
        self.helperURL = helperURL
        self.launcher = launcher
        self.requestIDProvider = requestIDProvider
        self.handshakeTimeout = handshakeTimeout
        self.shutdownGraceInterval = shutdownGraceInterval
    }

    func start() {
        availability = .connecting
        let requestID = requestIDProvider()
        handshakeRequestID = requestID

        do {
            process = try launcher.launch(
                at: helperURL,
                onLine: { [weak self] data in self?.receive(line: data) },
                onEOF: { [weak self] in self?.processExited() },
                onExit: { [weak self] _ in self?.processExited() }
            )
            try send(command: .handshake, requestID: requestID)
            let workItem = DispatchWorkItem { [weak self] in self?.fail() }
            handshakeTimeoutWorkItem = workItem
            DispatchQueue.main.asyncAfter(deadline: .now() + handshakeTimeout, execute: workItem)
        } catch {
            fail()
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
            try send(command: .shutdown, requestID: requestIDProvider())
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
        guard availability == .connecting else { return }
        guard let response = try? JSONDecoder().decode(HelperResponse<HandshakeResult>.self, from: line) else {
            fail()
            return
        }
        guard response.requestId == handshakeRequestID else { return }
        guard response.version == helperProtocolVersion,
              response.error == nil,
              response.result?.protocolVersion == helperProtocolVersion
        else {
            fail()
            return
        }

        handshakeTimeoutWorkItem?.cancel()
        handshakeTimeoutWorkItem = nil
        availability = .connected
    }

    private func send(command: HelperCommand, requestID: String) throws {
        var data = try JSONEncoder().encode(
            HelperRequest(
                version: helperProtocolVersion,
                requestId: requestID,
                command: command,
                payload: EmptyPayload()
            )
        )
        data.append(0x0A)
        try process?.send(data)
    }

    private func fail() {
        guard availability != .failed else { return }

        handshakeTimeoutWorkItem?.cancel()
        handshakeTimeoutWorkItem = nil
        availability = .failed
        process?.closeInput()
        if process?.isRunning == true {
            process?.terminate()
        }
    }

    private func processExited() {
        if isShuttingDown {
            finishShutdown()
        } else {
            fail()
        }
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
        if process?.isRunning == true {
            process?.terminate()
        }
        finishShutdown()
    }
}
