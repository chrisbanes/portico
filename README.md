# Portico

> Your local apps, through a private door.

Portico is a native macOS menu-bar app for giving web services reachable from
your Mac stable, private HTTPS addresses on a [Tailscale](https://tailscale.com/)
tailnet.

It creates an independent **Portal** for each service. A Portal keeps its own
Tailscale identity and URL, so you can share a durable private address without
putting the service on the public internet or routing it through your Mac's
existing Tailscale node.

![Portico private doorway](website/assets/og.png)

## What the app provides

- Stable HTTPS URLs for services running on the Mac, such as
  `http://127.0.0.1:8787` becoming `https://hermes.<tailnet>.ts.net`.
- Multiple independent Portals, each with an immutable Portal Name and a
  Tailscale-assigned identity.
- Local App destinations on the Mac and Remote App destinations reachable over
  the Mac's normal network.
- Browser authentication for each Portal, without asking you to manage auth
  keys.
- A menu-bar control and focused management window for daily work.
- Clear, separate state for the desired state, Tailscale connection, and
  destination reachability.
- Start, stop, repoint, copy, open, diagnose, and remove controls for each
  Portal.

## See it in action

The management window keeps the important facts together: the Portal's
identity, assigned name, connection state, destination reachability, and
private URL.

![Portico Portal detail showing identity, state, and URL](docs/assets/portal-detail.png)

Creating a Portal starts with a detected Local App or an explicitly configured
Local App or Remote App destination.

![Portico Add Portal sheet](docs/assets/add-portal.png)

The screenshots use example data from the native UI test fixture; they do not
represent a released build or a live tailnet.

The menu-bar popover is the everyday control surface: check Portal state,
open a private URL, or jump to settings, diagnostics, and the full management
window without leaving the menu bar.

![Portico menu-bar popover](docs/assets/menu-bar.png)

## How it works

1. **Choose a destination.** Select a detected Local App or enter a Local App
   or Remote App destination.
2. **Create a Portal.** Choose a durable Portal Name and complete browser
   authentication for its independent Tailscale node.
3. **Use the private URL.** Copy or open the Portal URL from the menu bar or
   management window wherever your tailnet can reach it.

Access remains governed by your Tailscale tailnet policy. Portico provides the
private doorway; it does not add a second application-level authorization layer.

## Current status

Portico is in active development and no public preview has been published yet.
The repository is the source of truth for the current executable, helper, and
MVP behavior.

You will need:

- A Mac running macOS 14 or later.
- A Tailscale tailnet with MagicDNS and HTTPS certificates enabled.
- A web service reachable from the Mac.

Portico is a selected working name and has not yet been legally cleared.

## Build from source

Prerequisites:

- Xcode 26.3 selected with `xcode-select`
- Go 1.26.5
- XcodeGen (`brew install xcodegen`)

Generate the local Xcode project, build, and launch:

```shell
./Scripts/generate-xcode-project.sh
xcodebuild \
  -project Portico.xcodeproj \
  -scheme Portico \
  -destination 'platform=macOS' \
  -derivedDataPath .build/xcode \
  build
open .build/xcode/Build/Products/Debug/Portico.app
```

Run the Swift unit tests, native UI tests, and Go helper tests:

```shell
xcodebuild \
  -project Portico.xcodeproj \
  -scheme Portico \
  -destination 'platform=macOS' \
  -derivedDataPath .build/xcode-unit \
  test
xcodebuild \
  -project Portico.xcodeproj \
  -scheme 'Portico UI Tests' \
  -destination 'platform=macOS' \
  -derivedDataPath .build/xcode-ui \
  test
(cd helper && go build ./cmd/portico-helper && go test ./...)
```

For the generated-project check and local app smoke test, see the scripts in
[`Scripts/`](Scripts/).

## Public previews

A public preview is a universal macOS 14+ application. The helper is built for
arm64 and x86_64, embedded in Portico.app, signed before the app, and
distributed in one notarized DMG. The direct download and Homebrew cask use
that same DMG; Portico does not check for, download, or install updates itself.
After the first public preview exists, Homebrew users can install or update it
with:

```shell
brew install --cask chrisbanes/tap/portico
```

The **Publish public preview** workflow is the only publishing path. It must be
dispatched manually with a `0.x.y` version and the full 40-character commit ID
to release. This allows the first public preview to be validated end to end from
a PR before it lands; retain that commit on `main` when merging the PR. Before
it creates a public GitHub Release or changes the tap, the workflow runs the
Swift and Go suites, builds, signs, notarizes, staples, mounts, and verifies the
exact DMG.
It then verifies the uploaded draft asset and audits the generated cask before
publishing the normal GitHub Release. It clean-installs the cask before
committing its version, URL, and SHA-256 to `chrisbanes/homebrew-tap`; a retry
for the same version and source commit resumes a release whose tap update did
not complete.

Keep these repository secrets outside the repository. The `public-release`
environment still protects the manual publishing job:

- `APPLE_SIGNING_CERTIFICATE_BASE64`
- `APPLE_SIGNING_CERTIFICATE_PASSWORD`
- `APPLE_NOTARY_KEY_BASE64`
- `APPLE_NOTARY_KEY_ID`
- `APPLE_NOTARY_ISSUER_ID`
- `HOMEBREW_TAP_TOKEN` — an expiring fine-grained token with Contents write
  access only to `chrisbanes/homebrew-tap`

The local credentialed build remains useful for verifying release material
before dispatching the workflow:

```shell
DEVELOPER_ID_APPLICATION='Developer ID Application: …' \
APPLE_NOTARY_KEY_PATH=/path/to/AuthKey_….p8 \
APPLE_NOTARY_KEY_ID=… \
APPLE_NOTARY_ISSUER_ID=… \
./Scripts/build-release-candidate.sh 0.1.0 .build/release
```

Keep the Developer ID certificate and App Store Connect team API key outside
the repository. Before configuring credentials, the universal helper build can
be verified locally with:

```shell
./Scripts/verify-universal-helper.sh
```

## Learn more

- [Domain language](CONTEXT.md)
- [MVP specification](docs/portico-mvp-spec.md)
- [Architectural decisions](docs/adr/)
- [Preliminary naming check](docs/naming.md)
- [Roadmap](docs/roadmap.md)
- [Marketing site](website/)
