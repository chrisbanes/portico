# Native UI coverage migration

Design inventory for [#102](https://github.com/chrisbanes/portico/issues/102),
against `6266f94579664ec860151bafc33f9985ca5352e9`.
[Service contract](local-service-contract.md) · [Architecture decision](adr/0009-local-go-management-service.md).

Revisions: on 15 November 2026 [#103's approved decision revision](https://github.com/chrisbanes/portico/issues/103#approved-decision-revision--15-november-2026)
replaced the Enrolment row's fresh-URL requirement with the revised AUTH-1 in the
[service contract](local-service-contract.md#interrupted-enrolment-and-transient-authentication).
The runtime-test note and the blocker line are reworded to match; every other row
and rule is unchanged. The map #102 approved at
[`2509e2e`](https://github.com/chrisbanes/portico/blob/2509e2eca9d27c2513b35c8c55d1bb43227bebbf/docs/ui-coverage-migration.md)
stays as history.

Baseline: **22 XCTest methods** in `PorticoUITests/PorticoUITests.swift`.
Several methods launch multiple scenario variants; 22 is not a count of all
states/assertions, and no execution-time measurement was taken here.
No existing test is removed by this design. Proposed remainder: **5 native UI
scenarios**, described below, consolidated from existing methods. That is a
planned count, not an achieved reduction or speed claim.

## Layers and evidence rules

- **S**: Go service behaviour with fake runtime/persistence/clock, #103. Domain
  matrices, durable intent and crash recovery belong here.
- **C**: Swift presentation and dispatch through a fake service client, #105.
  These tests observe state/actions/effects; they require no desktop automation.
- **I**: Small real socket and installed-service suite, #104/#105. Kernel peer
  identity, paths, restart/disconnect and ownership cannot be established by S/C.
- **U1–U5**: Retained native UI scenarios in the VM, #105/#106. Each has a
  distinct OS/SwiftUI integration guarantee.
- **M**: Manual actual-tailnet acceptance, separate from fake tests.

S check names below are committed Go tests in `helper/internal/service`
(`TestImportVersionsOneThroughFive` also runs at the import level in
`helper/internal/installation`); they use a fake runtime, so they are service
evidence, not real-runtime evidence. The real-runtime AUTH-1 check is the
opt-in `TestRealRuntimeAuth1ResumeAcrossDisconnectAndRestart`. C, I, U and M
names are still **planned**, not existing test methods. Existing references
are explicitly called prior art. #103/#105/#106 must replace
these names with committed test symbols and passing result pointers before
removing the associated assertions. Splitting a compound method is allowed only
when every assertion group remains accounted for. Deleting a method because a
similarly named service test exists is insufficient.

## Complete baseline inventory

| Current method | Guarantees and scenario variants | Replacement / retained coverage |
| --- | --- | --- |
| `testFirstRunGuidanceAndLoggingGate` | First-run overview/status/tailnet guidance; create disabled until logging choice; first-auth guidance; empty-state creation; sheet cancel; no add form in compact menu | S `TestLoggingChoiceGatesCreate` commits preference before creation; C `FirstRunGuidanceAndEntryPoints` checks first-auth guidance and both logging choices, empty-state and compact-menu control projection; U1 initial overview and U2 sheet wiring/cancel |
| `testOverviewToolbarOpensTheSameAddPortalSheet` | Toolbar creation opens sheet, valid manual fields enable submit; Escape cancels; reopening clears uncommitted name/port | C `CreationDraftLifecycle` covers toolbar/empty-state same route and draft reset; S `TestValidateCreate`; U2 Escape/reopen and manual creation |
| `testCorruptInstallationShowsPersistenceFailureWithoutStartingHelper` | Corrupt fixture shows safe persistence failure, no connecting/runtime start or retry, attention routes to overview | S `TestCorruptStateFailsClosed`: `Open` fails closed, so no engine or runtime can start; I `CorruptStateDoesNotStartNodes`; C `PersistenceFailureRouting` checks sanitised state and no retry; U1 representative service failure route |
| `testManagementSidebarOrdersAndSelectsActivePortals` | Overview precedes ordered records; Online/Connecting labels; settings/overview routes, correct selected UUID; creation sheet entry | C `SidebarOrderingSelectionAndRoutes` with two UUIDs and labels; U1 native menu/window/sidebar route and U2 add entry |
| `testManagementSidebarRetainsDurablePortalRecords` | Active, Removing and rejected-cleanup records stay visible and ordered; correct selected detail/warning | S `TestPendingCleanupRetainsRecord`; C `DurableRecordProjection` for all three lifecycle variants, ordering and selection; no desktop domain matrix |
| `testSelectedPortalDetailShowsFactsAndSafeURLActions` | Online identity/URL/addresses; stale last-known URL copy allowed/open disabled; authenticating shows action/guidance; stale-auth disables it; approval state has no auth/open; terminal failure hides unavailable actions | S `TestSnapshotFactsAndAvailability`; C `PortalFactsFreshnessAndEffects` for all six variants, safe URL copy/open and no stale auth; M private URL usability/node identity, not proved by this fixture |
| `testSelectedPortalPublicAccessContract` | Public—Off comes from stored mode plus structured status; Remote App edit disabled; Local App port change works | S `TestPublicModeDestinationRestrictions`; C `PublicAccessProjection`; preserve current private-only listener fact. Future Funnel cases below |
| `testSelectedPortalDetailDailyActionsAndIsolation` | Stop then Space starts same Portal; focus remains on action; second Portal unaffected; edit draft isolated by selection; valid port commit; diagnostics window | S `TestAddressedPortalOnly` and `TestDestinationCommit`; C `SelectionDraftIsolationAndDiagnosticsDispatch`; U3 OS keyboard/focus plus representative UUID dispatch and U1 diagnostics window |
| `testSelectedPortalDetailShowsDestinationValidationErrors` | Return submits invalid port 0; safe validation message | S `TestInvalidDestinationRejected`; C `DestinationErrorsAndSubmit`; U2 invalid-field Return/focus representative (creation), U3 edit dispatch. Exhaustive validation stays below UI |
| `testSelectedPortalDetailTracksDestinationChangesFromManagementWindow` | Initial 8080 edit to 8081 updates displayed committed value; unchanged submit disabled | S `TestDestinationCommit`; C `DestinationDraftRebasesOnSnapshot` with accepted versus pending snapshots and no-op availability |
| `testAddPortalSheetPreservesDiscoveryAndDiscardsOnlyUncommittedDrafts` | Discovered port 9342 ungrouped, safe label and suggested name; invalid name/port Return retains focus; Escape has no enrolment side effect; reopen clean; Back returns to discovery; manual override 4321 commits/selects Portal; authenticate focus once and hittability, not refocused on later selection | S `TestDiscoveryAndManualCreate` and `TestNoCreateBeforeSubmit`; C `DiscoveryDraftBackCancelAndCommit` including numeric formatting/manual override, creation selection and one-time focus intent; U2 actual field focus/Return/Escape/Back, then U3 one-time authenticate focus/hittability; I retained record after disconnect; M actual enrolment separate |
| `testOpenPorticoRaisesTheManagementWindowAndClosingItKeepsTheMenuBarAppRunning` | Menu and Cmd-Shift-O raise one focused management window; no duplicate; Cmd-W leaves status item alive | U1 retains OS window activation/single window/menu lifetime. C `ManagementRouteRequests` checks routing intent. I additionally proves service/Portal survives **UI exit**, which this test never proved |
| `testOnlyFreshInstallationAutoOpensOverviewAndPersistenceFailureIsSanitized` | Fresh auto-opens; existing creation and migrated fixtures do not; initial-save failure shows attention without leaking overview message into compact menu | C `StartupPresentationDecisions` for all four variants; S `TestInitialSaveFailure` and migration facts; U1 one fresh launch plus unavailable-service route. Do not infer same-root restart persistence: original test uses separate fixtures |
| `testCompactAttentionRoutesConfiguredPersistenceFailureToOverviewMessage` | Configured failure does not auto-open; menu attention shows actual safe overview message rather than No Portals | C `ConfiguredFailureRouting`; U1 representative menu attention route |
| `testCompactMenuPortalActionMatrix` | Online open-only; Stopped start; authenticating auth; stale/approval hide unsafe actions; Removing/rejected records show status without active commands; detailed controls absent | S `TestAuthoritativeOperationMatrix`; C `CompactMenuProjection` for all six variants, exact command UUID and absence of detailed controls; U3 one compact action wiring check, no desktop state matrix |
| `testLocalAndRemoteDestinationEditorsSupportCrossKindChanges` | Local→Remote and Remote→Local fields and valid submit; start/stop remains available | S `TestDestinationKindTransitions`; C `DestinationEditorKinds` for both directions and preserved selection; U3 representative destination edit only |
| `testCompactMenuAttentionAndCommands` | Terminal/recovery/removal failure attention; compact menu excludes retry-removal/login offer; Online commands/labels/order/alignment; Settings focuses management window; Cmd-W/Cmd-Shift-D opens diagnostics; Cmd-Q quits | C `CompactCommandsAndAttention` for terminal/recovery/removal/login/online states and labels; U1 actual windows/diagnostics/Quit, U3 representative compact alignment/hittability; I service survives UI quit |
| `testSettingsHasInitialAccessibilityFocusTarget` | Cmd-comma reaches settings heading; test name alone does not prove actual focus | U5 tests actual accessibility focus as well as heading/shortcut; C `SettingsRoute` |
| `testLoggingRestartAndTerminalFailureStates` | Logging preference changes; restarting shown in settings/menu; Settings raises focused existing window; completed restart becomes Connected in both; terminal failure offers recovery | S `TestLoggingRuntimeReconfigurationAndFailure`; C `RuntimeRecoveryProjection` for restarting/connected/terminal states and action dispatch; U1 settings activation, not helper restart matrix; I actual service recovery |
| `testManagementPortalRemovalConfirmationRecoveryAndFocus` | Warning/manual cleanup link; Escape cancel; Space reopens via restored focus; Return confirms; completion notice/dismissal/sidebar; pending cleanup progress without retry; failed cleanup explicit retry | S `TestDurableRemovalAndRetry`; C `RemovalPresentationAndDispatch` (cancel sends no request, correct UUID/confirmation, completion/dismissal, progress/retry variants); U4 actual destructive dialog, keyboard/default button/focus; I crash-safe cleanup; M remote device removal remains manual |
| `testOverviewRecoveryAndResetTailnet` | Terminal retry in overview not compact menu; pending/completed rejection warnings and dismiss; reset only empty bound install; Escape cancels and confirm clears binding; nonempty install no reset | S `TestRejectionCleanupAlertsAndReset` covers durable ordering, empty-only and explicit confirmation; C `OverviewRecoveryAndResetDispatch` covers all variants and cancellation; U4 confirmation family including reset cancel/confirm; I cleanup restart |
| `testDebugAppHasDevIdentityAndNoLaunchAtLoginUI` | Dev display name; menu excludes login reminder; settings excludes all production launch-at-login/offer controls | C `VariantStartupPresentation` with exhaustive identifiers; existing `AppVariantTests`/`LaunchAtLoginTests` are prior art; U5 Dev identity and absence of production startup controls. I proves dev/test service roots/labels never reach production |

## Planned retained scenarios (five)

These are consolidated replacements, not instructions to delete 17 methods now.
Each uses a fake service boundary for deterministic UI states; real IPC belongs
in I. Keep domain matrices out of these flows.

| ID / planned method | Retained native checks | What lower levels cannot prove |
| --- | --- | --- |
| U1 `testNativeLaunchWindowAndMenuRouting` | Fresh launch guidance, service-unavailable attention route, menu/Open shortcut brings exactly one focused management window; sidebar/settings and diagnostics routes; close management preserves menu; Quit exits UI | Actual AppKit activation, SwiftUI Window/MenuBarExtra scene identity, focus and OS shortcuts. Fake routing does not create or activate a window |
| U2 `testNativeCreationSheetKeyboardAndDraftLifecycle` | Toolbar/empty entry sheet, discovered candidate then manual override, invalid Return focus, Back, Escape/cancel/reopen, valid create/select | Native sheet presentation, field hit-testing, Return/Escape handling and focus recovery. Pure state tests cannot prove AppKit delivers input to the intended field |
| U3 `testNativePortalActionsAndOneTimeFocus` | Two records; keyboard stop/start focus, representative compact action/alignment, one destination edit; committed creation gives hittable authenticate focus once, not on reselection | Real accessibility focus/hittability and keyboard/control wiring across menu/detail; fake dispatch alone cannot establish the actual button invokes it |
| U4 `testNativeDestructiveConfirmationAndRecoveryFocus` | Remove warning/link, Escape cancellation, Space reopen/focus, Return confirmation and completion dismissal; reset-tailnet confirmation cancel/confirm | Native destructive dialog default action and focus after cancellation; service confirmation booleans cannot prove user-visible confirmation is usable or precedes dispatch |
| U5 `testNativeSettingsAccessibilityAndVariantIdentity` | Cmd-comma settings, explicit initial accessibility focus target, Dev identity, no production login controls | Exposed native accessibility tree/focus and displayed running-app identity. Existing settings test asserts a heading only; strengthen the retained test rather than claiming existing focus evidence |

U1/U3 use scenario relaunches where needed; report method count and total app
launches separately so consolidation cannot hide execution work. U4 shares the
native destructive-confirmation guarantee for removal and reset while S/C keep
their different safety rules exhaustive. If implementation demonstrates another
unique OS guarantee, record and justify a changed retained inventory before
adding desktop tests. No arbitrary percentage target applies.

## Existing lower-level evidence to carry forward

These are source references, not claims of replacement completion:

- `PorticoTests/PortalStoreTests.swift`: historical migrations (v1–v5), save before
  removing source, invalid historical/current records and no fallback, app-root
  isolation. New service import preserves originals rather than invoking Swift
  migration's destructive historical-file cleanup.
- `PortalControllerTests.swift`: `testRemovalCommitsBeforePublishingAndOmitsPortalBeforeDeletingState`,
  `testRemovalCompletionRetainsPendingRecordUntilAtomicSaveSucceeds`,
  `testResetRequiresEmptyInstallationAndExplicitConfirmation`,
  `testRecoveryMarksFactsStaleAndReconcilesNewestCommittedSnapshot`,
  `testMismatchPersistsTombstoneBeforeCleanupAndRelaunchNeverRestartsIt`,
  `testOpensAuthenticationURLOnlyForPendingExplicitAction`, generation/old-request
  rejection, and hostile facts/diagnostics redaction.
- `PortalActionAvailabilityTests.swift`: authoritative action matrix, active/stopped
  edits and public/Remote App restriction. `PortalPresentationTests.swift`: identity,
  last-known URL, public status, fixed announcements and safe failure projection.
- `HelperSupervisorTests`, `HelperShutdownTests`, `ProcessHelperLauncherTests`,
  `HelperProtocolTests`: bounded cancellation/recovery, private exact-version and
  generation guarantees. Preserve outcomes at the service seam when retiring the
  private helper; do not duplicate obsolete process architecture mechanically.
- `DiagnosticsTests`, `LocalAppReachabilityTests`, `AppVariantTests`,
  `LaunchAtLoginTests`, `SmokeLaunchConfigurationTests`: redaction, probes, variant
  isolation, UI startup and isolated root admission.
- `helper/internal/portal/*_test.go`: proxy/header/TLS contracts, reconciliation,
  listener/shutdown ownership, auth URL notification delivery and cancellation.
  `helper/internal/discovery/*_test.go`: privacy/deduplication/probing/bounds.
  These remain valuable runtime tests; none proves what Tailscale returns on an
  explicit resume, which needs the real-runtime AUTH-1 run.

## Additional service-migration acceptance

These cases are new acceptance, not secretly covered by the original 22:

| Case / owner | Planned observable check and layer |
| --- | --- |
| Historical identity (#103) | S `TestImportVersionsOneThroughFive`: same UUID, name, destination, createdAt, logging, binding, removal/rejection and Public Access facts; byte-identical checkpoint of opaque identities |
| Failed/interrupted import (#103/#104) | S/I `TestMigrationCrashBoundaries`: unreadable, unsupported, corrupt, unsafe path, copy/write/fsync failure and crash before/after manifest; no original byte changes on failed migration, no empty fallback or stale remigration |
| Exclusive production writer (#104/#105) | I `LegacyRelaunchDuringCutover`: competing service and old UI/helper relaunch while quiescing, after commit and while service is stopped/restarting; preserved originals never start a second identity owner; MIG-1 must be proven before automatic transfer, otherwise explicitly blocked |
| UI login offer (#103/#105) | S `TestLoginOfferTransitionsAndRecovery`: snapshot/history, valid/invalid transitions, same-ID recovery; C `LoginOfferCommitsBeforeRegistration`: presented/accepted/declined survive reconnect, failed save prevents registration, failed native registration retains accepted choice, disable leaves offer spent; Dev/Linux cannot mutate it |
| Downgrade (#104/#105/#85) | I plus manual recovery walkthrough: stop/unregister verified, preserve generation, never start old writer against stale metadata/identities; document no reverse migration after new changes |
| Lost acknowledgement (#103/#104/#105) | S/I `TestCreateAckLostAcrossRestart`: accepted Create with dropped reply, restart, same-ID/payload retry yields original UUID and exactly one record/node; C retains same request identity across UI restart |
| Recovery limits (#103/#105) | S `TestReceiptConflictExpiryAndCapacity`: same ID/different payload, concurrent retry, no-op, clock rollback, capacity, window expiry; S `TestDurableAcceptanceBeforeRuntimeAndFailedCommit` and `TestUncertainCommitIsNeverReportedNotAccepted`: interrupted commit; C shows unknown expired result with no automatic new Create |
| Enrolment (#103/#104/#105) | S (fake runtime): `TestEnrolmentResumeAcrossDisconnectAndRestart` (A disconnects, B explicitly resumes, service restarts; record, Enabled intent and identity survive; an equal URL goes once and only to B; nothing in state, receipts, snapshots, diagnostics or errors contains it; same-ID retry after restart returns the receipt without a new attempt), `TestEnrolmentRejectsCallbacksFromAReplacedRuntime`, `TestEnrolmentLeaseEndsOnStopRemovalAndRebuild`, `TestEnrolmentRejectsUnsafeURLs`, `TestEnrolmentRefusals`, `TestEnrolmentStartFailureIsReportedToTheRequester`. Real runtime (opt-in, `PORTICO_REAL_TSNET_AUTH1=1`): `TestRealRuntimeAuth1ResumeAcrossDisconnectAndRestart`. Planned for #104/#105: C no automatic browser opening on reconnect; I real-socket disconnect and requester routing |
| Platform capability (#103/#104) | S `TestDiscoveryAndManualCreate`, `TestLocalAppReachability`; I macOS automatic discovery retained; Linux explicitly unavailable with explicit-port creation/reachability working; unavailable parity is not a Linux release failure |
| Lifecycle (#104) | I with installed isolated roots: UI exits without stopping nodes; service restart restores enabled intent/pending cleanup; stopped records do not start; failed approval/start is visible and no fallback writer exists |
| First milestone (#104/#105) | I fake runtime proves bytes/identity and IPC lifecycle; M separately records actual original Tailscale stable node ID and private HTTPS continuity before/after UI exit and service restart |
| Public Access/Funnel (remaining delivery) | S matrices for public Local App-only creation/edit, explicit exposure consent, fail-closed unknown state, private/public listener separation and readiness, denied capability, switch/stop/removal/restart and failures; runtime tests prove actual listener shutdown before any private claim; C structured Public—Off/current/stale rendering and confirmation dispatch. M actual external exposure when Funnel ships. Do not claim current v7 private-only listener is implemented Funnel |

## Replacement and measurement gate

#103 supplies service command/snapshot test evidence before UI ownership moves.
#104 supplies real OS IPC/lifecycle and exclusive-owner evidence. #105 supplies
fake-client presentation/dispatch checks and the five retained VM scenarios.
#106 reconciles every assertion row to committed tests/results before removing
redundant native cases; preserve original coverage until replacement passes.
AUTH-1 (revised) and MIG-1 remain explicit blockers for affected acceptance. Preserve native
issue dependencies #102 → #103 → #104 → #105 → #106 (additional earlier blockers
remain recorded); completing this inventory does not mark them ready-for-agent.

Measure baseline before deleting tests and candidate after replacement with
`Scripts/test-ui-in-vm.sh` from the respective exact commits. Use the same host,
Tart `macos-golden-gate-xcode:27` image, CPU/RAM, Xcode/SDK, test selection and
cache/build conditions; record these plus commit, method count, scenario launch
count, total wall time and XCTest execution time separately. Run three successful
samples per suite, report each and median; separate image boot/build/setup from
execution. Do not compare a filtered warm test run with a full cold baseline.
Results belong in `.build/test-results/xcode-ui-vm/` with durable redacted summary
links in #106. Failed runs and unavailable VM/image remain disclosed; no host UI
execution is allowed. Service/client/IPC suites have separate timings and results.

For #102 the baseline source count is 22 and proposed native count is 5;
measured before/after times are **not collected**. Exact-baseline Swift and Go
[CI passed](https://github.com/chrisbanes/portico/actions/runs/41199004901), which
supports inventory context, not replacement coverage or a performance claim.
