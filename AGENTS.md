## Agent skills

### Issue tracker

Issues are tracked in GitHub Issues for this repository. See
`docs/agents/issue-tracker.md`.

### Triage labels

Use the five canonical triage labels without translation. See
`docs/agents/triage-labels.md`.

### GitHub Project

Run the configured Project lifecycle through
`docs/agents/run-github-project.md`.

### UI tests

Run native UI tests with `Scripts/test-ui-in-vm.sh`, not on the host. On the
host they terminate a running Portico Dev and take over the keyboard and mouse.
The script needs Tart and its `macos-golden-gate-xcode:27` image (about 81 GB).
Extra arguments reach `xcodebuild`, for example `-only-testing:...`. Results
land in `.build/test-results/xcode-ui-vm/`.

### Domain docs

This is a single-context repository with `CONTEXT.md` and `docs/adr/` at the
root. See `docs/agents/domain.md`.
