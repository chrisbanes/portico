#!/bin/bash

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
temporary_directory="$(mktemp -d)"
helper_path="$temporary_directory/portico-helper"

cleanup() {
  rm -rf "$temporary_directory"
}
trap cleanup EXIT

cd "$repo_root"
PORTICO_GO_BUILD_CACHE="$repo_root/.build/verify-go-build-cache/macos-14" \
  ARCHS='arm64 x86_64' \
  ./Scripts/build-helper.sh "$helper_path"

architectures="$(lipo -archs "$helper_path")"
[[ "$architectures" == *arm64* && "$architectures" == *x86_64* ]] || {
  echo "Expected universal helper, found: $architectures" >&2
  exit 1
}

for architecture in arm64 x86_64; do
  if ! vtool -arch "$architecture" -show-build "$helper_path" | awk '
    /minos/ { found = 1; if ($2 != "14.0") exit 1 }
    END { exit !found }
  '; then
    echo "Expected $architecture helper slice to support macOS 14.0" >&2
    exit 1
  fi
done

if ARCHS='arm64 unsupported' ./Scripts/build-helper.sh "$temporary_directory/invalid-helper"; then
  echo "Unsupported helper architectures must fail" >&2
  exit 1
fi

echo "Portico universal helper verification passed"
