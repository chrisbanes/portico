# Local Go management service contract

Design contract for [#102](https://github.com/chrisbanes/portico/issues/102),
implementing the approved architecture in [#81](https://github.com/chrisbanes/portico/issues/81)
and [#82](https://github.com/chrisbanes/portico/issues/82).
This document specifies future behaviour. It does not claim a working service,
a completed migration, or actual tailnet qualification. Baseline inspected:
`6266f94579664ec860151bafc33f9985ca5352e9` (installation v5, private helper protocol v7,
Tailscale v1.102.0). See the [coverage map](ui-coverage-migration.md) and
[superseding ADR](adr/0009-local-go-management-service.md).

## Revisions

- **15 November 2026:** [#103's approved decision revision](https://github.com/chrisbanes/portico/issues/103#approved-decision-revision--15-november-2026)
  replaces the AUTH-1 requirement for a newly issued URL. An explicit resume may
  now receive Tailscale's current valid URL, and the freshness-only error
  `authenticationFreshnessUnavailable` is removed from management v1. Every other
  rule, including MIG-1, is unchanged. The text #102 approved at
  [`2509e2e`](https://github.com/chrisbanes/portico/blob/2509e2eca9d27c2513b35c8c55d1bb43227bebbf/docs/local-service-contract.md)
  stays as history.

## Authority and scope

One unprivileged per-user Go process owns configuration, validation, durable
intent, tailnet binding, diagnostics, discovery/reachability, reconciliation,
and UUID-keyed tsnet identities. Reuse `helper/internal/portal` in that process;
a retained helper needs an explicit crash-isolation justification. SwiftUI and
the later Go CLI use the same management contract. They never open the production
store or start the old helper as a fallback.

Clients own forms, wording, confirmation presentation, accessibility, selection,
clipboard, browser opening and window management. Availability is a hint, never
authorization. The service rechecks every operation against current state.
Same-user means the account, not a trusted executable: other processes running
as that user can manage its Portals. Root is rejected as a peer; this is not a
protection against a machine administrator impersonating the user or altering files.
There is no network management endpoint, privileged/system-wide authority,
public helper-command wrapper, auth-key support or remote Tailscale-device deletion.
Windows and shared/web UI remain deferred.

## Transport, authentication and bounds

Use a pathname Unix-domain `SOCK_STREAM` socket, no TCP listener, abstract socket,
HTTP server, or passed file descriptors. Server and client both verify the peer's
kernel-reported effective UID equals their own nonzero UID before reading or
sending application messages. Credential lookup failure closes the connection.

| Platform | Peer verification | Endpoint and lifecycle |
| --- | --- | --- |
| macOS | `getpeereid` on connected socket | User's Darwin temporary directory, private `portico/management.sock` subdirectory; login LaunchAgent |
| Linux | `SO_PEERCRED` on connected socket | `$XDG_RUNTIME_DIR/portico/management.sock`; systemd user service |

Resolve the user's OS runtime directory, never trust a path from an IPC request.
Reject an unset/unsafe Linux runtime directory rather than falling back to shared
`/tmp`. On macOS use the OS user-temp lookup, not arbitrary `TMPDIR` from clients.
Verify owner, directory type and mode 0700 for the private endpoint directory;
socket mode 0600, umask 0077. Validate opened directory descriptors, reject
symlinks/untrusted ancestors and races, and bound the pathname to the platform's
socket limit. Dev/test variants have separate directories, locks and labels.
Clients validate endpoint ownership/type and peer identity too. Do not log
endpoint paths in shared diagnostics. A service holding the lifetime ownership
lock may remove a stale socket only after verifying its type/owner and that no
live server is reachable; never unlink another live service or an arbitrary file.

Each frame is a four-byte unsigned big-endian length followed by UTF-8 JSON
object bytes. Length 1–1,048,576 bytes inclusive; reject before allocation when
out of range. Maximum JSON depth 16; reject duplicate keys, invalid UTF-8,
unknown request fields/enum values, unexpected message kinds and non-finite
numbers. Strings are limited to 4 KiB unless a stricter domain rule applies;
request IDs are canonical UUID strings. Portal Names use current lower-case
DNS-label validation, destinations retain strict tagged shapes and port/host
validation, including Public Access/Remote App incompatibility.

Limit 16 connected clients, 8 in-flight calls per connection, 64 globally and
128 queued output frames per client. A full queue disconnects that client with
`slowConsumer` if space allows; it never blocks reconciliation. Limit handshake
and incomplete-frame reads to 5 seconds, ordinary calls to 10 seconds, discovery
to its existing 4-second budget. Subscriptions have no idle deadline. A timed-out
caller receives an uncertain outcome for mutations; it must recover by request
identity. Work admitted across durable acceptance is service-owned and survives
disconnection/deadline. Resource exhaustion rejects before acceptance, never
evicts unexpired recovery records.

[Apple's getpeereid manual](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man3/getpeereid.3.html)
documents kernel peer credentials and bidirectional verification.
[Linux unix(7)](https://man7.org/linux/man-pages/man7/unix.7.html)
documents `SO_PEERCRED` and pathname permissions; filesystem permissions alone
are insufficient as a portable authentication mechanism. Real tests on both OSes
must verify the selected Go platform bindings, not just mock the returned UID.

## Management version 1

Management, persistence and the old private helper versions are independent.
First frame: `{"type":"hello","version":1}`. Server replies
`{"type":"hello","version":1,"epoch":"<UUID>","capabilities":{"automaticDiscovery":"available"}}`.
`automaticDiscovery` is `available` or `unavailable`; it is not an authorization
bit. Unknown versions receive only `incompatibleVersion` and supported version
`1`, then close without reading commands. No version fallback or partial operation.
Future required fields/semantics bump this version; clients tolerate additional
response fields but reject unknown safety-relevant enum values as incompatible.

Calls have `type:"call"`, connection-local `callID` (UUID), `method`, `params`;
mutations also carry `requestID`, `issuedAt` (UTC RFC3339), and `expectedRevision`
(nonnegative durable installation revision). Replies echo `callID` and contain
either `result` or `error`. Integers must be within JSON's exact 53-bit range.
An example mutation is:

```json
{"type":"call","callID":"11111111-1111-4111-8111-111111111111","method":"createPortal","requestID":"22222222-2222-4222-8222-222222222222","issuedAt":"2026-11-14T22:00:00Z","expectedRevision":4,"params":{"name":"dashboard","destination":{"kind":"localApp","port":8080},"publicAccess":"private"}}
```

| Method | Params / result | Acceptance and availability |
| --- | --- | --- |
| `getSnapshot` | Empty; complete snapshot | Authenticated read, including safe startup failure |
| `subscribe` | Empty; initial snapshot then replacements | Atomic initial snapshot/observer registration; see ordering |
| `getRequestResult` | `requestID`, `issuedAt`; durable receipt or `notAccepted`/`resultExpired` | No side effects; never contains auth URL |
| `discoverLocalApps` | Empty; candidates with port, sanitised process label, optional suggested name | Bounded read; `capabilityUnavailable` on initial Linux service, explicit ports still work |
| `getDiagnostics` | Optional Portal UUID; bounded sanitised report | Never credentials, raw command lines or opaque paths |
| `createPortal` | Name, destination, `publicAccess`; receipt including allocated UUID | Logging choice made; valid input; public mode only with supported Local App constraints |
| `setDesiredState` | Portal UUID, `enabled`/`stopped`; receipt | Active record only; idempotent desired intent |
| `setDestination` | Portal UUID, tagged destination; receipt | Immutable UUID/name; reject Remote App when stored mode is public |
| `setLaunchAtLoginOffer` | `state`: `presented`/`accepted`/`declined`; receipt | Service-owned durable offer history, macOS production service capability only; native UI registration remains client-owned |
| `setOperationalLogging` | `enabled`/`disabled`; receipt | Commit preference before safely reconfiguring runtime |
| `removePortal` | Portal UUID, `confirmed:true`; receipt | Active record; durable Removing Portal before any deletion |
| `retryRemoval` | Portal UUID; receipt | Pending removal with retry available; never re-enable |
| `dismissAlert` | Alert UUID; receipt | Existing safe durable alert |
| `resetTailnet` | `confirmed:true`; receipt | No Portal records, including pending cleanup; binding exists |
| `retryRuntime` | Empty; receipt | Recoverable runtime failure; never persistence/ownership failure |
| `resumeEnrolment` | Portal UUID; receipt then private transient event | Enabled active Portal needing authentication; explicit action; feasibility gate below |

Mutating methods all use durable request identity, including retryRuntime and
resumeEnrolment. A new accepted runtime action has a durable action marker;
a restart exposes `interrupted` for transient enrolment actions and requires an
explicit new resume, never an automatic auth URL. A retried receipt starts no
second action. Set-to-current-value operations return a recorded no-op receipt.
`launchAtLoginOffer` is included in snapshots as `notOffered`, `presented`,
`accepted` or `declined`. Only the macOS production service exposes this preference;
Linux/Dev services return `capabilityUnavailable` for its mutation. The service accepts
`notOffered` → `presented`/`accepted`, `presented` → `accepted`/`declined`, and
`declined` → `accepted` (explicit enable in Settings); same-state is a recorded
no-op. Other transitions are `notAvailable`; turning native UI login off does
not clear its historical offer. Clients request `presented` after a fresh Online
fact or restore an already presented offer, and await durable acceptance before
showing/changing the offer. Accept commits `accepted` before native registration;
failed/uncertain save prevents registration until same-ID recovery proves the
commit. A registration error leaves `accepted` durable and offers native retry,
not another persistence owner. Native SMAppService status, register/unregister,
settings routing and approval wording stay client-owned. This preference never
controls service login startup.

Unsupported Funnel activation is `capabilityUnavailable`; this contract preserves
stored Public Access mode/status and current private-only listener behaviour,
not new public listeners. A future Funnel command/version must cover its safety
matrix before becoming available.

Errors have `code`, optional safe `field`, optional `portalID`, and `retryable`;
clients provide localised wording. Codes are `invalidRequest`, `notFound`,
`notAvailable`, `revisionConflict`, `loggingChoiceRequired`, `confirmationRequired`,
`capabilityUnavailable`, `requestConflict`, `requestNotYetValid`, `resultExpired`,
`capacityExceeded`, `persistenceUnavailable`, `unsupportedState`, `ownershipBusy`,
`runtimeUnavailable`, `incompatibleVersion`, `slowConsumer`. Transport failure is
not proof of rejection. `retryable` never instructs a client to allocate a new
request ID for an uncertain mutation.
Unauthenticated peers are closed without state/error detail.

### Message shapes

These fill in shapes the contract leaves open; the only extension is that a
timed-out mutation's reply may carry `uncertain` instead of `result` or
`error`, as the transport section's "uncertain outcome" requires.
`helper/internal/management` implements them.

- **Hello.** The reply's `epoch` is the snapshot epoch. A first frame that is
  not exactly `{"type":"hello","version":<integer>}` is malformed (below).
  Mismatch sends
  `{"type":"error","error":{"code":"incompatibleVersion","retryable":false},"supportedVersions":[1]}`.
- **Connection-level errors** are `{"type":"error","error":{code,retryable}}`
  and are followed by close. Frames that fail strict decoding, an unknown
  `type`, a second hello, or a call whose `callID` is not a canonical
  lower-case UUID get `invalidRequest` (not retryable). The transport sends
  `slowConsumer` and its client-limit `capacityExceeded` (both retryable). A
  snapshot set over the paging limits on a subscription gets `capacityExceeded`
  (not retryable). A `subscribe` already answered `runtimeUnavailable` at its
  deadline whose registration then completes gets `runtimeUnavailable`
  (retryable), so no stream follows that reply.
- **Calls.** `params` is always an object, `{}` when empty, decoded strictly
  per method. `requestID` is a canonical lower-case UUID, `issuedAt` is RFC 3339
  ending in `Z`, and `expectedRevision` is an integer ≥ 0. Non-mutating methods
  must not carry any of the three. Envelope faults in a parsed call (unknown
  `method`, missing or unexpected envelope member, bad `params`) are reply
  errors `invalidRequest` with `field` set to that member's name. A `portalID`
  or `alertID` param that is not a canonical lower-case UUID is
  `invalidRequest` with `field` `portalID` or `alertID`.
- **Replies** are `{"type":"reply","callID",...}` with `"result":{...}`, or
  `"error":{code,field?,portalID?,retryable}`, or `"uncertain":true` for
  timed-out mutations only. Each call has a 10-second deadline. A mutation
  past it gets `uncertain`: its outcome is unknown, its engine work continues,
  and the client recovers it by `getRequestResult` with the same request ID.
  A read past it gets `runtimeUnavailable` (retryable). A call over the
  in-flight limits gets `capacityExceeded` (retryable). An internal failure
  without a contract code is `runtimeUnavailable` (retryable) with no detail.
- **Mutation params:**

  | Method | Params |
  | --- | --- |
  | `createPortal` | `name`, `destination`, `publicAccess` |
  | `setDesiredState` | `portalID`, `state` |
  | `setDestination` | `portalID`, `destination` |
  | `setLaunchAtLoginOffer` | `state` |
  | `setOperationalLogging` | `logging` |
  | `removePortal` | `portalID`, `confirmed` |
  | `retryRemoval`, `resumeEnrolment` | `portalID` |
  | `dismissAlert` | `alertID` |
  | `resetTailnet` | `confirmed` |
  | `retryRuntime` | none |

  A destination is `{"kind":"localApp","port"}` or
  `{"kind":"remoteApp","scheme","host","port"}`.
- **Results.**
  - A mutation's result is its receipt: `requestID`, `acceptedAt`,
    `recoverUntil`, `acceptedRevision`, `operation`, optional `portalID`, and
    `outcome`.
  - `getRequestResult` takes `{requestID, issuedAt}` and returns
    `{"receipt":{...}}` or `{"notAccepted":true}`. `resultExpired` and the
    other refusals are reply errors.
  - `discoverLocalApps` returns
    `{"candidates":[{localAppPort, processLabel, suggestedPortalName?}]}`.
  - `getDiagnostics` takes `{portalID?}` and returns `{"report":"..."}`.
  - `getSnapshot` returns the snapshot as its result.
  - `subscribe` returns `{}`, then streams `{"type":"snapshot","snapshot":{...}}`
    frames. A connection has at most one subscription; a second `subscribe`
    is `notAvailable`.
- **Paging.** A snapshot frame or `getSnapshot` reply that exceeds the frame
  limit is sent as one contiguous set:
  - `{"type":"snapshotBegin","callID"?,"epoch","sequence","revision","pageCount"}`;
  - then `{"type":"snapshotPage","callID"?,"pageIndex","snapshot"?,"portals":[...]}`
    with `pageIndex` from 0, where only page 0 has `snapshot`, the
    installation fields with `portals` empty;
  - then `{"type":"snapshotEnd","callID"?,"sequence"}`.

  `callID` appears only on a `getSnapshot` reply. Over 64 pages, or a Portal
  that cannot fit a page, is `capacityExceeded` with nothing partial sent: a
  reply error for `getSnapshot`, or the connection-level error for a
  subscription.
- **Startup failure.** While the service holds the lifetime lock but its
  state cannot load, it still answers hello and an authenticated
  `getSnapshot`. That snapshot's `service.startupFailure` is the reason code,
  `unsupportedState` or `persistenceUnavailable`, never text; its `runtime`
  is `unavailable`, it has no Portals and its `availability` is empty: no
  operation is available. Every other well-formed call gets
  that code as its reply error, so no mutation can create an empty install.
  The field is absent from a loaded service's snapshots.
- **Enrolment events** go only to the connection that called `resumeEnrolment`:
  `{"type":"enrolment","event":"authenticationURL"|"cancelled"|"failed","requestID","portalID","nonce","url"?}`,
  with `url` only for `authenticationURL`. An event never precedes the reply
  to the `resumeEnrolment` call that requested it, whether that reply is the
  receipt, an error or `uncertain`.

## Durable acceptance, retry and observation

Serialize mutations and runtime state projection through one service authority.
Validate peer, framing and request shape first. For a previously accepted
request, match the canonical payload and recover its receipt *before* checking
the current revision/availability. For a new request, revalidate current state,
permissions, capacity and expectedRevision. Concurrent writes at the same
revision yield one accepted mutation and one `revisionConflict`. Clients fetch
fresh state and obtain a deliberate new action rather than silently overwriting
another client's edit. Runtime events are generation-fenced; an old node cannot
resurrect a removed Portal, bind a tailnet, or change the latest destination.

Acceptance commits installation intent, revision, allocated identities and
request receipt atomically and durably before acknowledging or causing runtime
side effects. Use a single transactionally replaced service-state document,
fsync file and containing directory; no independently committed receipt file.
If commit outcome is uncertain, stop accepting writes, reload authoritative
state and recover by request ID. Never acknowledge success before durable write.
The new private service-state schema is version 1, containing `installation`
(normalised v5 facts), `revision`, `requests`, `actions` and `migration`; reject
unknown schema versions without overwrite. tsnet files remain opaque, separate
from this intent/receipt transaction.

A receipt contains requestID, acceptedAt, recoverUntil, acceptedRevision,
operation, Portal UUID if applicable, and outcome `accepted` or `noOp`. It does
not assert Online or destination reachability. Runtime convergence is separately
observed: enabled intent may be connecting, need authentication/approval, be
online, or fail; stopped converges only when resources close; removal converges
only after guarded deletion and durable record removal. Failure preserves intent
and a structured reason; it does not roll back accepted intent.

Retry rules are deliberately bounded:

1. Clients persist requestID, issuedAt and exact semantic request before first
   dispatch (including across UI restart). The service hashes a canonical typed
   representation of version, method, issuedAt, expectedRevision and params;
   JSON whitespace/key order and callID are excluded. Same ID with another
   payload returns `requestConflict`, including after a no-op.
2. Recover accepted receipts for 24 hours after acceptance, across service
   restarts. Admit unseen requests only when issuedAt is no older than 24 hours
   and at most 5 minutes in the future. Retain each record until the later of
   acceptedAt + 24 hours or issuedAt + 24 hours + 5 minutes, so an unexpired
   timestamp cannot become a new operation after compaction. Keep a durable
   nondecreasing clock high-water mark to prevent clock rollback reopening an
   expired admission window. Clock jumps can close a window early; report expiry
   and require inspection rather than guessing acceptance.
3. Maximum 10,000 retained receipts and 64 MiB state document. Reject new
   mutations with `capacityExceeded` before either limit; never truncate active
   records. Duplicate recovery is still permitted at capacity. Reads/snapshots
   are paged to fit frame bounds (below).
4. `getRequestResult` on an unrecorded, still-admissible ID returns `notAccepted`
   only after serializing behind any in-flight acceptance for that ID. Retrying
   that exact request is safe. Outside the window return `resultExpired`, which
   means unknown, not rejection. A mutation with an expired unseen ID is never
   executed. A known record returns its receipt while retained.
5. After expiry clients stop automatic retry. They show uncertainty and inspect
   current Portal inventory/receipts with the user. Names need not be unique and
   absence of a name is not proof that Create failed. A new Create needs an
   explicit new user decision; no automatic new ID. Unknown future timestamps
   return `requestNotYetValid` and execute nothing.

Crash before the atomic commit: neither intent nor receipt exists. Crash after
commit but before ack/runtime work: same-ID retry returns the original UUID and
recovery reconciles intent once. Crash during removal: keep the pending record
until deletion and final durable removal succeed; never run it. Tailnet mismatch
first persists rejection/cleanup evidence, then closes/removes only that identity;
unrelated Portals continue. Failed close retains ownership and blocks replacement
until proven closed. Keep existing bounded cancellation and redacted diagnostics.

Snapshots contain protocol version, epoch (new UUID per service process), sequence
(monotonic within epoch), durable revision, service state/capabilities, logging
choice, launchAtLoginOffer, tailnet binding, sanitised alerts and Portal records. Each Portal includes
UUID, immutable Portal Name, destination, stored Public Access, desired/lifecycle
state, structured live connection/listener/public-access/reachability state,
Assigned Name, Portal URL/addresses when known, freshness `current`/`lastKnown`,
pending operation status/error and operation availability with reason codes.
Never expose helper commands, local identity paths, secrets or auth URLs.

Each subscription sends full replacement snapshots in increasing sequence order;
no reconnect history or auth-event replay. Initial snapshot and observer attach
are serialized with publication. Reconnect always obtains a fresh epoch/snapshot;
clients discard old-epoch queued work, mark cached facts stale while disconnected
and disable state-changing actions. A receipt's revision is durable; a snapshot's
sequence also advances for ephemeral facts. Read-after-accept returns a revision
at least the acceptedRevision. Clients recover uncertain calls separately from
resubscribing. Last-known URL may be copied but never opened as current.

For snapshots larger than a frame, send a `snapshotBegin` (epoch, sequence,
revision, pageCount), ordered `snapshotPage` frames (pageIndex, Portal records,
first-page installation fields), then `snapshotEnd`. Freeze the view until end;
maximum 64 pages/64 MiB, otherwise `capacityExceeded` without partial presentation.
Apply only the complete set; drop interrupted sets on reconnect. A subscription
queues a whole frozen set or disconnects; reads use the same framing. State-size
limits include serialized snapshots so an accepted install always remains readable.

## Interrupted enrolment and transient authentication

Creating a Portal retains a durable record even when its requester disappears.
It never opens a browser through a snapshot. An authenticated client explicitly
calls `resumeEnrolment`; another later same-user client can do this too. Bind the
attempt to connection + requestID + Portal UUID + runtime generation + attempt
nonce. At most one attempt per Portal: a new explicit resume invalidates the old
lease and delivers cancellation only to its former requester. Disconnect drops
the lease/URL, not the Portal or enabled intent. No unrelated subscriber gets it.

Only the initiating connection receives `authenticationURL` with call/request ID,
Portal UUID, attempt nonce and an HTTPS URL from the current attempt. Deliver at
most once, never store in receipts, snapshots, logs, reports or reconnect history.
Validate HTTPS and fence callbacks after supersession/removal/restart. A lost URL
requires an explicit new resume request, not replay of its durable receipt. No
client should automatically open URLs on reconnection or another client's action.

**AUTH-1 (revised 15 November 2026):** an explicit resume may deliver the
current valid URL that Tailscale returns, even when it equals an earlier
attempt's URL. URL equality alone is not an authentication failure. Portico never
caches or replays a URL itself: each delivery comes from the runtime for the
current attempt. Delivery goes only to the explicit requester, never through
persistence, receipts, snapshots or reconnect history, and no client opens a
browser automatically on reconnection. Resume preserves the Portal's UUID and
opaque identity; never use logout, delete state or create a replacement node.
Callbacks from a superseded lease, removed Portal, rebuilt runtime or earlier
service run are rejected.

Pinned [Tailscale v1.102.0 StartLoginInteractiveAs](https://github.com/tailscale/tailscale/blob/v1.102.0/ipn/ipnlocal/local.go#L4635-L4666)
re-sends its cached `authURL` through `popBrowserAuthNowLocked` while that URL is
less than 6 days 23 hours old, and otherwise starts `cc.Login`. Neither path logs
out or creates a node, so the existing `portalRuntime.authenticate` and watcher
are expected to deliver Tailscale's current valid URL; the real-runtime run below
confirms it. The service adds the fencing above.

Evidence comes in two separate categories:

- **Fake runtime (#103):** service tests drive resume, disconnect, supersession,
  restart and redaction with a fake runtime that emits a URL, including an equal
  one. They prove routing, fencing and that nothing stores the URL, not
  Tailscale's behaviour.
- **Real runtime (#103, opt-in):** use an isolated unenrolled tsnet node. Client
  A resumes and disconnects, client B resumes, the service restarts, and B
  resumes again. Assert that only B receives an HTTPS `*.tailscale.com` URL,
  nothing else exposes it, the UUID and identity directory are unchanged, and no
  logout or state deletion happens. Keep
  credentials out of artifacts and report only redacted outcomes.

Enrolment acceptance cannot be declared complete until both categories pass.
Actual enrolled-tailnet continuity stays with the first milestone below.

## Ownership migration and recovery

The service-state root is macOS's existing production Application Support/Portico
root; Dev/test retain their own roots. Linux uses the account's private
`$XDG_STATE_HOME/portico` (default `~/.local/state/portico`). Set private owned
state/staging/backups directories to 0700, files to 0600. Reject unsafe ownership,
symlinks and corrupt/unknown schemas; do not replace them with an empty install.
Clients cannot choose production roots. Guard all identity deletion through opened
owned directories and UUID-only children; never follow a symlink out of root.

Automatic cutover runs on the first upgraded launch; brief reconnect downtime is
allowed. Startup/approval failure is visible and never starts a local UI owner.

1. Register/approve the platform service before touching production state. The
   upgraded UI starts in client-only mode. The service holds a stable exclusive
   lifetime `flock` on the state root's `service-owner.lock` before reads that
   migrate, writes, runtime startup or cleanup. Never unlink/replace this lock.
   New service contenders fail with `ownershipBusy`.
2. **Legacy exclusivity gate MIG-1:** older UI/helper versions do not take this
   lock. A new lock alone proves nothing about them. Upgrade orchestration must
   disable old automatic UI starts, ask the known old UI to stop reconciliation
   and quit, await its exact helper termination, and verify the previously
   installed app cannot restart as a writer during transfer **and for the entire period of service ownership**, including
   after commit, service downtime and restart. Preserved originals/identity copies
   must never become a second legacy runtime source. No name-based kill
   or snapshot PID check counts as this guarantee. Require a persistent admission mechanism: a compatibility bridge release
   with shared ownership admission, or a verified legacy fail-closed guard for
   every supported historical reader. A guard must preserve original bytes in
   the checkpoint and restore them on failed transfer before any new node starts;
   guard installation/recovery is part of the exclusive migration transaction.
   A PID check, new lock or instruction to quit the old app is not a persistent
   mechanism. If existing upgrade mechanisms cannot establish it, introduce the
   bridge as a prerequisite rather than starting the service. Do not auto-start the new writer
   until #104/#105 demonstrate this gate with old-version relaunch attempts during transfer,
   after commit and while the service is stopped/restarting.
   If it cannot be established automatically, preserve originals and surface an
   actionable migration failure; the automatic-migration acceptance stays blocked.
3. After exclusive ownership, read original files without mutation: current
   installation-v5 first, then v4/v3/v2/portal-v1 in existing precedence order.
   A present corrupt/unsupported newer record fails closed; never fall back.
   Convert all historical facts, preserving UUID/name/destination/createdAt,
   desired state, tailnet binding, alerts/rejection/pending removal, logging
   consent and launch-at-login offer history. Historical defaulting follows
   `PortalStoreTests`, not a new policy. Do not call Swift's loading path, which
   writes migrated data and removes older sources.
4. Under the lock, make an immutable rollback checkpoint of original metadata
   and opaque identity trees; verify bytes, UUID mapping, permissions and no
   symlink escape. Stage a *working copy* of identity trees and normalised state
   in a new service-owned generation directory; do not move/delete originals.
   No tsnet instance opens originals. Ensure free space first; copy/validation
   failure removes only incomplete staging and leaves originals untouched.
5. Fsync staged metadata, identities and directories. Atomically commit the
   service generation manifest (root-relative generation ID, source hashes,
   schema and migration phase) and fsync its directory. Only a fully committed
   generation is authoritative. Start runtime only against that generation;
   service-state mutations use its transactional document. A committed generation
   always wins over historical source files on restart; corrupted committed
   state fails closed instead of silently remigrating originals.
6. Recovery before commit discards incomplete staging and retries from untouched
   originals after exclusive ownership is re-established. After commit, restore
   enabled Portals and cleanup from the committed generation, never recopy stale
   originals. Persist the first post-cutover accepted mutation marker atomically
   with that mutation. Runtime identity files may change as soon as started;
   even with no accepted command, rollback is not automatically safe.

**MIG-1 is an implementation proof gate, not completed evidence.** Neither the
baseline nor this design delivers legacy quiescence. #103 can develop import and
single-writer service tests with isolated roots; #104/#105 must settle and prove
persistent legacy exclusion before production cutover, including a legacy launch after successful migration. If a bridge release is required,
record its dependency and replan those deliveries instead of pretending a lock
retroactively protects older versions.

Manual downgrade: stop and unregister the service, prevent its restart, verify
all nodes closed and preserve the complete service generation for recovery. With
no service/runtime writes after checkpoint, a verified original checkpoint can
be restored under exclusive ownership. Otherwise there is no supported automatic
reverse import: originals may have removed/repointed Portals and stale tsnet
credentials. Preserve the current generation and seek an explicit compatible
export/recovery procedure; never launch the old app against stale originals or
merge opaque identities by hand. A later explicit destructive recovery is a new
user decision. Seamless downgrade is not required. Packaging must prevent an old
binary being offered as a safe downgrade after migration.

Retire production `PortalController` reconciliation/store writes and
`HelperSupervisor`/`ProcessHelperLauncher` ownership only when replacement service
coverage and real IPC pass. UI fixtures become fake clients. No dual-write or
shadow production reconciliation interval is permitted.

## Lifecycle, discovery and installation

macOS: a signed app-bundled Go executable plus LaunchAgent registered using
[SMAppService.agent](https://developer.apple.com/documentation/servicemanagement/smappservice/agent(plistname:)).
Use a distinct label per variant, RunAtLoad and restart on failure; no UI parent
lifetime or mandatory idle exit. Surface notRegistered/requiresApproval/startup
failure with a route to system settings; never interpret registration as running.
UI Launch at Login remains optional and separate. Quit UI does not unregister
or stop the service. Logout/OS shutdown ends the promised session lifetime.

Linux: `portico-service` as the user's own `systemd --user` unit, enabled for the
user default target, restart on failure with bounded backoff, UMask 0077. Install
unit/executable through #85; no sudo, system-wide service or automatic lingering.
Document user-manager availability and `systemctl --user` status/start/stop;
non-systemd hosts are unsupported initially. User manager lifetime is session
based unless separately configured; [systemd's lifecycle documentation](https://www.freedesktop.org/software/systemd/man/256/systemd-run.html)
describes lingering. Do not promise operation before login or after logout.

On startup verify schema/ownership before runtime. Recover Enabled Portals and
pending guarded cleanup; Stopped Portals stay disconnected. A failed close blocks
replacement. Repeated startup failure is visible; no empty-state reset. An idle
service is acceptable. Shutdown has a bounded deadline, cancels in-flight work,
and leaves durable intent/cleanup for restart without dropping ownership early.

macOS preserves automatic Local App discovery: current same-user libproc source,
sanitisations, deduplication, loopback probe, 4-second total budget and cancellation.
Linux initially advertises automatic discovery unavailable and returns
`capabilityUnavailable`; manual explicit-port creation and loopback reachability
still work. No process command lines enter discovery DTOs. Destination connections
continue through the host OS network, preserving proxy/header/TLS rules of ADR
0007; this does not require a system Tailscale daemon or privileged TUN setup.

## Verification and affected inventory

| Layer / owner | Required evidence before dependent acceptance |
| --- | --- |
| Go behaviour, #103 | Fake runtime/store/clock: full command validation, logging gate and durable UI login-offer transitions, ordered edits, atomic commit and failure injection, historical v1–v5 fixtures, duplicate payload/conflict/expiry/capacity, lost Create ack across restart, cross-tailnet rejection, pending cleanup, safe diagnostics, fake-runtime enrolment routing/redaction; opt-in real-runtime AUTH-1 run with redacted outcome; no actual tailnet claim |
| Real IPC, #104 | macOS and Linux OS credential tests (same UID success, other UID/root rejection), mode/symlink/stale socket checks, framing/bounds/version failures, concurrent clients, slow subscriber, disconnect before/after commit, restart recovery with same receipt/UUID; mock credentials alone insufficient |
| Platform lifecycle, #104/#105 | Isolated installed service: UI exit, service crash/restart, Stopped case, pending cleanup, denied approval, relaunch races, historical upgrade failure at every migration phase; assert original metadata/identity byte preservation before commit and fail-closed authoritative-state corruption |
| Client, #105 | Fake service: correct UUID/action, revision conflict, pending/lost ack persistence and same-ID retry, expiry uncertainty, stale/disconnected hints, local effects once, login-offer commit-before-native-registration and failed-save behaviour, new explicit enrolment after reconnect; retained UI scenarios via VM only |
| Coverage reconciliation, #106 | Every baseline assertion mapped, replacement evidence before deletion, comparable VM counts/timings, prospective Funnel safety below UI |
| Distribution, #85 | macOS arm64 signing/notarisation/agent embedding and approval; Linux service binary/unit/paths/permissions/install/uninstall preserving state; CLI packaging separately; explicit platform/architecture artifact matrix before claiming support |

First milestone: import a copy of an existing installation through the exclusive
migration path; record Portal UUID and opaque identity hashes, start Go service,
read/control via authenticated IPC, quit UI, crash/restart service and verify
Enabled recovery with unchanged UUID/identity and pending cleanup retained.
Fake runtime integration proves lifecycle/bytes/commands only. In a separate
manual real-tailnet run record original stable Tailscale node ID, Assigned Name,
private HTTPS response and destination reachability, then verify the same node
and HTTPS after UI exit/service restart. Report conditions and redacted results,
not credentials or opaque state contents. No timing or real-node continuity is
claimed by #102. Enrolment resumption has the separate AUTH-1 gate.

| Current location | Later migration responsibility |
| --- | --- |
| `Portico/PortalStore.swift`, `PortalConfiguration.swift`; `PortalStoreTests`, `PortalConfigurationTests` | Port v1–v5 decoding/defaults/validation and secure persistence; preserve all fields and precedence; immutable import/checkpoint fixtures (#103) |
| `PortalController.swift`, `HelperSupervisor.swift`, `ProcessHelperLauncher.swift`; corresponding tests | Move domain/recovery/removal/auth ownership to Go; retain cancellation/generation guarantees at new seam (#103); remove production Swift ownership (#105) |
| `HelperProtocol.swift`, `helper/internal/protocol`, `helper/cmd/portico-helper` | Private v7 remains until cutover; do not publish as management API; retire when no production caller remains (#103–#105) |
| `helper/internal/portal`, `helper/internal/discovery`, `LocalAppReachability.swift` | Reuse runtime/proxy tests; adapt ownership and platform build constraints; preserve macOS discovery, explicit Linux capability (#103/#104) |
| `AppDelegate.swift`, `PorticoApp.swift`, `PortalPresentation.swift`, `PortalActionAvailability.swift`, `Diagnostics.swift` | Thin connection/render/dispatch layer, native menu/window/keyboard/effects and redaction (#105) |
| `UITestLaunchConfiguration.swift`, `SmokeLaunchConfiguration.swift`, `PorticoTests/TestSupport.swift`, `PorticoUITests/PorticoUITests.swift` | Fake management-client fixtures, isolated roots and real-service smoke admission; map/replace before deletion (#105/#106) |
| `AppVariant.swift`, `LaunchAtLogin.swift`, `project.yml`, launcher Info.plist, generated Xcode project | Variant-specific roots/agent labels, embed service, independent UI startup, packaging metadata (#104/#105/#85) |
| `Scripts/build-helper.sh`, `verify-helper-architectures.sh`, `verify-release-artifact.sh`, `test-release.py`, `smoke-test-installed-app.sh`, `smoke-test-local-app.sh`, `test-ui-in-vm.sh`, `verify-xcode-project-generation.sh` | Update build/artifact/service smoke expectations; retain VM UI isolation and arm64 macOS policy (#104–#106/#85) |
| `fastlane/Fastfile`, `.github/workflows/ci.yml`, release workflow, README/release instructions and Homebrew cask generator/tap | Add Linux Go build/race CI, service lifecycle lanes, signing/artifact/cask tests; no unreviewed release mutation here (#104/#85) |
| ADRs 0001–0008; old #82 plan | Supersession/retention in ADR 0009; no reuse of obsolete Swift/XPC-only plan |

Resolved here: command/observation/retry semantics, platform privilege boundary,
coverage allocation, migration phases and downgrade limits. Still unproved:
AUTH-1 enrolment resumption, MIG-1 legacy exclusion, actual installed platform lifecycle,
packaged artifacts and real-tailnet continuity. They are downstream acceptance
gates, not grounds to relabel implementation ready merely because docs merge.
