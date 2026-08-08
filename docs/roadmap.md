# Portico Roadmap

## Phase 0: Executable lifecycle spike

- Scaffold the macOS 14 SwiftUI menu-bar app and bundled Go helper.
- Establish versioned JSON Lines IPC and helper supervision.
- Implement one Portal with browser-authentication events, persistent UUID
  state, TLS listener, and loopback reverse proxy.
- Exercise two simultaneous Portal runtimes through fake lifecycle tests.
- Complete the deferred real-tailnet acceptance run before declaring the
  lifecycle model proven.

## Phase 1: MVP vertical slice

- Add atomic Swift-owned configuration and full reconciliation.
- Implement multiple Portal controls, independent status dimensions, listener
  discovery, and safe name suggestions.
- Add TCP reachability, streaming/SSE/WebSocket proxy coverage, diagnostics,
  removal guidance, logging consent, and launch-at-login support.
- Produce local developer build and run instructions alongside the first
  executable project.

## Phase 2: Publishable 1.0

- Add Remote Apps as Portal Destinations over explicit HTTP or HTTPS through
  the Mac's normal network, with system-trusted HTTPS certificates.
- Harden crash recovery, state migrations, redaction, and destructive path
  validation.
- Complete accessibility, keyboard navigation, VoiceOver, localization
  readiness, and polished error recovery.
- Automate universal helper builds, inside-out signing, notarization, release
  verification, and versioned DMG or ZIP publication.
- Publish the initial Homebrew cask in
  [`chrisbanes/homebrew-tap`](https://github.com/chrisbanes/homebrew-tap) and a
  support/troubleshooting guide.
- Complete legal naming clearance and third-party licence notices.

## Later evaluation

- Assess Mac App Store sandbox feasibility without changing the direct-download
  architecture in advance.
- Consider automatic updates, optional tagged-node provisioning, and narrower
  delegated cleanup credentials only as separately designed features.
- Keep Funnel, Tailscale Services, raw forwarding, and multi-tailnet
  installations outside 1.0 unless product scope is explicitly reopened.
