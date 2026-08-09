#!/usr/bin/env python3

"""Credential-free checks for the externally visible release contract."""

from pathlib import Path


ROOT = Path(__file__).parents[1]
WORKFLOWS = ROOT / ".github" / "workflows"
RELEASE = (WORKFLOWS / "release.yml").read_text(encoding="utf-8")
FASTFILE = (ROOT / "fastlane" / "Fastfile").read_text(encoding="utf-8")
SMOKE_TEST = (ROOT / "Scripts" / "smoke-test-local-app.sh").read_text(encoding="utf-8")

assert "workflow_dispatch:" in RELEASE
assert not any(f"  {trigger}:" in RELEASE for trigger in ("pull_request", "push"))
assert "environment: public-release" in RELEASE
assert "bundle exec fastlane mac release" in RELEASE
assert all(
    "HOMEBREW_TAP_TOKEN" not in workflow.read_text(encoding="utf-8")
    for workflow in WORKFLOWS.glob("*.yml")
    if workflow.name != "release.yml"
)

for required in (
    r"/\A0\.(0|[1-9]\d*)\.(0|[1-9]\d*)\z/",
    r"/\A[0-9a-f]{40}\z/",
    "app_store_connect_api_key(",
    "build_mac_app(",
    "notarize(package:",
    "run_tests(",
    "set_github_release(",
    'script("verify-release-artifact.sh")',
    "https://github.com/chrisbanes/portico/releases/download/v\\#{version}/Portico-\\#{version}.dmg",
    '"brew", "audit", "--cask", "--strict"',
    '"operationalLogging":"disabled"',
    'script("smoke-test-local-app.sh")',
    '"push", "origin", "HEAD:main"',
):
    assert required in FASTFILE, required

ordered = (
    "build(version: version",
    "set_github_release(",
    "verify-release-artifact.sh",
    '"brew", "audit", "--cask", "--strict"',
    'body: { draft: false }',
    "smoke-test-local-app.sh",
    '"push", "origin", "HEAD:main"',
)
positions = [FASTFILE.index(marker, FASTFILE.index('lane :release')) for marker in ordered]
assert positions == sorted(positions)
assert 'pgrep -P "$app_pid" -x portico-helper' in SMOKE_TEST

print("Portico release contract passed")
