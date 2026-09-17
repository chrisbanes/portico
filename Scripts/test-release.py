#!/usr/bin/env python3

"""Credential-free checks for the externally visible release contract."""

from pathlib import Path


ROOT = Path(__file__).parents[1]
WORKFLOWS = ROOT / ".github" / "workflows"
RELEASE = (WORKFLOWS / "release.yml").read_text(encoding="utf-8")
FASTFILE = (ROOT / "fastlane" / "Fastfile").read_text(encoding="utf-8")
HOMEBREW_TAP = (ROOT / "fastlane" / "homebrew_tap.rb").read_text(encoding="utf-8")
LOCAL_SMOKE_TEST = (ROOT / "Scripts" / "smoke-test-local-app.sh").read_text(encoding="utf-8")
INSTALLED_SMOKE_TEST = (ROOT / "Scripts" / "smoke-test-installed-app.sh").read_text(encoding="utf-8")
ARTIFACT_VERIFIER = (ROOT / "Scripts" / "verify-release-artifact.sh").read_text(encoding="utf-8")
HELPER_VERIFIER = (ROOT / "Scripts" / "verify-helper-architectures.sh").read_text(encoding="utf-8")

assert "workflow_dispatch:" in RELEASE
assert not any(f"  {trigger}:" in RELEASE for trigger in ("pull_request", "push"))
assert "environment: public-release" in RELEASE
assert "github.ref == 'refs/heads/main'" in RELEASE
assert "ref: main" in RELEASE
assert "timeout-minutes: 180" in RELEASE
assert "source_commit:" not in RELEASE
assert "SOURCE_COMMIT" not in RELEASE
assert "bundle exec fastlane mac release" in RELEASE
assert all(
    "HOMEBREW_TAP_TOKEN" not in workflow.read_text(encoding="utf-8")
    for workflow in WORKFLOWS.glob("*.yml")
    if workflow.name != "release.yml"
)

for required in (
    r"/\A0\.(0|[1-9]\d*)\.(0|[1-9]\d*)\z/",
    'source_commit = sh("git", "rev-parse", "HEAD").strip',
    "app_store_connect_api_key(",
    "build_mac_app(",
    "notarize(package:",
    "run_tests(",
    "set_github_release(",
    'script("verify-release-artifact.sh")',
    'ARCHITECTURES = %w[arm64 x86_64].freeze',
    '"Portico-#{version}-#{architecture}.dmg"',
    "architectures: missing_architectures",
):
    assert required in FASTFILE, required

ordered = (
    "build(version: version",
    "set_github_release(",
    "verify-release-artifact.sh",
    "update_homebrew_tap(",
    'body: { draft: false }',
)
positions = [FASTFILE.index(marker, FASTFILE.index('lane :release')) for marker in ordered]
assert positions == sorted(positions)

for required in (
    'arch arm: "arm64", intel: "x86_64"',
    "sha256 arm:",
    "https://github.com/chrisbanes/portico/releases/download/v\\#{version}/Portico-\\#{version}-\\#{arch}.dmg",
    '"brew", "audit", "--cask", "--strict"',
    '"operationalLogging":"disabled"',
    '"push", "origin", "HEAD:main"',
):
    assert required in HOMEBREW_TAP, required

tap_ordered = (
    '"brew", "audit", "--cask", "--strict"',
    "publish_release.call",
    'File.join(state_root, "installation-v4.json")',
    '"Scripts/smoke-test-installed-app.sh"',
    '"push", "origin", "HEAD:main"',
)
tap_positions = [HOMEBREW_TAP.index(marker) for marker in tap_ordered]
assert tap_positions == sorted(tap_positions)
assert '"Scripts/smoke-test-local-app.sh"' not in HOMEBREW_TAP
assert '"PORTICO_APP_PATH" => File.join(app_directory, "Portico.app")' in HOMEBREW_TAP

assert 'app="$products_directory/Portico Dev.app"' in LOCAL_SMOKE_TEST
assert "*.app" not in LOCAL_SMOKE_TEST
assert "PORTICO_APP_PATH" not in LOCAL_SMOKE_TEST
assert "-configuration Debug" in LOCAL_SMOKE_TEST
assert "CFFIXED_USER_HOME" in LOCAL_SMOKE_TEST
assert "PORTICO_SMOKE_REAL_HELPER" in LOCAL_SMOKE_TEST
assert "PORTICO_SMOKE_EXPECTED_ROOT" in LOCAL_SMOKE_TEST
assert "CFBundleExecutable" in LOCAL_SMOKE_TEST
assert "CFBundleIdentifier" in LOCAL_SMOKE_TEST
assert 'kill "$app_pid"' in LOCAL_SMOKE_TEST
assert "osascript" not in LOCAL_SMOKE_TEST
assert "Portico.app" not in LOCAL_SMOKE_TEST
assert "\nHOME=" not in LOCAL_SMOKE_TEST

assert "PORTICO_APP_PATH is required" in INSTALLED_SMOKE_TEST
assert "CFBundleExecutable" in INSTALLED_SMOKE_TEST
assert "CFBundleDisplayName" in INSTALLED_SMOKE_TEST
assert "CFBundleIdentifier" in INSTALLED_SMOKE_TEST
assert "PorticoSupportDirectory" in INSTALLED_SMOKE_TEST
assert "PorticoLaunchAtLoginAvailable" in INSTALLED_SMOKE_TEST
assert '"Portico"' in INSTALLED_SMOKE_TEST
assert '"dev.chrisbanes.Portico"' in INSTALLED_SMOKE_TEST
assert '"YES"' in INSTALLED_SMOKE_TEST
assert 'pgrep -P "$app_pid" -x portico-helper' in INSTALLED_SMOKE_TEST
assert 'candidate_executable" -ef "$helper"' in INSTALLED_SMOKE_TEST
assert 'expected_state_root="$HOME/Library/Application Support/Portico/tsnet"' in INSTALLED_SMOKE_TEST
assert '"$candidate_state_root" -ef "$expected_state_root"' in INSTALLED_SMOKE_TEST
assert "sleep 3.5" in INSTALLED_SMOKE_TEST
assert 'kill "$app_pid"' in INSTALLED_SMOKE_TEST
assert "xcodebuild" not in INSTALLED_SMOKE_TEST
assert "CFFIXED_USER_HOME" not in INSTALLED_SMOKE_TEST
assert "PORTICO_SMOKE_" not in INSTALLED_SMOKE_TEST
assert "*.app" not in INSTALLED_SMOKE_TEST
assert "osascript" not in INSTALLED_SMOKE_TEST
assert "-configuration Debug" in (ROOT / "README.md").read_text(encoding="utf-8")
assert "Portico Dev.app" in (ROOT / "README.md").read_text(encoding="utf-8")
assert "Usage: $0 <version> <architecture> <dmg-path>" in ARTIFACT_VERIFIER
assert '[[ "$architectures" == "$architecture" ]]' in ARTIFACT_VERIFIER
assert 'ARCHS="$architecture"' in HELPER_VERIFIER
assert '[[ "$architectures" == "$architecture" ]]' in HELPER_VERIFIER

print("Portico release contract passed")
