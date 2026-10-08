import Network
import XCTest
@testable import PorticoApplication

@MainActor
final class LocalAppReachabilityTests: XCTestCase {
    func testLoopbackProbeTreatsZeroPortAsUnavailable() {
        var result: Bool?

        LoopbackTCPProbe().probe(port: 0, timeout: 0.5) { reachable in
            result = reachable
        }

        XCTAssertEqual(result, false)
    }

    func testSuccessfulLoopbackProbeReleasesResources() throws {
        let listener = try startLoopbackListener()
        defer { cancelListener(listener) }
        let port = try XCTUnwrap(listener.port).rawValue

        assertProbeReleasesResources(port: port, timeout: 0.5, expectedReachable: true)
    }

    func testRefusedLoopbackProbeReleasesResources() throws {
        let listener = try startLoopbackListener()
        let port = try XCTUnwrap(listener.port).rawValue
        cancelListener(listener)

        assertProbeReleasesResources(port: port, timeout: 0.5, expectedReachable: false)
    }

    func testTimedOutLoopbackProbeReleasesResources() throws {
        let listener = try startLoopbackListener()
        defer { cancelListener(listener) }
        let port = try XCTUnwrap(listener.port).rawValue

        // The immediate deadline is queued before the connection starts on that queue.
        assertProbeReleasesResources(port: port, timeout: 0, expectedReachable: false)
    }

    func testRepeatedLoopbackProbesReleaseAllResources() throws {
        let listener = try startLoopbackListener()
        defer { cancelListener(listener) }
        let port = try XCTUnwrap(listener.port).rawValue

        assertProbeReleasesResources(
            port: port,
            timeout: 0.5,
            expectedReachable: true,
            count: 100
        )
    }

    func testUnchangedPortalTargetsDoNotScheduleAnotherBatch() {
        let probe = FakeLocalAppProbe()
        let monitor = LocalAppReachability(probe: probe, scheduler: FakePorticoScheduler())
        let portal = portal(id: "11111111-1111-1111-1111-111111111111", port: 3000)

        monitor.update(portals: [portal])
        monitor.update(portals: [portal])
        probe.complete(index: 0, reachable: true)

        XCTAssertEqual(probe.requests.count, 1)
        XCTAssertEqual(monitor.states[portal.id], .reachable)
    }

    func testProbesAllNonCleanupPortalsAndCoalescesSupersededBatches() {
        let probe = FakeLocalAppProbe()
        let scheduler = FakePorticoScheduler()
        let monitor = LocalAppReachability(probe: probe, scheduler: scheduler)
        let enabled = portal(id: "11111111-1111-1111-1111-111111111111", port: 3000)
        let stopped = portal(id: "22222222-2222-2222-2222-222222222222", port: 4000, desiredState: .stopped)
        let cleanup = portal(id: "33333333-3333-3333-3333-333333333333", port: 5000, lifecycle: .pendingTailnetRejection)

        monitor.update(portals: [enabled, stopped, cleanup])

        XCTAssertEqual(probe.requests.map(\.port), [3000, 4000])
        XCTAssertEqual(probe.requests.map(\.timeout), [0.5, 0.5])
        XCTAssertEqual(monitor.states, [enabled.id: .unknown, stopped.id: .unknown])
        XCTAssertEqual(scheduler.pendingDelays, [10])

        monitor.refresh()
        var changed = enabled
        changed.localAppPort = 3001
        monitor.update(portals: [changed, stopped])
        XCTAssertEqual(probe.requests.count, 2)

        probe.complete(index: 0, reachable: true)
        probe.complete(index: 1, reachable: false)

        XCTAssertEqual(monitor.states[enabled.id], .unknown)
        XCTAssertEqual(monitor.states[stopped.id], .unknown)
        XCTAssertEqual(probe.requests.map(\.port), [3000, 4000, 3001, 4000])

        probe.complete(index: 2, reachable: true)
        probe.complete(index: 3, reachable: false)

        XCTAssertEqual(monitor.states[enabled.id], .reachable)
        XCTAssertEqual(monitor.states[stopped.id], .unavailable)
    }

    func testRemoteAppsAreExcludedFromLocalAppReachability() {
        let probe = FakeLocalAppProbe()
        let monitor = LocalAppReachability(probe: probe, scheduler: FakePorticoScheduler())
        let remote = PortalConfiguration(
            id: UUID(uuidString: "11111111-1111-1111-1111-111111111111")!,
            name: "remote",
            destination: PortalDestination(remoteAppScheme: .https, host: "app.example.com", port: 443)!,
            createdAt: Date()
        )

        monitor.update(portals: [remote])

        XCTAssertTrue(probe.requests.isEmpty)
        XCTAssertTrue(monitor.states.isEmpty)
    }

    private func startLoopbackListener() throws -> NWListener {
        let parameters = NWParameters.tcp
        parameters.requiredLocalEndpoint = .hostPort(host: "127.0.0.1", port: .any)
        let listener = try NWListener(using: parameters)
        let ready = expectation(description: "loopback listener ready")
        listener.stateUpdateHandler = { state in
            switch state {
            case .ready:
                ready.fulfill()
            case .failed(let error):
                XCTFail("Loopback listener failed: \(error)")
                ready.fulfill()
            default:
                break
            }
        }
        listener.newConnectionHandler = { $0.cancel() }
        listener.start(queue: DispatchQueue(label: "portico.tests.loopback-listener"))
        wait(for: [ready], timeout: 3)
        return listener
    }

    private func cancelListener(_ listener: NWListener) {
        let cancelled = expectation(description: "loopback listener cancelled")
        listener.stateUpdateHandler = { state in
            if case .cancelled = state { cancelled.fulfill() }
        }
        listener.cancel()
        wait(for: [cancelled], timeout: 3)
        listener.stateUpdateHandler = nil
        listener.newConnectionHandler = nil
    }

    private func assertProbeReleasesResources(
        port: UInt16,
        timeout: TimeInterval,
        expectedReachable: Bool,
        count: Int = 1
    ) {
        let completed = expectation(description: "each probe completes once")
        completed.expectedFulfillmentCount = count
        completed.assertForOverFulfill = true
        let released = expectation(description: "each probe releases its captured resource")
        released.expectedFulfillmentCount = count
        let probe = LoopbackTCPProbe()
        var results: [Bool] = []

        autoreleasepool {
            for _ in 0..<count {
                let resource = ProbeResource { released.fulfill() }
                probe.probe(port: port, timeout: timeout) { [resource] reachable in
                    withExtendedLifetime(resource) {
                        XCTAssertTrue(Thread.isMainThread)
                        results.append(reachable)
                        completed.fulfill()
                    }
                }
            }
        }

        // Cancelled deadline work can retain the callback until its scheduled time.
        wait(for: [completed, released], timeout: timeout + 3)
        XCTAssertEqual(results, Array(repeating: expectedReachable, count: count))
    }

    private func portal(
        id: String,
        port: UInt16,
        desiredState: PortalDesiredState = .enabled,
        lifecycle: PortalLifecycle = .active
    ) -> PortalConfiguration {
        PortalConfiguration(
            id: UUID(uuidString: id)!,
            name: "portal-\(port)",
            localAppPort: port,
            createdAt: Date(),
            desiredState: desiredState,
            lifecycle: lifecycle
        )
    }
}

private final class ProbeResource {
    private let onRelease: () -> Void

    init(onRelease: @escaping () -> Void) {
        self.onRelease = onRelease
    }

    deinit { onRelease() }
}

final class FakeLocalAppProbe: LocalAppProbing {
    struct Request {
        let port: UInt16
        let timeout: TimeInterval
        let completion: (Bool) -> Void
    }

    private(set) var requests: [Request] = []

    func probe(port: UInt16, timeout: TimeInterval, completion: @escaping (Bool) -> Void) {
        requests.append(Request(port: port, timeout: timeout, completion: completion))
    }

    func complete(index: Int, reachable: Bool) {
        requests[index].completion(reachable)
    }
}
