import Darwin
import Foundation
import XCTest
@testable import PorticoApplication

final class ProcessHelperLauncherTests: XCTestCase {
    func testEnabledLoggingRemovesInheritedOptOutWithoutChangingOtherVariables() throws {
        let environment = try ProcessHelperLauncher.childEnvironment(
            for: .enabled,
            inherited: ["PATH": "/usr/bin", "TS_NO_LOGS_NO_SUPPORT": "true"]
        )

        XCTAssertEqual(environment, ["PATH": "/usr/bin"])
    }

    func testDisabledLoggingSetsExactOptOutValue() throws {
        let environment = try ProcessHelperLauncher.childEnvironment(
            for: .disabled,
            inherited: ["PATH": "/usr/bin", "TS_NO_LOGS_NO_SUPPORT": "false"]
        )

        XCTAssertEqual(environment, ["PATH": "/usr/bin", "TS_NO_LOGS_NO_SUPPORT": "true"])
    }

    func testUndecidedLoggingCannotConstructHelperEnvironment() {
        XCTAssertThrowsError(
            try ProcessHelperLauncher.childEnvironment(for: .undecided, inherited: [:])
        )
    }

    func testRejectsOversizedUnterminatedJSONLFrame() throws {
        let buffer = JSONLineBuffer(maximumFrameBytes: 4)

        if case .failure(_, let error) = buffer.append(Data("12345".utf8)) {
            XCTAssertEqual(error, .frameTooLarge)
        } else {
            XCTFail("Expected oversized frame failure")
        }
    }

    func testRejectsUnterminatedFrameAtEOF() throws {
        let buffer = JSONLineBuffer(maximumFrameBytes: 4)
        _ = buffer.append(Data("ok".utf8))

        XCTAssertThrowsError(try buffer.finish()) { error in
            XCTAssertEqual(error as? JSONLineBufferError, .unterminatedFrame)
        }
    }

    func testKeepsCompleteFramesBeforeRejectingOversizedPartialFrame() {
        let buffer = JSONLineBuffer(maximumFrameBytes: 4)

        let result = buffer.append(Data("ok\n12345".utf8))
        XCTAssertEqual(result.frames, [Data("ok".utf8)])
        XCTAssertEqual(result.error, .frameTooLarge)
    }

    func testAcceptsExactFrameLimitWithLFAndCRLF() {
        let lf = JSONLineBuffer(maximumFrameBytes: 4)
        let crlf = JSONLineBuffer(maximumFrameBytes: 4)

        XCTAssertEqual(lf.append(Data("1234\n".utf8)).frames, [Data("1234".utf8)])
        XCTAssertEqual(crlf.append(Data("1234\r\n".utf8)).frames, [Data("1234".utf8)])
    }

    func testDeliveryQueueDrainsFramesBeforeByteOverflowTerminal() {
        var scheduledDrains: [() -> Void] = []
        var deliveryOrder: [String] = []
        let queue = FrameDeliveryQueue(
            maximumFrames: 3,
            maximumBytes: 2,
            deliver: { data in deliveryOrder.append(String(decoding: data, as: UTF8.self)) },
            scheduleOnMain: { scheduledDrains.append($0) }
        )

        XCTAssertTrue(queue.append([Data("a".utf8), Data("b".utf8)]))
        XCTAssertFalse(queue.append([Data("c".utf8)]))
        queue.appendTerminal { deliveryOrder.append("terminal") }
        XCTAssertEqual(scheduledDrains.count, 1)
        XCTAssertTrue(deliveryOrder.isEmpty)

        scheduledDrains.removeFirst()()

        XCTAssertEqual(deliveryOrder, ["a", "b", "terminal"])
        XCTAssertTrue(scheduledDrains.isEmpty)
    }

    func testDeliveryQueueDrainsFramesBeforeFrameOverflowTerminal() {
        var scheduledDrains: [() -> Void] = []
        var deliveryOrder: [String] = []
        let queue = FrameDeliveryQueue(
            maximumFrames: 2,
            maximumBytes: 3,
            deliver: { data in deliveryOrder.append(String(decoding: data, as: UTF8.self)) },
            scheduleOnMain: { scheduledDrains.append($0) }
        )

        XCTAssertTrue(queue.append([Data("a".utf8), Data("b".utf8)]))
        XCTAssertFalse(queue.append([Data("c".utf8)]))
        queue.appendTerminal { deliveryOrder.append("terminal") }

        scheduledDrains.removeFirst()()
        XCTAssertEqual(deliveryOrder, ["a", "b"])
        XCTAssertEqual(scheduledDrains.count, 1)

        scheduledDrains.removeFirst()()
        XCTAssertEqual(deliveryOrder, ["a", "b", "terminal"])
    }

    func testDeliveryQueueYieldsWhenDeliveryAddsMoreFrames() {
        var scheduledDrains: [() -> Void] = []
        var deliveryOrder: [String] = []
        var queue: FrameDeliveryQueue!
        queue = FrameDeliveryQueue(
            maximumFrames: 2,
            maximumBytes: 3,
            deliver: { data in
                let frame = String(decoding: data, as: UTF8.self)
                deliveryOrder.append(frame)
                if frame == "a" {
                    XCTAssertTrue(queue.append([Data("c".utf8)]))
                }
            },
            scheduleOnMain: { scheduledDrains.append($0) }
        )

        XCTAssertTrue(queue.append([Data("a".utf8), Data("b".utf8)]))
        XCTAssertEqual(scheduledDrains.count, 1)

        scheduledDrains.removeFirst()()

        XCTAssertEqual(deliveryOrder, ["a", "b"])
        XCTAssertEqual(scheduledDrains.count, 1)

        scheduledDrains.removeFirst()()

        XCTAssertEqual(deliveryOrder, ["a", "b", "c"])
    }

    func testRealChildDeliversFinalCoalescedFramesBeforeEOFAndExit() throws {
        let terminal = expectation(description: "EOF and exit")
        var events: [String] = []
        let launcher = ProcessHelperLauncher()

        let process = try launcher.launch(
            at: URL(fileURLWithPath: "/bin/sh"),
            arguments: ["-c", "printf 'first\\nsecond\\n'"],
            loggingPreference: .disabled,
            onLine: { data in events.append("line:" + String(decoding: data, as: UTF8.self)) },
            onEOF: { events.append("eof") },
            onExit: { status in
                events.append("exit:\(status)")
                terminal.fulfill()
            }
        )

        wait(for: [terminal], timeout: 2)

        XCTAssertFalse(process.isRunning)
        XCTAssertEqual(events, ["line:first", "line:second", "eof", "exit:0"])
    }

    func testRealChildDeliversSplitFramesInOrderBeforeEOFAndExit() throws {
        let terminal = expectation(description: "EOF and exit")
        var events: [String] = []
        let process = try ProcessHelperLauncher().launch(
            at: URL(fileURLWithPath: "/bin/sh"),
            arguments: ["-c", "printf first; sleep 0.01; printf '\\nsecond\\n'"],
            loggingPreference: .disabled,
            onLine: { data in events.append("line:" + String(decoding: data, as: UTF8.self)) },
            onEOF: { events.append("eof") },
            onExit: { status in
                events.append("exit:\(status)")
                terminal.fulfill()
            }
        )

        wait(for: [terminal], timeout: 2)

        XCTAssertFalse(process.isRunning)
        XCTAssertEqual(events, ["line:first", "line:second", "eof", "exit:0"])
    }

    func testRealChildOversizedFrameDeliversNoLineBeforeTerminalFailure() throws {
        let terminal = expectation(description: "EOF and exit")
        var events: [String] = []
        let process = try ProcessHelperLauncher().launch(
            at: URL(fileURLWithPath: "/bin/sh"),
            arguments: ["-c", "LC_ALL=C head -c 262145 /dev/zero | tr '\\000' x; printf '\\n'"],
            loggingPreference: .disabled,
            onLine: { data in events.append("line:" + String(decoding: data, as: UTF8.self)) },
            onEOF: { events.append("eof") },
            onExit: { status in
                events.append("exit:\(status)")
                terminal.fulfill()
            }
        )

        wait(for: [terminal], timeout: 2)

        XCTAssertFalse(process.isRunning)
        XCTAssertEqual(events, ["eof", "exit:0"])
    }

    func testRealChildFloodDeliversBoundedFIFOFramesBeforeTerminalFailure() throws {
        let terminal = expectation(description: "EOF and exit")
        var events: [String] = []
        let process = try ProcessHelperLauncher().launch(
            at: URL(fileURLWithPath: "/bin/sh"),
            arguments: ["-c", "i=1; while [ $i -le 17 ]; do printf '%s\\n' \"$i\"; i=$((i + 1)); done"],
            loggingPreference: .disabled,
            onLine: { data in events.append("line:" + String(decoding: data, as: UTF8.self)) },
            onEOF: { events.append("eof") },
            onExit: { status in
                events.append("exit:\(status)")
                terminal.fulfill()
            }
        )

        wait(for: [terminal], timeout: 2)

        XCTAssertFalse(process.isRunning)
        let lines = Array(events.dropLast(2))
        XCTAssertEqual(lines, lines.indices.map { "line:\($0 + 1)" })
        XCTAssertLessThanOrEqual(lines.count, 17)
        XCTAssertEqual(Array(events.suffix(2)), ["eof", "exit:0"])
    }

    func testRealChildBrokenPipeReportsFailedWrite() throws {
        let exited = expectation(description: "child exits")
        let failedWrite = expectation(description: "write fails")
        let process = try ProcessHelperLauncher().launch(
            at: URL(fileURLWithPath: "/bin/sh"),
            arguments: ["-c", "exit 0"],
            loggingPreference: .disabled,
            onLine: { _ in },
            onEOF: {},
            onExit: { _ in exited.fulfill() }
        )

        wait(for: [exited], timeout: 2)
        process.send(Data("request\\n".utf8)) { result in
            if case .failure = result {
                failedWrite.fulfill()
            }
        }
        wait(for: [failedWrite], timeout: 2)
    }

    func testRealChildSerializesStdinWritesInCallOrder() throws {
        let terminal = expectation(description: "echo child exits")
        var output: [String] = []
        let process = try ProcessHelperLauncher().launch(
            at: URL(fileURLWithPath: "/bin/sh"),
            arguments: ["-c", "cat"],
            loggingPreference: .disabled,
            onLine: { output.append(String(decoding: $0, as: UTF8.self)) },
            onEOF: {},
            onExit: { _ in terminal.fulfill() }
        )

        process.send(Data("first\n".utf8)) { _ in }
        process.send(Data("second\n".utf8)) { _ in }
        process.closeInput()
        wait(for: [terminal], timeout: 2)

        XCTAssertEqual(output, ["first", "second"])
    }

    func testRealTERMIgnoringChildDeliversEOFBeforeSIGKILLExit() throws {
        let pidURL = temporaryProcessFixtureURL(suffix: "pid")
        let readyURL = temporaryProcessFixtureURL(suffix: "ready")
        let scriptURL = try makeProcessFixture("""
        trap '' TERM
        printf '%s\\n' "$$" > '\(pidURL.path)'
        : > '\(readyURL.path)'
        while :; do :; done
        """)
        defer {
            try? FileManager.default.removeItem(at: scriptURL)
            try? FileManager.default.removeItem(at: pidURL)
            try? FileManager.default.removeItem(at: readyURL)
        }

        let terminal = expectation(description: "SIGKILL child exits")
        var events: [String] = []
        var didExit = false
        let process = try ProcessHelperLauncher().launch(
            at: scriptURL,
            arguments: [],
            loggingPreference: .disabled,
            onLine: { _ in },
            onEOF: { events.append("eof") },
            onExit: { status in
                didExit = true
                events.append("exit:\(status)")
                terminal.fulfill()
            }
        )
        defer {
            if !didExit {
                process.closeInput()
                process.terminate()
                process.kill()
                waitForProcessToStop(process)
            }
        }

        waitForProcessFixture(at: readyURL)
        let pidText = try XCTUnwrap(String(contentsOf: pidURL, encoding: .utf8))
        let pid = try XCTUnwrap(Int32(pidText.trimmingCharacters(in: .whitespacesAndNewlines)))
        XCTAssertEqual(Darwin.kill(pid, 0), 0)
        process.terminate()
        RunLoop.current.run(until: Date().addingTimeInterval(0.02))
        XCTAssertEqual(Darwin.kill(pid, 0), 0, "fixture must still own the PID after TERM")

        errno = 0
        let killResult = Darwin.kill(pid, SIGKILL)
        let killErrno = errno
        XCTAssertEqual(killResult, 0)
        XCTAssertEqual(killErrno, 0)
        wait(for: [terminal], timeout: 2)
        waitForPIDToExit(pid)

        XCTAssertEqual(events, ["eof", "exit:9"])
    }

    func testRealImmediatelyExitingChildrenDeliverOneTerminalCallbackEach() throws {
        let terminal = expectation(description: "every child exits")
        terminal.expectedFulfillmentCount = 100
        terminal.assertForOverFulfill = true
        var terminalCount = 0
        var statuses: [Int32] = []
        var eofCount = 0
        var children: [HelperProcess] = []

        for _ in 0..<100 {
            children.append(try ProcessHelperLauncher().launch(
                at: URL(fileURLWithPath: "/bin/sh"),
                arguments: ["-c", "exit 7"],
                loggingPreference: .disabled,
                onLine: { _ in },
                onEOF: { eofCount += 1 },
                onExit: { status in
                    terminalCount += 1
                    statuses.append(status)
                    terminal.fulfill()
                }
            ))
        }

        wait(for: [terminal], timeout: 2)
        XCTAssertEqual(terminalCount, 100)
        XCTAssertEqual(eofCount, 100)
        XCTAssertEqual(statuses, Array(repeating: 7, count: 100))
        XCTAssertEqual(children.count, 100)
    }

    func testBlockedStdinDoesNotBlockMainCaller() throws {
        let exited = expectation(description: "bounded child exits")
        let process = try ProcessHelperLauncher().launch(
            at: URL(fileURLWithPath: "/bin/sh"),
            arguments: ["-c", "sleep 0.2; cat >/dev/null"],
            loggingPreference: .disabled,
            onLine: { _ in },
            onEOF: {},
            onExit: { _ in exited.fulfill() }
        )
        defer {
            process.closeInput()
            wait(for: [exited], timeout: 2)
        }

        let clock = ContinuousClock()
        let start = clock.now
        process.send(Data(repeating: 0x78, count: 2 * 1024 * 1024)) { _ in }
        let elapsed = start.duration(to: clock.now)

        XCTAssertLessThan(elapsed, .milliseconds(50))
    }

}

private func makeProcessFixture(_ body: String) throws -> URL {
    let url = temporaryProcessFixtureURL(suffix: "sh")
    try "#!/bin/sh\n\(body)\n".write(to: url, atomically: true, encoding: .utf8)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
    return url
}

private func temporaryProcessFixtureURL(suffix: String) -> URL {
    FileManager.default.temporaryDirectory
        .appendingPathComponent("portico-process-\(UUID().uuidString)")
        .appendingPathExtension(suffix)
}

private func waitForProcessFixture(at url: URL) {
    let deadline = Date().addingTimeInterval(2)
    while !FileManager.default.fileExists(atPath: url.path), Date() < deadline {
        RunLoop.current.run(until: Date().addingTimeInterval(0.01))
    }
    XCTAssertTrue(FileManager.default.fileExists(atPath: url.path), "fixture did not become ready")
}

private func waitForPIDToExit(_ pid: Int32) {
    let deadline = Date().addingTimeInterval(2)
    while Date() < deadline {
        errno = 0
        if Darwin.kill(pid, 0) == -1, errno == ESRCH {
            return
        }
        RunLoop.current.run(until: Date().addingTimeInterval(0.01))
    }
    XCTFail("fixture PID \(pid) remained live after SIGKILL")
}

private func waitForProcessToStop(_ process: HelperProcess) {
    let deadline = Date().addingTimeInterval(2)
    while process.isRunning, Date() < deadline {
        RunLoop.current.run(until: Date().addingTimeInterval(0.01))
    }
    XCTAssertFalse(process.isRunning, "fixture child remained running after cleanup")
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
