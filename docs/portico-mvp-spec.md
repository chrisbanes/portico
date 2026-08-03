# Portico MVP Specification

**Status:** Confirmed product and technical specification

**Last updated:** 2026-08-03

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
button. It opens the transient URL only after the user clicks; the URL can be
copied from diagnostics but is never persisted or logged.

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

Swift owns an atomically written, versioned configuration containing:

- Portal UUID;
- immutable Portal Name;
- Local App port;
- desired state;
- creation time; and
- safe cached last-known Assigned Name, Portal URL, tailnet, and IPs.

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

The initial command set covers handshake, full reconciliation, start, stop,
Local App port update, removal, listener discovery, authentication retry, and
graceful shutdown.

If the helper exits unexpectedly, Swift restarts it with bounded exponential
backoff and restores Enabled Portals. After the retry budget is exhausted,
Portico shows a global failure with a manual Retry action.

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

- listener suggestion and manual-port Add Portal flows;
- prerequisite explanation and explicit Authenticate action;
- name-collision presentation;
- Start, Stop, port edit, Copy, Open, and diagnostics;
- cross-tailnet rejection;
- removal warning content; and
- Tailscale logging choice and launch-at-login suggestion.

### Packaging

- arm64 and x86_64 helper slices combine into a universal binary;
- helper and app signatures verify strictly with the hardened runtime;
- the helper is present at the expected bundle path; and
- a packaged app starts and shuts down without leaving a helper process.

## Deferred live acceptance

The first implementation does not mutate a real tailnet or request public
certificates automatically. Before calling the spike proven or publishing a
release, run this explicit manual acceptance scenario:

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
ticket where supported.

A Homebrew cask can consume the same notarized versioned artifact. Mac App
Store sandbox feasibility is a separate investigation; it must not change the
MVP process boundary until proven practical.

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
