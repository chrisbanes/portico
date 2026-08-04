import Foundation

enum ProcessHelperLauncherError: Error {
    case loggingChoiceRequired
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
        let lineBuffer = JSONLineBuffer()

        process.executableURL = executableURL
        process.arguments = arguments
        process.environment = try Self.childEnvironment(
            for: loggingPreference,
            inherited: ProcessInfo.processInfo.environment
        )
        process.standardInput = input
        process.standardOutput = output
        process.standardError = diagnostics

        output.fileHandleForReading.readabilityHandler = { handle in
            let data = handle.availableData
            if data.isEmpty {
                handle.readabilityHandler = nil
                if let remainder = lineBuffer.finish() {
                    DispatchQueue.main.async { onLine(remainder) }
                }
                DispatchQueue.main.async { onEOF() }
                return
            }

            for line in lineBuffer.append(data) {
                DispatchQueue.main.async { onLine(line) }
            }
        }
        diagnostics.fileHandleForReading.readabilityHandler = { handle in
            if handle.availableData.isEmpty {
                handle.readabilityHandler = nil
            }
        }
        process.terminationHandler = { process in
            output.fileHandleForReading.readabilityHandler = nil
            diagnostics.fileHandleForReading.readabilityHandler = nil
            DispatchQueue.main.async { onExit(process.terminationStatus) }
        }

        do {
            try process.run()
        } catch {
            output.fileHandleForReading.readabilityHandler = nil
            diagnostics.fileHandleForReading.readabilityHandler = nil
            throw error
        }

        return FoundationHelperProcess(process: process, input: input.fileHandleForWriting)
    }
}

private final class FoundationHelperProcess: HelperProcess {
    private let process: Process
    private let input: FileHandle

    init(process: Process, input: FileHandle) {
        self.process = process
        self.input = input
    }

    var isRunning: Bool { process.isRunning }

    func send(_ data: Data) throws {
        try input.write(contentsOf: data)
    }

    func closeInput() {
        try? input.close()
    }

    func terminate() {
        if process.isRunning {
            process.terminate()
        }
    }
}

private final class JSONLineBuffer {
    private let lock = NSLock()
    private var buffer = Data()

    func append(_ data: Data) -> [Data] {
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
            lines.append(line)
        }
        return lines
    }

    func finish() -> Data? {
        lock.lock()
        defer { lock.unlock() }

        guard !buffer.isEmpty else { return nil }
        defer { buffer.removeAll() }
        return buffer
    }
}
