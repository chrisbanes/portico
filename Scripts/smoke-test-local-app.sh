#!/bin/bash

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
derived_data="$repo_root/.build/xcode"
products_directory="$derived_data/Build/Products/Debug"
app=""
app_executable=""
helper=""
app_pid=""
helper_pid=""
candidate_helper_pid=""
smoke_home=""
production_sentinel=""
production_sentinel_expected=""

terminate_owned_processes() {
  if [[ -n "$app_pid" ]] && kill -0 "$app_pid" 2>/dev/null; then
    kill "$app_pid" 2>/dev/null || true
  fi
  if [[ -n "$helper_pid" ]] && kill -0 "$helper_pid" 2>/dev/null; then
    kill "$helper_pid" 2>/dev/null || true
  fi
  for _ in {1..100}; do
    if { [[ -z "$app_pid" ]] || ! kill -0 "$app_pid" 2>/dev/null; } \
      && { [[ -z "$helper_pid" ]] || ! kill -0 "$helper_pid" 2>/dev/null; }; then
      return 0
    fi
    sleep 0.1
  done
  return 1
}

cleanup() {
  if ! terminate_owned_processes; then
    echo "Owned smoke processes did not exit; preserving $smoke_home" >&2
    smoke_home=""
  fi
  if [[ -n "$candidate_helper_pid" ]] && kill -0 "$candidate_helper_pid" 2>/dev/null; then
    for _ in {1..100}; do
      ! kill -0 "$candidate_helper_pid" 2>/dev/null && break
      sleep 0.1
    done
    if kill -0 "$candidate_helper_pid" 2>/dev/null; then
      echo "Unvalidated candidate helper PID $candidate_helper_pid remained; preserving $smoke_home" >&2
      smoke_home=""
    fi
  fi
  if [[ -n "$smoke_home" ]] && [[ -d "$smoke_home" ]]; then
    rm -rf "$smoke_home"
  fi
}
trap cleanup EXIT

cd "$repo_root"
./Scripts/generate-xcode-project.sh
xcodebuild \
  -project Portico.xcodeproj \
  -scheme Portico \
  -configuration Debug \
  -destination 'platform=macOS,arch=arm64' \
  -derivedDataPath "$derived_data" \
  ONLY_ACTIVE_ARCH=YES \
  ARCHS=arm64 \
  build

app="$products_directory/Portico Dev.app"
[[ -d "$app" ]] || { echo "Expected Debug Portico Dev app product" >&2; exit 1; }
info_plist="$app/Contents/Info.plist"
app_executable_name="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "$info_plist")"
app_bundle_identifier="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$info_plist")"
app_display_name="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleDisplayName' "$info_plist")"
support_directory="$(/usr/libexec/PlistBuddy -c 'Print :PorticoSupportDirectory' "$info_plist")"
launch_at_login_available="$(/usr/libexec/PlistBuddy -c 'Print :PorticoLaunchAtLoginAvailable' "$info_plist")"
[[ "$app_display_name" == "Portico Dev" ]] || { echo "Debug display identity is invalid" >&2; exit 1; }
[[ "$app_bundle_identifier" == "dev.chrisbanes.Portico.Debug" ]] || { echo "Debug bundle identity is invalid" >&2; exit 1; }
[[ "$support_directory" == "Portico Dev" ]] || { echo "Debug support identity is invalid" >&2; exit 1; }
[[ "$launch_at_login_available" == "NO" ]] || { echo "Debug Launch at Login capability is invalid" >&2; exit 1; }
app_executable="$app/Contents/MacOS/$app_executable_name"
helper="$app/Contents/Helpers/portico-helper"

[[ -x "$app_executable" ]] || { echo "Portico app executable is missing" >&2; exit 1; }
[[ -x "$helper" ]] || { echo "Bundled helper is missing or not executable" >&2; exit 1; }
[[ "$(/usr/libexec/PlistBuddy -c 'Print :LSUIElement' "$app/Contents/Info.plist")" == "true" ]] || {
  echo "Built app is not configured as a menu-bar-only application" >&2
  exit 1
}

smoke_home="$(mktemp -d /private/tmp/PorticoSmoke.XXXXXX)"
production_root="$smoke_home/Library/Application Support/Portico"
dev_root="$smoke_home/Library/Application Support/Portico Dev"
production_sentinel="$production_root/production-sentinel"
production_sentinel_expected="$smoke_home/production-sentinel.expected"
mkdir -p "$production_root" "$dev_root"
printf '%s' 'production-sentinel-v1' > "$production_sentinel"
cp "$production_sentinel" "$production_sentinel_expected"

CFFIXED_USER_HOME="$smoke_home" \
PORTICO_SMOKE_REAL_HELPER="accepted-v5" \
PORTICO_SMOKE_EXPECTED_ROOT="$dev_root" \
"$app_executable" &
app_pid=$!
printf 'Smoke temporary home: %s\n' "$smoke_home"
printf 'Owned app PID: %s\n' "$app_pid"

for _ in {1..100}; do
  candidate_helper_pid="$(pgrep -P "$app_pid" -x portico-helper 2>/dev/null || true)"
  [[ -n "$candidate_helper_pid" ]] && break
  sleep 0.1
done

[[ "$candidate_helper_pid" =~ ^[0-9]+$ ]] || { echo "Expected exactly one helper child PID" >&2; exit 1; }
helper_command="$(ps -o command= -p "$candidate_helper_pid")"
delimiter=' --state-root '
[[ "$helper_command" == *"$delimiter"* ]] || {
  printf 'Candidate helper PID: %s\n' "$candidate_helper_pid" >&2
  printf 'Candidate helper command: %q\n' "$helper_command" >&2
  echo "Direct helper child has an unexpected command shape" >&2
  exit 1
}
candidate_executable="${helper_command%%"$delimiter"*}"
candidate_state_root="${helper_command#*"$delimiter"}"
[[ "$candidate_state_root" == *'/Library/Application Support/Portico Dev/tsnet' ]] \
  && [[ "$candidate_executable" -ef "$helper" ]] || {
    printf 'Candidate helper PID: %s\n' "$candidate_helper_pid" >&2
    printf 'Candidate helper command: %q\n' "$helper_command" >&2
    echo "Direct helper child is not the bundled helper" >&2
    exit 1
  }
helper_pid="$candidate_helper_pid"
printf 'Owned helper PID: %s\n' "$helper_pid"
for _ in {1..100}; do
  [[ -f "$dev_root/smoke-handshake-witness" ]] && break
  sleep 0.1
done
[[ -f "$dev_root/smoke-handshake-witness" ]] || { echo "Expected an accepted protocol-5 handshake witness" >&2; exit 1; }
[[ "$(cat "$dev_root/smoke-handshake-witness")" == "accepted-v5" ]] || { echo "Smoke witness is invalid" >&2; exit 1; }
[[ -f "$dev_root/installation-v4.json" ]] || { echo "Dev installation was not created" >&2; exit 1; }
[[ -d "$dev_root/tsnet" ]] || { echo "Dev helper state root was not created" >&2; exit 1; }
cmp -s "$production_sentinel" "$production_sentinel_expected" || { echo "Production sentinel changed during startup" >&2; exit 1; }
kill -0 "$app_pid" 2>/dev/null || { echo "Portico exited before the handshake window completed" >&2; exit 1; }
kill -0 "$helper_pid" 2>/dev/null || { echo "Bundled helper did not survive the handshake window" >&2; exit 1; }

kill "$app_pid"

for _ in {1..100}; do
  if ! kill -0 "$app_pid" 2>/dev/null && ! kill -0 "$helper_pid" 2>/dev/null; then
    break
  fi
  sleep 0.1
done

kill -0 "$app_pid" 2>/dev/null && { echo "Portico did not quit" >&2; exit 1; }
kill -0 "$helper_pid" 2>/dev/null && { echo "Recorded helper PID survived Portico quit" >&2; exit 1; }
cmp -s "$production_sentinel" "$production_sentinel_expected" || { echo "Production sentinel changed during shutdown" >&2; exit 1; }

app_pid=""
helper_pid=""
echo "Portico local app smoke test passed"
