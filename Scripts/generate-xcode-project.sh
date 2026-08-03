#!/bin/bash

set -euo pipefail

required_version="2.46.0"
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"

if ! version_output="$(xcodegen --version 2>&1)"; then
  echo "XcodeGen $required_version is required but xcodegen is unavailable" >&2
  exit 1
fi

installed_version="${version_output##* }"
if [[ "$installed_version" != "$required_version" ]]; then
  echo "XcodeGen $required_version is required but found $installed_version" >&2
  exit 1
fi

cd "$repo_root"
exec xcodegen generate --spec project.yml --project .
