import Foundation

struct AppVariant: Equatable {
    enum ConfigurationError: Error, Equatable {
        case invalidMetadata
    }

    let displayName: String
    let supportDirectoryName: String
    let isLaunchAtLoginAvailable: Bool
    let bundleIdentifier: String

    private init(
        displayName: String,
        supportDirectoryName: String,
        isLaunchAtLoginAvailable: Bool,
        bundleIdentifier: String
    ) {
        self.displayName = displayName
        self.supportDirectoryName = supportDirectoryName
        self.isLaunchAtLoginAvailable = isLaunchAtLoginAvailable
        self.bundleIdentifier = bundleIdentifier
    }

    static let dev = AppVariant(
        displayName: "Portico Dev",
        supportDirectoryName: "Portico Dev",
        isLaunchAtLoginAvailable: false,
        bundleIdentifier: "dev.chrisbanes.Portico.Debug"
    )

    static let production = AppVariant(
        displayName: "Portico",
        supportDirectoryName: "Portico",
        isLaunchAtLoginAvailable: true,
        bundleIdentifier: "dev.chrisbanes.Portico"
    )

    init(info: [String: Any]) throws {
        guard let displayName = info["CFBundleDisplayName"] as? String,
              let supportDirectoryName = info["PorticoSupportDirectory"] as? String,
              let rawLaunchAtLoginAvailability = info["PorticoLaunchAtLoginAvailable"] as? String,
              let bundleIdentifier = info["CFBundleIdentifier"] as? String,
              !displayName.isEmpty,
              !supportDirectoryName.isEmpty,
              !bundleIdentifier.isEmpty
        else {
            throw ConfigurationError.invalidMetadata
        }

        switch (displayName, supportDirectoryName, rawLaunchAtLoginAvailability, bundleIdentifier) {
        case ("Portico Dev", "Portico Dev", "NO", "dev.chrisbanes.Portico.Debug"):
            self = .dev
        case ("Portico", "Portico", "YES", "dev.chrisbanes.Portico"):
            self = .production
        default:
            throw ConfigurationError.invalidMetadata
        }
    }

    static func current(bundle: Bundle = .main) throws -> AppVariant {
        try select(info: bundle.infoDictionary ?? [:], expected: expectedForCurrentBuild)
    }

    static func select(info: [String: Any], expected: AppVariant) throws -> AppVariant {
        let variant = try AppVariant(info: info)
        guard variant == expected else {
            throw ConfigurationError.invalidMetadata
        }
        return variant
    }

    private static var expectedForCurrentBuild: AppVariant {
#if DEBUG
        .dev
#else
        .production
#endif
    }
}
