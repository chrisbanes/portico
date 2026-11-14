# Move Portal management into one local Go service

Accepted design for [#102](https://github.com/chrisbanes/portico/issues/102)
under the approved 14 November 2026 architecture in #81/#82. Implementation and
runtime qualification remain with #103–#106 and distribution with #85.

The UI currently owns configuration/reconciliation and a private Go child helper.
That ties Enabled Portal lifetime to the UI and duplicates operational behaviour
for a future CLI. One unprivileged per-user Go service will instead own durable
intent, validation, identities and recovery on macOS and Linux. SwiftUI and the
Go CLI are clients of a versioned same-user Unix-domain socket contract. Kernel
peer identity, restrictive paths and server-side revalidation protect the local
boundary; there is no network or privileged management surface.

The [service contract](../local-service-contract.md) defines commands, durable
request recovery, ordered snapshots, transient authentication and migration.
The [coverage map](../ui-coverage-migration.md) assigns existing guarantees to
service, client, IPC and native UI checks. Replace coverage before deleting it.

This explicitly supersedes [ADR 0002](0002-use-one-bundled-go-helper.md)'s Swift
configuration ownership, UI-supervised runtime lifetime and mandatory child
helper. Reuse the Go runtime in the service process by default. A separate helper
requires a documented crash-isolation justification. The old #82 Swift/XPC plan,
Mach-only transport, mandatory idle exit and v4/v6 baseline are historical.
A login LaunchAgent and Linux user service can remain idle; no pre-login or
post-logout guarantee is added.

[ADR 0006](0006-require-exact-helper-protocol-versions.md) continues to govern
old app/helper pairs during transition (currently v7), but is not the public
management contract. Management v1 and persistence versions evolve independently.
[ADR 0001](0001-one-tsnet-node-per-portal.md)'s per-Portal UUID/identity,
[ADR 0003](0003-use-browser-authentication-and-manual-cleanup.md)'s browser/manual
remote cleanup, [ADR 0005](0005-persist-portal-removal-intent.md)'s durable removal,
and [ADR 0007](0007-route-destination-connections-through-the-mac.md)'s host-network
destination routing remain. Its host-network principle also applies on Linux.
[ADR 0004](0004-ship-a-direct-download-first.md)'s direct download/signing and
[ADR 0008](0008-ship-arm64-only-releases.md)'s macOS arm64 distribution policy
remain; #85 defines the separate Linux artifact matrix.

Cutover is automatic only after exclusive ownership, preserving original data
and Portal identities. The service never silently falls back to old state after
accepting changes. Manual downgrade has explicit limits; seamless downgrade is
not promised. Legacy writer exclusion (MIG-1) and fresh enrolment URL generation
(AUTH-1) still require implementation evidence under the contract. This ADR does
not waive either gate or claim they are already solved.
