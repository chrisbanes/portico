#!/usr/bin/env python3

"""Deterministic release-contract checks that need neither credentials nor a DMG build."""

from __future__ import annotations

import importlib.util
import re
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("release.py")
REPOSITORY_ROOT = SCRIPT.parents[1]
SPEC = importlib.util.spec_from_file_location("release", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
release = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release)


class ReleaseTests(unittest.TestCase):
    source_commit = "0123456789abcdef0123456789abcdef01234567"

    def test_accepts_release_and_immutable_commit(self) -> None:
        release.validate_release("0.12.3", self.source_commit)

    def test_rejects_invalid_versions(self) -> None:
        for version in ("1.0.0", "0.1", "0.01.0", "0.1.00", "0.1.0-rc1"):
            with self.subTest(version=version):
                with self.assertRaises(ValueError):
                    release.validate_release(version, self.source_commit)

    def test_rejects_non_immutable_commit(self) -> None:
        for commit in ("main", self.source_commit[:12], self.source_commit.upper()):
            with self.subTest(commit=commit):
                with self.assertRaises(ValueError):
                    release.validate_release("0.1.0", commit)

    def test_prepared_cask_matches_the_verified_artifact(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            dmg = root / "Portico-0.1.0.dmg"
            cask = root / "Casks" / "portico.rb"
            dmg.write_bytes(b"verified release")

            metadata = release.prepare("0.1.0", self.source_commit, dmg, cask)

            self.assertEqual(metadata["asset_name"], "Portico-0.1.0.dmg")
            self.assertEqual(
                metadata["asset_url"],
                "https://github.com/chrisbanes/portico/releases/download/v0.1.0/Portico-0.1.0.dmg",
            )
            self.assertEqual(
                metadata["sha256"],
                "1ce4572138ddacf54f7b7834f96aef9b61cc975676daa26fef2fbdf5c7a2d4bf",
            )
            self.assertEqual(
                cask.read_text(encoding="utf-8"),
                '''cask "portico" do
  version "0.1.0"
  sha256 "1ce4572138ddacf54f7b7834f96aef9b61cc975676daa26fef2fbdf5c7a2d4bf"

  url "https://github.com/chrisbanes/portico/releases/download/v#{version}/Portico-#{version}.dmg"
  name "Portico"
  desc "Make a web service on your Mac reachable on your tailnet"
  homepage "https://github.com/chrisbanes/portico"

  depends_on macos: :sonoma

  app "Portico.app"
end
''',
            )

    def test_rejects_missing_artifact_and_invalid_checksum(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with self.assertRaises(ValueError):
                release.prepare("0.1.0", self.source_commit, root / "missing.dmg", root / "portico.rb")
            with self.assertRaises(ValueError):
                release.write_cask("0.1.0", "not-a-checksum", root / "portico.rb")
            with self.assertRaises(ValueError):
                release.write_cask("1.0.0", "0" * 64, root / "portico.rb")

    def test_incomplete_draft_is_rebuilt_and_uploaded_on_retry(self) -> None:
        state = release.release_state("0.1.0", {"isDraft": True, "assets": []})

        self.assertEqual(
            state,
            {
                "RELEASE_EXISTS": "true",
                "RELEASE_IS_DRAFT": "true",
                "RELEASE_HAS_ASSET": "false",
            },
        )

    def test_existing_release_asset_is_reused(self) -> None:
        for is_draft in (True, False):
            with self.subTest(is_draft=is_draft):
                state = release.release_state(
                    "0.1.0",
                    {"isDraft": is_draft, "assets": [{"name": "Portico-0.1.0.dmg"}]},
                )

                self.assertEqual(state["RELEASE_HAS_ASSET"], "true")
                self.assertEqual(state["RELEASE_IS_DRAFT"], str(is_draft).lower())

    def test_workflow_keeps_publishing_manual_and_ordered(self) -> None:
        workflows = REPOSITORY_ROOT / ".github" / "workflows"
        release_workflow = (workflows / "release.yml").read_text(encoding="utf-8")

        self.assertIn("workflow_dispatch:", release_workflow)
        self.assertNotRegex(release_workflow, r"(?m)^  (?:pull_request|push):")
        self.assertIn("environment: public-release", release_workflow)

        for workflow in workflows.glob("*.yml"):
            if workflow.name != "release.yml":
                self.assertNotIn("HOMEBREW_TAP_TOKEN", workflow.read_text(encoding="utf-8"))

        step_names = re.findall(r"(?m)^      - name: (.+)$", release_workflow)
        ordered_steps = (
            "Build, sign, notarize, and verify DMG",
            "Create private draft release",
            "Upload the verified DMG to the draft release",
            "Verify the uploaded release asset",
            "Generate cask from verified DMG",
            "Audit generated Homebrew cask",
            "Publish verified GitHub Release",
            "Install and publish the Homebrew cask",
        )
        step_indexes = [step_names.index(step) for step in ordered_steps]
        self.assertEqual(step_indexes, sorted(step_indexes))
        self.assertNotIn("git merge-base --is-ancestor", release_workflow)
        self.assertEqual(release_workflow.count("if: env.RELEASE_HAS_ASSET != 'true'"), 6)

    def test_clean_cask_smoke_test_configures_and_restores_helper_state(self) -> None:
        release_workflow = (REPOSITORY_ROOT / ".github" / "workflows" / "release.yml").read_text(encoding="utf-8")
        smoke_test = (REPOSITORY_ROOT / "Scripts" / "smoke-test-local-app.sh").read_text(encoding="utf-8")

        self.assertIn("prepare_smoke_state", release_workflow)
        self.assertIn('"operationalLogging":"disabled"', release_workflow)
        self.assertIn("restore_smoke_state", release_workflow)
        self.assertLess(
            release_workflow.index("\n          prepare_smoke_state\n"),
            release_workflow.index("PORTICO_APP_PATH=\"$app_directory/Portico.app\""),
        )
        self.assertIn('pgrep -P "$app_pid" -x portico-helper', smoke_test)


if __name__ == "__main__":
    unittest.main()
