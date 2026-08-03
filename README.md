# Portico

Portico gives local web apps stable, private HTTPS addresses on a Tailscale
tailnet. Each Portal is an independent Tailscale node and proxies its HTTPS
traffic to one HTTP service on the local Mac.

Portico is currently in the design and executable-spike phase. The repository
contains a local-only menu bar app and supervised helper process; it does not
yet implement Portal networking.

## Local development

Prerequisites:

- An Apple silicon Mac running macOS 14 or later
- Xcode 26.3 selected with `xcode-select`
- Go 1.26.5

Build the app and its bundled helper:

```shell
xcodebuild \
  -project Portico.xcodeproj \
  -scheme Portico \
  -destination 'platform=macOS' \
  -derivedDataPath .build/xcode \
  build
```

Launch the result:

```shell
open .build/xcode/Build/Products/Debug/Portico.app
```

Run the unit tests:

```shell
(cd helper && go test ./...)
xcodebuild \
  -project Portico.xcodeproj \
  -scheme Portico \
  -destination 'platform=macOS' \
  -derivedDataPath .build/xcode \
  test
```

Run the local app smoke test, which builds and launches the app, verifies its
bundled helper process, and confirms that both stop cleanly:

```shell
./Scripts/smoke-test-local-app.sh
```

## Documentation

- [Domain language](./CONTEXT.md)
- [MVP specification](./docs/portico-mvp-spec.md)
- [Architectural decisions](./docs/adr/)
- [Preliminary naming check](./docs/naming.md)
- [Roadmap](./docs/roadmap.md)
