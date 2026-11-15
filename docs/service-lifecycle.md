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
- The service creates a missing state root with mode 0700 and tightens a
  looser one it owns. It refuses a root that is a symlink or that another
  user owns.

**macOS production and Dev are refused for now.** An older Portico app may
still write those roots, and nothing yet stops it. Until #104's T5 slice
installs the MIG-1 legacy-writer guard, `portico-service` exits with status 1
for these two variants before it touches any state. The test variant and
both Linux variants run normally, because no older app runs on Linux.

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
only, never paths. When stopped it exits 1, so the service manager records a
failed run and restarts it after its back-off. Recovery is manual: fix or
restore the state root, then restart the service.

## macOS: launchd

`helper/packaging/macos/` holds one LaunchAgent per variant:

- `dev.chrisbanes.Portico.Service` (production);
- `dev.chrisbanes.Portico.Debug.Service` (Dev);
- `dev.chrisbanes.Portico.ServiceTest` (test). This is a template that
  `Scripts/test-service-lifecycle.sh` fills in with temporary paths. It is
  never registered with `SMAppService`.

Each LaunchAgent runs at load and is restarted after a crash or failed exit
(`KeepAlive` with `SuccessfulExit` false), throttled to one start per 10
seconds, with umask 077. The service runs for the login session: logout or
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
seconds apart. Five starts within 5 minutes leaves the unit failed until
`systemctl --user reset-failed portico-service` or a manual start. A clean
stop is not restarted.

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

CI runs it on macOS and Linux in the `Service lifecycle` job.
