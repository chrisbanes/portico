import Foundation

#if DEBUG
struct SmokeLaunchConfiguration: Equatable {
    enum ConfigurationError: Error, Equatable {
        case invalidConsent
        case invalidExpectedRoot
        case unsupportedVariant
    }

    static let consentEnvironmentKey = "PORTICO_SMOKE_REAL_HELPER"
    static let expectedRootEnvironmentKey = "PORTICO_SMOKE_EXPECTED_ROOT"
    static let consentValue = "accepted-v6"

    let expectedRootURL: URL

    static func current(
        environment: [String: String] = ProcessInfo.processInfo.environment,
        variant: AppVariant
    ) throws -> SmokeLaunchConfiguration? {
        guard environment[consentEnvironmentKey] != nil || environment[expectedRootEnvironmentKey] != nil else {
            return nil
        }
        guard variant == .dev else {
            throw ConfigurationError.unsupportedVariant
        }
        guard environment[consentEnvironmentKey] == consentValue else {
            throw ConfigurationError.invalidConsent
        }
        guard let rawExpectedRoot = environment[expectedRootEnvironmentKey], !rawExpectedRoot.isEmpty else {
            throw ConfigurationError.invalidExpectedRoot
        }
        let expectedRootURL = URL(fileURLWithPath: rawExpectedRoot, isDirectory: true).standardizedFileURL
        let canonicalExpectedRootURL = expectedRootURL.resolvingSymlinksInPath().standardizedFileURL
        guard expectedRootURL.lastPathComponent == "Portico Dev",
              expectedRootURL.deletingLastPathComponent().lastPathComponent == "Application Support",
              isTemporaryURL(expectedRootURL),
              expectedRootURL == canonicalExpectedRootURL
        else {
            throw ConfigurationError.invalidExpectedRoot
        }
        return SmokeLaunchConfiguration(expectedRootURL: expectedRootURL)
    }

    func admits(resolvedRootURL: URL) -> Bool {
        resolvedRootURL.resolvingSymlinksInPath().standardizedFileURL == expectedRootURL
    }

    func recordAcceptedHandshake() throws {
        try Data("accepted-v6\n".utf8).write(
            to: expectedRootURL.appendingPathComponent("smoke-handshake-witness", isDirectory: false),
            options: .atomic
        )
    }

    private static func isTemporaryURL(_ url: URL) -> Bool {
        let temporaryRoots = [
            FileManager.default.temporaryDirectory,
            URL(fileURLWithPath: "/private/tmp", isDirectory: true),
        ].map(\.standardizedFileURL)
        return temporaryRoots.contains { root in
            url.path.hasPrefix(root.path + "/")
        }
    }
}

enum SmokeRootAdmission {
    static func run(
        configuration: SmokeLaunchConfiguration,
        resolvedRootURL: URL,
        prepareStore: () -> Void
    ) -> Bool {
        guard configuration.admits(resolvedRootURL: resolvedRootURL) else { return false }
        prepareStore()
        return true
    }
}
#endif
