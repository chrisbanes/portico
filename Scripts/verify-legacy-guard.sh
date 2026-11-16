#!/usr/bin/env bash
# Proves the MIG-1 legacy-writer guard against every released reader: it
# compiles Portico/PortalStore.swift and PortalConfiguration.swift from each
# v* tag and HEAD with Scripts/legacy-guard-probe/main.swift, then runs
# TestLegacyReadersExcludedAtEveryPhase against those readers. The sources
# come from git show into a temporary directory; nothing is checked out.
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/portico-legacy-guard.XXXXXX")
trap 'rm -rf "$work"' EXIT

[[ $(uname -s) == Darwin ]] || { echo "FAIL: the legacy readers are macOS-only" >&2; exit 1; }
command -v swiftc >/dev/null || { echo "FAIL: swiftc is required" >&2; exit 1; }

# macOS ships bash 3.2, which has no mapfile.
versions=()
while IFS= read -r tag; do versions+=("$tag"); done < <(git -C "$repo" tag -l 'v*')
# The releases pinned in test-legacy-exclusion-in-vm.sh must be among them, so
# a clone without tags cannot pass on HEAD alone.
for required in v0.0.1 v0.0.2; do
  [[ " ${versions[*]} " == *" $required "* ]] || { echo "FAIL: no tag $required; fetch the tags" >&2; exit 1; }
done
versions+=(HEAD)
for version in "${versions[@]}"; do
  dir="$work/$version"
  mkdir -p "$dir/src"
  for file in PortalStore.swift PortalConfiguration.swift; do
    git -C "$repo" show "$version:Portico/$file" >"$dir/src/$file"
  done
  swiftc -O -o "$dir/reader" "$repo/Scripts/legacy-guard-probe/main.swift" "$dir/src/PortalStore.swift" "$dir/src/PortalConfiguration.swift"
  rm -rf "$dir/src"
  echo "compiled $version ($(git -C "$repo" rev-parse --short "$version^{commit}"))"
done

log="$work/test.log"
status=0
(cd "$repo/helper" && PORTICO_LEGACY_PROBES="$work" go test -count=1 -v -run '^TestLegacyReadersExcludedAtEveryPhase$' ./internal/service) >"$log" 2>&1 || status=$?
cat "$log"

echo "---"
for version in "${versions[@]}"; do
  if grep -q " $version: excluded at all 4 phases" "$log"; then
    echo "PASS $version: excluded at all 4 phases"
  else
    echo "FAIL $version: not excluded at every phase"
    status=1
  fi
  if grep -q " $version saved over the unflagged guard: detected" "$log"; then
    echo "PASS sensitivity $version: an unflagged guard is detected (save succeeds)"
  else
    echo "FAIL sensitivity $version: the probe did not detect an unflagged guard"
    status=1
  fi
done
exit "$status"
