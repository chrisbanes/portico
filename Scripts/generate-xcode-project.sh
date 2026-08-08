#!/bin/bash

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"

if ! command -v xcodegen >/dev/null; then
  echo "XcodeGen is required; install it with: brew install xcodegen" >&2
  exit 1
fi

xcodegen --version
cd "$repo_root"
exec xcodegen generate --spec project.yml --project .
