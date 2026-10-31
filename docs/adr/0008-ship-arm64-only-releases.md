# Ship arm64-only releases

Releases ship only an arm64 DMG. This supersedes the x86_64 part of
[ADR 0004](0004-ship-a-direct-download-first.md); its direct-download,
signing, and notarization decisions stand.

An Intel build is a second, untested build path that doubles notarization time
for every release, and the 15 total DMG downloads to date do not justify it.
The release lane still builds a list of architectures and names assets
`Portico-<version>-<arch>.dmg`, so re-adding Intel is a one-line change to
`ARCHITECTURES` in `fastlane/Fastfile`, plus restoring the cask's `arch`
stanza and the matching checks in `Scripts/`.

The Homebrew cask declares `depends_on arch: :arm64`. Intel users on the
universal 0.0.2 release are therefore held back by Homebrew with no upgrade
path. We accept this given the download count.
