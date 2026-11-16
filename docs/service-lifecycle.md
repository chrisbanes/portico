# Service lifecycle

`portico-service` is the per-user Portal-management service from
[the local service contract](local-service-contract.md). This page covers how
it runs under launchd and `systemd --user`, where it keeps its state, and how
its failures show up. Packaging the executable and unit belongs to #85, and
registering the macOS LaunchAgent belongs to #105.

## Variants and roots

| Variant | `--variant` | State root | Management endpoint |
| --- | --- | --- | --- |
| Production | `production` | macOS: `~/Library/Application Support/Portico`. Linux: `$XDG_STATE_HOME/portico` | `portico/management.sock` in the user's runtime directory |
| Dev | `dev` | macOS: `~/Library/Application Support/Portico Dev`. Linux: `$XDG_STATE_HOME/portico-dev` | `portico-dev/management.sock` |
| Test | `test` | `--state-root` (required, absolute) | `portico-test/management.sock` under `--runtime-dir` (required, absolute) |

- `$XDG_STATE_HOME` defaults to `~/.local/state`. A relative value is refused.
- The runtime directory is the Darwin per-user temporary directory on macOS,
  and `$XDG_RUNTIME_DIR` on Linux. A missing, relative or shared
  `$XDG_RUNTIME_DIR` is refused; there is no `/tmp` fallback.
- Only the test variant takes path flags. The service never takes a path from
  a client.
- The service refuses to run as root, before it touches any file.
- The service creates a missing state root with mode 0700. It tightens an
  existing root it owns that others can only read or search, and refuses one
  that is a symlink, that another user owns, or that its group or others can
  write, since they may have planted entries in it.

**macOS production and Dev run behind the legacy-writer guard.** An older
Portico app may still read and write those roots, so on macOS both variants
always import through the MIG-1 guard described below, and require it on
every later start. A test variant asks for the guard with `--legacy-guard`,
which only macOS accepts. On macOS a test variant without it refuses a
`--state-root` that is, contains or is inside either root, after resolving
symlinks and ignoring case. Linux has no older app, so neither Linux
variant has a guard.

## Startup and shutdown

On start the service:

1. resolves and validates its roots;
2. takes the lifetime lock, `service-owner.lock` in the state root;
3. loads the committed state, or imports the historical installation;
4. starts the engine, which reconciles Enabled Portals and retries pending
   cleanup once, while Stopped Portals stay down;
5. listens on the management socket and serves.

If another service already holds the lock, the new one exits with status 1
and the reason `ownershipBusy`, without touching the endpoint.

On SIGTERM or SIGINT the service stops accepting connections, closes every
client session, then closes the runtime within 10 seconds. It releases the
lock only after that close returns, and then exits 0. If the runtime does not
close in time, the service exits 1 while still holding the lock, so no
replacement starts until the old nodes have ended with the process.

Disconnecting or killing a client never changes Portal intent. An idle
service with no clients keeps running.

## Startup failures

When the lock is held but the state cannot be loaded, because it is
unsupported, corrupt or unreadable, the service does not reset to an empty
install and starts no runtime. Instead it keeps the lock and serves:

- the management hello and an authenticated `getSnapshot` whose
  `service.startupFailure` is `unsupportedState` or `persistenceUnavailable`;
- that same code as the error for every other call.

It also logs `startup failure: <code>` to standard error. Logs carry codes
only, never paths. A guard failure is `unsupportedState` to clients, and the
log adds the specific reason: `startup failure: unsupportedState
(migrationFailed)` or `(migrationGuardMissing)`.

- `unsupportedState` needs a person: the service serves it until it is
  stopped. Recovery is manual: fix or restore the state root, then restart
  the service.
- `persistenceUnavailable` may clear by itself, for example once a volume is
  mounted again. The service serves it for 30 seconds, then exits 1, so the
  service manager's back-off loads the state again. The failure stays
  visible: in the snapshot while it is served, and in the manager's status
  and log between runs. Under systemd the start limit below ends the retries
  after five failed starts: each cycle takes about 35 seconds (30 served plus
  `RestartSec=5`), so after about 3 minutes the unit stays failed and
  clients see the service as not running rather than `persistenceUnavailable`,
  until `systemctl --user reset-failed portico-service` and a start.

Either way the service exits 1 when it stops, so the manager records a
failed run. An explicit `launchctl bootout` or `systemctl --user stop` does
not restart it; only an unexpected exit, such as a signal from elsewhere or
the `persistenceUnavailable` exit, does.

## Legacy-writer guard (macOS)

Portico v0.0.1 and v0.0.2 read `installation-v4.json` first, and the current
app reads `installation-v5.json` first. Neither takes the service's lock. On
macOS the service's first import therefore replaces both files with the
guard: the bytes `{"version":2147483647,"porticoServiceGuard":1}`, mode 0600,
with the owner's immutable flag (`uchg`). No reader accepts that version,
and none can save over an immutable file, so an old app fails to load and
to save, and so starts no Portal. The flag needs no root to set or clear.

The import runs in this order under the lifetime lock:

1. Read and validate the authoritative record, writing nothing.
2. Checkpoint each guarded record and the source in `service/guard/`:
   copies in `originals/`, and their presence, mode and SHA-256 in
   `checkpoint.json`. Fsync.
3. Write `service/guard/intent.json`, naming the checkpoint's hash. Fsync.
4. Install the guard at each record: temporary file, fsync, rename, root
   fsync, then the flag, then verify.
5. Stage the generation from the checkpointed source, and commit the
   manifest. Only then can the runtime start.

An interruption before step 3 leaves the originals untouched, and the next
start imports again. After step 3 the next start rolls forward: it verifies
the checkpoint against the intent, imports from the checkpoint copies, and
never removes the guard. A guard without a verified checkpoint fails closed
with `unsupportedState`, touching nothing. A failure no retry can fix, such
as an identity that became unsafe, whether in the first attempt or a roll
forward, first writes `service/migration-failed.json` and then restores the
original bytes and modes, or removes a guard whose record did not exist. If
the marker cannot be written, nothing is restored and the next start rolls
forward again. Every later start reports `migrationFailed`, and nothing
guards the root again until the manual recovery below. A record that
already has a file flag such as `uchg` is refused with `unsupportedState`
before anything is guarded.

After the commit, every start verifies that both guards are present,
regular, 0600, immutable and unchanged before the runtime starts. Otherwise
the start fails with `migrationGuardMissing`, and nothing re-guards the root
automatically.

`Scripts/verify-legacy-guard.sh` compiles the reader from every `v*` tag and
from HEAD, and runs `TestLegacyReadersExcludedAtEveryPhase` against them. CI
runs it in the `Legacy guard` job. `Scripts/test-legacy-exclusion-in-vm.sh`
repeats the proof with the real released apps in a Tart VM.

**Uninstalling.** The guards keep the immutable flag after the service is
removed. Deleting the Portico folder in `~/Library/Application Support`
fails with "Operation not permitted" until the flag is cleared:
`chflags -R nouchg ~/Library/Application\ Support/Portico`. An uninstaller
(#85) must do the same.

## Recovery and downgrade (macOS)

There is no automatic downgrade. To return a root to an older app:

1. Stop the service and prevent its restart: `launchctl bootout
   gui/$(id -u)/dev.chrisbanes.Portico.Service`, or `...Debug.Service` for Dev,
   until #105 provides unregistration. Check that `launchctl print` no longer
   finds the job, and that `lsof` shows nothing holding
   `service-owner.lock`.
2. Preserve the service state outside the root:
   `ditto ~/Library/Application\ Support/Portico/service ~/Library/Application\ Support/Portico\ Service\ Backup`.
3. Restore only when nothing happened after the checkpoint. In the
   committed generation named by `service/manifest.json`, `state.json` must
   have no `migration.firstMutationRequestID`, and every `tsnet/...` hash in
   the manifest's `sources` must still match the generation's file. If either
   check fails, stop: there is no supported reverse import, so keep the
   generation and never start the old app against the stale originals.
4. For each record in `service/guard/checkpoint.json`, clear the flag with
   `chflags nouchg`. If the checkpoint marks it present, copy
   `service/guard/originals/<name>` back with the recorded mode and check its
   SHA-256. If it was absent, delete the guard.
5. Remove `service/` and `service-owner.lock` from the root. Only then open
   the older app.

After `migrationFailed`, stop the service first, as in step 1. The
originals should already be restored: check each record against the
checkpoint, and apply step 4 to any target that still holds the guard. Then
move `service/` aside as in steps 2 and 5. The next start imports and guards again. After
`migrationGuardMissing`, an older app may have written the records: compare
them with the checkpoint before doing anything else. If they changed, keep
both and do not merge identities by hand.

`Scripts/test-legacy-exclusion-in-vm.sh` follows these steps in its
recovery phase.

## macOS: launchd

`helper/packaging/macos/` holds one LaunchAgent per variant:

- `dev.chrisbanes.Portico.Service` (production);
- `dev.chrisbanes.Portico.Debug.Service` (Dev);
- `dev.chrisbanes.Portico.ServiceTest` (test). This is a template that
  `Scripts/test-service-lifecycle.sh` fills in with temporary paths. It is
  never registered with `SMAppService`.

Each LaunchAgent runs at load and is restarted after a crash or failed exit
(`KeepAlive` with `SuccessfulExit` false), throttled to one start per 10
seconds, with umask 077. `launchctl bootout` removes the job without a
restart. The service runs for the login session: logout or
shutdown ends it. To inspect a loaded agent, run
`launchctl print gui/$(id -u)/<label>`. Its `state`, `pid`, `runs` and
`last exit code` show a crash loop or a failed start.

## Linux: systemd --user

`helper/packaging/linux/portico-service.service` is a user unit.

**User and privilege model.** The service runs as the logged-in user, in that
user's own systemd user manager, with no root, sudo, setuid or system-wide
unit. `NoNewPrivileges=yes` stops it gaining privileges. It refuses to run as
root, and its socket accepts only peers whose kernel-reported UID is its own.
State and socket permissions come from `UMask=0077` and the service's own
0700 and 0600 modes.

**Commands.** Install the unit as `~/.config/systemd/user/portico-service.service`
and the executable at `~/.local/bin/portico-service`. #85 may choose other
locations. Then:

```sh
systemctl --user daemon-reload
systemctl --user enable --now portico-service   # start now and at every login
systemctl --user status portico-service         # state, last exit, recent log
systemctl --user stop portico-service
systemctl --user disable portico-service
journalctl --user -u portico-service            # full log, where readable
```

`Restart=on-failure` restarts the service after a crash or a failed exit, 5
seconds apart. Five starts within 5 minutes leaves the unit failed, and while
that limit holds even a manual start is refused, until the 5 minutes pass or
`systemctl --user reset-failed portico-service` runs. `systemctl --user stop`
is never followed by a restart, whatever the exit status.

**Session-only lifetime.** The unit is wanted by the user's
`default.target`, so it starts when the user manager does, at the user's
first login. It stops when the last session ends. Portico does not enable
lingering, so it does not promise to run before login or after logout. A
user who wants that can run `loginctl enable-linger` themselves; the CI
lifecycle job does this only to get a user manager on a headless runner.

**Unsupported hosts.** Hosts without a systemd user manager are not
supported yet. This includes non-systemd distributions, many containers, and
sessions where `systemctl --user` cannot reach a user bus. On those hosts the
service can still be run by hand, but nothing restarts it or starts it at
login.

**Failure visibility.** `systemctl --user status portico-service` shows a
failed start as `activating (auto-restart)` or `failed`, with
`code=exited, status=1/FAILURE`. Its recent log lines include
`startup failure: <code>` or the reason for an early exit. The snapshot's
`service.startupFailure` gives the same code over IPC.

## Lifecycle check

`Scripts/test-service-lifecycle.sh` runs a `porticofake` build, with no
tailnet nodes, under the platform's service manager. It uses only the test
label `dev.chrisbanes.Portico.ServiceTest` or the unit
`portico-service-test.service`, with roots in a fresh `/tmp/portico-lc.*`
directory that it removes on exit. It prints a PASS line for each step:

1. starting under launchd or `systemctl --user`;
2. Portal creation and enabling over IPC;
3. a killed client that leaves the Enabled Portals running;
4. recovery after `kill -9`: Enabled Portals reconciled, the Stopped one
   down, the pending removal retried, and UUIDs and identity hashes unchanged;
5. a same-ID retry that returns the original receipt;
6. a visible startup failure on unsupported state;
7. no TCP listener, and private modes;
8. a clean stop.

On macOS the service runs with `--legacy-guard`, so the seeded v5 fixture is
imported through the guard, and the check also asserts the guard after the
import, after the `kill -9` restart and after the stop. This is the fake
first milestone.

CI runs it on macOS and Linux in the `Service lifecycle` job.

## Milestone M (manual)

M proves that a real Portal keeps its Tailscale identity when the service
takes over. It needs an enrolled Portico app, a running Local App, and a
second device on the same tailnet. Record only redacted outcomes: a short
hash of the stable node ID, "unchanged" or "changed", and HTTP status codes.
Never record node IDs, names, URLs, keys or paths.

1. With the old app running and its Portal Enabled, record:
   - the stable node ID of the Portal's node, from `tailscale status --json`
     on the second device, as `printf %s "$id" | shasum -a 256 | cut -c1-12`;
   - that the Assigned Name shown in Portico is present;
   - the status of `curl -sS -o /dev/null -w '%{http_code}\n'
     https://<assigned-name>.<tailnet>.ts.net/` from the second device;
   - that the Local App answers on its port on this Mac.
2. Turn off Launch at Login in the old app, quit it, and check that
   `pgrep -x portico-helper` prints nothing. Do not reopen it until step 5.
3. Build the real service and the test client, then copy the root and run
   the test variant against the copy, through the guarded import:

   ```sh
   m=$(mktemp -d /tmp/portico-m.XXXXXX)
   (cd helper && go build -o "$m/portico-service" ./cmd/portico-service &&
     go build -o "$m/lifecycleclient" ./internal/servicehost/internal/lifecycleclient)
   ditto ~/Library/Application\ Support/Portico "$m/state"
   mkdir -m 700 "$m/run"
   "$m/portico-service" --variant test --state-root "$m/state" --runtime-dir "$m/run" --legacy-guard
   ```

   In another terminal, `"$m/lifecycleclient" -runtime-dir "$m/run" state`
   must show `startupFailure=` empty and the Portal `enabled`.
4. Repeat the step 1 checks:
   - with no client connected: run `"$m/lifecycleclient" -runtime-dir "$m/run" subscribe`,
     and press Ctrl-C;
   - after a restart: press Ctrl-C on the service, wait for it to exit 0,
     and restart the service with the `"$m/portico-service"` command from step 3.

   The node-ID hash and the HTTPS status must be unchanged each time.
5. Stop the service with Ctrl-C and delete the copy:
   `chflags -R nouchg "$m" && rm -rf "$m"`. Then reopen the old app, turn
   Launch at Login back on if it was on, and check its Portal again.

If M cannot be run, report it as the remaining human-required evidence for
#104.
