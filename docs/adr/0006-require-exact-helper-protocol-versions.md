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
