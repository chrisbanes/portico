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

for target in PorticoApplication Portico PorticoTests PorticoUITests; do
  if ! awk -v target="$target" '/^[[:space:]]*Targets:/ { in_targets = 1; next } in_targets && /^[[:space:]]*Schemes:/ { exit } in_targets && $0 ~ "^[[:space:]]*" target "[[:space:]]*$" { found = 1 } END { exit !found }' <<<"$project_listing"; then
    echo "Generated project is missing the $target target" >&2
    exit 1
  fi
done

application_settings="$(xcodebuild -showBuildSettings -project "$project_path" -target PorticoApplication -configuration Debug)"
if ! awk '/^[[:space:]]*PRODUCT_TYPE[[:space:]]*=[[:space:]]*com\.apple\.product-type\.library\.static$/ { found = 1 } END { exit !found }' <<<"$application_settings"; then
  echo "PorticoApplication must be a static library" >&2
  exit 1
fi

project_metadata="$project_path/project.pbxproj"
library_target_id="$(awk '
  /\/\* PorticoApplication \*\/ = \{$/ { candidate = $1; in_target = 1; next }
  in_target && /isa = PBXNativeTarget;/ { native_target = 1 }
  in_target && native_target && /name = PorticoApplication;/ { print candidate; exit }
  in_target && /^\t\t};$/ { in_target = 0; native_target = 0 }
' "$project_metadata")"
library_dependency_id="$(awk -v target="$library_target_id" '
  /\/\* PBXTargetDependency \*\/ = \{$/ { candidate = $1; in_dependency = 1; next }
  in_dependency && $0 ~ "target = " target " /\\* PorticoApplication \\*/;" { print candidate; exit }
  in_dependency && /^\t\t};$/ { in_dependency = 0 }
' "$project_metadata")"
if [[ -z "$library_target_id" || -z "$library_dependency_id" ]] || ! awk -v dependency="$library_dependency_id" '
  /\/\* PorticoTests \*\/ = \{$/ { in_target = 1; next }
  in_target && $1 == dependency { found = 1; next }
  in_target && /name = PorticoTests;/ { exit }
  END { exit !found }
' "$project_metadata"; then
  echo "PorticoTests must directly depend on PorticoApplication" >&2
  exit 1
fi

unit_test_settings="$(xcodebuild -showBuildSettings -project "$project_path" -target PorticoTests -configuration Debug)"
if awk '/^[[:space:]]*(TEST_HOST|BUNDLE_LOADER)[[:space:]]*=/ { found = 1 } END { exit !found }' <<<"$unit_test_settings"; then
  echo "PorticoTests must not use an application host or bundle loader" >&2
  exit 1
fi

ui_test_settings="$(xcodebuild -showBuildSettings -project "$project_path" -target PorticoUITests -configuration Debug)"
if ! awk '/^[[:space:]]*TEST_TARGET_NAME[[:space:]]*=[[:space:]]*Portico$/ { found = 1 } END { exit !found }' <<<"$ui_test_settings"; then
  echo "PorticoUITests must target the Portico application" >&2
  exit 1
fi

for scheme in Portico "Portico UI Tests"; do
  if ! awk -v scheme="$scheme" '/^[[:space:]]*Schemes:/ { in_schemes = 1; next } in_schemes && $0 ~ "^[[:space:]]*" scheme "[[:space:]]*$" { found = 1 } END { exit !found }' <<<"$project_listing"; then
    echo "Generated project is missing the $scheme scheme" >&2
    exit 1
  fi
done

scheme_dir="$project_path/xcshareddata/xcschemes"
scheme_test_action() {
  awk 'index($0, "<TestAction") { in_test_action = 1 } in_test_action { print } index($0, "</TestAction>") { exit }' "$1"
}

headless_test_action="$(scheme_test_action "$scheme_dir/Portico.xcscheme")"
ui_test_action="$(scheme_test_action "$scheme_dir/Portico UI Tests.xcscheme")"
headless_testable_count="$(awk 'index($0, "<TestableReference") { count++ } END { print count + 0 }' <<<"$headless_test_action")"
ui_testable_count="$(awk 'index($0, "<TestableReference") { count++ } END { print count + 0 }' <<<"$ui_test_action")"

if [[ "$headless_testable_count" != 1 ]] || ! grep -Fq 'BlueprintName = "PorticoTests"' <<<"$headless_test_action"; then
  echo "Portico scheme must select only PorticoTests" >&2
  exit 1
fi

if [[ "$ui_testable_count" != 1 ]] || ! grep -Fq 'BlueprintName = "PorticoUITests"' <<<"$ui_test_action"; then
  echo "Portico UI Tests scheme must select only PorticoUITests" >&2
  exit 1
fi
