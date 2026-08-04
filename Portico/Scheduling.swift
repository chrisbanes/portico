import Foundation

protocol ScheduledTask: AnyObject {
    func cancel()
}

@MainActor
protocol PorticoScheduling: AnyObject {
    @discardableResult
    func schedule(after delay: TimeInterval, _ action: @escaping () -> Void) -> ScheduledTask
}

@MainActor
final class MainQueueScheduler: PorticoScheduling {
    func schedule(after delay: TimeInterval, _ action: @escaping () -> Void) -> ScheduledTask {
        let workItem = DispatchWorkItem(block: action)
        DispatchQueue.main.asyncAfter(deadline: .now() + delay, execute: workItem)
        return DispatchScheduledTask(workItem: workItem)
    }
}

private final class DispatchScheduledTask: ScheduledTask {
    private let workItem: DispatchWorkItem

    init(workItem: DispatchWorkItem) {
        self.workItem = workItem
    }

    func cancel() {
        workItem.cancel()
    }
}
