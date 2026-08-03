#!/bin/bash

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
project_path="$repo_root/Portico.xcodeproj"
snapshot_dir="$(mktemp -d)"

cleanup() {
  rm -rf "$snapshot_dir"
}
trap cleanup EXIT

cd "$repo_root"

if [[ -n "$(git ls-files -- Portico.xcodeproj)" ]]; then
  echo "Portico.xcodeproj must be untracked" >&2
  exit 1
fi

if ! git check-ignore --quiet --no-index Portico.xcodeproj/; then
  echo "Portico.xcodeproj must be ignored" >&2
  exit 1
fi

if [[ -e "$project_path" && ! -d "$project_path" ]]; then
  echo "Portico.xcodeproj is not a directory" >&2
  exit 1
fi

rm -rf "$project_path"
./Scripts/generate-xcode-project.sh
cp -R "$project_path" "$snapshot_dir/Portico.xcodeproj"
./Scripts/generate-xcode-project.sh
diff -ru "$snapshot_dir/Portico.xcodeproj" "$project_path"

project_listing="$(xcodebuild -list -project "$project_path")"
printf '%s\n' "$project_listing"

if ! awk '/^[[:space:]]*Targets:/ { in_targets = 1; next } in_targets && /^[[:space:]]*Schemes:/ { exit } in_targets && /^[[:space:]]*Portico[[:space:]]*$/ { found = 1 } END { exit !found }' <<<"$project_listing"; then
  echo "Generated project is missing the Portico target" >&2
  exit 1
fi

if ! awk '/^[[:space:]]*Schemes:/ { in_schemes = 1; next } in_schemes && /^[[:space:]]*Portico[[:space:]]*$/ { found = 1 } END { exit !found }' <<<"$project_listing"; then
  echo "Generated project is missing the Portico scheme" >&2
  exit 1
fi
