#!/bin/bash

# Runs the native UI tests in a disposable Tart macOS VM so they cannot
# terminate the host's Portico Dev or take over its keyboard and mouse.
# Extra arguments go to the guest's xcodebuild, e.g.
#   -only-testing:PorticoUITests/PorticoUITests/testSettingsHasInitialAccessibilityFocusTarget

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
tart="${TART:-$(command -v tart || echo "$HOME/Applications/tart.app/Contents/MacOS/tart")}"
image="${PORTICO_VM_IMAGE:-ghcr.io/cirruslabs/macos-golden-gate-xcode:27}"
derived_data="$repo_root/.build/xcode-ui-vm"
results="$repo_root/.build/test-results/xcode-ui-vm"
vm="portico-ui-tests-$$"
vm_pid=""

[[ -x "$tart" ]] || { echo "Tart is required: https://tart.run" >&2; exit 1; }

cleanup() {
  if [[ -n "$vm_pid" ]] && kill -0 "$vm_pid" 2>/dev/null; then
    "$tart" stop "$vm" >/dev/null 2>&1 || true
    wait "$vm_pid" 2>/dev/null || true
  fi
  "$tart" delete "$vm" >/dev/null 2>&1 || true
}
trap cleanup EXIT

cd "$repo_root"
./Scripts/generate-xcode-project.sh
xcodebuild \
  -project Portico.xcodeproj \
  -scheme "Portico UI Tests" \
  -configuration Debug \
  -destination 'platform=macOS,arch=arm64' \
  -derivedDataPath "$derived_data" \
  build-for-testing

rm -rf "$results"
mkdir -p "$results"

"$tart" clone "$image" "$vm"
"$tart" run --no-graphics \
  --dir="products:$derived_data/Build/Products:ro" \
  --dir="results:$results" \
  "$vm" &
vm_pid=$!

for _ in {1..90}; do
  "$tart" exec "$vm" true >/dev/null 2>&1 && break
  kill -0 "$vm_pid" 2>/dev/null || { echo "VM exited during boot" >&2; exit 1; }
  sleep 2
done
"$tart" exec "$vm" true >/dev/null || { echo "VM guest agent did not become ready" >&2; exit 1; }

# Run from the guest's own disk; the shared folder is read-only.
"$tart" exec "$vm" /bin/bash -c '
  set -euo pipefail
  shared="/Volumes/My Shared Files"
  for _ in {1..30}; do [[ -d "$shared/products" ]] && break; sleep 1; done
  rm -rf ~/portico-products
  cp -R "$shared/products" ~/portico-products
  xctestrun="$(ls ~/portico-products/*.xctestrun | head -1)"
  xcodebuild test-without-building \
    -xctestrun "$xctestrun" \
    -destination "platform=macOS,arch=arm64" \
    -resultBundlePath ~/portico-ui-tests.xcresult \
    "$@" \
    && status=0 || status=$?
  [[ -d ~/portico-ui-tests.xcresult ]] && cp -R ~/portico-ui-tests.xcresult "$shared/results/"
  exit "$status"
' _ "$@"
