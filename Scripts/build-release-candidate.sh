#!/bin/bash

set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "Usage: $0 <version> <output-directory>" >&2
  exit 64
fi

version="$1"
output_directory="$2"
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"

require_environment() {
  local name="$1"
  if [[ -z "${!name:-}" ]]; then
    echo "$name must be set" >&2
    exit 1
  fi
}

if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Version must be numeric semantic versioning, such as 0.1.0" >&2
  exit 1
fi

for name in DEVELOPER_ID_APPLICATION APPLE_NOTARY_KEY_PATH APPLE_NOTARY_KEY_ID APPLE_NOTARY_ISSUER_ID; do
  require_environment "$name"
done

if [[ ! -r "$APPLE_NOTARY_KEY_PATH" ]]; then
  echo "APPLE_NOTARY_KEY_PATH must name a readable App Store Connect team API key" >&2
  exit 1
fi

mkdir -p "$output_directory"
output_directory="$(cd -- "$output_directory" && pwd)"
archive_path="$output_directory/Portico.xcarchive"
app_path="$archive_path/Products/Applications/Portico.app"
helper_path="$app_path/Contents/Helpers/portico-helper"
derived_data_path="$output_directory/DerivedData"
dmg_staging_directory="$output_directory/dmg-root"
dmg_path="$output_directory/Portico-$version.dmg"

rm -rf "$archive_path" "$derived_data_path" "$dmg_staging_directory" "$dmg_path"
mkdir -p "$dmg_staging_directory"

cd "$repo_root"
./Scripts/generate-xcode-project.sh
xcodebuild \
  -project Portico.xcodeproj \
  -scheme Portico \
  -configuration Release \
  -destination 'generic/platform=macOS' \
  -archivePath "$archive_path" \
  -derivedDataPath "$derived_data_path" \
  ARCHS='arm64 x86_64' \
  MARKETING_VERSION="$version" \
  CURRENT_PROJECT_VERSION=1 \
  ONLY_ACTIVE_ARCH=NO \
  CODE_SIGNING_ALLOWED=NO \
  CODE_SIGNING_REQUIRED=NO \
  archive

[[ -x "$app_path/Contents/MacOS/Portico" ]] || { echo "Packaged app executable is missing" >&2; exit 1; }
[[ -x "$helper_path" ]] || { echo "Packaged helper is missing" >&2; exit 1; }
packaged_version="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$app_path/Contents/Info.plist")"
[[ "$packaged_version" == "$version" ]] || {
  echo "Expected app version $version, found $packaged_version" >&2
  exit 1
}

for binary in "$app_path/Contents/MacOS/Portico" "$helper_path"; do
  architectures="$(lipo -archs "$binary")"
  [[ "$architectures" == *arm64* && "$architectures" == *x86_64* ]] || {
    echo "Expected universal binary at $binary, found: $architectures" >&2
    exit 1
  }
done

codesign --force --options runtime --timestamp --sign "$DEVELOPER_ID_APPLICATION" "$helper_path"
codesign --force --options runtime --timestamp --sign "$DEVELOPER_ID_APPLICATION" "$app_path"
codesign --verify --deep --strict --verbose=2 "$app_path"
codesign --display --verbose=4 "$app_path" 2>&1 | grep -q 'runtime' || {
  echo "Portico must use the hardened runtime" >&2
  exit 1
}
cp -R "$app_path" "$dmg_staging_directory/Portico.app"
hdiutil create \
  -volname Portico \
  -srcfolder "$dmg_staging_directory" \
  -ov \
  -format UDZO \
  "$dmg_path"
hdiutil verify "$dmg_path"

xcrun notarytool submit "$dmg_path" \
  --key "$APPLE_NOTARY_KEY_PATH" \
  --key-id "$APPLE_NOTARY_KEY_ID" \
  --issuer "$APPLE_NOTARY_ISSUER_ID" \
  --wait
xcrun stapler staple "$dmg_path"
xcrun stapler validate "$dmg_path"
spctl --assess --type open --verbose=4 "$dmg_path"

shasum -a 256 "$dmg_path"
echo "Release candidate: $dmg_path"
