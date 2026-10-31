import Foundation
@testable import PorticoApplication

private struct TestPortalStoreError: Error {
    let reason: String
}

extension HelperSupervisor {
    /// Tests that never launch the helper do not care where its state root points.
    convenience init(
        helperURL: URL,
        launcher: HelperLaunching,
        requestIDProvider: @escaping () -> String = { UUID().uuidString },
        scheduler: PorticoScheduling? = nil,
        history: DiagnosticHistory? = nil,
        handshakeTimeout: TimeInterval = 3,
        shutdownGraceInterval: TimeInterval = 5,
        shutdownTerminationInterval: TimeInterval = 2,
        shutdownKillObservationInterval: TimeInterval = 1
    ) {
        self.init(
            helperURL: helperURL,
            stateRootURL: URL(fileURLWithPath: "/unused/tsnet"),
            launcher: launcher,
            requestIDProvider: requestIDProvider,
            scheduler: scheduler,
            history: history,
            handshakeTimeout: handshakeTimeout,
            shutdownGraceInterval: shutdownGraceInterval,
            shutdownTerminationInterval: shutdownTerminationInterval,
            shutdownKillObservationInterval: shutdownKillObservationInterval
        )
    }
}

extension PortalStore {
    func load() throws -> PortalConfiguration? {
        try loadInstallation().portals.first
    }

    func save(_ portal: PortalConfiguration) throws {
        var installation = try loadInstallation()
        guard installation.portals.isEmpty else {
            throw TestPortalStoreError(reason: "a Portal already exists")
        }
        installation.portals.append(portal)
        try save(installation)
    }
}

extension PortalController {
    var portal: PortalConfiguration? { portals.first }
    var status: PortalStatusPayload? { portal.flatMap { statuses[$0.id] } }

    func authenticate() {
        guard let portal else { return }
        authenticate(id: portal.id)
    }
}

extension PortalConfiguration {
    init(
        id: UUID,
        name: String,
        localAppPort: UInt16,
        createdAt: Date,
        desiredState: PortalDesiredState = .enabled,
        lifecycle: PortalLifecycle = .active,
        removalAssignedName: String? = nil,
        publicAccess: PortalPublicAccess = .private
    ) {
        guard let destination = PortalDestination(localAppPort: localAppPort) else {
            preconditionFailure("Portal destinations require a port from 1 through 65535.")
        }
        self.init(
            id: id,
            name: name,
            destination: destination,
            createdAt: createdAt,
            desiredState: desiredState,
            lifecycle: lifecycle,
            removalAssignedName: removalAssignedName,
            publicAccess: publicAccess
        )
    }
}
