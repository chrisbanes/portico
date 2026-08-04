import Foundation
import Network

enum LocalAppReachabilityState: String, Equatable {
    case unknown
    case reachable
    case unavailable
}

protocol LocalAppProbing {
    func probe(port: UInt16, timeout: TimeInterval, completion: @escaping (Bool) -> Void)
}

final class LoopbackTCPProbe: LocalAppProbing {
    func probe(port: UInt16, timeout: TimeInterval, completion: @escaping (Bool) -> Void) {
        guard port != 0, let endpointPort = NWEndpoint.Port(rawValue: port) else {
            completion(false)
            return
        }
        let connection = NWConnection(
            host: NWEndpoint.Host("127.0.0.1"),
            port: endpointPort,
            using: .tcp
        )
        let queue = DispatchQueue(label: "dev.chrisbanes.portico.reachability")
        let lock = NSLock()
        var finished = false
        var timeoutWorkItem: DispatchWorkItem?
        let finish: (Bool) -> Void = { reachable in
            lock.lock()
            guard !finished else {
                lock.unlock()
                return
            }
            finished = true
            lock.unlock()
            timeoutWorkItem?.cancel()
            connection.cancel()
            DispatchQueue.main.async { completion(reachable) }
        }
        connection.stateUpdateHandler = { state in
            switch state {
            case .ready:
                finish(true)
            case .failed, .cancelled:
                finish(false)
            default:
                break
            }
        }
        let workItem = DispatchWorkItem { finish(false) }
        timeoutWorkItem = workItem
        queue.asyncAfter(deadline: .now() + timeout, execute: workItem)
        connection.start(queue: queue)
    }
}

@MainActor
final class LocalAppReachability: ObservableObject {
    @Published private(set) var states: [UUID: LocalAppReachabilityState] = [:]
    var onChange: (([UUID: LocalAppReachabilityState]) -> Void)?

    private let probe: LocalAppProbing
    private let scheduler: PorticoScheduling
    private var portalPorts: [UUID: UInt16] = [:]
    private var cycle = 0
    private var activeRemaining: Set<UUID>?
    private var pendingCycle: Int?
    private var periodicTask: ScheduledTask?

    init(probe: LocalAppProbing, scheduler: PorticoScheduling) {
        self.probe = probe
        self.scheduler = scheduler
        schedulePeriodicProbe()
    }

    func update(portals: [PortalConfiguration]) {
        let pairs: [(UUID, UInt16)] = portals.compactMap { portal in
            guard portal.lifecycle == .active, let port = portal.localAppPort else { return nil }
            return (portal.id, port)
        }
        let updated = Dictionary(uniqueKeysWithValues: pairs)
        let previous = portalPorts
        guard updated != previous else { return }
        portalPorts = updated
        states = states.filter { updated[$0.key] != nil }
        for (id, port) in updated where previous[id] != port || states[id] == nil {
            states[id] = .unknown
        }
        publish()
        trigger()
    }

    func refresh() {
        trigger()
    }

    func helperRecovered() {
        trigger()
    }

    private func trigger() {
        cycle += 1
        let requestedCycle = cycle
        guard activeRemaining == nil else {
            pendingCycle = requestedCycle
            return
        }
        startBatch(cycle: requestedCycle)
    }

    private func startBatch(cycle requestedCycle: Int) {
        let snapshot = portalPorts
        let ids = Set(snapshot.keys)
        guard !ids.isEmpty else {
            activeRemaining = nil
            return
        }
        activeRemaining = ids
        for (id, port) in snapshot.sorted(by: {
            $0.key.uuidString.lowercased() < $1.key.uuidString.lowercased()
        }) {
            probe.probe(port: port, timeout: 0.5) { [weak self] reachable in
                guard let self else { return }
                self.complete(id: id, port: port, cycle: requestedCycle, reachable: reachable)
            }
        }
    }

    private func complete(id: UUID, port: UInt16, cycle completedCycle: Int, reachable: Bool) {
        guard activeRemaining?.remove(id) != nil else { return }
        if completedCycle == cycle, portalPorts[id] == port {
            states[id] = reachable ? .reachable : .unavailable
            publish()
        }
        guard activeRemaining?.isEmpty == true else { return }
        activeRemaining = nil
        if let pendingCycle {
            self.pendingCycle = nil
            startBatch(cycle: pendingCycle)
        }
    }

    private func schedulePeriodicProbe() {
        periodicTask = scheduler.schedule(after: 10) { [weak self] in
            guard let self else { return }
            self.periodicTask = nil
            self.trigger()
            self.schedulePeriodicProbe()
        }
    }

    private func publish() {
        onChange?(states)
    }
}
