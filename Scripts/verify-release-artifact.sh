#!/bin/bash

set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "Usage: $0 <version> <architecture> <dmg-path>" >&2
  exit 64
fi

version="$1"
architecture="$2"
dmg_path="$3"
mounted_volume=""

cleanup() {
  if [[ -n "$mounted_volume" ]]; then
    hdiutil detach "$mounted_volume" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Version must be numeric semantic versioning, such as 0.1.0" >&2
  exit 1
fi
case "$architecture" in
  arm64|x86_64) ;;
  *) echo "Unsupported architecture: $architecture" >&2; exit 1 ;;
esac
[[ -f "$dmg_path" ]] || { echo "DMG is missing: $dmg_path" >&2; exit 1; }

hdiutil verify "$dmg_path"
xcrun stapler validate "$dmg_path"

mounted_volume="$(hdiutil attach -readonly -nobrowse "$dmg_path" | awk -F '\t' '/\/Volumes\// { print $NF; exit }')"
[[ -n "$mounted_volume" && -d "$mounted_volume" ]] || {
  echo "Unable to mount the Portico DMG" >&2
  exit 1
}

app_path="$mounted_volume/Portico.app"
app_executable="$app_path/Contents/MacOS/Portico"
helper_path="$app_path/Contents/Helpers/portico-helper"
[[ -x "$app_executable" ]] || { echo "Mounted DMG is missing Portico.app" >&2; exit 1; }
[[ -x "$helper_path" ]] || { echo "Mounted app is missing the embedded helper" >&2; exit 1; }

packaged_version="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$app_path/Contents/Info.plist")"
[[ "$packaged_version" == "$version" ]] || {
  echo "Expected app version $version, found $packaged_version" >&2
  exit 1
}

for binary in "$app_executable" "$helper_path"; do
  architectures="$(lipo -archs "$binary")"
  [[ "$architectures" == "$architecture" ]] || {
    echo "Expected $architecture binary at $binary, found: $architectures" >&2
    exit 1
  }
  codesign --verify --strict --verbose=2 "$binary"
  codesign --display --verbose=4 "$binary" 2>&1 | grep 'flags=.*runtime' >/dev/null || {
    echo "Expected hardened runtime at $binary" >&2
    exit 1
  }
done

codesign --verify --deep --strict --verbose=2 "$app_path"
spctl --assess --type execute --verbose=4 "$app_path"

echo "Portico release artifact verification passed"
