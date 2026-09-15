# Require exact helper protocol versions

The macOS app and bundled helper support only exact-matching protocol versions.
Full desired-set reconciliation introduces protocol version 2 because it
replaces imperative lifecycle commands and a new app must not pass a version-1
handshake with a helper that cannot fulfil that contract; version 2 has no
version-1 command fallback. Guarded Portal removal introduces protocol version
3 because `removePortal` is a new required command; version 3 retains the
version-2 reconciliation contract and accepts neither version 1 nor version 2
peers. Compatible optional fields may evolve within a
version only when older peers remain correct, while any new required command,
field, outcome, or semantic change bumps the version and ships in the same app
bundle as its helper.

Remote App support introduces protocol version 4 because full reconciliation
now requires the tagged `destination` field: `PortalDestination` in Swift and
`portal.Destination` in Go. This replaces the version-3 Local App port shape.
A version-4 app and helper must exact-match, and reject version-1, version-2,
and version-3 peers.

The persistence record's version 4 (`InstallationRecord.currentVersion` and
`installation-v4.json`) also introduced Remote App records, but it is a
separate migration domain. Persistence and helper-protocol versions have
independent migration and compatibility rules even when a feature changes both.
