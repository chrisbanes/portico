#!/usr/bin/env bash
# Runs a porticofake portico-service under this user's service manager
# (launchd on macOS, systemd --user on Linux) with the test label and
# isolated temporary roots, and checks its lifecycle over real IPC. It never
# touches the production or Dev roots, labels or units, and it boots out or
# stops its own job and removes its roots on exit.
set -euo pipefail

readonly LABEL=dev.chrisbanes.Portico.ServiceTest
readonly UNIT=portico-service-test.service
readonly HERMES=9F55CA93-D7B3-4EAB-A871-310EA576005A
readonly HERMES_ID=9f55ca93-d7b3-4eab-a871-310ea576005a

repo=$(cd "$(dirname "$0")/.." && pwd)
os=$(uname -s)

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

case "$os" in
  Darwin) domain="gui/$(id -u)" ;;
  Linux) command -v systemctl >/dev/null || fail "systemd --user is required" ;;
  *) fail "unsupported OS $os" ;;
esac

# Refuse anything but the test label and a fresh temporary work directory.
refuse_unsafe() {
  [[ "$LABEL" == dev.chrisbanes.Portico.ServiceTest && "$UNIT" == portico-service-test.service ]] ||
    fail "refusing a non-test label"
  case "$work" in
    *"Application Support/Portico"* | */.local/state/portico* | */Library/LaunchAgents*) fail "refusing a non-test root" ;;
  esac
  [[ "$work" == "$tmp_base"/portico-lc.* ]] || fail "refusing a root outside the temporary directory"
}

job_loaded() {
  if [[ "$os" == Darwin ]]; then
    launchctl print "$domain/$LABEL" >/dev/null 2>&1
  else
    systemctl --user cat "$UNIT" >/dev/null 2>&1
  fi
}

if job_loaded; then
  fail "$LABEL is already loaded; another run may be using it"
fi

# /tmp, not a long TMPDIR, keeps the socket path within sun_path.
tmp_base=$(cd /tmp && pwd -P)
work=$(mktemp -d "$tmp_base/portico-lc.XXXXXX")
work=$(cd "$work" && pwd -P)
refuse_unsafe
state="$work/state"
run_dir="$work/run"
fake="$work/fake"
bin="$work/bin"
requests="$work/requests"
stderr_log="$work/service.stderr"
unit_link=${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/$UNIT
subscriber=""

cleanup() {
  local status=$?
  set +e
  [[ -n "$subscriber" ]] && kill -9 "$subscriber" 2>/dev/null
  if [[ "$os" == Darwin ]]; then
    launchctl bootout "$domain/$LABEL" 2>/dev/null
  else
    systemctl --user stop "$UNIT" 2>/dev/null
    systemctl --user reset-failed "$UNIT" 2>/dev/null
    # Remove only the link this run created into its own work directory.
    if [[ -L "$unit_link" && "$(readlink "$unit_link")" == "$work/"* ]]; then
      rm -f "$unit_link"
    fi
    systemctl --user daemon-reload 2>/dev/null
  fi
  job_loaded && echo "WARNING: $LABEL is still loaded" >&2
  # The work directory holds only this run's binaries and fixture copy.
  [[ "$work" == "$tmp_base"/portico-lc.* ]] && rm -rf "$work"
  exit "$status"
}
trap cleanup EXIT

step() { echo "PASS $1: $2"; }

wait_for() {
  local what=$1 deadline=$((SECONDS + ${3:-60}))
  until eval "$2"; do
    ((SECONDS < deadline)) || fail "timed out waiting for $what"
    sleep 0.5
  done
}

client() { "$bin/lifecycleclient" -runtime-dir "$run_dir" "$@"; }

mutate() {
  local name=$1 method=$2 params=$3
  client mutate "$method" "$params" "$requests/$name" >"$requests/$name.receipt"
  cat "$requests/$name.receipt"
}

portal_id() { sed -n 's/.*"portalID":"\([^"]*\)".*/\1/p' "$requests/$1.receipt"; }

# portal_line ID prints the Portal's "portal ID NAME DESIRED LIFECYCLE PROBLEM" line.
portal_line() { client state | grep "^portal $1 " || true; }

service_pid() {
  if [[ "$os" == Darwin ]]; then
    launchctl print "$domain/$LABEL" 2>/dev/null | awk '$1 == "pid" && $2 == "=" { print $3 }'
  else
    local pid
    pid=$(systemctl --user show -p MainPID --value "$UNIT")
    [[ "$pid" != 0 ]] && echo "$pid"
  fi
}

manager_status() {
  if [[ "$os" == Darwin ]]; then
    launchctl print "$domain/$LABEL"
  else
    systemctl --user status --no-pager "$UNIT" || true
  fi
}

start_job() {
  if [[ "$os" == Darwin ]]; then
    launchctl bootstrap "$domain" "$work/$LABEL.plist"
  else
    # Each run starts the unit several times; keep StartLimitBurst for real crashes.
    systemctl --user reset-failed "$UNIT" 2>/dev/null || true
    systemctl --user start "$UNIT"
  fi
}

stop_job() {
  if [[ "$os" == Darwin ]]; then
    launchctl bootout "$domain/$LABEL"
    wait_for "the job to unload" '! job_loaded'
  else
    systemctl --user stop "$UNIT"
  fi
}

# log_count PID CALL counts the fake runtime's CALL entries from PID.
log_count() { grep -c "\"pid\":$1,\"call\":\"$2\"" "$fake/runtime.log" 2>/dev/null || true; }
last_reconcile() { grep "\"pid\":$1,\"call\":\"reconcile\"" "$fake/runtime.log" | tail -n 1; }

hash_tree() {
  (cd "$1" && find . -type f | LC_ALL=C sort | while read -r f; do
    if command -v sha256sum >/dev/null; then sha256sum "$f"; else shasum -a 256 "$f"; fi
  done) | if command -v sha256sum >/dev/null; then sha256sum; else shasum -a 256; fi
}

file_mode() {
  if [[ "$os" == Darwin ]]; then stat -f '%Lp' "$1"; else stat -c '%a' "$1"; fi
}

# Build the porticofake service and the test client.
mkdir -m 700 "$state" "$run_dir" "$fake" "$bin" "$requests"
(cd "$repo/helper" &&
  go build -tags porticofake -o "$bin/portico-service" ./cmd/portico-service &&
  go build -o "$bin/lifecycleclient" ./internal/servicehost/internal/lifecycleclient)

# Seed the v5 fixture with hermes Stopped, so step 2 enables it, and a fake
# identity tree for it.
sed 's/"desiredState":"enabled"/"desiredState":"stopped"/' \
  "$repo/helper/internal/installation/testdata/v5-public-local-app.json" >"$state/installation-v5.json"
chmod 600 "$state/installation-v5.json"
mkdir -m 700 "$state/tsnet" "$state/tsnet/$HERMES" "$state/tsnet/$HERMES/certs"
head -c 256 /dev/urandom >"$state/tsnet/$HERMES/tailscaled.state"
head -c 64 /dev/urandom >"$state/tsnet/$HERMES/certs/node.crt"
chmod 600 "$state/tsnet/$HERMES/tailscaled.state" "$state/tsnet/$HERMES/certs/node.crt"
original_hash=$(hash_tree "$state/tsnet")

if [[ "$os" == Darwin ]]; then
  sed -e "s|@PROGRAM@|$bin/portico-service|" -e "s|@STATE_ROOT@|$state|" -e "s|@RUNTIME_DIR@|$run_dir|" \
    -e "s|@FAKE_RUNTIME@|$fake|" -e "s|@STDERR@|$stderr_log|" \
    "$repo/helper/packaging/macos/$LABEL.plist" >"$work/$LABEL.plist"
  plutil -lint "$work/$LABEL.plist" >/dev/null
else
  sed -e "s|^Description=.*|Description=Portico service lifecycle test|" \
    -e "s|^ExecStart=.*|ExecStart=$bin/portico-service --variant test --state-root $state --runtime-dir $run_dir --fake-runtime $fake|" \
    "$repo/helper/packaging/linux/portico-service.service" >"$work/$UNIT"
  systemctl --user link "$work/$UNIT" >/dev/null
  systemctl --user daemon-reload
fi

# 1. Start under the service manager.
start_job
wait_for "the service" 'client state >/dev/null 2>&1'
pid1=$(service_pid)
[[ -n "$pid1" ]] || fail "no service pid"
wait_for "the first reconcile" '[[ $(log_count "$pid1" reconcile) -ge 1 ]]'
step 1 "started under $( [[ "$os" == Darwin ]] && echo "launchd ($domain/$LABEL)" || echo "systemd --user ($UNIT)"), pid $pid1"

# 2. Create a Portal, enable the seeded one, and prepare a Stopped Portal and
# a pending removal for step 4.
dest='{"kind":"localApp","port":18080}'
mutate alpha createPortal "{\"name\":\"alpha\",\"destination\":$dest,\"publicAccess\":\"private\"}" >/dev/null
alpha=$(portal_id alpha)
mutate enable-hermes setDesiredState "{\"portalID\":\"$HERMES_ID\",\"state\":\"enabled\"}" >/dev/null
mutate bravo createPortal "{\"name\":\"bravo\",\"destination\":$dest,\"publicAccess\":\"private\"}" >/dev/null
bravo=$(portal_id bravo)
mutate stop-bravo setDesiredState "{\"portalID\":\"$bravo\",\"state\":\"stopped\"}" >/dev/null
mutate charlie createPortal "{\"name\":\"charlie\",\"destination\":$dest,\"publicAccess\":\"private\"}" >/dev/null
charlie=$(portal_id charlie)
touch "$fake/fail-removal"
mutate remove-charlie removePortal "{\"portalID\":\"$charlie\",\"confirmed\":true}" >/dev/null
wait_for "charlie's failed removal" '[[ $(portal_line "$charlie") == *" pendingRemoval removalFailed" ]]'
wait_for "the enabled Portals to run" \
  '[[ $(last_reconcile "$pid1") == *"\"enabled\":["*"$HERMES_ID"* && $(last_reconcile "$pid1") == *"$alpha"* ]]'
[[ $(portal_line "$HERMES_ID") == *" enabled active"* && $(portal_line "$alpha") == *" enabled active"* ]] ||
  fail "hermes and alpha are not Enabled"
step 2 "created alpha ($alpha), enabled seeded hermes; bravo Stopped, charlie pending removal"

# 3. A subscribed client is killed: the Enabled Portals keep running.
"$bin/lifecycleclient" -runtime-dir "$run_dir" subscribe >"$work/subscriber.out" 2>&1 &
subscriber=$!
wait_for "the subscription" 'grep -q subscribed "$work/subscriber.out"' 20
kill -9 "$subscriber"
wait "$subscriber" 2>/dev/null || true
subscriber=""
log_before=$(wc -l <"$fake/runtime.log")
sleep 2
[[ $(log_count "$pid1" close) == 0 ]] || fail "the runtime closed after the client died"
if tail -n +"$((log_before + 1))" "$fake/runtime.log" | grep '"call":"reconcile"' | grep -v "$alpha" | grep -q .; then
  fail "a reconcile after the client died dropped alpha"
fi
[[ $(service_pid) == "$pid1" && $(portal_line "$HERMES_ID") == *" enabled active"* && $(portal_line "$alpha") == *" enabled active"* ]] ||
  fail "Enabled Portals changed after the client died"
step 3 "killed a subscribed client; runtime not closed, hermes and alpha still Enabled (fake-runtime log)"

# 4. kill -9 the service: the manager restarts it, and it recovers the
# committed intent with no client connected.
generation=$(find "$state/service/generations" -mindepth 1 -maxdepth 1 -type d)
generation_hash=$(hash_tree "$generation/tsnet")
ids_before=$(client state | awk '$1 == "portal" && $2 != "'"$charlie"'" { print $2 }' | LC_ALL=C sort)
rm "$fake/fail-removal"
kill -9 "$pid1"
wait_for "the manager to restart the service" 'p=$(service_pid); [[ -n "$p" && "$p" != "$pid1" ]]' 90
pid2=$(service_pid)
wait_for "the restarted reconcile" '[[ $(log_count "$pid2" reconcile) -ge 1 ]]'
wait_for "the retried removal" '[[ $(log_count "$pid2" remove) -ge 1 ]]'
reconcile=$(last_reconcile "$pid2")
[[ "$reconcile" == *"\"enabled\":["*"$HERMES_ID"* && "$reconcile" == *"$alpha"* ]] || fail "Enabled Portals not reconciled: $reconcile"
[[ "$reconcile" == *"\"stopped\":[\"$bravo\"]"* ]] || fail "bravo did not stay Stopped: $reconcile"
grep "\"pid\":$pid2,\"call\":\"remove\"" "$fake/runtime.log" | grep -q "\"portalID\":\"$charlie\"" || fail "the removal was not retried"
wait_for "charlie's removal to finish" '[[ -z $(portal_line "$charlie") ]]'
ids_after=$(client state | awk '$1 == "portal" { print $2 }' | LC_ALL=C sort)
[[ "$ids_after" == "$ids_before" ]] || fail "Portal UUIDs changed: $ids_before -> $ids_after"
[[ $(hash_tree "$state/tsnet") == "$original_hash" ]] || fail "the original identity tree changed"
[[ $(hash_tree "$generation/tsnet") == "$generation_hash" ]] || fail "the working identity tree changed"
step 4 "kill -9 pid $pid1; restarted as pid $pid2; hermes and alpha reconciled, bravo Stopped, charlie's removal retried and finished; UUIDs and identity hashes unchanged"

# 5. A same-ID retry across the restart returns the original receipt.
original=$(cat "$requests/alpha.receipt")
retried=$(client mutate createPortal "{\"name\":\"alpha\",\"destination\":$dest,\"publicAccess\":\"private\"}" "$requests/alpha")
[[ "$retried" == "$original" ]] || fail "retry receipt differs: $original vs $retried"
[[ $(client state | grep -c ' alpha ') == 1 ]] || fail "the retry created a second alpha"
step 5 "same-ID createPortal retry after the restart returned the original receipt"

# 6. Unsupported state fails startup visibly, with no runtime started.
stop_job
cp -p "$state/service/manifest.json" "$work/manifest.json"
printf '{"schema":99}' >"$state/service/manifest.json"
starts_before=$(grep -c '"call":"start"' "$fake/runtime.log")
start_job
wait_for "the startup failure over IPC" '[[ $(client state 2>/dev/null) == *"startupFailure=unsupportedState"* ]]'
refused=$(client mutate setDesiredState "{\"portalID\":\"$alpha\",\"state\":\"stopped\"}" "$requests/refused" || true)
[[ "$refused" == "error unsupportedState" ]] || fail "a mutation was not refused with unsupportedState: $refused"
failed_pid=$(service_pid)
kill -TERM "$failed_pid"
if [[ "$os" == Darwin ]]; then
  wait_for "launchd to record the failed exit" 'launchctl print "$domain/$LABEL" | grep -q "last exit code = 1"'
  manager_status | grep -E 'state =|last exit code' | sed 's/^/    /'
  grep -q 'startup failure: unsupportedState' "$stderr_log" || fail "no startup failure in the service's stderr"
  sed 's/^/    stderr: /' "$stderr_log"
else
  wait_for "systemd to record the failed exit" '[[ $(systemctl --user show -p ExecMainStatus --value "$UNIT") == 1 ]]'
  manager_status | sed -n '1,4p' | sed 's/^/    /'
fi
[[ $(grep -c '"call":"start"' "$fake/runtime.log") == "$starts_before" ]] || fail "a runtime started against unsupported state"
stop_job
cp -p "$work/manifest.json" "$state/service/manifest.json"
start_job
wait_for "the recovered service" 'client state 2>/dev/null | head -n 1 | grep -q "startupFailure=$"'
step 6 "unsupported manifest: startupFailure=unsupportedState over IPC, mutation refused, failed exit visible to the manager, no runtime started"

# 7. No TCP listener, and private modes.
pid=$(service_pid)
if [[ "$os" == Darwin ]]; then
  if lsof -nP -a -p "$pid" -iTCP -sTCP:LISTEN 2>/dev/null | grep -q LISTEN; then fail "the service listens on TCP"; fi
else
  if ss -ltnpH 2>/dev/null | grep -q "pid=$pid,"; then fail "the service listens on TCP"; fi
fi
for dir in "$state" "$state/service" "$generation" "$run_dir/portico-test"; do
  [[ $(file_mode "$dir") == 700 ]] || fail "mode $(file_mode "$dir") on a private directory"
done
for file in "$state/service-owner.lock" "$state/service/manifest.json" "$generation/state.json" "$run_dir/portico-test/management.sock"; do
  [[ $(file_mode "$file") == 600 ]] || fail "mode $(file_mode "$file") on a private file"
done
step 7 "no TCP listener for pid $pid; state, generation and socket directories 0700, lock, manifest, state and socket 0600"

# 8. Clean stop.
closes_before=$(log_count "$pid" close)
stop_job
wait_for "the process to exit" '! kill -0 "$pid" 2>/dev/null' 30
[[ $(log_count "$pid" close) -gt "$closes_before" ]] || fail "the runtime was not closed"
[[ ! -e "$run_dir/portico-test/management.sock" ]] || fail "the socket was left behind"
if [[ "$os" == Linux && $(systemctl --user show -p Result --value "$UNIT") != success ]]; then
  fail "the unit did not stop cleanly"
fi
step 8 "clean stop: runtime closed, socket removed, process $pid exited"

echo "PASS: all 8 lifecycle steps"
