#!/bin/bash

set -euo pipefail

if [[ -z "${PORTICO_APP_PATH:-}" ]]; then
  echo "PORTICO_APP_PATH is required" >&2
  exit 1
fi

app="$PORTICO_APP_PATH"
expected_state_root="$HOME/Library/Application Support/Portico/tsnet"
app_executable=""
helper=""
app_pid=""
helper_pid=""
candidate_helper_pid=""

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
  terminate_owned_processes || echo "Owned installed-smoke processes did not exit" >&2
  if [[ -n "$candidate_helper_pid" ]] && kill -0 "$candidate_helper_pid" 2>/dev/null; then
    for _ in {1..100}; do
      ! kill -0 "$candidate_helper_pid" 2>/dev/null && break
      sleep 0.1
    done
    kill -0 "$candidate_helper_pid" 2>/dev/null \
      && echo "Unvalidated candidate helper PID $candidate_helper_pid remained" >&2
  fi
}
trap cleanup EXIT

info_plist="$app/Contents/Info.plist"
[[ -f "$info_plist" ]] || { echo "Installed Portico Info.plist is missing" >&2; exit 1; }
app_executable_name="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "$info_plist")"
app_display_name="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleDisplayName' "$info_plist")"
app_bundle_identifier="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$info_plist")"
support_directory="$(/usr/libexec/PlistBuddy -c 'Print :PorticoSupportDirectory' "$info_plist")"
launch_at_login_available="$(/usr/libexec/PlistBuddy -c 'Print :PorticoLaunchAtLoginAvailable' "$info_plist")"
[[ "$app_display_name" == "Portico" ]] || { echo "Installed display identity is invalid" >&2; exit 1; }
[[ "$app_bundle_identifier" == "dev.chrisbanes.Portico" ]] || { echo "Installed bundle identity is invalid" >&2; exit 1; }
[[ "$support_directory" == "Portico" ]] || { echo "Installed support identity is invalid" >&2; exit 1; }
[[ "$launch_at_login_available" == "YES" ]] || { echo "Installed Launch at Login capability is invalid" >&2; exit 1; }
app_executable="$app/Contents/MacOS/$app_executable_name"
helper="$app/Contents/Helpers/portico-helper"

[[ -x "$app_executable" ]] || { echo "Installed Portico app executable is missing" >&2; exit 1; }
[[ -x "$helper" ]] || { echo "Installed bundled helper is missing or not executable" >&2; exit 1; }
[[ "$(/usr/libexec/PlistBuddy -c 'Print :LSUIElement' "$info_plist")" == "true" ]] || {
  echo "Installed app is not configured as a menu-bar-only application" >&2
  exit 1
}

"$app_executable" &
app_pid=$!

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
[[ "$candidate_state_root" -ef "$expected_state_root" ]] \
  && [[ "$candidate_executable" -ef "$helper" ]] || {
    printf 'Candidate helper PID: %s\n' "$candidate_helper_pid" >&2
    printf 'Candidate helper command: %q\n' "$helper_command" >&2
    echo "Direct helper child is not the bundled helper" >&2
    exit 1
  }
helper_pid="$candidate_helper_pid"

sleep 3.5
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

app_pid=""
helper_pid=""
echo "Portico installed app smoke test passed"
