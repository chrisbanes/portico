#!/bin/bash

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
derived_data="$repo_root/.build/xcode"
app="$derived_data/Build/Products/Debug/Portico.app"
app_executable="$app/Contents/MacOS/Portico"
helper="$app/Contents/Helpers/portico-helper"
app_pid=""
helper_pid=""

cleanup() {
  if [[ -n "$app_pid" ]] && kill -0 "$app_pid" 2>/dev/null; then
    kill "$app_pid" 2>/dev/null || true
  fi
  if [[ -n "$helper_pid" ]] && kill -0 "$helper_pid" 2>/dev/null; then
    kill "$helper_pid" 2>/dev/null || true
  fi
}
trap cleanup EXIT

cd "$repo_root"
./Scripts/generate-xcode-project.sh
xcodebuild \
  -project Portico.xcodeproj \
  -scheme Portico \
  -destination 'platform=macOS,arch=arm64' \
  -derivedDataPath "$derived_data" \
  ONLY_ACTIVE_ARCH=YES \
  ARCHS=arm64 \
  build

[[ -x "$app_executable" ]] || { echo "Portico app executable is missing" >&2; exit 1; }
[[ -x "$helper" ]] || { echo "Bundled helper is missing or not executable" >&2; exit 1; }
[[ "$(/usr/libexec/PlistBuddy -c 'Print :LSUIElement' "$app/Contents/Info.plist")" == "true" ]] || {
  echo "Built app is not configured as a menu-bar-only application" >&2
  exit 1
}

"$app_executable" &
app_pid=$!

for _ in {1..100}; do
  helper_pid="$(pgrep -P "$app_pid" -x portico-helper 2>/dev/null || true)"
  [[ -n "$helper_pid" ]] && break
  sleep 0.1
done

[[ "$helper_pid" =~ ^[0-9]+$ ]] || { echo "Expected exactly one helper child PID" >&2; exit 1; }
sleep 3.5
kill -0 "$app_pid" 2>/dev/null || { echo "Portico exited before the handshake window completed" >&2; exit 1; }
kill -0 "$helper_pid" 2>/dev/null || { echo "Bundled helper did not survive the handshake window" >&2; exit 1; }

osascript -e 'tell application id "dev.chrisbanes.Portico" to quit'

for _ in {1..100}; do
  if ! kill -0 "$app_pid" 2>/dev/null; then
    break
  fi
  sleep 0.1
done

kill -0 "$app_pid" 2>/dev/null && { echo "Portico did not quit" >&2; exit 1; }
kill -0 "$helper_pid" 2>/dev/null && { echo "Recorded helper PID survived Portico quit" >&2; exit 1; }

app_pid=""
helper_pid=""
echo "Portico local app smoke test passed"
