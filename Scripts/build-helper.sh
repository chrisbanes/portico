#!/bin/bash

set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "Usage: $0 <output-path>" >&2
  exit 64
fi

output_path="$1"
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
helper_root="$repo_root/helper"
architectures="${ARCHS:-${CURRENT_ARCH:-$(uname -m)}}"
work_dir="$(mktemp -d)"
macos_sdk="$(xcrun --sdk macosx --show-sdk-path)"
clang="$(xcrun --sdk macosx --find clang)"
go_build_cache="${PORTICO_GO_BUILD_CACHE:-$repo_root/.build/go-build-cache/macos-14}"
go_mod_cache="${PORTICO_GO_MOD_CACHE:-$helper_root/.build/go-mod-cache}"

cleanup() {
  rm -rf "$work_dir"
}
trap cleanup EXIT

mkdir -p "$(dirname -- "$output_path")" "$go_build_cache" "$go_mod_cache"

helper_architectures=()
for architecture in $architectures; do
  case "$architecture" in
    arm64)
      go_architecture="arm64"
      ;;
    x86_64)
      go_architecture="amd64"
      ;;
    *)
      echo "Unsupported helper architecture: $architecture" >&2
      exit 1
      ;;
  esac

  helper_architectures+=("$architecture")
  helper_slice="$work_dir/portico-helper-$architecture"
  (
    cd "$helper_root"
    CC="$clang -arch $architecture -isysroot $macos_sdk" \
      CGO_ENABLED=1 \
      GOOS=darwin \
      GOARCH="$go_architecture" \
      MACOSX_DEPLOYMENT_TARGET=14.0 \
      GOCACHE="$go_build_cache" \
      GOMODCACHE="$go_mod_cache" \
      go build -trimpath -o "$helper_slice" ./cmd/portico-helper
  )
done

if [[ ${#helper_architectures[@]} -eq 1 ]]; then
  cp "$work_dir/portico-helper-${helper_architectures[0]}" "$output_path"
else
  lipo -create "$work_dir"/portico-helper-* -output "$output_path"
fi

chmod 755 "$output_path"
