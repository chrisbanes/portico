import Foundation
import XCTest
@testable import PorticoApplication

final class PortalStoreTests: XCTestCase {
    func testFreshDevStoreDoesNotReadOrChangeSiblingProductionRecord() throws {
        let parent = temporaryRoot()
        let productionRoot = parent.appendingPathComponent("Portico", isDirectory: true)
        let devRoot = parent.appendingPathComponent("Portico Dev", isDirectory: true)
        defer { try? FileManager.default.removeItem(at: parent) }
        let productionStore = PortalStore(rootURL: productionRoot)
        let production = InstallationRecord(operationalLogging: .disabled, launchAtLoginOffer: .accepted)
        try productionStore.save(production)
        let productionBytes = try Data(contentsOf: productionStore.installationURL)

        let devStore = PortalStore(rootURL: devRoot)
        XCTAssertEqual(try devStore.prepareForStartup(), .freshInstallation)

        XCTAssertEqual(try Data(contentsOf: productionStore.installationURL), productionBytes)
        XCTAssertEqual(try productionStore.loadInstallation(), production)
        XCTAssertTrue(FileManager.default.fileExists(atPath: devStore.installationURL.path))
        XCTAssertFalse(FileManager.default.fileExists(atPath: productionRoot.appendingPathComponent("tsnet").path))
    }

    func testPrepareForStartupPersistsOnlyGenuinelyNewInstallations() throws {
        let freshStore = PortalStore(rootURL: temporaryRoot())

        XCTAssertEqual(try freshStore.prepareForStartup(), .freshInstallation)
        XCTAssertEqual(try freshStore.loadInstallation(), InstallationRecord())
        XCTAssertEqual(try freshStore.prepareForStartup(), .existingInstallation)

        let migrations: [(KeyPath<PortalStore, URL>, Data)] = [
            (\.legacyConfigurationURL, historicalVersionOneData()),
            (\.versionTwoInstallationURL, Data(#"{"version":2,"portals":[],"alerts":[]}"#.utf8)),
            (\.versionThreeInstallationURL, Data(
                #"{"version":3,"portals":[],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}"#.utf8
            )),
        ]
        for (path, data) in migrations {
            let migratedStore = PortalStore(rootURL: temporaryRoot())
            try FileManager.default.createDirectory(at: migratedStore.rootURL, withIntermediateDirectories: true)
            try data.write(to: migratedStore[keyPath: path])

            XCTAssertEqual(try migratedStore.prepareForStartup(), .existingInstallation)
            XCTAssertTrue(FileManager.default.fileExists(atPath: migratedStore.installationURL.path))
        }

        struct ExpectedFailure: Error {}
        let failedStore = PortalStore(
            rootURL: temporaryRoot(),
            writeData: { _, _ in throw ExpectedFailure() }
        )

        XCTAssertThrowsError(try failedStore.prepareForStartup())
        XCTAssertFalse(FileManager.default.fileExists(atPath: failedStore.installationURL.path))
    }

    func testNewInstallationUsesVersionFiveWithPrivatePublicAccess() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        let installation = InstallationRecord(portals: [makePortal()])
        let encoded = try JSONEncoder().encode(installation)
        let saved = try XCTUnwrap(JSONSerialization.jsonObject(with: encoded) as? [String: Any])
        let portal = try XCTUnwrap((saved["portals"] as? [[String: Any]])?.first)

        XCTAssertEqual(InstallationRecord.currentVersion, 5)
        XCTAssertEqual(installation.version, 5)
        XCTAssertEqual(portal["publicAccess"] as? String, "private")
        XCTAssertEqual(store.installationURL.lastPathComponent, "installation-v5.json")
    }

    func testVersionOneMigrationEnablesLoggingAndStartsLoginOfferUnspent() throws {
        let root = temporaryRoot()
        let store = PortalStore(rootURL: root)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        try historicalVersionOneData().write(to: store.legacyConfigurationURL)

        let installation = try store.loadInstallation()

        XCTAssertEqual(installation.operationalLogging, .enabled)
        XCTAssertEqual(installation.launchAtLoginOffer, .notOffered)
        XCTAssertEqual(installation.portals.count, 1)
        XCTAssertEqual(installation.portals.first?.publicAccess, .private)
        XCTAssertEqual(
            installation,
            InstallationRecord(
                portals: [
                    PortalConfiguration(
                        id: UUID(uuidString: "9f55ca93-d7b3-4eab-a871-310ea576005a")!,
                        name: "hermes",
                        localAppPort: 8787,
                        createdAt: Date(timeIntervalSince1970: 1_786_000_000),
                        desiredState: .enabled,
                        lifecycle: .active,
                        publicAccess: .private
                    )
                ],
                operationalLogging: .enabled,
                launchAtLoginOffer: .notOffered
            )
        )
        XCTAssertFalse(FileManager.default.fileExists(atPath: store.legacyConfigurationURL.path))
    }

    func testPristineVersionTwoMigrationRequiresLoggingChoice() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        try FileManager.default.createDirectory(at: store.rootURL, withIntermediateDirectories: true)
        try Data(#"{"version":2,"portals":[],"alerts":[]}"#.utf8).write(to: store.versionTwoInstallationURL)

        let installation = try store.loadInstallation()

        XCTAssertEqual(installation.operationalLogging, .undecided)
        XCTAssertEqual(installation.launchAtLoginOffer, .notOffered)
        XCTAssertEqual(
            installation,
            InstallationRecord(operationalLogging: .undecided, launchAtLoginOffer: .notOffered)
        )
        XCTAssertFalse(FileManager.default.fileExists(atPath: store.versionTwoInstallationURL.path))
    }

    func testVersionThreeMigrationWritesVersionFiveBeforeRemovingSource() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        try FileManager.default.createDirectory(at: store.rootURL, withIntermediateDirectories: true)
        try Data(
            #"{"version":3,"tailnetBinding":{"name":"opaque-tailnet-id","magicDNSSuffix":"one.ts.net"},"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","localAppPort":8787,"createdAt":807692800,"desiredState":"stopped","lifecycle":"pendingTailnetRejection"}],"alerts":[{"id":"1F93E456-69EC-445A-8374-D7FC5558D0C7","kind":"crossTailnetRejection","portalName":"hermes","assignedName":"hermes-1","expectedMagicDNSSuffix":"one.ts.net","rejectedMagicDNSSuffix":"two.ts.net","createdAt":807692900}],"operationalLogging":"disabled","launchAtLoginOffer":"accepted"}"#.utf8
        ).write(to: store.versionThreeInstallationURL)

        let installation = try store.loadInstallation()

        XCTAssertEqual(
            installation,
            InstallationRecord(
                tailnetBinding: TailnetBinding(name: "opaque-tailnet-id", magicDNSSuffix: "one.ts.net"),
                portals: [
                    PortalConfiguration(
                        id: UUID(uuidString: "9f55ca93-d7b3-4eab-a871-310ea576005a")!,
                        name: "hermes",
                        localAppPort: 8787,
                        createdAt: Date(timeIntervalSince1970: 1_786_000_000),
                        desiredState: .stopped,
                        lifecycle: .pendingTailnetRejection,
                        publicAccess: .private
                    )
                ],
                alerts: [
                    InstallationAlert(
                        id: UUID(uuidString: "1f93e456-69ec-445a-8374-d7fc5558d0c7")!,
                        kind: .crossTailnetRejection,
                        portalName: "hermes",
                        assignedName: "hermes-1",
                        expectedMagicDNSSuffix: "one.ts.net",
                        rejectedMagicDNSSuffix: "two.ts.net",
                        createdAt: Date(timeIntervalSince1970: 1_786_000_100)
                    )
                ],
                operationalLogging: .disabled,
                launchAtLoginOffer: .accepted
            )
        )
        XCTAssertTrue(FileManager.default.fileExists(atPath: store.installationURL.path))
        XCTAssertFalse(FileManager.default.fileExists(atPath: store.versionThreeInstallationURL.path))
    }

    func testVersionTwoMigrationPreservesAllApplicableFactsAndAddsPrivateAccess() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        try FileManager.default.createDirectory(at: store.rootURL, withIntermediateDirectories: true)
        try Data(
            #"{"version":2,"tailnetBinding":{"name":"opaque-tailnet-id","magicDNSSuffix":"one.ts.net"},"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","localAppPort":8787,"createdAt":807692800,"desiredState":"stopped","lifecycle":"pendingRemoval","removalAssignedName":"hermes-1"}],"alerts":[{"id":"1F93E456-69EC-445A-8374-D7FC5558D0C7","kind":"crossTailnetRejection","portalName":"hermes","assignedName":"hermes-1","expectedMagicDNSSuffix":"one.ts.net","rejectedMagicDNSSuffix":"two.ts.net","createdAt":807692900}]}"#.utf8
        ).write(to: store.versionTwoInstallationURL)

        let installation = try store.loadInstallation()

        XCTAssertEqual(
            installation,
            InstallationRecord(
                tailnetBinding: TailnetBinding(name: "opaque-tailnet-id", magicDNSSuffix: "one.ts.net"),
                portals: [
                    PortalConfiguration(
                        id: UUID(uuidString: "9f55ca93-d7b3-4eab-a871-310ea576005a")!,
                        name: "hermes",
                        localAppPort: 8787,
                        createdAt: Date(timeIntervalSince1970: 1_786_000_000),
                        desiredState: .stopped,
                        lifecycle: .pendingRemoval,
                        removalAssignedName: "hermes-1",
                        publicAccess: .private
                    )
                ],
                alerts: [
                    InstallationAlert(
                        id: UUID(uuidString: "1f93e456-69ec-445a-8374-d7fc5558d0c7")!,
                        kind: .crossTailnetRejection,
                        portalName: "hermes",
                        assignedName: "hermes-1",
                        expectedMagicDNSSuffix: "one.ts.net",
                        rejectedMagicDNSSuffix: "two.ts.net",
                        createdAt: Date(timeIntervalSince1970: 1_786_000_100)
                    )
                ],
                operationalLogging: .enabled,
                launchAtLoginOffer: .notOffered
            )
        )
    }

    func testInvalidVersionThreeDestinationFailsClosedWithoutRemovingSource() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        try FileManager.default.createDirectory(at: store.rootURL, withIntermediateDirectories: true)
        let source = Data(
            #"{"version":3,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","localAppPort":0,"createdAt":807692800,"lifecycle":"active"}],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}"#.utf8
        )
        try source.write(to: store.versionThreeInstallationURL)

        XCTAssertThrowsError(try store.loadInstallation())
        XCTAssertEqual(try Data(contentsOf: store.versionThreeInstallationURL), source)
        XCTAssertFalse(FileManager.default.fileExists(atPath: store.installationURL.path))
    }

    func testInvalidVersionOneDestinationFailsClosedWithoutRemovingSource() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        try FileManager.default.createDirectory(at: store.rootURL, withIntermediateDirectories: true)
        let source = Data(
            #"{"version":1,"portal":{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","localAppPort":0,"createdAt":807692800}}"#.utf8
        )
        try source.write(to: store.legacyConfigurationURL)

        XCTAssertThrowsError(try store.loadInstallation())
        XCTAssertEqual(try Data(contentsOf: store.legacyConfigurationURL), source)
        XCTAssertFalse(FileManager.default.fileExists(atPath: store.installationURL.path))
    }

    func testInvalidHistoricalRecordsDoNotCreateVersionFiveOrRemoveTheirSource() throws {
        let records: [(KeyPath<PortalStore, URL>, Data)] = [
            (\.legacyConfigurationURL, Data(
                #"{"version":1,"portal":{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"Invalid Name","localAppPort":8787,"createdAt":807692800}}"#.utf8
            )),
            (\.versionTwoInstallationURL, Data(
                #"{"version":2,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","localAppPort":8787,"createdAt":807692800,"lifecycle":"active"},{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"atlas","localAppPort":8788,"createdAt":807692801,"lifecycle":"active"}],"alerts":[]}"#.utf8
            )),
            (\.versionThreeInstallationURL, Data(
                #"{"version":3,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"Invalid Name","localAppPort":8787,"createdAt":807692800,"lifecycle":"active"}],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}"#.utf8
            )),
            (\.versionThreeInstallationURL, Data(
                #"{"version":3,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","localAppPort":8787,"createdAt":807692800,"lifecycle":"pendingTailnetRejection"}],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}"#.utf8
            )),
        ]

        for (path, source) in records {
            let store = PortalStore(rootURL: temporaryRoot())
            try FileManager.default.createDirectory(at: store.rootURL, withIntermediateDirectories: true)
            try source.write(to: store[keyPath: path])

            XCTAssertThrowsError(try store.loadInstallation())
            XCTAssertEqual(try Data(contentsOf: store[keyPath: path]), source)
            XCTAssertFalse(FileManager.default.fileExists(atPath: store.installationURL.path))
        }
    }

    func testNonPristineVersionTwoMigrationsPreserveExistingLoggingBehavior() throws {
        let records = [
            #"{"version":2,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","localAppPort":8787,"createdAt":807692800,"lifecycle":"active"}],"alerts":[]}"#,
            #"{"version":2,"tailnetBinding":{"name":"opaque-tailnet-id","magicDNSSuffix":"one.ts.net"},"portals":[],"alerts":[]}"#,
            #"{"version":2,"portals":[],"alerts":[{"id":"1F93E456-69EC-445A-8374-D7FC5558D0C7","kind":"crossTailnetRejection","portalName":"hermes","createdAt":807692800}]}"#,
        ]

        for record in records {
            let store = PortalStore(rootURL: temporaryRoot())
            try FileManager.default.createDirectory(at: store.rootURL, withIntermediateDirectories: true)
            try Data(record.utf8).write(to: store.versionTwoInstallationURL)

            let installation = try store.loadInstallation()

            XCTAssertEqual(installation.operationalLogging, .enabled, record)
            XCTAssertEqual(installation.launchAtLoginOffer, .notOffered, record)
            XCTAssertTrue(installation.portals.allSatisfy { $0.publicAccess == .private }, record)
        }
    }

    func testValidVersionFiveWinsAfterInterruptedOlderCleanup() throws {
        let root = temporaryRoot()
        let store = PortalStore(rootURL: root)
        let staleVersionFour = Data(
            #"{"version":4,"portals":[],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}"#.utf8
        )
        let authoritative = InstallationRecord(
            tailnetBinding: TailnetBinding(name: "opaque-tailnet-id", magicDNSSuffix: "example.ts.net"),
            operationalLogging: .disabled,
            launchAtLoginOffer: .accepted
        )
        try store.save(authoritative)
        try FileManager.default.createDirectory(at: store.rootURL, withIntermediateDirectories: true)
        try staleVersionFour.write(to: root.appendingPathComponent("installation-v4.json"))
        try Data(#"{"version":2,"portals":[],"alerts":[]}"#.utf8).write(to: store.versionTwoInstallationURL)
        try historicalVersionOneData(name: "stale").write(to: store.legacyConfigurationURL)

        XCTAssertEqual(try store.loadInstallation(), authoritative)
        XCTAssertFalse(FileManager.default.fileExists(atPath: root.appendingPathComponent("installation-v4.json").path))
        XCTAssertFalse(FileManager.default.fileExists(atPath: store.versionTwoInstallationURL.path))
        XCTAssertFalse(FileManager.default.fileExists(atPath: store.legacyConfigurationURL.path))
    }

    func testVersionFourRemoteAppMigrationPreservesInstallationFactsAndBecomesPrivate() throws {
        let root = temporaryRoot()
        let historicalURL = root.appendingPathComponent("installation-v4.json", isDirectory: false)
        let historicalVersionThreeURL = root.appendingPathComponent("installation-v3.json", isDirectory: false)
        let historicalVersionTwoURL = root.appendingPathComponent("installation-v2.json", isDirectory: false)
        let historicalVersionOneURL = root.appendingPathComponent("portal-v1.json", isDirectory: false)
        let olderSources = [
            historicalVersionThreeURL,
            historicalVersionTwoURL,
            historicalVersionOneURL,
        ]
        var historicalWasPresentWhenCurrentRecordWasWritten = false
        let store = PortalStore(rootURL: root, writeData: { data, destinationURL in
            try data.write(to: destinationURL, options: .atomic)
            historicalWasPresentWhenCurrentRecordWasWritten = FileManager.default.fileExists(atPath: destinationURL.path)
                && FileManager.default.fileExists(atPath: historicalURL.path)
                && olderSources.allSatisfy { FileManager.default.fileExists(atPath: $0.path) }
        })
        let historical = Data(
            #"{"version":4,"tailnetBinding":{"name":"opaque-tailnet-id","magicDNSSuffix":"one.ts.net"},"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","destination":{"kind":"remoteApp","scheme":"https","host":"app.example.com","port":8443},"createdAt":807692800,"desiredState":"stopped","lifecycle":"pendingRemoval","removalAssignedName":"hermes-1"}],"alerts":[{"id":"1F93E456-69EC-445A-8374-D7FC5558D0C7","kind":"crossTailnetRejection","portalName":"hermes","assignedName":"hermes-1","expectedMagicDNSSuffix":"one.ts.net","rejectedMagicDNSSuffix":"two.ts.net","createdAt":807692900}],"operationalLogging":"disabled","launchAtLoginOffer":"accepted"}"#.utf8
        )
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        try historical.write(to: historicalURL)
        try Data(
            #"{"version":3,"portals":[],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}"#.utf8
        ).write(to: historicalVersionThreeURL)
        try Data(#"{"version":2,"portals":[],"alerts":[]}"#.utf8).write(to: historicalVersionTwoURL)
        try historicalVersionOneData(name: "stale").write(to: historicalVersionOneURL)

        let installation = try store.loadInstallation()
        let expectedPortal = PortalConfiguration(
            id: UUID(uuidString: "9f55ca93-d7b3-4eab-a871-310ea576005a")!,
            name: "hermes",
            destination: .remoteApp(scheme: .https, host: "app.example.com", port: 8443),
            createdAt: Date(timeIntervalSince1970: 1_786_000_000),
            desiredState: .stopped,
            lifecycle: .pendingRemoval,
            removalAssignedName: "hermes-1",
            publicAccess: .private
        )
        let expectedAlert = InstallationAlert(
            id: UUID(uuidString: "1f93e456-69ec-445a-8374-d7fc5558d0c7")!,
            kind: .crossTailnetRejection,
            portalName: "hermes",
            assignedName: "hermes-1",
            expectedMagicDNSSuffix: "one.ts.net",
            rejectedMagicDNSSuffix: "two.ts.net",
            createdAt: Date(timeIntervalSince1970: 1_786_000_100)
        )
        let expectedInstallation = InstallationRecord(
            tailnetBinding: TailnetBinding(name: "opaque-tailnet-id", magicDNSSuffix: "one.ts.net"),
            portals: [expectedPortal],
            alerts: [expectedAlert],
            operationalLogging: .disabled,
            launchAtLoginOffer: .accepted
        )

        XCTAssertEqual(installation, expectedInstallation)
        XCTAssertTrue(historicalWasPresentWhenCurrentRecordWasWritten)
        XCTAssertTrue(FileManager.default.fileExists(atPath: store.installationURL.path))
        XCTAssertFalse(FileManager.default.fileExists(atPath: historicalURL.path))
        XCTAssertTrue(olderSources.allSatisfy { !FileManager.default.fileExists(atPath: $0.path) })
    }

    func testCorruptVersionFiveFailsClosedWithoutFallingBack() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        try FileManager.default.createDirectory(at: store.rootURL, withIntermediateDirectories: true)
        let corrupt = Data("not-json".utf8)
        try corrupt.write(to: store.installationURL)
        try Data(#"{"version":2,"portals":[],"alerts":[]}"#.utf8).write(to: store.versionTwoInstallationURL)

        XCTAssertThrowsError(try store.loadInstallation())
        XCTAssertEqual(try Data(contentsOf: store.installationURL), corrupt)
        XCTAssertTrue(FileManager.default.fileExists(atPath: store.versionTwoInstallationURL.path))
    }

    func testUnsupportedVersionFiveFailsClosedWithoutFallingBack() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        try FileManager.default.createDirectory(at: store.rootURL, withIntermediateDirectories: true)
        let unsupported = Data(
            #"{"version":6,"operationalLogging":"disabled","launchAtLoginOffer":"accepted","portals":[],"alerts":[]}"#.utf8
        )
        try unsupported.write(to: store.installationURL)
        try Data(#"{"version":2,"portals":[],"alerts":[]}"#.utf8).write(to: store.versionTwoInstallationURL)

        XCTAssertThrowsError(try store.loadInstallation())
        XCTAssertEqual(try Data(contentsOf: store.installationURL), unsupported)
        XCTAssertTrue(FileManager.default.fileExists(atPath: store.versionTwoInstallationURL.path))
    }

    func testInvalidVersionFivePublicAccessFailsClosedWithoutChangingAnyStoredBytes() throws {
        let records = [
            #"{"version":5,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","destination":{"kind":"localApp","port":8787},"createdAt":807692800,"desiredState":"enabled","lifecycle":"active"}],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}"#,
            #"{"version":5,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","destination":{"kind":"localApp","port":8787},"publicAccess":null,"createdAt":807692800,"desiredState":"enabled","lifecycle":"active"}],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}"#,
            #"{"version":5,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","destination":{"kind":"localApp","port":8787},"publicAccess":true,"createdAt":807692800,"desiredState":"enabled","lifecycle":"active"}],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}"#,
            #"{"version":5,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","destination":{"kind":"localApp","port":8787},"publicAccess":"funnel","createdAt":807692800,"desiredState":"enabled","lifecycle":"active"}],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}"#,
            #"{"version":5,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","destination":{"kind":"remoteApp","scheme":"https","host":"app.example.com","port":8443},"publicAccess":"public","createdAt":807692800,"desiredState":"enabled","lifecycle":"active"}],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}"#,
        ]
        let olderVersionFour = Data(
            #"{"version":4,"portals":[],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}"#.utf8
        )

        for record in records {
            let store = PortalStore(rootURL: temporaryRoot())
            let authoritative = Data(record.utf8)
            let olderVersionOne = historicalVersionOneData(name: "stale")
            try FileManager.default.createDirectory(at: store.rootURL, withIntermediateDirectories: true)
            try authoritative.write(to: store.installationURL)
            try olderVersionFour.write(to: store.rootURL.appendingPathComponent("installation-v4.json"))
            try olderVersionOne.write(to: store.legacyConfigurationURL)

            XCTAssertThrowsError(try store.loadInstallation(), record)
            XCTAssertEqual(try Data(contentsOf: store.installationURL), authoritative)
            XCTAssertEqual(
                try Data(contentsOf: store.rootURL.appendingPathComponent("installation-v4.json")),
                olderVersionFour
            )
            XCTAssertEqual(try Data(contentsOf: store.legacyConfigurationURL), olderVersionOne)
        }
    }

    func testSavingPublicRemoteAppFailsBeforeReplacingVersionFiveBytes() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        let authoritative = Data(
            #"{"version":5,"portals":[],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}"#.utf8
        )
        try FileManager.default.createDirectory(at: store.rootURL, withIntermediateDirectories: true)
        try authoritative.write(to: store.installationURL)
        let invalidPortal = PortalConfiguration(
            id: UUID(uuidString: "9f55ca93-d7b3-4eab-a871-310ea576005a")!,
            name: "hermes",
            destination: .remoteApp(scheme: .https, host: "app.example.com", port: 8443),
            createdAt: Date(timeIntervalSince1970: 1_786_000_000),
            publicAccess: .public
        )

        XCTAssertThrowsError(try store.save(InstallationRecord(portals: [invalidPortal])))
        XCTAssertEqual(try Data(contentsOf: store.installationURL), authoritative)
    }

    func testFailedVersionTwoMigrationPreservesOlderAuthority() throws {
        struct ExpectedFailure: Error {}
        let root = temporaryRoot()
        let versionTwoURL = root.appendingPathComponent("installation-v2.json")
        let source = Data(#"{"version":2,"portals":[],"alerts":[]}"#.utf8)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        try source.write(to: versionTwoURL)
        let store = PortalStore(rootURL: root, writeData: { _, _ in throw ExpectedFailure() })

        XCTAssertThrowsError(try store.loadInstallation())
        XCTAssertEqual(try Data(contentsOf: versionTwoURL), source)
        XCTAssertFalse(FileManager.default.fileExists(atPath: store.installationURL.path))
    }

    func testFailedVersionFourWritePreservesAllOlderAuthoritativeBytes() throws {
        struct ExpectedFailure: Error {}
        let root = temporaryRoot()
        let versionFourURL = root.appendingPathComponent("installation-v4.json")
        let versionThreeURL = root.appendingPathComponent("installation-v3.json")
        let versionTwoURL = root.appendingPathComponent("installation-v2.json")
        let versionOneURL = root.appendingPathComponent("portal-v1.json")
        let versionFour = Data(
            #"{"version":4,"portals":[],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}"#.utf8
        )
        let versionThree = Data(
            #"{"version":3,"portals":[],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}"#.utf8
        )
        let versionTwo = Data(#"{"version":2,"portals":[],"alerts":[]}"#.utf8)
        let versionOne = historicalVersionOneData(name: "older")
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        try versionFour.write(to: versionFourURL)
        try versionThree.write(to: versionThreeURL)
        try versionTwo.write(to: versionTwoURL)
        try versionOne.write(to: versionOneURL)
        let store = PortalStore(rootURL: root, writeData: { _, _ in throw ExpectedFailure() })

        XCTAssertThrowsError(try store.loadInstallation())
        XCTAssertEqual(try Data(contentsOf: versionFourURL), versionFour)
        XCTAssertEqual(try Data(contentsOf: versionThreeURL), versionThree)
        XCTAssertEqual(try Data(contentsOf: versionTwoURL), versionTwo)
        XCTAssertEqual(try Data(contentsOf: versionOneURL), versionOne)
        XCTAssertFalse(FileManager.default.fileExists(atPath: store.installationURL.path))
    }

    func testMigratesValidVersionOnePortalIntoUnboundVersionTwoInstallation() throws {
        let root = temporaryRoot()
        let store = PortalStore(rootURL: root)
        let legacyURL = root.appendingPathComponent("portal-v1.json", isDirectory: false)
        let installationURL = store.installationURL
        let portal = PortalConfiguration(
            id: UUID(uuidString: "9f55ca93-d7b3-4eab-a871-310ea576005a")!,
            name: "hermes",
            localAppPort: 8787,
            createdAt: Date(timeIntervalSince1970: 1_786_000_000)
        )
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        try historicalVersionOneData().write(to: legacyURL)

        _ = try store.load()

        XCTAssertTrue(FileManager.default.fileExists(atPath: installationURL.path))
        XCTAssertFalse(FileManager.default.fileExists(atPath: legacyURL.path))
        XCTAssertEqual(try permissions(of: root), 0o700)
        XCTAssertEqual(try permissions(of: installationURL), 0o600)
        XCTAssertEqual(
            try store.loadInstallation(),
            InstallationRecord(portals: [portal], operationalLogging: .enabled)
        )
    }

    func testFailedVersionTwoWritePreservesVersionOneSource() throws {
        struct ExpectedFailure: Error {}
        let root = temporaryRoot()
        let legacyURL = root.appendingPathComponent("portal-v1.json", isDirectory: false)
        let store = PortalStore(rootURL: root, writeData: { _, _ in throw ExpectedFailure() })
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        let source = historicalVersionOneData()
        try source.write(to: legacyURL)

        XCTAssertThrowsError(try store.loadInstallation())
        XCTAssertEqual(try Data(contentsOf: legacyURL), source)
        XCTAssertFalse(FileManager.default.fileExists(atPath: store.installationURL.path))
    }

    func testValidVersionTwoIsAuthoritativeAndRemovesStaleVersionOne() throws {
        let root = temporaryRoot()
        let store = PortalStore(rootURL: root)
        let authoritative = InstallationRecord(
            tailnetBinding: TailnetBinding(name: "opaque-tailnet-id", magicDNSSuffix: "example.ts.net"),
            portals: [makePortal(name: "authoritative")]
        )
        try store.save(authoritative)
        try historicalVersionOneData(name: "stale").write(to: store.legacyConfigurationURL)

        XCTAssertEqual(try store.loadInstallation(), authoritative)
        XCTAssertFalse(FileManager.default.fileExists(atPath: store.legacyConfigurationURL.path))
    }

    func testInstallationRoundTripsPortalLifecycleBindingAndAlert() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        var pending = makePortal()
        pending.lifecycle = .pendingTailnetRejection
        let alert = InstallationAlert(
            id: UUID(uuidString: "1f93e456-69ec-445a-8374-d7fc5558d0c7")!,
            kind: .crossTailnetRejection,
            portalName: "hermes",
            assignedName: "hermes-1",
            expectedMagicDNSSuffix: "one.ts.net",
            rejectedMagicDNSSuffix: "two.ts.net",
            createdAt: Date(timeIntervalSince1970: 1_786_000_100)
        )
        let installation = InstallationRecord(
            tailnetBinding: TailnetBinding(name: "opaque-tailnet-id", magicDNSSuffix: "one.ts.net"),
            portals: [pending],
            alerts: [alert]
        )

        try store.save(installation)

        XCTAssertEqual(try store.loadInstallation(), installation)
        XCTAssertEqual(try permissions(of: store.rootURL), 0o700)
        XCTAssertEqual(try permissions(of: store.installationURL), 0o600)
    }

    func testSavingReplacesAnExistingFileWithSecurePermissions() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        try FileManager.default.createDirectory(at: store.rootURL, withIntermediateDirectories: true)
        try Data("stale".utf8).write(to: store.installationURL)
        try FileManager.default.setAttributes(
            [.posixPermissions: 0o644],
            ofItemAtPath: store.installationURL.path
        )

        try store.save(InstallationRecord(operationalLogging: .disabled))

        XCTAssertEqual(try permissions(of: store.installationURL), 0o600)
        XCTAssertEqual(
            try store.loadInstallation(),
            InstallationRecord(operationalLogging: .disabled)
        )
    }

    func testHistoricalVersionTwoDefaultsToEnabledAndNextSavePersistsDesiredState() throws {
        let root = temporaryRoot()
        let versionTwoURL = root.appendingPathComponent("installation-v2.json", isDirectory: false)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        let historical = Data(
            #"{"version":2,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","localAppPort":8787,"createdAt":807692800,"lifecycle":"active"},{"id":"5EA74329-3144-4BA2-925F-138D14D61FCC","name":"atlas","localAppPort":8788,"createdAt":807692801,"lifecycle":"active"}],"alerts":[]}"#.utf8
        )
        try historical.write(to: versionTwoURL)
        let store = PortalStore(rootURL: root)

        var installation = try store.loadInstallation()

        XCTAssertEqual(installation.portals.map(\.desiredState), [.enabled, .enabled])
        installation.portals[1].desiredState = .stopped
        try store.save(installation)

        let saved = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(contentsOf: store.installationURL)) as? [String: Any])
        XCTAssertEqual(saved["version"] as? Int, 5)
        let portals = try XCTUnwrap(saved["portals"] as? [[String: Any]])
        XCTAssertEqual(portals.map { $0["desiredState"] as? String }, ["enabled", "stopped"])
        XCTAssertEqual(saved["operationalLogging"] as? String, "enabled")
        XCTAssertEqual(try store.loadInstallation(), installation)
    }

    func testHistoricalVersionTwoDefaultsRemovalAssignedNameAndPendingRemovalRoundTrips() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        try FileManager.default.createDirectory(at: store.rootURL, withIntermediateDirectories: true)
        let historical = Data(
            #"{"version":2,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","localAppPort":8787,"createdAt":807692800,"desiredState":"enabled","lifecycle":"active"}],"alerts":[]}"#.utf8
        )
        try historical.write(to: store.versionTwoInstallationURL)

        var installation = try store.loadInstallation()

        XCTAssertEqual(installation.version, 5)
        XCTAssertNil(installation.portals[0].removalAssignedName)

        installation.portals[0].lifecycle = .pendingRemoval
        installation.portals[0].removalAssignedName = "hermes-1"
        try store.save(installation)

        XCTAssertEqual(try store.loadInstallation(), installation)
        XCTAssertEqual(try store.loadInstallation().version, 5)
    }

    func testExplicitNullDesiredStateIsCorruptRatherThanDefaulting() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        try FileManager.default.createDirectory(at: store.rootURL, withIntermediateDirectories: true)
        let corrupt = Data(
            #"{"version":2,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","localAppPort":8787,"createdAt":807692800,"desiredState":null,"lifecycle":"active"}],"alerts":[]}"#.utf8
        )
        try corrupt.write(to: store.versionTwoInstallationURL)

        XCTAssertThrowsError(try store.loadInstallation())
        XCTAssertEqual(try Data(contentsOf: store.versionTwoInstallationURL), corrupt)
    }

    func testSavedPortalReloadsWithSameImmutableDefinition() throws {
        let root = temporaryRoot()
        let store = PortalStore(rootURL: root)
        let portal = PortalConfiguration(
            id: UUID(uuidString: "9f55ca93-d7b3-4eab-a871-310ea576005a")!,
            name: "hermes",
            localAppPort: 8787,
            createdAt: Date(timeIntervalSince1970: 1_786_000_000)
        )

        try store.save(portal)

        XCTAssertEqual(try store.load(), portal)
        XCTAssertEqual(try permissions(of: root), 0o700)
        XCTAssertEqual(try permissions(of: store.installationURL), 0o600)
        XCTAssertThrowsError(try store.save(portal))
    }

    func testVersionFivePersistenceUsesTheTaggedDestinationAndPrivateMode() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        let portal = PortalConfiguration(
            id: UUID(uuidString: "9f55ca93-d7b3-4eab-a871-310ea576005a")!,
            name: "hermes",
            localAppPort: 8787,
            createdAt: Date(timeIntervalSince1970: 1_786_000_000)
        )

        try store.save(InstallationRecord(portals: [portal]))

        let saved = try XCTUnwrap(
            JSONSerialization.jsonObject(with: Data(contentsOf: store.installationURL)) as? [String: Any]
        )
        let savedPortal = try XCTUnwrap((saved["portals"] as? [[String: Any]])?.first)
        XCTAssertEqual(saved["version"] as? Int, 5)
        XCTAssertEqual(savedPortal["publicAccess"] as? String, "private")
        XCTAssertNil(savedPortal["localAppPort"])
        XCTAssertEqual((savedPortal["destination"] as? [String: Any])?["kind"] as? String, "localApp")
        XCTAssertEqual((savedPortal["destination"] as? [String: Any])?["port"] as? Int, 8787)
        XCTAssertEqual(try store.loadInstallation().portals.first?.destination, portal.destination)
    }

    func testVersionFiveAcceptsPublicLocalApp() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        var portal = makePortal()
        portal.publicAccess = .public

        try store.save(InstallationRecord(portals: [portal]))

        XCTAssertEqual(try store.loadInstallation().portals.first?.publicAccess, .public)
    }

    func testVersionFourRejectsAnInvalidLocalAppPort() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        let versionFourURL = store.rootURL.appendingPathComponent("installation-v4.json", isDirectory: false)
        try FileManager.default.createDirectory(at: store.rootURL, withIntermediateDirectories: true)
        try Data(
            #"{"version":4,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","destination":{"kind":"localApp","port":0},"createdAt":807692800,"desiredState":"enabled","lifecycle":"active"}],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}"#.utf8
        ).write(to: versionFourURL)

        XCTAssertThrowsError(try store.loadInstallation())
        XCTAssertTrue(FileManager.default.fileExists(atPath: versionFourURL.path))
        XCTAssertFalse(FileManager.default.fileExists(atPath: store.installationURL.path))
    }

    func testInvalidVersionFourRecordsFailClosedBeforeOlderCleanup() throws {
        let records = [
            #"{"version":4,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","destination":{"kind":"localApp","port":8787},"createdAt":807692800,"lifecycle":"active"}],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}"#,
            #"{"version":4,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","destination":{"kind":"localApp","port":8787},"createdAt":807692800,"desiredState":"enabled","lifecycle":"active"},{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"atlas","destination":{"kind":"localApp","port":8788},"createdAt":807692801,"desiredState":"enabled","lifecycle":"active"}],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}"#,
            #"{"version":4,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"Invalid Name","destination":{"kind":"localApp","port":8787},"createdAt":807692800,"desiredState":"enabled","lifecycle":"active"}],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}"#,
            #"{"version":4,"tailnetBinding":{"name":"opaque-tailnet-id","magicDNSSuffix":"not a suffix"},"portals":[],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}"#,
            #"{"version":4,"tailnetBinding":{"name":"","magicDNSSuffix":"example.ts.net"},"portals":[],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}"#,
            #"{"version":4,"portals":[],"alerts":[{"id":"1F93E456-69EC-445A-8374-D7FC5558D0C7","kind":"crossTailnetRejection","portalName":"hermes","assignedName":"not a name","createdAt":807692800}],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}"#,
            #"{"version":4,"portals":[],"alerts":[{"id":"1F93E456-69EC-445A-8374-D7FC5558D0C7","kind":"crossTailnetRejection","portalName":"hermes","createdAt":807692800},{"id":"1F93E456-69EC-445A-8374-D7FC5558D0C7","kind":"crossTailnetRejection","portalName":"atlas","createdAt":807692801}],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}"#,
            #"{"version":4,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","destination":{"kind":"localApp","port":8787},"createdAt":807692800,"desiredState":"enabled","lifecycle":"active","removalAssignedName":"hermes-1"}],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}"#,
        ]

        for record in records {
            let store = PortalStore(rootURL: temporaryRoot())
            let authoritative = Data(record.utf8)
            let versionFourURL = store.rootURL.appendingPathComponent("installation-v4.json", isDirectory: false)
            try FileManager.default.createDirectory(at: store.rootURL, withIntermediateDirectories: true)
            try authoritative.write(to: versionFourURL)
            try Data(#"{"version":2,"portals":[],"alerts":[]}"#.utf8).write(to: store.versionTwoInstallationURL)

            XCTAssertThrowsError(try store.loadInstallation())
            XCTAssertEqual(try Data(contentsOf: versionFourURL), authoritative)
            XCTAssertTrue(FileManager.default.fileExists(atPath: store.versionTwoInstallationURL.path))
            XCTAssertFalse(FileManager.default.fileExists(atPath: store.installationURL.path))
        }
    }

    func testSavingFirstPortalPreservesInstallationBindingAndAlerts() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        let alert = InstallationAlert(
            id: UUID(uuidString: "1f93e456-69ec-445a-8374-d7fc5558d0c7")!,
            kind: .crossTailnetRejection,
            portalName: "atlas",
            assignedName: nil,
            expectedMagicDNSSuffix: "one.ts.net",
            rejectedMagicDNSSuffix: nil,
            createdAt: Date(timeIntervalSince1970: 1_786_000_100)
        )
        let binding = TailnetBinding(name: "opaque-tailnet-id", magicDNSSuffix: "one.ts.net")
        try store.save(InstallationRecord(tailnetBinding: binding, alerts: [alert]))

        let portal = makePortal()
        try store.save(portal)

        XCTAssertEqual(
            try store.loadInstallation(),
            InstallationRecord(tailnetBinding: binding, portals: [portal], alerts: [alert])
        )
    }

    func testMissingConfigurationLoadsAsNil() throws {
        XCTAssertNil(try PortalStore(rootURL: temporaryRoot()).load())
    }

    func testCorruptConfigurationFailsWithoutReplacement() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        try FileManager.default.createDirectory(at: store.rootURL, withIntermediateDirectories: true)
        let corrupt = Data("not-json".utf8)
        try corrupt.write(to: store.configurationURL)

        XCTAssertThrowsError(try store.load())
        XCTAssertEqual(try Data(contentsOf: store.configurationURL), corrupt)
    }

    func testUnsupportedVersionFailsWithoutReplacement() throws {
        let store = PortalStore(rootURL: temporaryRoot())
        try FileManager.default.createDirectory(at: store.rootURL, withIntermediateDirectories: true)
        let unsupported = Data(#"{"version":2,"portal":{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","localAppPort":8787,"createdAt":"2026-08-03T00:00:00Z"}}"#.utf8)
        try unsupported.write(to: store.configurationURL)

        XCTAssertThrowsError(try store.load())
        XCTAssertEqual(try Data(contentsOf: store.configurationURL), unsupported)
    }

    private func temporaryRoot() -> URL {
        FileManager.default.temporaryDirectory
            .appendingPathComponent("PorticoTests-\(UUID().uuidString)", isDirectory: true)
    }

    private func makePortal(name: String = "hermes") -> PortalConfiguration {
        PortalConfiguration(
            id: UUID(uuidString: "9f55ca93-d7b3-4eab-a871-310ea576005a")!,
            name: name,
            localAppPort: 8787,
            createdAt: Date(timeIntervalSince1970: 1_786_000_000)
        )
    }

    private func historicalVersionOneData(name: String = "hermes") -> Data {
        Data(
            #"{"version":1,"portal":{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"\#(name)","localAppPort":8787,"createdAt":807692800}}"#.utf8
        )
    }

    private func permissions(of url: URL) throws -> Int {
        let attributes = try FileManager.default.attributesOfItem(atPath: url.path)
        return (attributes[.posixPermissions] as? NSNumber)?.intValue ?? -1
    }
}
