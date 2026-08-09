#!/usr/bin/env python3

"""Credential-free checks for the externally visible release contract."""

from pathlib import Path


ROOT = Path(__file__).parents[1]
WORKFLOWS = ROOT / ".github" / "workflows"
RELEASE = (WORKFLOWS / "release.yml").read_text(encoding="utf-8")
SMOKE_TEST = (ROOT / "Scripts" / "smoke-test-local-app.sh").read_text(encoding="utf-8")

assert "workflow_dispatch:" in RELEASE
assert not any(f"  {trigger}:" in RELEASE for trigger in ("pull_request", "push"))
assert "environment: public-release" in RELEASE
assert all(
    "HOMEBREW_TAP_TOKEN" not in workflow.read_text(encoding="utf-8")
    for workflow in WORKFLOWS.glob("*.yml")
    if workflow.name != "release.yml"
)

for required in (
    '[[ "$VERSION" =~ ^0\\.',
    '[[ "$SOURCE_COMMIT" =~ ^[0-9a-f]{40}$ ]]',
    'gh release create "$RELEASE_TAG" "$RUNNER_TEMP/release/Portico-$VERSION.dmg"',
    'shasum -a 256 "$RUNNER_TEMP/release/Portico-$VERSION.dmg"',
    'https://github.com/chrisbanes/portico/releases/download/v#{version}/Portico-#{version}.dmg',
    "brew audit --cask --strict chrisbanes/tap/portico",
    '"operationalLogging":"disabled"',
    'PORTICO_APP_PATH="$app_directory/Portico.app" ./Scripts/smoke-test-local-app.sh',
    'git -C "$tap_directory" push origin HEAD:main',
):
    assert required in RELEASE, required

ordered = (
    "Build, sign, notarize, and verify DMG",
    "Create private draft release with verified DMG",
    "Verify the uploaded release asset",
    "Generate cask from verified DMG",
    "Audit generated Homebrew cask",
    "Publish verified GitHub Release",
    'PORTICO_APP_PATH="$app_directory/Portico.app"',
    'git -C "$tap_directory" push origin HEAD:main',
)
positions = [RELEASE.index(marker) for marker in ordered]
assert positions == sorted(positions)
assert 'pgrep -P "$app_pid" -x portico-helper' in SMOKE_TEST

print("Portico release contract passed")
