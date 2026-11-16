package service

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRecoveryScriptAgainstAGuardedCommit runs the VM proof's recovery steps
// (Scripts/legacy-guard-probe/recovery.py) against a real guarded commit, so
// their assumptions about the manifest, checkpoint and generation layout are
// checked without a VM.
func TestRecoveryScriptAgainstAGuardedCommit(t *testing.T) {
	script, err := filepath.Abs("../../../Scripts/legacy-guard-probe/recovery.py")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("/usr/bin/python3"); err != nil {
		t.Skip("no /usr/bin/python3")
	}
	for _, tc := range []struct{ fixture, file string }{
		{"v3.json", "installation-v3.json"},
		{"v4-remote-app.json", "installation-v4.json"},
		{"v5-public-local-app.json", "installation-v5.json"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			dir := guardTemp(t)
			write(t, filepath.Join(dir, tc.file), fixture(t, tc.fixture), 0o640)
			writeIdentities(t, dir)
			before := snapshot(t, dir, nil)
			mustOpenGuarded(t, dir).Close()
			assertGuarded(t, dir)

			run := func(step string) {
				t.Helper()
				if out, err := exec.Command("/usr/bin/python3", script, step, dir).CombinedOutput(); err != nil {
					t.Fatalf("recovery.py %s: %v\n%s", step, err, out)
				}
			}
			run("verify")
			run("restore")
			// Then, as the script does after copying them out, remove service/
			// and the lock.
			clearFlags(t, filepath.Join(dir, "service"))
			for _, name := range []string{"service", lockFile} {
				if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
			}
			if after := snapshot(t, dir, nil); !maps.Equal(before, after) {
				t.Fatalf("after recovery:\n%v\nwant:\n%v", after, before)
			}
		})
	}
}

// TestRecoveryScriptRefusesIdentityStateAddedAfterCutover: restoring the
// originals would discard identity state the runtime created after the
// commit, so verify refuses any added, removed or changed entry.
func TestRecoveryScriptRefusesIdentityStateAddedAfterCutover(t *testing.T) {
	script, err := filepath.Abs("../../../Scripts/legacy-guard-probe/recovery.py")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("/usr/bin/python3"); err != nil {
		t.Skip("no /usr/bin/python3")
	}
	for name, after := range map[string]func(t *testing.T, identities string){
		"a file under an empty checkpointed directory": func(t *testing.T, identities string) {
			write(t, filepath.Join(identities, hermesID, "empty", "tailscaled.state"), []byte("new"), 0o600)
		},
		"a new Portal directory": func(t *testing.T, identities string) {
			write(t, filepath.Join(identities, unknownID, "tailscaled.state"), []byte("new node"), 0o600)
		},
		"a removed file": func(t *testing.T, identities string) {
			if err := os.Remove(filepath.Join(identities, hermesID, "tailscaled.state")); err != nil {
				t.Fatal(err)
			}
		},
		"a changed file": func(t *testing.T, identities string) {
			write(t, filepath.Join(identities, hermesID, "tailscaled.state"), []byte("rotated"), 0o600)
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := guardTemp(t)
			write(t, filepath.Join(dir, "installation-v3.json"), fixture(t, "v3.json"), 0o600)
			writeIdentities(t, dir)
			if err := os.Mkdir(filepath.Join(dir, "tsnet", hermesOriginal, "empty"), 0o700); err != nil {
				t.Fatal(err)
			}
			s := mustOpenGuarded(t, dir)
			identities := s.IdentityRoot()
			s.Close()
			after(t, identities)
			out, err := exec.Command("/usr/bin/python3", script, "verify", dir).CombinedOutput()
			if err == nil || !strings.Contains(string(out), "the identities changed since the checkpoint") {
				t.Fatalf("verify with %s = %v:\n%s", name, err, out)
			}
		})
	}
}
