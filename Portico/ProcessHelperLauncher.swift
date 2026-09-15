import Foundation
import Darwin

enum ProcessHelperLauncherError: Error {
    case loggingChoiceRequired
}

enum JSONLineBufferError: Error, Equatable {
    case frameTooLarge
    case unterminatedFrame
}

final class ProcessHelperLauncher: HelperLaunching {
    static func childEnvironment(
        for preference: OperationalLoggingPreference,
        inherited: [String: String]
    ) throws -> [String: String] {
        var environment = inherited
        switch preference {
        case .undecided:
            throw ProcessHelperLauncherError.loggingChoiceRequired
        case .enabled:
            environment.removeValue(forKey: "TS_NO_LOGS_NO_SUPPORT")
        case .disabled:
            environment["TS_NO_LOGS_NO_SUPPORT"] = "true"
        }
        return environment
    }

    func launch(
        at executableURL: URL,
        arguments: [String],
        loggingPreference: OperationalLoggingPreference,
        onLine: @escaping (Data) -> Void,
        onEOF: @escaping () -> Void,
        onExit: @escaping (Int32) -> Void
    ) throws -> HelperProcess {
        let process = Process()
        let input = Pipe()
        let output = Pipe()
        let diagnostics = Pipe()
        let outputCoordinator = ProcessOutputCoordinator(
            onLine: onLine,
            onEOF: onEOF,
            onExit: onExit
        )
        let parserQueue = DispatchQueue(label: "dev.chrisbanes.portico.helper.stdout")
        let outputDescriptor = output.fileHandleForReading.fileDescriptor
        let drainAfterExit: (Int32) -> Void = { status in
            diagnostics.fileHandleForReading.readabilityHandler = nil
            parserQueue.async {
                output.fileHandleForReading.readabilityHandler = nil
                while true {
                    switch readOutputChunk(from: outputDescriptor) {
                    case .data(let remaining): outputCoordinator.receive(remaining)
                    case .eof, .unavailable, .failure:
                        outputCoordinator.receiveEOF()
                        outputCoordinator.receiveExit(status)
                        return
                    }
                }
            }
        }

        process.executableURL = executableURL
        process.arguments = arguments
        process.environment = try Self.childEnvironment(
            for: loggingPreference,
            inherited: ProcessInfo.processInfo.environment
        )
        process.standardInput = input
        process.standardOutput = output
        process.standardError = diagnostics
        guard fcntl(input.fileHandleForWriting.fileDescriptor, F_SETNOSIGPIPE, 1) == 0 else {
            throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)
        }
        let outputFlags = fcntl(outputDescriptor, F_GETFL)
        guard outputFlags >= 0,
              fcntl(outputDescriptor, F_SETFL, outputFlags | O_NONBLOCK) == 0
        else {
            throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)
        }

        output.fileHandleForReading.readabilityHandler = { handle in
            parserQueue.sync {
                switch readOutputChunk(from: handle.fileDescriptor) {
                case .data(let data): outputCoordinator.receive(data)
                case .eof, .failure:
                    handle.readabilityHandler = nil
                    outputCoordinator.receiveEOF()
                case .unavailable: break
                }
            }
        }
        diagnostics.fileHandleForReading.readabilityHandler = { handle in
            if handle.availableData.isEmpty {
                handle.readabilityHandler = nil
            }
        }
        process.terminationHandler = { process in
            drainAfterExit(process.terminationStatus)
        }
        do {
            try process.run()
            try? input.fileHandleForReading.close()
            try? output.fileHandleForWriting.close()
            try? diagnostics.fileHandleForWriting.close()
            return FoundationHelperProcess(
                process: process,
                input: input.fileHandleForWriting,
                outputCoordinator: outputCoordinator
            )
        } catch {
            output.fileHandleForReading.readabilityHandler = nil
            diagnostics.fileHandleForReading.readabilityHandler = nil
            throw error
        }

    }
}

private enum OutputReadResult { case data(Data), eof, unavailable, failure }

private func readOutputChunk(from descriptor: Int32) -> OutputReadResult {
    var bytes = [UInt8](repeating: 0, count: 64 * 1024)
    let count = bytes.withUnsafeMutableBytes { Darwin.read(descriptor, $0.baseAddress, $0.count) }
    if count > 0 { return .data(Data(bytes.prefix(Int(count)))) }
    if count == 0 { return .eof }
    if errno == EAGAIN || errno == EWOULDBLOCK { return .unavailable }
    return .failure
}

private final class FoundationHelperProcess: HelperProcess {
    private let process: Process
    private let input: FileHandle
    private let outputCoordinator: ProcessOutputCoordinator
    private let writerQueue = DispatchQueue(label: "dev.chrisbanes.portico.helper.stdin")
    private var inputClosed = false

    init(process: Process, input: FileHandle, outputCoordinator: ProcessOutputCoordinator) {
        self.process = process
        self.input = input
        self.outputCoordinator = outputCoordinator
    }

    var isRunning: Bool { process.isRunning }

    func send(_ data: Data, completion: @escaping (Result<Void, Error>) -> Void) {
        writerQueue.async { [weak self] in
            let result: Result<Void, Error>
            guard let self, !self.inputClosed else {
                DispatchQueue.main.async { completion(.failure(HelperProcessWriteError.inputClosed)) }
                return
            }
            do {
                try self.input.write(contentsOf: data)
                result = .success(())
            } catch {
                result = .failure(error)
            }
            DispatchQueue.main.async { completion(result) }
        }
    }

    func closeInput() {
        writerQueue.async { [weak self] in
            guard let self, !self.inputClosed else { return }
            self.inputClosed = true
            try? self.input.close()
        }
    }

    func terminate() {
        if process.isRunning {
            process.terminate()
        }
    }

    func kill() {
        guard process.isRunning else { return }
        Darwin.kill(process.processIdentifier, SIGKILL)
    }

}

private enum HelperProcessWriteError: Error {
    case inputClosed
}

final class JSONLineBuffer {
    private let lock = NSLock()
    private let maximumFrameBytes: Int
    private var buffer = Data()

    init(maximumFrameBytes: Int = 256 * 1024) {
        self.maximumFrameBytes = maximumFrameBytes
    }

    func append(_ data: Data) -> JSONLineBufferAppendResult {
        lock.lock()
        defer { lock.unlock() }

        buffer.append(data)
        var lines: [Data] = []
        while let newline = buffer.firstIndex(of: 0x0A) {
            var line = Data(buffer[..<newline])
            buffer.removeSubrange(...newline)
            if line.last == 0x0D {
                line.removeLast()
            }
            guard line.count <= maximumFrameBytes else {
                buffer.removeAll()
                return .failure(lines, .frameTooLarge)
            }
            lines.append(line)
        }

        let unterminatedFrameBytes = buffer.last == 0x0D ? buffer.count - 1 : buffer.count
        guard unterminatedFrameBytes <= maximumFrameBytes else {
            buffer.removeAll()
            return .failure(lines, .frameTooLarge)
        }
        return .lines(lines)
    }

    func finish() throws {
        lock.lock()
        defer { lock.unlock() }

        guard !buffer.isEmpty else { return }
        buffer.removeAll()
        throw JSONLineBufferError.unterminatedFrame
    }
}

enum JSONLineBufferAppendResult {
    case lines([Data])
    case failure([Data], JSONLineBufferError)
}

private final class ProcessOutputCoordinator {
    private let buffer = JSONLineBuffer()
    private let delivery: FrameDeliveryQueue
    private let onEOF: () -> Void
    private let onExit: (Int32) -> Void
    private var finished = false
    private var exitStatus: Int32?

    init(
        onLine: @escaping (Data) -> Void,
        onEOF: @escaping () -> Void,
        onExit: @escaping (Int32) -> Void
    ) {
        delivery = FrameDeliveryQueue(deliver: onLine)
        self.onEOF = onEOF
        self.onExit = onExit
    }

    func receive(_ data: Data) {
        guard !finished else { return }
        let result = buffer.append(data)
        guard delivery.append(result.frames) else {
            finish()
            return
        }
        if result.error != nil {
            finish()
        }
    }

    func receiveEOF() {
        guard !finished else { return }
        _ = try? buffer.finish()
        finish()
    }

    func receiveExit(_ status: Int32) {
        guard exitStatus == nil else { return }
        exitStatus = status
        finishIfPossible()
    }

    private func finish() {
        guard !finished else { return }
        finished = true
        delivery.appendTerminal(onEOF)
        finishIfPossible()
    }

    private func finishIfPossible() {
        guard finished, let exitStatus else { return }
        delivery.appendTerminal { [onExit] in onExit(exitStatus) }
    }
}

final class FrameDeliveryQueue {
    private let lock = NSLock()
    private let maximumFrames: Int
    private let maximumBytes: Int
    private let deliver: (Data) -> Void
    private let scheduleOnMain: (@escaping () -> Void) -> Void
    private var frames: [Data] = []
    private var bytes = 0
    private var terminalCallbacks: [() -> Void] = []
    private var drainScheduled = false

    init(
        maximumFrames: Int = 16,
        maximumBytes: Int = 512 * 1024,
        deliver: @escaping (Data) -> Void,
        scheduleOnMain: @escaping (@escaping () -> Void) -> Void = { work in
            DispatchQueue.main.async(execute: work)
        }
    ) {
        self.maximumFrames = maximumFrames
        self.maximumBytes = maximumBytes
        self.deliver = deliver
        self.scheduleOnMain = scheduleOnMain
    }

    func append(_ newFrames: [Data]) -> Bool {
        lock.lock()
        defer { lock.unlock() }

        guard terminalCallbacks.isEmpty else { return false }
        for frame in newFrames {
            guard frames.count < maximumFrames, bytes + frame.count <= maximumBytes else {
                scheduleDrainLocked()
                return false
            }
            frames.append(frame)
            bytes += frame.count
        }
        scheduleDrainLocked()
        return true
    }

    func appendTerminal(_ callback: @escaping () -> Void) {
        lock.lock()
        terminalCallbacks.append(callback)
        scheduleDrainLocked()
        lock.unlock()
    }

    private func scheduleDrainLocked() {
        guard !drainScheduled else { return }
        drainScheduled = true
        scheduleOnMain { [weak self] in self?.drain() }
    }

    private func drain() {
        var delivered = 0
        while delivered < maximumFrames {
            let next: () -> Void
            lock.lock()
            if !frames.isEmpty {
                let frame = frames.removeFirst()
                bytes -= frame.count
                next = { [deliver] in deliver(frame) }
            } else if !terminalCallbacks.isEmpty {
                next = terminalCallbacks.removeFirst()
            } else {
                drainScheduled = false
                lock.unlock()
                return
            }
            lock.unlock()
            next()
            delivered += 1
        }

        lock.lock()
        drainScheduled = false
        if !frames.isEmpty || !terminalCallbacks.isEmpty {
            scheduleDrainLocked()
        }
        lock.unlock()
    }
}

private extension JSONLineBufferAppendResult {
    var frames: [Data] {
        switch self {
        case .lines(let frames), .failure(let frames, _): frames
        }
    }

    var error: JSONLineBufferError? {
        if case .failure(_, let error) = self { error } else { nil }
    }
}
