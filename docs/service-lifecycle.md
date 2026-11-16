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

**macOS production and Dev are refused for now.** An older Portico app may
still write those roots, and nothing yet stops it. Until #104's T5 slice
installs the MIG-1 legacy-writer guard, `portico-service` exits with status 1
for these two variants before it touches any state. On macOS the test variant
also refuses a `--state-root` that is, contains or is inside either root,
after resolving symlinks and ignoring case. The test variant and both Linux
variants otherwise run normally, because no older app runs on Linux.

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
only, never paths.

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

CI runs it on macOS and Linux in the `Service lifecycle` job.
