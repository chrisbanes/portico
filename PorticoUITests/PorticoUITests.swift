import XCTest

final class PorticoUITests: XCTestCase {
    func testFirstRunGuidanceAndLoggingGate() {
        let app = launch(scenario: "first-run")

        let overview = app.descendants(matching: .any)["management-overview"]
        XCTAssertTrue(overview.waitForExistence(timeout: 3), app.debugDescription)
        XCTAssertTrue(app.staticTexts["overview-helper-state"].exists)
        XCTAssertTrue(app.staticTexts["overview-tailnet"].exists)
        XCTAssertTrue(app.descendants(matching: .any)["overview-first-portal-guidance"].exists)
        XCTAssertFalse(app.buttons["overview-add-portal"].isEnabled)
        XCTAssertTrue(app.buttons["overview-logging-enabled"].exists)
        XCTAssertTrue(app.buttons["overview-logging-disabled"].exists)

        app.buttons["overview-logging-enabled"].click()
        XCTAssertTrue(app.staticTexts["Connect Your Tailnet"].waitForExistence(timeout: 3), app.debugDescription)
        XCTAssertTrue(
            app.staticTexts["overview-first-portal-authentication-guidance"].waitForExistence(timeout: 3),
            app.debugDescription
        )
        XCTAssertTrue(app.buttons["overview-add-portal"].isEnabled)
        let emptyStateAddPortal = app.buttons["overview-empty-add-portal"]
        XCTAssertTrue(emptyStateAddPortal.waitForExistence(timeout: 3), app.debugDescription)
        emptyStateAddPortal.click()
        XCTAssertTrue(app.descendants(matching: .any)["add-portal-sheet"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.buttons["add-portal-next"].isEnabled)
        XCTAssertFalse(app.buttons["add-portal"].exists)

        app.buttons["add-portal-cancel"].click()
        XCTAssertFalse(app.descendants(matching: .any)["add-portal-sheet"].waitForExistence(timeout: 1))
        openMenuBarExtra(app)
        XCTAssertFalse(app.descendants(matching: .any)["add-portal-sheet"].exists)
        XCTAssertFalse(app.buttons["add-portal"].exists)
    }

    func testOverviewToolbarOpensTheSameAddPortalSheet() {
        let app = launch(scenario: "first-run")

        let overview = app.descendants(matching: .any)["management-overview"]
        XCTAssertTrue(overview.waitForExistence(timeout: 3), app.debugDescription)
        app.buttons["overview-logging-enabled"].click()
        XCTAssertTrue(app.staticTexts["Connect Your Tailnet"].waitForExistence(timeout: 3))
        app.buttons["overview-add-portal"].click()
        XCTAssertTrue(app.descendants(matching: .any)["add-portal-sheet"].waitForExistence(timeout: 3))
        app.buttons["add-portal-next"].click()
        let portalName = app.textFields["portal-name-field"]
        XCTAssertTrue(portalName.waitForExistence(timeout: 3), app.debugDescription)
        portalName.click()
        portalName.typeText("overview-test")
        let localAppPort = app.textFields["local-app-port-field"]
        localAppPort.click()
        localAppPort.typeKey("a", modifierFlags: .command)
        localAppPort.typeText("8080")
        XCTAssertTrue(app.buttons["add-portal"].isEnabled)
        app.typeKey(.escape, modifierFlags: [])
        XCTAssertFalse(app.descendants(matching: .any)["add-portal-sheet"].waitForExistence(timeout: 1))
        app.buttons["overview-add-portal"].click()
        app.buttons["add-portal-next"].click()
        XCTAssertTrue(app.textFields["portal-name-field"].waitForExistence(timeout: 3))
        XCTAssertEqual(app.textFields["portal-name-field"].value as? String, "")
        XCTAssertEqual(app.textFields["local-app-port-field"].value as? String, "")
    }

    func testManagementSidebarOrdersAndSelectsActivePortals() {
        let app = launch(scenario: "management")
        app.typeKey("o", modifierFlags: [.command, .shift])

        XCTAssertTrue(app.descendants(matching: .any)["management-overview"].waitForExistence(timeout: 3))
        let firstPortal = app.buttons["management-sidebar-portal-first-portal"]
        let secondPortal = app.buttons["management-sidebar-portal-second-portal"]
        XCTAssertTrue(firstPortal.waitForExistence(timeout: 3), app.debugDescription)
        XCTAssertTrue(secondPortal.waitForExistence(timeout: 3), app.debugDescription)
        let overview = app.descendants(matching: .any)["management-sidebar-overview"]
        XCTAssertTrue(overview.exists)
        XCTAssertLessThan(overview.frame.minY, firstPortal.frame.minY)
        XCTAssertLessThan(firstPortal.frame.minY, secondPortal.frame.minY)
        XCTAssertTrue(firstPortal.label.contains("Online"))
        XCTAssertTrue(secondPortal.label.contains("Connecting"))

        app.descendants(matching: .any)["management-sidebar-settings"].click()
        XCTAssertTrue(app.radioButtons["Allow operational-support logging"].waitForExistence(timeout: 3))
        XCTAssertFalse(app.buttons["overview-add-portal"].exists)
        app.descendants(matching: .any)["management-sidebar-overview"].click()
        XCTAssertTrue(app.buttons["overview-add-portal"].waitForExistence(timeout: 3))

        firstPortal.click()
        XCTAssertTrue(waitForValue("first-portal", element: app.staticTexts["selected-portal-name"], timeout: 3))
        secondPortal.click()
        XCTAssertTrue(waitForValue("second-portal", element: app.staticTexts["selected-portal-name"], timeout: 3))

        app.buttons["overview-add-portal"].click()
        XCTAssertTrue(app.descendants(matching: .any)["add-portal-sheet"].waitForExistence(timeout: 3))
        app.buttons["add-portal-cancel"].click()
        XCTAssertFalse(app.descendants(matching: .any)["add-portal-sheet"].waitForExistence(timeout: 1))
    }

    func testManagementSidebarRetainsDurablePortalRecords() {
        let app = launch(scenario: "durable-management")
        app.typeKey("o", modifierFlags: [.command, .shift])

        XCTAssertTrue(app.descendants(matching: .any)["management-overview"].waitForExistence(timeout: 3))
        let active = app.buttons["management-sidebar-portal-durable-active"]
        let removing = app.buttons["management-sidebar-portal-durable-removing"]
        let rejected = app.buttons["management-sidebar-portal-durable-rejected"]
        XCTAssertTrue(active.waitForExistence(timeout: 3), app.debugDescription)
        XCTAssertTrue(removing.exists, app.debugDescription)
        XCTAssertTrue(rejected.exists, app.debugDescription)
        XCTAssertLessThan(active.frame.minY, removing.frame.minY)
        XCTAssertLessThan(removing.frame.minY, rejected.frame.minY)
        XCTAssertTrue(removing.label.contains("Removing"))
        XCTAssertTrue(rejected.label.contains("Cleanup in progress"))

        active.click()
        XCTAssertTrue(waitForValue("durable-active", element: app.staticTexts["selected-portal-name"], timeout: 3))
        removing.click()
        XCTAssertTrue(app.descendants(matching: .any)["removing-portal"].waitForExistence(timeout: 3))
        rejected.click()
        XCTAssertTrue(
            waitForText(
                "Removing durable-rejected from a different tailnet.",
                element: app.staticTexts["pending-portal-detail"],
                timeout: 3
            )
        )
    }

    func testSelectedPortalDetailShowsFactsAndSafeURLActions() {
        var app = launch(scenario: "online")
        app.typeKey("o", modifierFlags: [.command, .shift])
        app.buttons["management-sidebar-portal-portal-one"].click()
        XCTAssertTrue(waitForValue("portal-one", element: app.staticTexts["selected-portal-name"], timeout: 3))
        XCTAssertTrue(app.staticTexts["selected-assigned-name"].exists)
        XCTAssertTrue(app.staticTexts["selected-desired-state"].exists)
        XCTAssertTrue(app.staticTexts["selected-tailscale-state"].exists)
        XCTAssertTrue(app.staticTexts["selected-local-app-state"].exists)
        let currentURL = app.staticTexts["selected-portal-url"]
        XCTAssertTrue(waitForText("https://portal-one-1.example.ts.net", element: currentURL, timeout: 3))
        XCTAssertTrue(app.staticTexts["selected-addresses"].exists)
        XCTAssertTrue(app.buttons["selected-copy-portal-url"].isEnabled)
        XCTAssertTrue(app.buttons["selected-open-portal-url"].isEnabled)

        app = launch(scenario: "stale")
        app.typeKey("o", modifierFlags: [.command, .shift])
        app.buttons["management-sidebar-portal-portal-one"].click()
        let staleURL = app.staticTexts["selected-portal-url"]
        XCTAssertTrue(staleURL.waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["Portal URL — Last Known"].exists)
        XCTAssertTrue(app.buttons["selected-copy-portal-url"].isEnabled)
        XCTAssertFalse(app.buttons["selected-open-portal-url"].isEnabled)

        app = launch(scenario: "authenticating")
        app.typeKey("o", modifierFlags: [.command, .shift])
        app.buttons["management-sidebar-portal-portal-one"].click()
        XCTAssertTrue(app.buttons["selected-authenticate"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.buttons["selected-authenticate"].isEnabled)
        XCTAssertTrue(app.staticTexts["selected-authentication-guidance"].exists)
        XCTAssertFalse(app.buttons["selected-open-portal-url"].isEnabled)

        app = launch(scenario: "stale-authenticating")
        app.typeKey("o", modifierFlags: [.command, .shift])
        app.buttons["management-sidebar-portal-portal-one"].click()
        XCTAssertTrue(waitForValue(
            "Authentication required — Last Known",
            element: app.staticTexts["selected-tailscale-state"],
            timeout: 3
        ))
        XCTAssertTrue(app.buttons["selected-authenticate"].exists)
        XCTAssertFalse(app.buttons["selected-authenticate"].isEnabled)
        XCTAssertFalse(app.staticTexts["selected-authentication-guidance"].exists)

        app = launch(scenario: "awaiting-approval")
        app.typeKey("o", modifierFlags: [.command, .shift])
        app.buttons["management-sidebar-portal-portal-one"].click()
        XCTAssertTrue(waitForValue("Awaiting approval", element: app.staticTexts["selected-tailscale-state"], timeout: 3))
        XCTAssertFalse(app.buttons["selected-authenticate"].exists)
        XCTAssertFalse(app.buttons["selected-open-portal-url"].isEnabled)

        app = launch(scenario: "terminal-failure")
        app.typeKey("o", modifierFlags: [.command, .shift])
        let unavailablePortal = app.buttons["management-sidebar-portal-portal-one"]
        XCTAssertTrue(unavailablePortal.waitForExistence(timeout: 3))
        unavailablePortal.click()
        XCTAssertFalse(app.buttons["selected-authenticate"].exists)
        XCTAssertFalse(app.buttons["selected-open-portal-url"].exists)
    }

    func testSelectedPortalDetailDailyActionsAndIsolation() {
        let app = launch(scenario: "management")
        app.typeKey("o", modifierFlags: [.command, .shift])

        app.buttons["management-sidebar-portal-first-portal"].click()
        XCTAssertTrue(waitForValue("Enabled", element: app.staticTexts["selected-desired-state"], timeout: 3))
        app.buttons["selected-edit-destination-remote"].click()
        XCTAssertTrue(app.textFields["selected-edit-remote-app-host"].waitForExistence(timeout: 3))
        let startStop = app.buttons["selected-start-stop"]
        startStop.click()
        XCTAssertTrue(waitForValue("Stopped", element: app.staticTexts["selected-desired-state"], timeout: 3))
        let focusedStartStop = app.buttons.matching(
            NSPredicate(format: "identifier == %@ AND hasKeyboardFocus == true", "selected-start-stop")
        ).firstMatch
        XCTAssertTrue(focusedStartStop.waitForExistence(timeout: 3), app.debugDescription)
        app.typeKey(.space, modifierFlags: [])
        XCTAssertTrue(waitForValue("Enabled", element: app.staticTexts["selected-desired-state"], timeout: 3))

        app.buttons["management-sidebar-portal-second-portal"].click()
        XCTAssertTrue(waitForValue("Enabled", element: app.staticTexts["selected-desired-state"], timeout: 3))
        XCTAssertTrue(app.textFields["selected-edit-local-app-port"].waitForExistence(timeout: 3))
        XCTAssertTrue(waitForValue("Connecting", element: app.staticTexts["selected-tailscale-state"], timeout: 3))
        XCTAssertFalse(app.buttons["selected-authenticate"].exists)
        XCTAssertFalse(app.buttons["selected-open-portal-url"].isEnabled)

        app.buttons["management-sidebar-portal-first-portal"].click()
        let port = app.textFields["selected-edit-local-app-port"]
        XCTAssertTrue(port.waitForExistence(timeout: 3))
        port.click()
        port.typeKey("a", modifierFlags: .command)
        port.typeText("8081")
        XCTAssertTrue(app.buttons["selected-update-destination"].isEnabled)
        app.buttons["selected-update-destination"].click()
        XCTAssertTrue(app.buttons["selected-portal-diagnostics"].isEnabled)
        app.buttons["selected-portal-diagnostics"].click()
        XCTAssertTrue(app.staticTexts["diagnostics-heading"].waitForExistence(timeout: 3))
    }

    func testSelectedPortalDetailShowsDestinationValidationErrors() {
        let app = launch(scenario: "management")
        app.typeKey("o", modifierFlags: [.command, .shift])
        app.buttons["management-sidebar-portal-first-portal"].click()

        let port = app.textFields["selected-edit-local-app-port"]
        XCTAssertTrue(port.waitForExistence(timeout: 3), app.debugDescription)
        port.click()
        port.typeKey("a", modifierFlags: .command)
        port.typeText("0")
        port.typeKey(.return, modifierFlags: [])

        XCTAssertTrue(
            waitForText(
                "Enter valid destination details.",
                element: app.staticTexts["selected-portal-message"],
                timeout: 3
            ),
            app.debugDescription
        )
    }

    func testSelectedPortalDetailTracksDestinationChangesFromManagementWindow() {
        let app = launch(scenario: "management")
        app.typeKey("o", modifierFlags: [.command, .shift])
        app.buttons["management-sidebar-portal-first-portal"].click()

        let selectedPort = app.textFields["selected-edit-local-app-port"]
        XCTAssertTrue(waitForValue("8080", element: selectedPort, timeout: 3), app.debugDescription)

        selectedPort.click()
        selectedPort.typeKey("a", modifierFlags: .command)
        selectedPort.typeText("8081")
        app.buttons["selected-update-destination"].click()

        XCTAssertTrue(waitForValue("8081", element: selectedPort, timeout: 3), app.debugDescription)
        XCTAssertFalse(app.buttons["selected-update-destination"].isEnabled)
    }

    func testAddPortalSheetPreservesDiscoveryAndDiscardsOnlyUncommittedDrafts() {
        let root = makeRoot()
        let app = launch(scenario: "creation", root: root)
        app.typeKey("o", modifierFlags: [.command, .shift])
        XCTAssertTrue(app.staticTexts["Connect Your Tailnet"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.buttons["overview-add-portal"].waitForExistence(timeout: 3))
        app.buttons["overview-add-portal"].click()
        XCTAssertTrue(app.descendants(matching: .any)["add-portal-sheet"].waitForExistence(timeout: 3))

        app.buttons["refresh-local-apps"].click()
        let candidate = app.buttons["local-app-9342"]
        XCTAssertTrue(candidate.waitForExistence(timeout: 3), app.debugDescription)
        XCTAssertTrue(candidate.label.contains("localhost:9342"))
        XCTAssertFalse(candidate.label.contains("9,342"))
        candidate.click()
        XCTAssertTrue(waitForValue("detected-portal", element: app.textFields["portal-name-field"], timeout: 3))
        XCTAssertTrue(waitForValue("9342", element: app.textFields["local-app-port-field"], timeout: 3))

        let portalName = app.textFields["portal-name-field"]
        portalName.click()
        portalName.typeKey("a", modifierFlags: .command)
        portalName.typeText("Invalid Name")
        portalName.typeKey(.return, modifierFlags: [])
        XCTAssertEqual(portalName.value(forKey: "hasKeyboardFocus") as? Bool, true)
        let localPort = app.textFields["local-app-port-field"]
        portalName.typeKey("a", modifierFlags: .command)
        portalName.typeText("manual-portal")
        localPort.click()
        localPort.typeKey("a", modifierFlags: .command)
        localPort.typeText("0")
        localPort.typeKey(.return, modifierFlags: [])
        XCTAssertEqual(localPort.value(forKey: "hasKeyboardFocus") as? Bool, true)

        app.typeKey(.escape, modifierFlags: [])
        XCTAssertFalse(app.descendants(matching: .any)["add-portal-sheet"].waitForExistence(timeout: 1))
        XCTAssertFalse(FileManager.default.fileExists(atPath: "\(root)/helper-enrollment.txt"))

        app.buttons["overview-add-portal"].click()
        app.buttons["add-portal-next"].click()
        XCTAssertTrue(app.textFields["portal-name-field"].waitForExistence(timeout: 3))
        XCTAssertEqual(app.textFields["portal-name-field"].value as? String, "")
        XCTAssertEqual(app.textFields["local-app-port-field"].value as? String, "")
        app.buttons["add-portal-back"].click()
        app.buttons["refresh-local-apps"].click()
        XCTAssertTrue(candidate.waitForExistence(timeout: 3))
        candidate.click()
        portalName.click()
        portalName.typeKey("a", modifierFlags: .command)
        portalName.typeText("manual-portal")
        localPort.click()
        localPort.typeKey("a", modifierFlags: .command)
        localPort.typeText("4321")
        app.buttons["add-portal"].click()

        XCTAssertFalse(app.descendants(matching: .any)["add-portal-sheet"].waitForExistence(timeout: 3))
        XCTAssertTrue(waitForValue("manual-portal", element: app.staticTexts["selected-portal-name"], timeout: 3))
        XCTAssertTrue(app.staticTexts["selected-authentication-guidance"].waitForExistence(timeout: 3))
        let authenticate = app.buttons["selected-authenticate"]
        XCTAssertTrue(authenticate.isEnabled)
        let focusedAuthenticate = app.buttons.matching(
            NSPredicate(format: "identifier == %@ AND hasKeyboardFocus == true", "selected-authenticate")
        ).firstMatch
        XCTAssertTrue(focusedAuthenticate.waitForExistence(timeout: 3), app.debugDescription)
        XCTAssertTrue(authenticate.isHittable, app.debugDescription)
        app.buttons["management-sidebar-overview"].click()
        app.buttons["management-sidebar-portal-manual-portal"].click()
        XCTAssertFalse(focusedAuthenticate.waitForExistence(timeout: 1), app.debugDescription)
        XCTAssertTrue(FileManager.default.fileExists(atPath: "\(root)/helper-enrollment.txt"))
    }

    func testOpenPorticoRaisesTheManagementWindowAndClosingItKeepsTheMenuBarAppRunning() {
        let app = launch(scenario: "online")

        openMenuBarExtra(app)
        app.buttons["open-portico"].click()
        let overview = app.descendants(matching: .any)["management-overview"]
        XCTAssertTrue(overview.waitForExistence(timeout: 3), app.debugDescription)
        XCTAssertFalse(app.descendants(matching: .any)["overview-first-portal-guidance"].exists)
        let managementWindows = app.windows.matching(NSPredicate(format: "identifier == %@", "management"))
        XCTAssertEqual(managementWindows.count, 1, app.debugDescription)
        let focusedManagementWindow = app.windows.matching(
            NSPredicate(format: "identifier == %@ AND hasKeyboardFocus == true", "management")
        )
        XCTAssertTrue(focusedManagementWindow.firstMatch.waitForExistence(timeout: 3), app.debugDescription)

        app.typeKey("o", modifierFlags: [.command, .shift])
        XCTAssertTrue(overview.waitForExistence(timeout: 3), app.debugDescription)
        XCTAssertEqual(managementWindows.count, 1, app.debugDescription)
        XCTAssertTrue(focusedManagementWindow.firstMatch.waitForExistence(timeout: 3), app.debugDescription)

        app.typeKey("w", modifierFlags: .command)
        XCTAssertFalse(overview.waitForExistence(timeout: 3), app.debugDescription)
        XCTAssertFalse(managementWindows.firstMatch.exists)
        openMenuBarExtra(app)
        XCTAssertTrue(app.statusItems.firstMatch.exists)
    }

    func testOnlyFreshInstallationAutoOpensOverviewAndPersistenceFailureIsSanitized() {
        var app = launch(scenario: "first-run")
        XCTAssertTrue(app.descendants(matching: .any)["management-overview"].waitForExistence(timeout: 3))
        app.terminate()
        XCTAssertTrue(app.wait(for: .notRunning, timeout: 5))

        app = launch(scenario: "creation")
        XCTAssertFalse(app.descendants(matching: .any)["management-overview"].waitForExistence(timeout: 2))
        app.terminate()
        XCTAssertTrue(app.wait(for: .notRunning, timeout: 5))

        app = launch(scenario: "migrated")
        XCTAssertFalse(app.descendants(matching: .any)["management-overview"].waitForExistence(timeout: 2))
        app.terminate()
        XCTAssertTrue(app.wait(for: .notRunning, timeout: 5))

        app = launch(scenario: "initial-save-failure")
        XCTAssertFalse(app.descendants(matching: .any)["management-overview"].waitForExistence(timeout: 2))
        openMenuBarExtra(app)
        XCTAssertTrue(app.buttons["compact-attention"].waitForExistence(timeout: 3))
        XCTAssertFalse(app.staticTexts["Saved Portal configuration could not be loaded."].exists)
        app.buttons["compact-attention"].click()
        XCTAssertTrue(app.descendants(matching: .any)["management-overview"].waitForExistence(timeout: 3))
    }

    func testCompactAttentionRoutesConfiguredPersistenceFailureToOverviewMessage() {
        let app = launch(scenario: "configured-message")
        XCTAssertFalse(app.descendants(matching: .any)["management-overview"].waitForExistence(timeout: 2))
        openMenuBarExtra(app)
        XCTAssertTrue(app.buttons["compact-attention"].waitForExistence(timeout: 3))
        app.buttons["compact-attention"].click()
        XCTAssertTrue(
            waitForText(
                "Saved Portal configuration could not be loaded.",
                element: app.staticTexts["overview-message"],
                timeout: 3
            ),
            app.debugDescription
        )
        XCTAssertFalse(app.staticTexts["No Portals"].exists)
    }

    func testCompactMenuPortalActionMatrix() {
        var app = launch(scenario: "online")
        openMenuBarExtra(app)

        let openPortalURL = app.buttons["compact-open-portal-url"]
        XCTAssertTrue(openPortalURL.waitForExistence(timeout: 3), app.debugDescription)
        XCTAssertTrue(openPortalURL.isEnabled)
        XCTAssertFalse(app.buttons["compact-authenticate"].exists)
        XCTAssertFalse(app.buttons["compact-start"].exists)
        assertNoDetailedMenuControls(in: app)

        app = launch(scenario: "stopped")
        openMenuBarExtra(app)
        XCTAssertTrue(app.buttons["compact-start"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.buttons["compact-start"].isEnabled)
        XCTAssertFalse(app.buttons["compact-open-portal-url"].exists)

        app = launch(scenario: "authenticating")
        openMenuBarExtra(app)
        XCTAssertTrue(app.buttons["compact-authenticate"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.buttons["compact-authenticate"].isEnabled)
        XCTAssertFalse(app.buttons["compact-open-portal-url"].exists)

        app = launch(scenario: "stale")
        openMenuBarExtra(app)
        XCTAssertTrue(app.buttons["open-portico"].waitForExistence(timeout: 3))
        XCTAssertFalse(app.buttons["compact-authenticate"].exists)
        XCTAssertFalse(app.buttons["compact-start"].exists)
        XCTAssertFalse(app.buttons["compact-open-portal-url"].exists)

        app = launch(scenario: "awaiting-approval")
        openMenuBarExtra(app)
        XCTAssertTrue(app.buttons["open-portico"].waitForExistence(timeout: 3))
        XCTAssertFalse(app.buttons["compact-authenticate"].exists)
        XCTAssertFalse(app.buttons["compact-start"].exists)
        XCTAssertFalse(app.buttons["compact-open-portal-url"].exists)

        app = launch(scenario: "durable-management")
        openMenuBarExtra(app)
        XCTAssertTrue(app.buttons["compact-open-portal-url"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.buttons["compact-open-portal-url"].isEnabled)
        XCTAssertTrue(
            waitForText("Removing", element: app.descendants(matching: .any)["compact-portal-status-durable-removing"], timeout: 3),
            app.debugDescription
        )
        XCTAssertTrue(
            waitForText("Cleanup in progress", element: app.descendants(matching: .any)["compact-portal-status-durable-rejected"], timeout: 3),
            app.debugDescription
        )
        XCTAssertFalse(app.buttons["compact-authenticate"].exists)
        XCTAssertFalse(app.buttons["compact-start"].exists)
        assertNoDetailedMenuControls(in: app)
    }

    func testLocalAndRemoteDestinationEditorsSupportCrossKindChanges() {
        var app = launch(scenario: "online")
        app.typeKey("o", modifierFlags: [.command, .shift])
        app.buttons["management-sidebar-portal-portal-one"].click()
        XCTAssertTrue(app.textFields["selected-edit-local-app-port"].waitForExistence(timeout: 3))
        app.buttons["selected-edit-destination-remote"].click()
        XCTAssertTrue(app.textFields["selected-edit-remote-app-host"].waitForExistence(timeout: 3))
        XCTAssertFalse(app.textFields["selected-edit-local-app-port"].exists)
        app.textFields["selected-edit-remote-app-host"].click()
        app.textFields["selected-edit-remote-app-host"].typeText("app.example.com")
        app.textFields["selected-edit-remote-app-port"].click()
        app.textFields["selected-edit-remote-app-port"].typeText("443")
        XCTAssertTrue(app.buttons["selected-update-destination"].isEnabled)
        app.buttons["selected-update-destination"].click()
        XCTAssertTrue(app.buttons["selected-start-stop"].isEnabled)

        app = launch(scenario: "remote-online")
        app.typeKey("o", modifierFlags: [.command, .shift])
        app.buttons["management-sidebar-portal-portal-one"].click()
        XCTAssertTrue(app.textFields["selected-edit-remote-app-host"].waitForExistence(timeout: 3))
        XCTAssertFalse(app.textFields["selected-edit-local-app-port"].exists)
        app.buttons["selected-edit-destination-local"].click()
        XCTAssertTrue(app.textFields["selected-edit-local-app-port"].waitForExistence(timeout: 3))
        XCTAssertFalse(app.textFields["selected-edit-remote-app-host"].exists)
        app.textFields["selected-edit-local-app-port"].click()
        app.textFields["selected-edit-local-app-port"].typeKey("a", modifierFlags: .command)
        app.textFields["selected-edit-local-app-port"].typeText("8081")
        XCTAssertTrue(app.buttons["selected-update-destination"].isEnabled)
        app.buttons["selected-update-destination"].click()
        XCTAssertTrue(app.buttons["selected-start-stop"].isEnabled)
    }

    func testCompactMenuAttentionAndCommands() {
        var app = launch(scenario: "terminal-failure")
        openMenuBarExtra(app)
        let attention = app.buttons["compact-attention"]
        XCTAssertTrue(attention.waitForExistence(timeout: 5), app.debugDescription)
        XCTAssertEqual(attention.label, "Review in Portico")
        attention.click()
        XCTAssertTrue(app.descendants(matching: .any)["management-overview"].waitForExistence(timeout: 3))

        app = launch(scenario: "recovery")
        openMenuBarExtra(app)
        XCTAssertTrue(app.buttons["compact-attention"].waitForExistence(timeout: 3))

        app = launch(scenario: "removing-failure")
        openMenuBarExtra(app)
        XCTAssertTrue(app.buttons["compact-attention"].waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["Retry Removal"].exists)

        app = launch(scenario: "login-offer")
        openMenuBarExtra(app)
        XCTAssertTrue(app.buttons["login-offer-reminder"].waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["login-offer-enable"].exists)
        XCTAssertFalse(app.buttons["login-offer-decline"].exists)

        app = launch(scenario: "online")
        openMenuBarExtra(app)
        let portalAction = app.buttons["compact-open-portal-url"]
        let openPortico = app.buttons["open-portico"]
        let settings = app.buttons["settings"]
        let diagnostics = app.buttons["diagnostics"]
        let quit = app.buttons["quit"]
        XCTAssertTrue(portalAction.waitForExistence(timeout: 3), app.debugDescription)
        XCTAssertTrue(openPortico.exists)
        XCTAssertTrue(settings.exists)
        XCTAssertTrue(diagnostics.exists)
        XCTAssertLessThan(portalAction.frame.maxY, quit.frame.minY)
        let compactPortal = app.descendants(matching: .any)["compact-portal-portal-one"]
        XCTAssertLessThan(abs(portalAction.frame.midY - compactPortal.frame.midY), 8)
        settings.click()
        XCTAssertTrue(app.radioButtons["Allow operational-support logging"].waitForExistence(timeout: 3))
        let focusedManagementWindow = app.windows.matching(
            NSPredicate(format: "identifier == %@ AND hasKeyboardFocus == true", "management")
        )
        XCTAssertTrue(focusedManagementWindow.firstMatch.waitForExistence(timeout: 3), app.debugDescription)
        app.typeKey("w", modifierFlags: .command)
        app.typeKey("d", modifierFlags: [.command, .shift])
        XCTAssertTrue(app.staticTexts["diagnostics-heading"].waitForExistence(timeout: 3))
        app.typeKey("q", modifierFlags: .command)
        XCTAssertTrue(app.wait(for: .notRunning, timeout: 5))
    }

    func testSettingsHasInitialAccessibilityFocusTarget() {
        let app = launch(scenario: "online")
        app.typeKey(",", modifierFlags: .command)
        XCTAssertTrue(app.staticTexts["settings-heading"].waitForExistence(timeout: 3))
    }

    func testLoggingRestartAndTerminalFailureStates() {
        var app = launch(scenario: "restarting")
        app.typeKey(",", modifierFlags: .command)
        let disabledLogging = app.radioButtons["Disable operational-support logging"]
        XCTAssertTrue(disabledLogging.waitForExistence(timeout: 3), app.debugDescription)
        disabledLogging.click()
        XCTAssertTrue(waitForValue("Restarting", element: app.staticTexts["settings-helper-state"], timeout: 2))
        openMenuBarExtra(app)
        let compactHelperState = app.descendants(matching: .any)["helper-state"]
        XCTAssertTrue(waitForText("Restarting", element: compactHelperState, timeout: 2))
        app.typeKey("r", modifierFlags: [.command, .shift])
        XCTAssertTrue(waitForText("Connected", element: compactHelperState, timeout: 5))

        app = launch(scenario: "terminal-failure")
        app.typeKey("o", modifierFlags: [.command, .shift])
        XCTAssertTrue(waitForValue("Helper unavailable", element: app.staticTexts["overview-helper-state"], timeout: 3))
        XCTAssertTrue(app.buttons["overview-retry-helper"].isEnabled)
    }

    func testManagementPortalRemovalConfirmationRecoveryAndFocus() {
        var app = launch(scenario: "online")
        app.typeKey("o", modifierFlags: [.command, .shift])
        app.buttons["management-sidebar-portal-portal-one"].click()
        app.buttons["selected-remove-portal"].click()
        XCTAssertTrue(app.staticTexts["remove-confirmation-heading"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.staticTexts["remove-warning"].exists)
        XCTAssertTrue(app.links["How to remove a Tailscale device"].exists)
        app.typeKey(.escape, modifierFlags: [])
        XCTAssertFalse(app.staticTexts["remove-confirmation-heading"].waitForExistence(timeout: 1))
        let removePortal = app.buttons["selected-remove-portal"]
        XCTAssertTrue(removePortal.exists)
        XCTAssertTrue(
            removePortal.waitForExistence(timeout: 3),
            app.debugDescription
        )
        app.typeKey(.space, modifierFlags: [])
        XCTAssertTrue(app.staticTexts["remove-confirmation-heading"].waitForExistence(timeout: 3))
        app.typeKey(.escape, modifierFlags: [])

        app.buttons["selected-remove-portal"].click()
        XCTAssertTrue(app.buttons["remove-confirm"].waitForExistence(timeout: 3))
        app.typeKey(.return, modifierFlags: [])
        let removalComplete = app.descendants(matching: .any)["removal-complete"]
        XCTAssertTrue(removalComplete.waitForExistence(timeout: 5), app.debugDescription)
        XCTAssertTrue(app.descendants(matching: .any)["management-sidebar-overview"].exists)
        XCTAssertTrue(app.buttons["removal-complete-dismiss"].exists)
        app.buttons["removal-complete-dismiss"].click()
        XCTAssertFalse(removalComplete.waitForExistence(timeout: 3), app.debugDescription)

        app = launch(scenario: "removing")
        app.typeKey("o", modifierFlags: [.command, .shift])
        app.buttons["management-sidebar-portal-portal-one"].click()
        XCTAssertTrue(app.descendants(matching: .any)["removing-portal"].waitForExistence(timeout: 3))
        XCTAssertFalse(app.buttons["Retry Removal"].exists)
        XCTAssertTrue(app.descendants(matching: .any)["removing-progress"].exists)
        XCTAssertTrue(app.links["How to remove a Tailscale device"].exists)

        app = launch(scenario: "removing-failure")
        app.typeKey("o", modifierFlags: [.command, .shift])
        app.buttons["management-sidebar-portal-portal-one"].click()
        let retryRemoval = app.buttons["Retry Removal"]
        XCTAssertTrue(retryRemoval.waitForExistence(timeout: 5), app.debugDescription)
        XCTAssertTrue(retryRemoval.isEnabled)
    }

    func testOverviewRecoveryAndResetTailnet() {
        var app = launch(scenario: "terminal-failure")
        app.typeKey("o", modifierFlags: [.command, .shift])
        XCTAssertTrue(waitForValue("Helper unavailable", element: app.staticTexts["overview-helper-state"], timeout: 3))
        XCTAssertTrue(app.buttons["overview-retry-helper"].isEnabled)
        openMenuBarExtra(app)
        XCTAssertFalse(app.buttons["retry-helper"].exists)

        app = launch(scenario: "recovery")
        app.typeKey("o", modifierFlags: [.command, .shift])
        XCTAssertTrue(app.descendants(matching: .any)["pending-portal-warning"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.descendants(matching: .any)["completed-warning"].exists)
        XCTAssertTrue(app.buttons["completed-warning-dismiss"].exists)
        XCTAssertFalse(app.buttons["overview-reset-tailnet"].exists)
        app.buttons["completed-warning-dismiss"].click()
        XCTAssertFalse(app.descendants(matching: .any)["completed-warning"].waitForExistence(timeout: 3))

        app = launch(scenario: "reset-eligible")
        app.typeKey("o", modifierFlags: [.command, .shift])
        let reset = app.buttons["overview-reset-tailnet"]
        XCTAssertTrue(reset.waitForExistence(timeout: 3), app.debugDescription)
        reset.click()
        XCTAssertTrue(app.staticTexts["Reset this installation's tailnet binding? This does not remove any remote Tailscale node."].waitForExistence(timeout: 3))
        app.typeKey(.escape, modifierFlags: [])
        XCTAssertTrue(reset.exists)
        reset.click()
        XCTAssertTrue(app.buttons["overview-confirm-reset-tailnet"].waitForExistence(timeout: 3))
        app.buttons["overview-confirm-reset-tailnet"].click()
        XCTAssertFalse(reset.waitForExistence(timeout: 3), app.debugDescription)

        app = launch(scenario: "durable-management")
        app.typeKey("o", modifierFlags: [.command, .shift])
        XCTAssertFalse(app.buttons["overview-reset-tailnet"].exists)
    }

    func testLaunchAtLoginApprovalFromOverviewOffer() {
        let app = launch(scenario: "login-offer-approval")
        openMenuBarExtra(app)
        app.buttons["login-offer-reminder"].click()
        XCTAssertTrue(app.buttons["overview-login-offer-enable"].waitForExistence(timeout: 3))
        app.buttons["overview-login-offer-enable"].click()
        XCTAssertFalse(app.descendants(matching: .any)["overview-launch-at-login-offer"].waitForExistence(timeout: 3))
        app.typeKey(",", modifierFlags: .command)
        XCTAssertTrue(app.radioButtons["Allow operational-support logging"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.buttons["open-login-items-settings"].isEnabled)
    }

    func testLaunchAtLoginRegistrationFailureFromOverviewOffer() {
        let app = launch(scenario: "login-offer-error")
        openMenuBarExtra(app)
        app.buttons["login-offer-reminder"].click()
        XCTAssertTrue(app.buttons["overview-login-offer-enable"].waitForExistence(timeout: 3))
        app.buttons["overview-login-offer-enable"].click()
        XCTAssertFalse(app.descendants(matching: .any)["overview-launch-at-login-offer"].waitForExistence(timeout: 3))
        app.typeKey(",", modifierFlags: .command)
        XCTAssertTrue(app.staticTexts["launch-at-login-error"].waitForExistence(timeout: 3))
        XCTAssertTrue(waitForValue("Off", element: app.staticTexts["launch-at-login-status"], timeout: 3), app.debugDescription)
        let retry = app.buttons["retry-launch-at-login"]
        XCTAssertTrue(retry.waitForExistence(timeout: 3))
        XCTAssertTrue(retry.isEnabled)
    }

    func testLaunchAtLoginOfferRoutesFromMenuToOverviewAndStaysUntilExplicitChoice() {
        let root = makeRoot()
        let app = launch(scenario: "login-offer", root: root)
        openMenuBarExtra(app)
        let reminder = app.buttons["login-offer-reminder"]
        XCTAssertTrue(reminder.waitForExistence(timeout: 5), app.debugDescription)
        XCTAssertFalse(app.buttons["login-offer-enable"].exists)
        XCTAssertFalse(app.buttons["login-offer-decline"].exists)

        reminder.click()
        XCTAssertTrue(app.descendants(matching: .any)["management-overview"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.descendants(matching: .any)["overview-launch-at-login-offer"].exists)

        app.buttons["management-sidebar-portal-portal-one"].click()
        XCTAssertTrue(waitForValue("portal-one", element: app.staticTexts["selected-portal-name"], timeout: 3))
        openMenuBarExtra(app)
        XCTAssertTrue(app.buttons["login-offer-reminder"].waitForExistence(timeout: 3))
        app.buttons["login-offer-reminder"].click()
        XCTAssertTrue(app.descendants(matching: .any)["overview-launch-at-login-offer"].waitForExistence(timeout: 3))
        XCTAssertFalse(app.staticTexts["selected-portal-name"].exists)

        XCTAssertTrue(app.buttons["overview-login-offer-decline"].waitForExistence(timeout: 3))
        app.buttons["overview-login-offer-decline"].click()
        XCTAssertFalse(app.descendants(matching: .any)["overview-launch-at-login-offer"].waitForExistence(timeout: 2))
    }

    func testLaunchAtLoginOfferIsActionableInEmptyOverview() {
        let app = launch(scenario: "login-offer-empty")
        openMenuBarExtra(app)

        app.buttons["login-offer-reminder"].click()

        XCTAssertTrue(app.staticTexts["No Portals"].waitForExistence(timeout: 3), app.debugDescription)
        XCTAssertTrue(app.descendants(matching: .any)["overview-launch-at-login-offer"].exists)
        app.buttons["overview-login-offer-decline"].click()
        XCTAssertFalse(app.descendants(matching: .any)["overview-launch-at-login-offer"].waitForExistence(timeout: 2))
    }

    private func launch(scenario: String, root: String? = nil) -> XCUIApplication {
        let app = XCUIApplication()
        app.terminate()
        XCTAssertTrue(app.wait(for: .notRunning, timeout: 5))
        app.launchArguments = ["--ui-testing"]
        app.launchEnvironment = [
            "PORTICO_UI_TEST_SCENARIO": scenario,
            "PORTICO_UI_TEST_ROOT": root ?? makeRoot(),
        ]
        app.launch()
        return app
    }

    private func makeRoot() -> String {
        "/private/tmp/PorticoUITests-\(UUID().uuidString)"
    }

    private func waitForValue(_ value: String, element: XCUIElement, timeout: TimeInterval) -> Bool {
        let predicate = NSPredicate(format: "value == %@", value)
        let expectation = XCTNSPredicateExpectation(predicate: predicate, object: element)
        return XCTWaiter.wait(for: [expectation], timeout: timeout) == .completed
    }

    private func waitForText(_ text: String, element: XCUIElement, timeout: TimeInterval) -> Bool {
        let predicate = NSPredicate(format: "label CONTAINS %@ OR value CONTAINS %@", text, text)
        let expectation = XCTNSPredicateExpectation(predicate: predicate, object: element)
        return XCTWaiter.wait(for: [expectation], timeout: timeout) == .completed
    }

    private func assertNoDetailedMenuControls(in app: XCUIApplication) {
        XCTAssertFalse(app.buttons["copy-portal-url"].exists)
        XCTAssertFalse(app.buttons["start-stop"].exists)
        XCTAssertFalse(app.buttons["edit-destination-local"].exists)
        XCTAssertFalse(app.buttons["edit-destination-remote"].exists)
        XCTAssertFalse(app.buttons["portal-diagnostics"].exists)
        XCTAssertFalse(app.textFields["edit-local-app-port"].exists)
        XCTAssertFalse(app.staticTexts["portal-url"].exists)
    }

    private func openMenuBarExtra(_ app: XCUIApplication) {
        let item = app.statusItems.firstMatch
        XCTAssertTrue(item.waitForExistence(timeout: 5))
        item.click()
    }
}
