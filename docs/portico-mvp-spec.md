# Portico MVP Specification

**Status:** Confirmed product and technical specification

**Last updated:** 2026-08-04

## Product

Portico is a native macOS menu-bar app that gives a local HTTP application a
stable, private HTTPS address on a Tailscale tailnet. A user creates a Portal
such as:

```text
https://hermes.<tailnet>.ts.net/ -> http://127.0.0.1:8787
```

Each Portal is an independent Tailscale node. Portico never renames, retags,
or proxies through the Mac's existing Tailscale node, and it does not require
the Tailscale macOS app to be installed or running.

The MVP is for a technical tailnet owner or administrator. Portals are
user-owned nodes authenticated in a browser; auth keys, OAuth clients, and
tagged service identities are outside the MVP.

## Validated Tailscale model

Current official Tailscale documentation supports the intended model:

- [`tsnet` embeds independent userspace Tailscale nodes](https://tailscale.com/docs/features/tsnet).
- A process can create multiple [`tsnet.Server`](https://tailscale.com/docs/reference/tsnet-server-api)
  instances when each uses a unique persistent directory.
- `Server.ListenTLS("tcp", ":443")` provides HTTPS inside the tailnet.
- Persistent server state lets a node reconnect after the process restarts.
- Structured local status exposes authentication, connectivity, IP,
  certificate-domain, and tailnet information through
  [`local.Client`](https://pkg.go.dev/tailscale.com/client/local) and
  [`ipnstate.Status`](https://pkg.go.dev/tailscale.com/ipn/ipnstate).

The executable spike must still prove lifecycle details against the pinned
Tailscale version. Live enrollment, certificate issuance, identity retention,
and two-node coexistence are deliberately deferred to the manual acceptance
run described below.

## User experience

### App shape

Portico is a `MenuBarExtra` app without a Dock icon. The popover contains the
Portal list, Add Portal flow, and daily controls. Settings and detailed
diagnostics open in separate native windows.

Launch at login is off by default. After the first Portal becomes online,
Portico offers one non-blocking choice to enable it and does not repeat the
prompt after the user declines. Enabled Portals reconnect whenever Portico is
running; a Stopped Portal remains stopped across relaunches.

The live `SMAppService.mainApp.status` value is the sole truth for whether
launch at login is enabled, disabled, awaiting approval, or unavailable.
Portico persists only whether the one-time offer has not been offered, was
presented, was declined, or was accepted. It records the presented or accepted
intent before showing the offer or asking ServiceManagement to register. A
registration failure leaves the accepted intent intact and exposes an explicit
Retry action in Settings. Signed-app registration is verified under #11 rather
than from the unsigned development or UI-test app.

### Add Portal

Before enrollment, Portico briefly explains that the tailnet might require:

- MagicDNS to be enabled;
- HTTPS certificates to be enabled;
- administrative device approval; and
- acceptance that the certificate name is published in Certificate
  Transparency logs.

Portico cannot verify all tailnet-wide settings without broader API access,
so this explanation does not block setup. Runtime failures link to the
relevant Tailscale administration page. See Tailscale's
[HTTPS setup](https://tailscale.com/docs/how-to/set-up-https-certificates) and
[device approval](https://tailscale.com/docs/features/access-control/device-management/device-approval)
documentation.

The user supplies:

1. An immutable Portal Name.
2. A local HTTP port from 1 through 65535.

The port can be entered manually or selected from a refreshable list of
current-user TCP listeners that accept a connection through `127.0.0.1`.
Discovery is a convenience, not proof that a listener speaks HTTP or is
healthy. Portico does not scan port ranges and does not issue HTTP requests to
classify services.

Detected listeners show a sanitized process label. Portico never displays,
persists, or logs a raw command line. It offers an editable name hint only
when a useful entry point can be extracted confidently:

- `java -jar /path/to/tool/bin.jar` suggests `bin`;
- `python -m package.server` suggests `server`;
- `python /path/to/hermes.py` suggests `hermes`;
- a Node entry script or recognized tool suggests its basename; and
- a non-generic direct executable suggests its executable basename.

Flags, values, parent paths, and extensions are discarded. Generic or
ambiguous commands such as bare `node`, `python`, or `java` leave the Portal
Name blank. Suggested names are normalized conservatively to a DNS-style
label and remain editable until enrollment starts.

When tsnet requires authentication, Portico displays an **Authenticate**
button. It opens the transient URL only after the user clicks. Authentication
URLs are never persisted, logged, copied into diagnostics, or reused after the
helper generation that supplied them becomes stale.

The first Portal to become online binds this Portico installation to one
tailnet. Later Portals must report the same `CurrentTailnet.Name`. A Portal
authenticated into a different tailnet is rejected, its local state is
deleted, and Portico warns that the newly created remote node might require
manual cleanup. The tailnet binding can be reset only when no Portals exist.

Tailscale enforces unique machine names and can assign `hermes-1` when
`hermes` already exists. Portico accepts the Assigned Name and derives the
Portal URL from structured certificate-domain status rather than constructing
it from the requested name. See Tailscale's
[machine-name behavior](https://tailscale.com/kb/1098/machine-names).

### Portal controls

Each Portal offers:

- Start and Stop, with the desired state persisted;
- Edit Local App Port without changing the Portal identity;
- Copy Portal URL;
- Open Portal URL;
- Diagnostics; and
- Remove.

Portal Name is immutable after enrollment. A different address requires a
new Portal.

Removal stops the Portal and permanently deletes its local configuration and
tsnet identity after one explicit warning. The warning shows the Assigned
Name, states that the remote tailnet node remains, and links to Tailscale's
[manual device-removal instructions](https://tailscale.com/docs/features/access-control/device-management/how-to/remove).

The UI derives every action from one authoritative availability projection:

| Action | Available when | Unavailable or stale behavior |
| --- | --- | --- |
| Add Portal | Logging is decided and the name and port validate | Saves locally without requiring a helper connection |
| Refresh Local Apps | Helper is connected and no refresh is active | Disabled while awaiting choice, restarting, retrying, or failed |
| Start / Stop | Active Portal whose desired state permits the transition | Commits locally while the helper is unavailable |
| Edit Local App Port | Active Portal and a changed, valid port | Commits locally while the helper is unavailable |
| Authenticate | Active and Enabled, helper connected, current non-stale state authenticating, no request pending | Never uses stale state or a stale authentication URL |
| Copy Portal URL | A sanitized current or session-stale URL exists | Session-stale URLs are labelled **Last Known** and remain copyable |
| Open Portal URL | Current non-stale URL and current Tailscale state online | Never opens a stale URL; Local App unreachability does not disable it |
| Diagnostics | Globally always; per Portal while its record exists | Independent of helper availability |
| Remove | Active Portal | Commits destructive intent locally; cleanup waits for connection |
| Retry Removal | Removing, cleanup failed, and helper connected | Otherwise waits for helper recovery |
| Reset Tailnet | A binding exists and no Portal record remains | Preserves logging and launch-at-login offer state |
| Settings | Always | Remains usable if ServiceManagement is unavailable |

### Keyboard and accessibility

The popover uses visible text and standard focusable controls; helper, Portal,
and action status never relies on color or an icon alone. Traversal follows the
visual source order: helper and global state, Portal rows in creation order,
the Add Portal guidance and form, Reset Tailnet when eligible, then Settings,
Diagnostics, and Quit. Within a row, identity and the three independent states
precede URL actions, authentication, port edit, Start or Stop, Diagnostics,
and Remove.

`Command-,` opens Settings, `Command-Shift-D` opens Diagnostics, and
`Command-Q` quits. Space or Return activates the focused control and Escape
cancels destructive confirmation without mutation. Settings and Diagnostics
initially focus their headings; removal restores focus to the Portal row or
completion notice, and Add validation errors return focus to the invalid
field. Portico posts one fixed polite VoiceOver announcement for helper
connection or terminal failure, a Portal becoming online, removal success or
failure, and logging-restart success or failure. It never announces retry
ticks, raw errors, paths, URLs, or credentials.

## Runtime model

### State

Runtime state has independent dimensions rather than one overloaded enum:

- Desired state: enabled or stopped.
- Tailscale state: authenticating, awaiting approval, connecting, online,
  stopped, or error.
- Local App reachability: unknown, reachable, or unavailable.

For example, a Portal can be online on its tailnet while its Local App is
unavailable. Reachability means only that `127.0.0.1:<port>` accepts a short
TCP connection. It is not an application health check and never sends HTTP.

### Persistence

Swift owns an atomically written version-3 configuration at
`installation-v3.json` containing:

- Portal UUID;
- immutable Portal Name;
- Local App port;
- desired state;
- creation time;
- the installation-global operational-support logging preference; and
- the one-time launch-at-login offer state.

Assigned Name, Portal URL, tailnet, IPs, and authentication URL are helper
facts, not persisted Portal configuration. Safe Portal facts may remain marked
**Last Known** only within the current app session after helper loss. After a
relaunch, actions wait for newly received current facts.

A genuinely new installation starts with logging undecided and does not launch
the helper. Existing v1 installations, and v2 installations containing any
Portal, tailnet binding, or alert, migrate to logging enabled so their previous
behavior is preserved; a pristine empty v2 installation migrates to undecided.
Every migration starts with the launch-at-login offer not offered. A valid v3
record takes precedence, while an invalid v3 fails closed instead of falling
back. Migration atomically saves v3 before removing the older record.

The installation tailnet binding stores `CurrentTailnet.Name` and caches its
MagicDNS suffix for display. The helper owns no separate product database.

Each Portal's tsnet state lives beneath a UUID-keyed directory under Portico's
Application Support directory. Hostnames are never used as identity paths.
The state directory and files are restricted to the current user and treated
as sensitive node identity material.

### Native/helper boundary

Swift launches and supervises one bundled Go helper. On launch it sends the
complete desired Portal set and reconciles helper state. The helper contains
one serialized lifecycle owner and one `tsnet.Server` for every running
Portal.

The protocol is versioned JSON Lines over standard input and output:

- every request has a protocol version, request ID, command, and typed
  payload;
- every response echoes the request ID and contains either a typed result or
  structured error;
- asynchronous events carry an event type, optional Portal UUID, and typed
  payload; and
- standard output is protocol-only while standard error is sanitized
  diagnostics-only.

The macOS app and bundled helper require an exact protocol-version match. Full
desired-set reconciliation established protocol version 2. Guarded Portal
removal establishes protocol version 3 because it adds the required
`removePortal` command; version 3 retains the version-2 reconciliation
semantics and accepts neither version 1 nor version 2 peers. A new required
command, field, outcome, or semantic change requires another version bump,
while an optional field may remain in a version only when older peers can
safely ignore its presence or absence. The app and helper ship together.

Protocol version 3 uses `reconcilePortals` as the only command that starts or
stops a Portal or changes its Local App port. The command carries the complete
set of Portal records whose cleanup lifecycle is `active`:

```json
{
  "version": 3,
  "requestId": "request-42",
  "command": "reconcilePortals",
  "payload": {
    "portals": [
      {
        "portalId": "9f55ca93-d7b3-4eab-a871-310ea576005a",
        "portalName": "hermes",
        "localAppPort": 8787,
        "desiredState": "enabled"
      }
    ]
  }
}
```

The helper validates the complete snapshot before changing any runtime. An
invalid UUID, Portal Name, Local App port, desired state, duplicate UUID, or
unknown field rejects the whole command with the fixed `invalidPayload`
protocol error and performs no lifecycle work. Request array order has no
meaning.

For a valid request, the helper serially processes the union of snapshot UUIDs
and UUIDs present in its runtime map when reconciliation begins, in ascending
lower-case UUID order. It returns exactly one entry for every UUID in that
union, in the same order:

```json
{
  "version": 3,
  "requestId": "request-42",
  "result": {
    "entries": [
      {
        "portalId": "9f55ca93-d7b3-4eab-a871-310ea576005a",
        "outcome": "converged"
      }
    ]
  }
}
```

The fixed outcome enum is `converged | startFailed | closeFailed`:

- `converged` means the helper reached the local lifecycle target. An Enabled
  Portal has one runtime with the requested immutable Portal Name and current
  Local App port; it need not yet be authenticated or online. A Stopped Portal
  has no runtime. A runtime omitted from the snapshot has been closed without
  deleting its UUID-keyed identity directory. An already-satisfied target is
  also `converged` and causes no restart.
- `startFailed` means an Enabled Portal could not be brought to that local
  target. The helper retains ownership of any partially created runtime until
  cleanup is confirmed, never starts a second runtime for that UUID, and never
  replaces an existing runtime whose Portal Name conflicts with the immutable
  name for that UUID. A later reconciliation may retry.
- `closeFailed` means a runtime that had to close is not confirmed closed. The
  helper retains ownership of it, never starts a second runtime for that UUID,
  and retries closure only during a later reconciliation. Other entries still
  reconcile.

Changing the Local App port on an existing Enabled Portal atomically routes
new requests through a new loopback proxy handler. Requests and WebSockets
already accepted by the old handler drain against the old port. The tsnet node,
listener, state directory, Assigned Name, and Portal URL are unchanged.

Per-Portal runtime errors map only to the fixed outcomes above. Raw error text,
paths, and underlying library errors never appear in the result or a protocol
error. A valid reconciliation always returns a result after every relevant
entry has completed its attempt, even when one or more entries failed; partial
failure is not an envelope error and does not terminate the helper. If the
process or connection ends before the correlated response, Swift treats the
request as unresolved and later reconciles the newest committed snapshot.

The envelope `requestId` is the sole wire correlation token; the result does
not repeat the snapshot or add an acceptance flag. Swift associates the
request with a local configuration generation, permits newer edits while it is
in flight, and coalesces them into the newest committed snapshot. An outcome
for a superseded generation cannot update current UI state; after that request
completes, Swift immediately sends the latest snapshot. Desired state is never
rolled back after a failed outcome.

After a Portal's durable lifecycle becomes `pendingRemoval`, Swift omits it
from active reconciliation and sends one automatic cleanup cycle during that
controller session. A cycle first awaits the newest active-only reconciliation
result. A missing or `converged` entry permits the UUID-only removal request;
`closeFailed`, another non-converged outcome, an envelope failure, or helper
loss retains the pending record and requires an explicit Retry. Helper
reconnection does not replenish the automatic attempt.

```json
{
  "version": 3,
  "requestId": "request-43",
  "command": "removePortal",
  "payload": {
    "portalId": "9f55ca93-d7b3-4eab-a871-310ea576005a"
  }
}
```

The removal payload contains no path or hostname. The helper normalizes and
validates the UUID, opens the canonical trusted state root with rooted
filesystem operations, rejects a symlink or non-directory target, closes the
addressed runtime, and removes only that direct UUID child. An absent child is
idempotent success. Runtime ownership is released only after close and deletion
both succeed; fixed protocol errors never expose underlying paths or errors.
Swift removes the pending record only after helper success and a successful
atomic configuration save. A save failure retains the pending record for an
explicit idempotent retry. Completion is a session-only notice and always
explains that the remote Tailscale device may require manual removal; the
installation tailnet binding is unchanged.

Handshake, authentication, cross-tailnet rejected-identity cleanup, listener
discovery, and graceful shutdown remain separate commands. Authentication
remains transient. Imperative protocol-v1 `startPortal` is not accepted as a
lifecycle fallback by a protocol-v3 helper.

If the helper exits unexpectedly, Swift restarts it with bounded exponential
backoff and restores Enabled Portals. After the retry budget is exhausted,
Portico shows a global failure with a manual Retry action.

Changing the committed operational-support logging choice is a controlled,
commit-first restart. Swift saves and publishes the new value, marks helper
facts stale, gracefully shuts down the old helper with the existing one-second
limit, and starts the replacement only after old-process ownership clears.
The replacement receives a fresh recovery budget and the newest coalesced
Portal snapshot. Old-process responses, events, and authentication URLs are
generation-fenced. Local Add, Start, Stop, port edit, and Remove intent may
still commit while the helper is unavailable; helper-dependent Authenticate,
listener refresh, and removal Retry remain unavailable until reconnection.

For logging enabled, Swift removes any inherited
`TS_NO_LOGS_NO_SUPPORT` from the child environment. For logging disabled, it
sets that variable to exactly `true` before the helper starts. This preference
does not alter the helper command line or protocol version.

### tsnet lifecycle

Each server has a unique state directory and immutable requested hostname.
The lifecycle owner serializes all calls and must respect the documented rule
that `Server.Close` cannot run before or concurrently with `Server.Start`.

Status comes from `StatusWithoutPeers` and IPN bus notifications, including
`AuthURL`, backend state, health, current tailnet, Tailscale IPs, and
certificate domains. Human-readable tsnet logs are not parsed as the primary
state protocol.

### HTTPS reverse proxy

Each running Portal listens only inside its tailnet using
`ListenTLS("tcp", ":443")`. The proxy destination is constructed internally
as `http://127.0.0.1:<validated-port>`; the IPC protocol cannot supply a host,
scheme, path, or arbitrary URL.

The proxy must:

- support HTTP streaming, immediate SSE flushing, and WebSocket upgrades;
- propagate request cancellation and shut down cleanly;
- remove untrusted inbound forwarding headers before setting
  `X-Forwarded-For`, `X-Forwarded-Host`, and `X-Forwarded-Proto: https`;
- use the loopback authority as the backend `Host` while preserving the
  original public host in `X-Forwarded-Host`; and
- never log request or response bodies, cookies, or authorization headers.

Changing a Local App port updates the proxy destination without replacing the
Portal's tsnet identity.

## Privacy and security

- Only `127.0.0.1` destinations are accepted in the MVP.
- Auth keys, OAuth secrets, API keys, node keys, cookies, and raw command
  lines must never appear in configuration, logs, diagnostics, or IPC errors.
- Authentication URLs are transient and redacted from logs and copied
  diagnostic reports.
- The app does not introduce an authorization layer; Tailscale policy remains
  authoritative for access to a Portal.
- Portico collects no accounts, analytics, or product telemetry.
- Before first Portal setup, the user must explicitly choose whether standard
  Tailscale operational support logging is enabled. The setting is global
  because the tsnet opt-out is process-wide; changing it restarts the helper
  and active Portals. See Tailscale's
  [logging documentation](https://tailscale.com/docs/features/logging).

Diagnostics show safe current facts and a bounded history of state
transitions. A copied report excludes secrets, authentication URLs, request
traffic, raw process arguments, and sensitive filesystem paths.

## MVP boundaries

Included:

- native menu-bar UI;
- multiple simultaneous Portals;
- HTTPS on port 443 to loopback HTTP;
- persistent independent tsnet identities;
- browser authentication and device-approval states;
- copy/open URL and sanitized diagnostics;
- launch-at-login support;
- HTTP streaming, SSE, and WebSockets;
- listener discovery and confident name hints;
- clean shutdown, restart, and bounded helper recovery; and
- clear manual tailnet-node cleanup.

Excluded:

- Funnel or public exposure;
- raw TCP/UDP forwarding;
- non-loopback, LAN, or remote Local Apps;
- path routing, load balancing, or application health orchestration;
- Tailscale Services;
- auth keys, tagged nodes, and automated Tailscale API administration;
- multiple tailnets in one installation;
- accounts, subscriptions, cloud services, or Portico telemetry; and
- automatic application updates in the initial slice.

## Testing

### Swift

- versioned configuration round trips, atomic replacement, and migration;
- persisted Start and Stop intent;
- first-tailnet binding, mismatch rejection, and empty-installation reset;
- requested versus Assigned Name presentation;
- independent Tailscale and Local App state derivation;
- bounded helper restart and manual retry;
- one-time launch-at-login suggestion; and
- redacted diagnostic report generation.

### Go helper

- lifecycle serialization and two simultaneous fake Portal runtimes;
- reconciliation, start, stop, port update, removal, and shutdown;
- UUID state-path validation and destructive-operation guards;
- malformed, oversized, unsupported-version, and unknown IPC messages;
- request/response correlation and asynchronous events;
- authentication and error redaction; and
- current-user listener parsing, loopback reachability, deduplication, and
  stable sorting.

Listener discovery fixtures cover Node scripts, Python modules and scripts,
Java `-jar` entry points, direct executables, generic runtimes, ambiguous
commands, secret-bearing arguments, and invalid name suggestions.

### Proxy

- ordinary HTTP request and response behavior;
- unavailable Local App handling;
- strict rejection of non-loopback destinations;
- forwarding-header replacement;
- streaming response delivery and SSE flush timing;
- WebSocket upgrade and bidirectional traffic;
- client cancellation; and
- graceful close with active and idle connections.

### Native UI

- first-run prerequisite guidance, logging gate, and Add availability;
- separately labelled Portal identity, Assigned Name, desired state,
  Tailscale state, and Local App reachability;
- current versus session-stale URL actions and explicit Authenticate action;
- Start, Stop, port edit, Copy, Open, Diagnostics, and Remove availability;
- controlled logging restart, retry, and terminal helper failure;
- removal confirmation, Escape cancellation, completion, in-progress, and
  retryable-failure states;
- one-time launch-at-login offer persistence plus approval and registration
  error states;
- separate native Settings and Diagnostics windows; and
- `Command-,`, `Command-Shift-D`, `Command-Q`, Return, and Escape behavior.

The XCUITest target selects a test-only launch configuration before dependency
construction. It uses a retained temporary installation root, an in-process
fake helper, a fake ServiceManagement adapter, and intercepted URL/open and
copy actions. It never starts the real helper, writes Application Support,
registers a login item, or opens an external URL.

### Packaging

- arm64 and x86_64 helper slices combine into a universal binary;
- helper and app signatures verify strictly with the hardened runtime;
- the helper is present at the expected bundle path; and
- a packaged app starts and shuts down without leaving a helper process.

### Manual macOS 14 accessibility acceptance

Automated UI tests do not replace a macOS 14 check with Full Keyboard Access,
VoiceOver, and Accessibility Inspector. Before release, verify all of the
following on that configuration:

1. First-run traversal reads prerequisite guidance, logging choice, and Add
   controls in visual order, with Add unavailable until logging is chosen.
2. Portal Name, collision Assigned Name, desired state, Tailscale state, and
   Local App reachability are read as separate facts.
3. A current URL exposes Copy and Open, while a **Last Known** URL is labelled
   stale, remains copyable, and cannot be opened or authenticated.
4. `Command-,` and `Command-Shift-D` open separate native windows whose
   headings receive initial accessibility focus.
5. A logging change retains useful focus and produces exactly one restart
   completion or failure announcement.
6. Removal confirmation supports Return and Escape, cancellation preserves the
   Portal, and focus returns to the row or completion notice.
7. The launch-at-login offer is non-blocking and does not repeat after a
   dismissed, declined, presented-before-crash, or accepted choice.
8. Accessibility labels and values expose no authentication URL, credential,
   raw error, sensitive path, or secret-bearing process argument.

## Deferred live acceptance

The first implementation does not mutate a real tailnet or request public
certificates automatically. Before calling the spike proven or publishing a
release, issue #12 owns this explicit manual acceptance scenario:

1. Start an HTTP test server on `127.0.0.1:8787`.
2. Add a Portal named `hermes`.
3. Authenticate it in the browser and satisfy device approval if required.
4. Visit its actual `https://<assigned-name>.<tailnet>.ts.net/` URL from a
   second tailnet device.
5. Restart Portico and verify the same node identity, IP, and URL return.
6. Add and run a second independently named Portal at the same time.
7. Verify ordinary HTTP, SSE, streaming, and WebSocket traffic.
8. Record the pinned Tailscale version and any lifecycle behavior that differs
   from this specification.

Certificate issuance publishes the Portal domain in Certificate Transparency
and can be rate-limited, so the run requires explicit user participation and
stable test identities.

## Distribution

The first supported artifact is a macOS 14+ universal app distributed outside
the Mac App Store. Build arm64 and x86_64 helper binaries, combine them,
embed and sign the helper before signing the outer app, enable the hardened
runtime, verify both signatures, notarize the final ZIP or DMG, and staple the
ticket where supported. Issue #11 owns this signed-app work, including the
production `SMAppService.mainApp` registration smoke check.

The initial Homebrew cask will live in
[`chrisbanes/homebrew-tap`](https://github.com/chrisbanes/homebrew-tap) and
consume the same notarized versioned artifact. It must retain a concrete
version and SHA-256 checksum; the tap is an installation channel, not a second
build or release pipeline. Mac App Store sandbox feasibility is a separate
investigation; it must not change the MVP process boundary until proven
practical.

## Unresolved risks

- Exact `tsnet.Server` start, unauthenticated stop, and restart ordering must
  be falsified by the executable lifecycle spike.
- Certificate acquisition requires tailnet-wide HTTPS configuration and has
  external rate limits.
- Name collisions mean the requested name cannot guarantee the final URL.
- Listener ownership and process arguments can be incomplete under macOS
  privacy restrictions; manual port entry remains authoritative.
- Spawning system inspection tools and the Go helper may complicate a future
  App Store sandbox submission.
- Immediate local removal can orphan a remote node; the UI must never imply
  otherwise.
