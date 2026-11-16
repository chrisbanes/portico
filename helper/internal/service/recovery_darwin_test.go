package service

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
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
			// Then, as the script does, move service/ and the lock aside.
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
