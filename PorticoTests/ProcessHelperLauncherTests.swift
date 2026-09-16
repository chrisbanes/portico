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

    func testDeliveryQueueDeliversQueuedFramesBeforeExplicitTerminalAtByteCapacity() {
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

    func testDeliveryQueueDeliversQueuedFramesBeforeExplicitTerminalAtFrameCapacity() {
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

    func testDeliveryQueueYieldsWhenDeliveryAppendsTerminalCallback() {
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
                    queue.appendTerminal { deliveryOrder.append("terminal") }
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

        XCTAssertEqual(deliveryOrder, ["a", "b", "terminal"])
    }

    func testDeliveryQueueResumesAfterFrameCapacityDrains() {
        var scheduledDrains: [() -> Void] = []
        var deliveryOrder: [String] = []
        let queue = FrameDeliveryQueue(
            maximumFrames: 2,
            maximumBytes: 10,
            deliver: { data in deliveryOrder.append(String(decoding: data, as: UTF8.self)) },
            scheduleOnMain: { scheduledDrains.append($0) }
        )

        XCTAssertTrue(queue.append([Data("a".utf8), Data("b".utf8)]))
        XCTAssertFalse(queue.append([Data("c".utf8)]))
        XCTAssertEqual(scheduledDrains.count, 1)

        scheduledDrains.removeFirst()()

        XCTAssertTrue(queue.append([Data("c".utf8)]))
        scheduledDrains.removeFirst()()
        XCTAssertEqual(deliveryOrder, ["a", "b", "c"])
    }

    func testDeliveryQueueResumesAfterByteCapacityDrains() {
        var scheduledDrains: [() -> Void] = []
        var deliveryOrder: [String] = []
        let queue = FrameDeliveryQueue(
            maximumFrames: 4,
            maximumBytes: 3,
            deliver: { data in deliveryOrder.append(String(decoding: data, as: UTF8.self)) },
            scheduleOnMain: { scheduledDrains.append($0) }
        )

        XCTAssertTrue(queue.append([Data("aa".utf8)]))
        XCTAssertFalse(queue.append([Data("bb".utf8)]))
        XCTAssertEqual(scheduledDrains.count, 1)

        scheduledDrains.removeFirst()()

        XCTAssertTrue(queue.append([Data("bb".utf8)]))
        scheduledDrains.removeFirst()()
        XCTAssertEqual(deliveryOrder, ["aa", "bb"])
    }

    func testDeliveryProgressResetsTheBackpressureDeadlineBeforeAFrameFits() {
        let releaseDeliveries = DispatchSemaphore(value: 0)
        let deliveryStarted = DispatchSemaphore(value: 0)
        let largeFrameDelivered = expectation(description: "large frame is delivered")
        let lock = NSLock()
        var deliveryTimes: [UInt64] = []
        var pendingDrain: (() -> Void)?
        var startDraining = false
        let queue = FrameDeliveryQueue(
            maximumFrames: 4,
            maximumBytes: 6,
            deliver: { data in
                if data == Data("dddddd".utf8) {
                    largeFrameDelivered.fulfill()
                    return
                }
                deliveryStarted.signal()
                XCTAssertEqual(releaseDeliveries.wait(timeout: .now() + 1), .success)
                lock.lock()
                deliveryTimes.append(DispatchTime.now().uptimeNanoseconds)
                lock.unlock()
            },
            scheduleOnMain: { work in
                lock.lock()
                if startDraining {
                    lock.unlock()
                    DispatchQueue.global().async(execute: work)
                } else {
                    XCTAssertNil(pendingDrain)
                    pendingDrain = work
                    lock.unlock()
                }
            }
        )
        let largeFrame = Data("dddddd".utf8)

        XCTAssertTrue(queue.append([Data("aa".utf8), Data("bb".utf8), Data("cc".utf8)]))
        lock.lock()
        startDraining = true
        let initialDrain = pendingDrain
        pendingDrain = nil
        lock.unlock()
        XCTAssertNotNil(initialDrain)

        DispatchQueue.global().asyncAfter(deadline: .now() + 0.01) {
            initialDrain?()
        }
        DispatchQueue.global().async {
            for _ in 0..<3 {
                guard deliveryStarted.wait(timeout: .now() + 1) == .success else { return }
                Thread.sleep(forTimeInterval: 0.3)
                releaseDeliveries.signal()
            }
        }

        XCTAssertTrue(queue.waitUntilCanAppend(largeFrame, timeout: 0.5))
        XCTAssertTrue(queue.append([largeFrame]))
        wait(for: [largeFrameDelivered], timeout: 2)

        lock.lock()
        XCTAssertEqual(deliveryTimes.count, 3)
        XCTAssertGreaterThan(
            Double(deliveryTimes[2] - deliveryTimes[0]) / 1_000_000_000,
            0.5
        )
        XCTAssertTrue(zip(deliveryTimes, deliveryTimes.dropFirst()).allSatisfy {
            Double($1 - $0) / 1_000_000_000 < 0.5
        })
        lock.unlock()
    }

    func testDeliveryDeadlineDoesNotTreatInFlightCallbackAsCapacityOrProgress() {
        let firstDeliveryStarted = expectation(description: "first delivery starts")
        let deadlineElapsed = expectation(description: "deadline elapses while first callback is held")
        let suffixAppended = expectation(description: "suffix appends after held callback completes")
        let terminalDelivered = expectation(description: "terminal follows both frames")
        let releaseFirstDelivery = DispatchSemaphore(value: 0)
        let lock = NSLock()
        var deliveryOrder: [String] = []
        var couldAppendBeforeCompletion: Bool?
        let queue = FrameDeliveryQueue(
            maximumFrames: 1,
            maximumBytes: 2,
            deliver: { data in
                let line = String(decoding: data, as: UTF8.self)
                if line == "a" {
                    firstDeliveryStarted.fulfill()
                    XCTAssertEqual(releaseFirstDelivery.wait(timeout: .now() + 1), .success)
                }
                lock.lock()
                deliveryOrder.append(line)
                lock.unlock()
            },
            scheduleOnMain: { work in DispatchQueue.global().async(execute: work) }
        )

        XCTAssertTrue(queue.append([Data("a".utf8)]))
        wait(for: [firstDeliveryStarted], timeout: 1)

        DispatchQueue.global().async {
            let canAppend = queue.waitUntilCanAppend(Data("b".utf8), timeout: 0.2)
            lock.lock()
            couldAppendBeforeCompletion = canAppend
            lock.unlock()
            deadlineElapsed.fulfill()
        }
        wait(for: [deadlineElapsed], timeout: 1)

        lock.lock()
        XCTAssertEqual(couldAppendBeforeCompletion, false)
        lock.unlock()

        DispatchQueue.global().async {
            guard queue.waitUntilCanAppend(Data("b".utf8), timeout: nil) else { return }
            XCTAssertTrue(queue.append([Data("b".utf8)]))
            suffixAppended.fulfill()
        }
        releaseFirstDelivery.signal()
        wait(for: [suffixAppended], timeout: 1)

        queue.appendTerminal {
            lock.lock()
            deliveryOrder.append("terminal")
            lock.unlock()
            terminalDelivered.fulfill()
        }
        wait(for: [terminalDelivered], timeout: 1)

        lock.lock()
        XCTAssertEqual(deliveryOrder, ["a", "b", "terminal"])
        lock.unlock()
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

    func testRealChildFloodDeliversAllFiniteFramesInFIFOOrder() throws {
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
        XCTAssertEqual(events, (1...17).map { "line:\($0)" } + ["eof", "exit:0"])
    }

    func testRealChildDeliversValidBurstBeyondQueueCapacityAfterDelayedMainDrain() throws {
        let terminal = expectation(description: "EOF and exit")
        let releaseFirstDelivery = DispatchSemaphore(value: 0)
        var events: [String] = []
        var delayedFirstDelivery = false
        let burst = (1...64).map(String.init).joined(separator: "\n") + "\n"
        let process = try ProcessHelperLauncher().launch(
            at: URL(fileURLWithPath: "/bin/sh"),
            arguments: ["-c", "printf '%s' '\(burst)'"],
            loggingPreference: .disabled,
            onLine: { data in
                if !delayedFirstDelivery {
                    delayedFirstDelivery = true
                    DispatchQueue.global().asyncAfter(deadline: .now() + 0.2) {
                        releaseFirstDelivery.signal()
                    }
                    XCTAssertEqual(releaseFirstDelivery.wait(timeout: .now() + 1), .success)
                }
                events.append("line:" + String(decoding: data, as: UTF8.self))
            },
            onEOF: { events.append("eof") },
            onExit: { status in
                events.append("exit:\(status)")
                terminal.fulfill()
            }
        )

        wait(for: [terminal], timeout: 2)

        XCTAssertFalse(process.isRunning)
        XCTAssertTrue(delayedFirstDelivery)
        XCTAssertEqual(events, [
            "line:1", "line:2", "line:3", "line:4", "line:5", "line:6", "line:7", "line:8",
            "line:9", "line:10", "line:11", "line:12", "line:13", "line:14", "line:15", "line:16",
            "line:17", "line:18", "line:19", "line:20", "line:21", "line:22", "line:23", "line:24",
            "line:25", "line:26", "line:27", "line:28", "line:29", "line:30", "line:31", "line:32",
            "line:33", "line:34", "line:35", "line:36", "line:37", "line:38", "line:39", "line:40",
            "line:41", "line:42", "line:43", "line:44", "line:45", "line:46", "line:47", "line:48",
            "line:49", "line:50", "line:51", "line:52", "line:53", "line:54", "line:55", "line:56",
            "line:57", "line:58", "line:59", "line:60", "line:61", "line:62", "line:63", "line:64",
            "eof", "exit:0",
        ])
    }

    func testBackpressuredChildFailsAfterNoDeliveryProgressButRetainsValidSuffix() throws {
        let eof = expectation(description: "backpressure failure reaches EOF")
        let exited = expectation(description: "fixture exits after cleanup")
        let releaseFirstDelivery = DispatchSemaphore(value: 0)
        var events: [String] = []
        var delayedFirstDelivery = false
        let burst = (1...64).map(String.init).joined(separator: "\n") + "\n"
        let process = try ProcessHelperLauncher(backpressureTimeout: 0.05).launch(
            at: URL(fileURLWithPath: "/bin/sh"),
            arguments: ["-c", "printf '%s' '\(burst)'; while :; do :; done"],
            loggingPreference: .disabled,
            onLine: { data in
                if !delayedFirstDelivery {
                    delayedFirstDelivery = true
                    DispatchQueue.global().asyncAfter(deadline: .now() + 0.2) {
                        releaseFirstDelivery.signal()
                    }
                    XCTAssertEqual(releaseFirstDelivery.wait(timeout: .now() + 1), .success)
                }
                events.append("line:" + String(decoding: data, as: UTF8.self))
            },
            onEOF: {
                events.append("eof")
                eof.fulfill()
            },
            onExit: { status in
                events.append("exit:\(status)")
                exited.fulfill()
            }
        )
        defer {
            if process.isRunning {
                process.terminate()
                process.kill()
                wait(for: [exited], timeout: 2)
            }
        }

        wait(for: [eof], timeout: 2)

        XCTAssertTrue(delayedFirstDelivery)
        XCTAssertTrue(process.isRunning)
        XCTAssertEqual(events, (1...64).map { "line:\($0)" } + ["eof"])
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
