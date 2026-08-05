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
        XCTAssertTrue(app.staticTexts["No Portals"].waitForExistence(timeout: 3), app.debugDescription)
        XCTAssertTrue(app.buttons["overview-add-portal"].isEnabled)
        let emptyStateAddPortal = app.buttons["overview-empty-add-portal"]
        XCTAssertTrue(emptyStateAddPortal.waitForExistence(timeout: 3), app.debugDescription)
        emptyStateAddPortal.click()
        XCTAssertTrue(app.descendants(matching: .any)["add-portal-sheet"].waitForExistence(timeout: 3))
        XCTAssertFalse(app.buttons["add-portal"].isEnabled)

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
        XCTAssertTrue(app.staticTexts["No Portals"].waitForExistence(timeout: 3))
        app.buttons["overview-add-portal"].click()
        XCTAssertTrue(app.descendants(matching: .any)["add-portal-sheet"].waitForExistence(timeout: 3))
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
        XCTAssertTrue(app.textFields["portal-name-field"].waitForExistence(timeout: 3))
        XCTAssertEqual(app.textFields["portal-name-field"].value as? String, "")
        XCTAssertEqual(app.textFields["local-app-port-field"].value as? String, "")
    }

    func testAddPortalSheetPreservesDiscoveryAndDiscardsOnlyUncommittedDrafts() {
        let root = makeRoot()
        let app = launch(scenario: "creation", root: root)
        app.typeKey("o", modifierFlags: [.command, .shift])
        XCTAssertTrue(app.staticTexts["No Portals"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.buttons["overview-add-portal"].waitForExistence(timeout: 3))
        app.buttons["overview-add-portal"].click()
        XCTAssertTrue(app.descendants(matching: .any)["add-portal-sheet"].waitForExistence(timeout: 3))

        app.buttons["refresh-local-apps"].click()
        let candidate = app.buttons["local-app-3000"]
        XCTAssertTrue(candidate.waitForExistence(timeout: 3), app.debugDescription)
        candidate.click()
        XCTAssertTrue(waitForValue("detected-portal", element: app.textFields["portal-name-field"], timeout: 3))
        XCTAssertTrue(waitForValue("3000", element: app.textFields["local-app-port-field"], timeout: 3))

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
        XCTAssertTrue(app.textFields["portal-name-field"].waitForExistence(timeout: 3))
        XCTAssertEqual(app.textFields["portal-name-field"].value as? String, "")
        XCTAssertEqual(app.textFields["local-app-port-field"].value as? String, "")
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
        XCTAssertTrue(app.staticTexts["overview-portal-manual-portal"].waitForExistence(timeout: 3))
        XCTAssertTrue(FileManager.default.fileExists(atPath: "\(root)/helper-enrollment.txt"))
    }

    func testOpenPorticoRaisesTheManagementWindowAndClosingItKeepsTheMenuBarAppRunning() {
        let app = launch(scenario: "online")

        app.typeKey("o", modifierFlags: [.command, .shift])
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
        let root = makeRoot()
        var app = launch(scenario: "first-run", root: root)
        XCTAssertTrue(app.descendants(matching: .any)["management-overview"].waitForExistence(timeout: 3))
        app.terminate()
        XCTAssertTrue(app.wait(for: .notRunning, timeout: 5))

        app = launch(scenario: "first-run", root: root)
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
        XCTAssertTrue(app.staticTexts["Saved Portal configuration could not be loaded."].exists)
    }

    func testOnlinePortalActionsAndNativeWindows() {
        let app = launch(scenario: "online")
        openMenuBarExtra(app)

        XCTAssertTrue(app.descendants(matching: .any)["portal-name"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.descendants(matching: .any)["assigned-name"].exists)
        XCTAssertTrue(app.descendants(matching: .any)["desired-state"].exists)
        XCTAssertTrue(app.descendants(matching: .any)["tailscale-state"].exists)
        XCTAssertTrue(app.descendants(matching: .any)["local-app-state"].exists)
        XCTAssertTrue(app.buttons["copy-portal-url"].isEnabled)
        XCTAssertTrue(app.buttons["open-portal-url"].isEnabled)
        let startStop = app.buttons["start-stop"]
        XCTAssertTrue(startStop.isEnabled)
        XCTAssertFalse(app.buttons["authenticate"].exists)

        startStop.click()
        XCTAssertTrue(waitForValue("Stopped", element: app.staticTexts["desired-state"], timeout: 3))
        let focusedStartStop = app.buttons.matching(
            NSPredicate(format: "identifier == %@ AND hasKeyboardFocus == true", "start-stop")
        ).firstMatch
        XCTAssertTrue(focusedStartStop.waitForExistence(timeout: 3), app.debugDescription)

        app.typeKey(",", modifierFlags: .command)
        XCTAssertTrue(app.staticTexts["settings-heading"].waitForExistence(timeout: 3))
        app.typeKey("w", modifierFlags: .command)
        app.typeKey("d", modifierFlags: [.command, .shift])
        XCTAssertTrue(app.staticTexts["diagnostics-heading"].waitForExistence(timeout: 3))
        XCTAssertFalse(app.staticTexts["settings-heading"].exists)
        app.typeKey("q", modifierFlags: .command)
        XCTAssertTrue(app.wait(for: .notRunning, timeout: 5))
    }

    func testLocalAndRemoteDestinationEditorsSupportCrossKindChanges() {
        var app = launch(scenario: "online")
        openMenuBarExtra(app)
        XCTAssertTrue(app.textFields["edit-local-app-port"].waitForExistence(timeout: 3))
        app.buttons["edit-destination-remote"].click()
        XCTAssertTrue(app.textFields["edit-remote-app-host"].waitForExistence(timeout: 3))
        XCTAssertFalse(app.textFields["edit-local-app-port"].exists)
        app.textFields["edit-remote-app-host"].click()
        app.textFields["edit-remote-app-host"].typeText("app.example.com")
        app.textFields["edit-remote-app-port"].click()
        app.textFields["edit-remote-app-port"].typeText("443")
        XCTAssertTrue(app.buttons["update-destination"].isEnabled)
        app.buttons["update-destination"].click()
        XCTAssertTrue(app.buttons["start-stop"].isEnabled)

        app = launch(scenario: "remote-online")
        openMenuBarExtra(app)
        XCTAssertTrue(app.textFields["edit-remote-app-host"].waitForExistence(timeout: 3))
        XCTAssertFalse(app.textFields["edit-local-app-port"].exists)
        app.buttons["edit-destination-local"].click()
        XCTAssertTrue(app.textFields["edit-local-app-port"].waitForExistence(timeout: 3))
        XCTAssertFalse(app.textFields["edit-remote-app-host"].exists)
        app.textFields["edit-local-app-port"].click()
        app.textFields["edit-local-app-port"].typeKey("a", modifierFlags: .command)
        app.textFields["edit-local-app-port"].typeText("8081")
        XCTAssertTrue(app.buttons["update-destination"].isEnabled)
        app.buttons["update-destination"].click()
        XCTAssertTrue(app.buttons["start-stop"].isEnabled)
    }

    func testStaleAndAuthenticatingActions() {
        var app = launch(scenario: "stale")
        openMenuBarExtra(app)

        let lastKnown = app.staticTexts.matching(
            NSPredicate(format: "label CONTAINS %@ OR value CONTAINS %@", "Last Known", "Last Known")
        ).firstMatch
        XCTAssertTrue(lastKnown.waitForExistence(timeout: 5), app.debugDescription)
        XCTAssertTrue(app.buttons["copy-portal-url"].isEnabled)
        XCTAssertFalse(app.buttons["open-portal-url"].isEnabled)

        app = launch(scenario: "authenticating")
        openMenuBarExtra(app)
        XCTAssertTrue(app.buttons["authenticate"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.buttons["authenticate"].isEnabled)
        XCTAssertTrue(app.buttons["copy-portal-url"].isEnabled)
        XCTAssertFalse(app.buttons["open-portal-url"].isEnabled)
    }

    func testLoggingRestartAndTerminalFailureStates() {
        var app = launch(scenario: "restarting")
        app.typeKey(",", modifierFlags: .command)
        let disabledLogging = app.radioButtons["Disable operational-support logging"]
        XCTAssertTrue(disabledLogging.waitForExistence(timeout: 3), app.debugDescription)
        disabledLogging.click()
        XCTAssertTrue(waitForValue("Restarting", element: app.staticTexts["settings-helper-state"], timeout: 2))
        app.typeKey("r", modifierFlags: [.command, .shift])
        app.typeKey("w", modifierFlags: .command)
        openMenuBarExtra(app)
        XCTAssertTrue(waitForValue("Restarting", element: app.staticTexts["helper-state"], timeout: 2))
        XCTAssertTrue(waitForValue("Connected", element: app.staticTexts["helper-state"], timeout: 5))

        app = launch(scenario: "terminal-failure")
        openMenuBarExtra(app)
        XCTAssertTrue(waitForValue("Helper unavailable", element: app.staticTexts["helper-state"], timeout: 3))
        XCTAssertTrue(app.buttons["retry-helper"].isEnabled)
        XCTAssertTrue(app.buttons["start-stop"].isEnabled)
    }

    func testRemovalConfirmationCancelCompletionAndFailure() {
        var app = launch(scenario: "online")
        openMenuBarExtra(app)
        app.buttons["remove-portal"].click()
        XCTAssertTrue(app.staticTexts["remove-confirmation-heading"].waitForExistence(timeout: 3))
        app.typeKey(.escape, modifierFlags: [])
        XCTAssertFalse(app.staticTexts["remove-confirmation-heading"].waitForExistence(timeout: 1))
        XCTAssertTrue(app.descendants(matching: .any)["portal-name"].exists)

        app.buttons["remove-portal"].click()
        XCTAssertTrue(app.buttons["remove-confirm"].waitForExistence(timeout: 3))
        app.typeKey(.return, modifierFlags: [])
        let removalComplete = app.descendants(matching: .any)["removal-complete"]
        if !removalComplete.waitForExistence(timeout: 1) {
            openMenuBarExtra(app)
        }
        XCTAssertTrue(removalComplete.waitForExistence(timeout: 5), app.debugDescription)

        app = launch(scenario: "removing")
        openMenuBarExtra(app)
        XCTAssertTrue(app.descendants(matching: .any)["removing-portal"].waitForExistence(timeout: 3))
        XCTAssertFalse(app.buttons["Retry Removal"].exists)

        app = launch(scenario: "removing-failure")
        openMenuBarExtra(app)
        let retryRemoval = app.buttons["Retry Removal"]
        if !retryRemoval.waitForExistence(timeout: 1) {
            openMenuBarExtra(app)
        }
        XCTAssertTrue(retryRemoval.waitForExistence(timeout: 5), app.debugDescription)
        XCTAssertTrue(retryRemoval.isEnabled)
    }

    func testLaunchAtLoginApprovalAndRegistrationError() {
        var app = launch(scenario: "login-approval")
        app.typeKey(",", modifierFlags: .command)
        XCTAssertTrue(app.staticTexts["settings-heading"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.buttons["open-login-items-settings"].isEnabled)

        app = launch(scenario: "login-error")
        app.typeKey(",", modifierFlags: .command)
        XCTAssertTrue(app.buttons["enable-launch-at-login"].waitForExistence(timeout: 3))
        app.buttons["enable-launch-at-login"].click()
        XCTAssertTrue(app.staticTexts["launch-at-login-error"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.buttons["retry-launch-at-login"].isEnabled)
    }

    func testLaunchAtLoginOfferDoesNotRepeatAfterRelaunch() {
        let root = makeRoot()
        var app = launch(scenario: "login-offer", root: root)
        openMenuBarExtra(app)
        XCTAssertTrue(app.descendants(matching: .any)["launch-at-login-offer"].waitForExistence(timeout: 5))

        app.statusItems.firstMatch.click()
        openMenuBarExtra(app)
        XCTAssertFalse(app.descendants(matching: .any)["launch-at-login-offer"].waitForExistence(timeout: 2))

        app.terminate()
        XCTAssertTrue(app.wait(for: .notRunning, timeout: 5))
        app = launch(scenario: "login-offer", root: root)
        openMenuBarExtra(app)
        XCTAssertFalse(app.descendants(matching: .any)["launch-at-login-offer"].waitForExistence(timeout: 2))
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

    private func openMenuBarExtra(_ app: XCUIApplication) {
        let item = app.statusItems.firstMatch
        XCTAssertTrue(item.waitForExistence(timeout: 5))
        item.click()
    }
}
