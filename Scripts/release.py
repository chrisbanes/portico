#!/usr/bin/env python3

"""Validate release inputs and generate the matching Homebrew cask."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from pathlib import Path


RELEASE_VERSION = re.compile(r"0\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)")
SOURCE_COMMIT = re.compile(r"[0-9a-f]{40}")
SHA256 = re.compile(r"[0-9a-f]{64}")
REPOSITORY = "https://github.com/chrisbanes/portico"


def validate_version(version: str) -> None:
    if not RELEASE_VERSION.fullmatch(version):
        raise ValueError("Version must be a 0.x.y release, such as 0.1.0")


def validate_release(version: str, source_commit: str) -> None:
    validate_version(version)
    if not SOURCE_COMMIT.fullmatch(source_commit):
        raise ValueError("Source commit must be a lowercase 40-character Git commit ID")


def asset_name(version: str) -> str:
    return f"Portico-{version}.dmg"


def asset_url(version: str) -> str:
    return f"{REPOSITORY}/releases/download/v{version}/{asset_name(version)}"


def digest(path: Path) -> str:
    hasher = hashlib.sha256()
    with path.open("rb") as file:
        for chunk in iter(lambda: file.read(1024 * 1024), b""):
            hasher.update(chunk)
    return hasher.hexdigest()


def cask_contents(version: str, checksum: str) -> str:
    checksum = checksum.lower()
    if not SHA256.fullmatch(checksum):
        raise ValueError("SHA-256 must be a 64-character hexadecimal digest")

    return f'''cask "portico" do
  version "{version}"
  sha256 "{checksum}"

  url "{REPOSITORY}/releases/download/v#{{version}}/Portico-#{{version}}.dmg"
  name "Portico"
  desc "Make a web service on your Mac reachable on your tailnet"
  homepage "{REPOSITORY}"

  depends_on macos: :sonoma

  app "Portico.app"
end
'''


def write_cask(version: str, checksum: str, output: Path) -> None:
    validate_version(version)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(cask_contents(version, checksum), encoding="utf-8")


def prepare(version: str, source_commit: str, dmg: Path, cask: Path) -> dict[str, str]:
    validate_release(version, source_commit)
    if not dmg.is_file():
        raise ValueError(f"DMG does not exist: {dmg}")

    checksum = digest(dmg)
    write_cask(version, checksum, cask)
    return {
        "asset_name": asset_name(version),
        "asset_url": asset_url(version),
        "sha256": checksum,
    }


def release_state(version: str, release: dict[str, object]) -> dict[str, str]:
    """Return workflow environment values for an existing GitHub Release response."""
    assets = release.get("assets")
    if not isinstance(assets, list):
        raise ValueError("GitHub Release response must contain an assets list")

    names = {
        asset["name"]
        for asset in assets
        if isinstance(asset, dict) and isinstance(asset.get("name"), str)
    }
    is_draft = release.get("isDraft")
    if not isinstance(is_draft, bool):
        raise ValueError("GitHub Release response must contain an isDraft boolean")

    return {
        "RELEASE_EXISTS": "true",
        "RELEASE_IS_DRAFT": str(is_draft).lower(),
        "RELEASE_HAS_ASSET": str(asset_name(version) in names).lower(),
    }


def parse_arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)

    validate = commands.add_parser("validate", help="validate release inputs")
    validate.add_argument("--version", required=True)
    validate.add_argument("--source-commit", required=True)

    cask = commands.add_parser("write-cask", help="write a cask from verified metadata")
    cask.add_argument("--version", required=True)
    cask.add_argument("--sha256", required=True)
    cask.add_argument("--output", type=Path, required=True)

    prepare_command = commands.add_parser("prepare", help="hash a verified DMG and write its cask")
    prepare_command.add_argument("--version", required=True)
    prepare_command.add_argument("--source-commit", required=True)
    prepare_command.add_argument("--dmg", type=Path, required=True)
    prepare_command.add_argument("--cask", type=Path, required=True)

    release_state_command = commands.add_parser(
        "release-state", help="write workflow state from a GitHub Release JSON response"
    )
    release_state_command.add_argument("--version", required=True)

    return parser.parse_args()


def main() -> None:
    arguments = parse_arguments()
    try:
        if arguments.command == "validate":
            validate_release(arguments.version, arguments.source_commit)
        elif arguments.command == "write-cask":
            write_cask(arguments.version, arguments.sha256, arguments.output)
        elif arguments.command == "release-state":
            validate_version(arguments.version)
            state = release_state(arguments.version, json.load(sys.stdin))
            for key, value in state.items():
                print(f"{key}={value}")
        else:
            for key, value in prepare(
                arguments.version,
                arguments.source_commit,
                arguments.dmg,
                arguments.cask,
            ).items():
                print(f"{key}={value}")
    except ValueError as error:
        raise SystemExit(str(error)) from error


if __name__ == "__main__":
    main()
