#!/bin/bash

# Proves the MIG-1 legacy-writer guard against the real released apps in a
# disposable Tart macOS VM. On the host it downloads the v0.0.1 and v0.0.2
# release DMGs, verifies their pinned SHA-256 checksums, and builds the
# porticofake service. In the VM it seeds the production root with a fixture
# and a fake identity tree, runs the service against that root with
# --legacy-guard, and launches each old app during the transfer, after the
# commit, with the service stopped and in a kill -9 restart loop, asserting
# the guard, the records, the identities and that no helper started. Last,
# it follows the documented manual recovery and shows that, unguarded on a
# fresh seed, each version rewrites the records: the positive controls.
#
#   Scripts/test-legacy-exclusion-in-vm.sh            run it (downloads, Tart)
#   Scripts/test-legacy-exclusion-in-vm.sh --dry-run  validate and print the plan only

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
tart="${TART:-$(command -v tart || echo "$HOME/Applications/tart.app/Contents/MacOS/tart")}"
image="${PORTICO_VM_IMAGE:-ghcr.io/cirruslabs/macos-golden-gate-xcode:27}"
work="$repo_root/.build/legacy-exclusion-vm"
results="$repo_root/.build/test-results/legacy-exclusion-vm"
vm="portico-legacy-exclusion-$$"
vm_pid=""

# The released apps, pinned to their GitHub release asset digests.
versions=(0.0.1 0.0.2)
sha256s=(
  bdb0858934440f345902808bfacf9d79d83d073c9ab7ada04739a7d424cffe82
  254de987bc3f6d6a2869cc59d5982f6738f87a4124a64ec7ad0a674d6762f50b
)
dmg_url() { echo "https://github.com/chrisbanes/portico/releases/download/v$1/Portico-$1.dmg"; }

fixture="$repo_root/helper/internal/installation/testdata/v3.json"
plist="$repo_root/helper/packaging/macos/dev.chrisbanes.Portico.ServiceTest.plist"
recovery="$repo_root/Scripts/legacy-guard-probe/recovery.py"

dry_run=0
case "${1:-}" in
  "") ;;
  --dry-run) dry_run=1 ;;
  *) echo "usage: $0 [--dry-run]" >&2; exit 2 ;;
esac

fail() { echo "FAIL: $*" >&2; exit 1; }

validate() {
  [[ $(uname -s) == Darwin ]] || fail "the VM proof runs on a macOS host"
  [[ ${#versions[@]} == "${#sha256s[@]}" ]] || fail "each version needs one checksum"
  local i
  for i in "${!versions[@]}"; do
    [[ ${versions[$i]} =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "bad version ${versions[$i]}"
    [[ ${sha256s[$i]} =~ ^[0-9a-f]{64}$ ]] || fail "bad checksum for ${versions[$i]}"
    git -C "$repo_root" rev-parse -q --verify "refs/tags/v${versions[$i]}" >/dev/null ||
      fail "no tag v${versions[$i]} for a pinned release"
  done
  [[ -f "$fixture" ]] || fail "missing fixture $fixture"
  # The guest's seed edits the fixture with sed; fail loudly if it drifts.
  local pattern
  for pattern in '"desiredState":"stopped","lifecycle":"pendingTailnetRejection"' \
    '"alerts":\[.*\],"operationalLogging"' '"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A"'; do
    grep -qE "$pattern" "$fixture" || fail "the seed no longer matches $fixture: $pattern"
  done
  /usr/bin/python3 -c 'import ast, sys; ast.parse(open(sys.argv[1]).read())' "$recovery" || fail "the recovery steps do not parse"
  grep -q '<string>@FAKE_RUNTIME@</string>' "$plist" || fail "the test LaunchAgent template changed"
  [[ -f "$repo_root/helper/internal/servicehost/internal/lifecycleclient/main.go" ]] || fail "missing lifecycleclient"
  if ((dry_run)); then
    return
  fi
  [[ -x "$tart" ]] || fail "Tart is required: https://tart.run"
  local tool
  for tool in curl go hdiutil shasum; do
    command -v "$tool" >/dev/null || fail "$tool is required"
  done
}

plan() {
  cat <<EOF
Plan (image $image, VM $vm):
host:
  1. download $(for v in "${versions[@]}"; do printf '%s ' "$(dmg_url "$v")"; done)
     into $work/dmg, and verify SHA-256 ${sha256s[*]}
  2. mount each DMG read-only, copy Portico.app to products/apps/Portico-<version>.app, detach
  3. build portico-service -tags porticofake and lifecycleclient for darwin/arm64
  4. tart clone the image, run it with products shared read-only and results writable
guest (the VM's own ~/Library/Application Support/Portico, fresh):
  5. seed installation-v3.json (hermes Enabled) and a fake tsnet identity tree
  6. transfer: run the service with --legacy-guard --pause-after-guard; while paused
     after the guard, launch v0.0.1 and v0.0.2; assert; resume and commit
  7. after the commit: bootstrap the test LaunchAgent with --legacy-guard; launch each app; assert
  8. stopped: launchctl bootout; launch each app; assert
  9. restarting: bootstrap; three times kill -9 the service and launch an app at once; assert
     each, then wait for launchd's restart
 10. recovery: bootout, check no post-cutover mutation and unchanged identities, preserve
     service/, clear uchg and restore the checkpointed records
 11. positive controls: for each version, re-seed unguarded, launch it, and expect it
     to rewrite the records
assert at every launch: the app is still running after 20 seconds; both guards hold the
guard bytes with uchg; no installation record created or changed; tsnet identity hashes
unchanged; no helper child process at any poll in those 20 seconds.
results: $results/summary.log
EOF
}

# guest_script prints the steps run inside the VM, as its admin user.
guest_script() {
  cat <<'GUEST'
set -euo pipefail
shared="/Volumes/My Shared Files"
for _ in {1..30}; do [[ -d "$shared/products" ]] && break; sleep 1; done
w=~/portico-legacy
rm -rf "$w"
cp -R "$shared/products" "$w"
xattr -cr "$w"
root="$HOME/Library/Application Support/Portico"
label=dev.chrisbanes.Portico.ServiceTest
domain="gui/$(id -u)"
hermes=9F55CA93-D7B3-4EAB-A871-310EA576005A
guard='{"version":2147483647,"porticoServiceGuard":1}'
run=$(mktemp -d /tmp/plx.XXXXXX)
fake="$w/fake"
pause="$w/pause"
mkdir -m 700 "$fake" "$pause"

fail() { echo "FAIL: $*"; exit 1; }
pass() { echo "PASS $*"; }
client() { "$w/bin/lifecycleclient" -runtime-dir "$run" "$@"; }
wait_for() {
  local what=$1 deadline=$((SECONDS + ${3:-60}))
  until eval "$2"; do
    ((SECONDS < deadline)) || fail "timed out waiting for $what"
    sleep 0.5
  done
}
identity_hash() { (cd "$root/tsnet" && find . -type f | LC_ALL=C sort | xargs shasum -a 256) | shasum -a 256; }
# records lists every legacy record with its mode, flags and hash.
records() {
  local f
  for f in "$root"/installation-v*.json "$root"/portal-v1.json "$root"/.installation-*; do
    [[ -e "$f" ]] && echo "${f##*/} $(stat -f '%Lp %Sf' "$f") $(shasum -a 256 <"$f")"
  done
  return 0
}
service_pid() { launchctl print "$domain/$label" 2>/dev/null | awk '$1 == "pid" && $2 == "=" { print $3 }'; }
ready() { client state 2>/dev/null | head -n 1 | grep -q 'startupFailure=$'; }

# seed writes a fresh unguarded root and records its baseline.
seed() {
  mkdir -m 700 "$root" "$root/tsnet" "$root/tsnet/$hermes"
  sed -e 's/"desiredState":"stopped","lifecycle":"pendingTailnetRejection"/"desiredState":"enabled","lifecycle":"active"/' \
    -e 's/"alerts":\[.*\],"operationalLogging"/"alerts":[],"operationalLogging"/' "$w/v3.json" >"$root/installation-v3.json"
  head -c 256 /dev/urandom >"$root/tsnet/$hermes/tailscaled.state"
  chmod 600 "$root/installation-v3.json" "$root/tsnet/$hermes/tailscaled.state"
  original_records=$(records)
  original_identity=$(identity_hash)
  pass "seed: installation-v3.json with hermes Enabled, fake identity tree"
}
[[ ! -e "$root" ]] || fail "this VM already has a production root"
seed

guarded_records=""
assert_guarded() {
  local t
  for t in installation-v5.json installation-v4.json; do
    [[ $(cat "$root/$t") == "$guard" ]] || fail "$1: $t is not the guard"
    [[ $(stat -f '%Sf' "$root/$t") == *uchg* ]] || fail "$1: $t is not immutable"
  done
  [[ $(records) == "$guarded_records" ]] || fail "$1: a record was created or changed: $(records)"
  [[ $(identity_hash) == "$original_identity" ]] || fail "$1: the identity tree changed"
}

# run_app VERSION PHASE runs the old app for 20 seconds, polling for child
# processes throughout, and fails the phase if the app exits early. It sets
# children to every child seen ("pid command" lines), and stops them too.
run_app() {
  local app="$w/apps/Portico-$1.app" exe pid seen
  exe=$(defaults read "$app/Contents/Info" CFBundleExecutable)
  "$app/Contents/MacOS/$exe" >/dev/null 2>&1 &
  pid=$!
  children=""
  for _ in {1..40}; do
    seen=$(ps -axo ppid=,pid=,comm= | awk -v p="$pid" '$1 == p { $1 = ""; print }')
    [[ -z "$seen" ]] || children+="$seen"$'\n'
    # A zombie has exited too.
    ps -o stat= -p "$pid" 2>/dev/null | grep -qv '^Z' || fail "v$1 $2: the app exited by itself"
    sleep 0.5
  done
  children=$(printf '%s' "$children" | sort -u)
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  local child
  for child in $(awk '{ print $1 }' <<<"$children"); do kill "$child" 2>/dev/null || true; done
}

# launch VERSION PHASE runs the old app and asserts it took nothing.
launch() {
  run_app "$1" "$2"
  [[ -z "$children" ]] || fail "v$1 $2: started $children"
  assert_guarded "v$1 $2"
  pass "v$1 $2: guard bytes and uchg intact, no record created or changed, identities unchanged, no helper"
}

plist() {
  sed -e "s|@PROGRAM@|$w/bin/portico-service|" -e "s|@STATE_ROOT@|$root|" -e "s|@RUNTIME_DIR@|$run|" \
    -e "s|<string>@FAKE_RUNTIME@</string>|<string>$fake</string><string>--legacy-guard</string>|" \
    -e "s|@STDERR@|$w/service.stderr|" "$w/ServiceTest.plist" >"$w/$label.plist"
  plutil -lint "$w/$label.plist" >/dev/null
}

# 6. During the transfer, paused after the guard.
"$w/bin/portico-service" --variant test --state-root "$root" --runtime-dir "$run" \
  --fake-runtime "$fake" --legacy-guard --pause-after-guard "$pause" 2>"$w/transfer.stderr" &
transfer=$!
wait_for "the pause after the guard" '[[ -e "$pause/paused" ]]'
guarded_records=$(records)
assert_guarded "paused after the guard"
for v in 0.0.1 0.0.2; do launch "$v" "during the transfer"; done
touch "$pause/resume"
wait_for "the transfer to commit" ready 60
kill -TERM "$transfer"
wait "$transfer"

# 7. After the commit, with the service running.
plist
launchctl bootstrap "$domain" "$w/$label.plist"
wait_for "the service" ready
for v in 0.0.1 0.0.2; do launch "$v" "after the commit, service running"; done

# 8. With the service stopped.
pid=$(service_pid)
launchctl bootout "$domain/$label"
wait_for "the service to stop" '! kill -0 "$pid" 2>/dev/null'
for v in 0.0.1 0.0.2; do launch "$v" "service stopped"; done

# 9. In a kill -9 restart loop.
launchctl bootstrap "$domain" "$w/$label.plist"
wait_for "the service" ready
for i in 1 2 3; do
  pid=$(service_pid)
  kill -9 "$pid"
  v=0.0.$((i % 2 + 1))
  launch "$v" "kill -9 restart $i"
  wait_for "launchd's restart" 'p=$(service_pid); [[ -n "$p" && "$p" != "$pid" ]] && ready' 90
done
grep -q '"call":"start"' "$fake/runtime.log" || fail "the fake runtime never started"

# 10. Manual recovery, as docs/service-lifecycle.md describes.
pid=$(service_pid)
launchctl bootout "$domain/$label"
wait_for "the service to stop" '! kill -0 "$pid" 2>/dev/null'
/usr/bin/python3 "$w/recovery.py" verify "$root"
backup="$HOME/Library/Application Support/Portico Service Backup"
ditto "$root/service" "$backup"
/usr/bin/python3 "$w/recovery.py" restore "$root"
chflags -R nouchg "$root/service"
rm -rf "$root/service" "$root/service-owner.lock"
[[ $(records) == "$original_records" ]] || fail "recovery did not restore the original records: $(records)"
[[ $(identity_hash) == "$original_identity" ]] || fail "recovery changed the identity tree"
pass "recovery: guard cleared, original records restored byte- and mode-exact, service/ preserved outside the root"

# 11. Positive controls: unguarded, each version takes a fresh root.
for v in 0.0.1 0.0.2; do
  chflags -R nouchg "$root"
  rm -rf "$root"
  seed
  run_app "$v" "positive control"
  [[ $(records) != "$original_records" || $(identity_hash) != "$original_identity" ]] ||
    fail "positive control: unguarded v$v changed nothing, so the checks cannot see a writer"
  echo "FAIL-as-expected positive control: unguarded v$v rewrote the records ($(records | tr '\n' ' '))"
done
echo "PASS: legacy exclusion proven for v0.0.1 and v0.0.2"
GUEST
}

validate
if ((dry_run)); then
  guest_script | bash -n || fail "the guest script does not parse"
  plan
  echo "dry run: inputs valid; nothing downloaded, built or started"
  exit 0
fi

mounted=""
# shellcheck disable=SC2329 # invoked by the EXIT trap
cleanup() {
  [[ -n "$mounted" ]] && hdiutil detach -quiet "$mounted" 2>/dev/null
  if [[ -n "$vm_pid" ]] && kill -0 "$vm_pid" 2>/dev/null; then
    "$tart" stop "$vm" >/dev/null 2>&1 || true
    wait "$vm_pid" 2>/dev/null || true
  fi
  "$tart" delete "$vm" >/dev/null 2>&1 || true
}
trap cleanup EXIT

plan
products="$work/products"
rm -rf "$products" "$results"
mkdir -p "$work/dmg" "$products/apps" "$products/bin" "$results"

for i in "${!versions[@]}"; do
  version=${versions[$i]}
  dmg="$work/dmg/Portico-$version.dmg"
  if [[ ! -f "$dmg" ]]; then
    curl -fL --retry 3 -o "$dmg.part" "$(dmg_url "$version")"
    mv "$dmg.part" "$dmg"
  fi
  sum=$(shasum -a 256 "$dmg" | cut -d ' ' -f 1)
  [[ "$sum" == "${sha256s[$i]}" ]] || { rm -f "$dmg"; fail "Portico-$version.dmg checksum $sum"; }
  mounted=$(mktemp -d "$work/mount.XXXXXX")
  hdiutil attach -quiet -nobrowse -readonly -mountpoint "$mounted" "$dmg"
  app=$(find "$mounted" -maxdepth 1 -name '*.app' -print -quit)
  [[ -n "$app" ]] || fail "no app in Portico-$version.dmg"
  ditto "$app" "$products/apps/Portico-$version.app"
  hdiutil detach -quiet "$mounted"
  rmdir "$mounted"
  mounted=""
  echo "host: Portico-$version.dmg verified ($sum)"
done

(cd "$repo_root/helper" &&
  GOOS=darwin GOARCH=arm64 go build -tags porticofake -o "$products/bin/portico-service" ./cmd/portico-service &&
  GOOS=darwin GOARCH=arm64 go build -o "$products/bin/lifecycleclient" ./internal/servicehost/internal/lifecycleclient)
cp "$fixture" "$products/v3.json"
cp "$plist" "$products/ServiceTest.plist"
cp "$recovery" "$products/recovery.py"

"$tart" clone "$image" "$vm"
"$tart" run --no-graphics \
  --dir="products:$products:ro" \
  --dir="results:$results" \
  "$vm" &
vm_pid=$!

for _ in {1..90}; do
  "$tart" exec "$vm" true >/dev/null 2>&1 && break
  kill -0 "$vm_pid" 2>/dev/null || fail "VM exited during boot"
  sleep 2
done
"$tart" exec "$vm" true >/dev/null || fail "VM guest agent did not become ready"

status=0
"$tart" exec "$vm" /bin/bash -c "$(guest_script)" 2>&1 | tee "$results/summary.log" || status=$?
exit "$status"
