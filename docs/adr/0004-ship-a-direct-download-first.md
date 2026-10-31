# Ship a direct-download macOS app first

The first release targets macOS 14 and later as separate arm64 and x86_64,
hardened, signed, and notarized direct-download apps. Mac App Store sandbox
feasibility will be assessed separately so sandbox and review constraints do
not distort the tsnet lifecycle spike or MVP architecture.

The x86_64 part is superseded by
[ADR 0008](0008-ship-arm64-only-releases.md); the rest still stands.
