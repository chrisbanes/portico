package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestLegacyReadersExcludedAtEveryPhase runs every released Portico reader,
// compiled by Scripts/verify-legacy-guard.sh into
// $PORTICO_LEGACY_PROBES/<version>/reader, against a guarded root at each
// phase of the service's ownership. Each reader must fail to start, load
// and save, and leave the root as it was.
func TestLegacyReadersExcludedAtEveryPhase(t *testing.T) {
	probes := os.Getenv("PORTICO_LEGACY_PROBES")
	if probes == "" {
		t.Skip("set PORTICO_LEGACY_PROBES; see Scripts/verify-legacy-guard.sh")
	}
	entries, err := os.ReadDir(probes)
	if err != nil {
		t.Fatal(err)
	}
	var versions []string
	for _, e := range entries {
		if e.IsDir() {
			versions = append(versions, e.Name())
		}
	}
	if len(versions) == 0 {
		t.Fatal("no compiled readers")
	}
	readers := func(t *testing.T, dir string) map[string]string {
		t.Helper()
		results := map[string]string{}
		for _, v := range versions {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			out, err := exec.CommandContext(ctx, filepath.Join(probes, v, "reader"), dir).Output()
			cancel()
			if err != nil {
				t.Fatalf("%s reader: %v", v, err)
			}
			results[v] = strings.TrimSpace(string(out))
		}
		return results
	}

	dir := guardTemp(t)
	// As prepareRoot leaves it; v0.0.x readers chmod the root before saving.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	guardedSource(t, dir)
	identity := snapshot(t, filepath.Join(dir, "tsnet"), nil)
	topLevel := func() []string {
		names, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, n := range names {
			got = append(got, n.Name())
		}
		return got
	}
	want := []string{"installation-v4.json", "installation-v5.json", serviceDir, lockFile, "tsnet"}
	slices.Sort(want)
	excluded := map[string]int{}
	probe := func(phase string) {
		t.Helper()
		for v, out := range readers(t, dir) {
			lines := strings.Split(out, "\n")
			ok := len(lines) == 3
			for i, op := range []string{"prepareForStartup", "loadInstallation", "save"} {
				ok = ok && strings.HasPrefix(lines[i], op+" error ")
			}
			if !ok {
				t.Errorf("RE-PLAN: %s was not excluded at %s:\n%s", v, phase, out)
				continue
			}
			excluded[v]++
			t.Logf("%s excluded at %s: %s", v, phase, strings.Join(lines, "; "))
		}
		assertGuarded(t, dir)
		if got := snapshot(t, filepath.Join(dir, "tsnet"), nil); !reflect.DeepEqual(got, identity) {
			t.Errorf("identity tree changed at %s: %v -> %v", phase, identity, got)
		}
		if got := topLevel(); !reflect.DeepEqual(got, want) {
			t.Errorf("root entries at %s = %v, want %v", phase, got, want)
		}
	}

	// 1. Guard installed, before the commit; then the import is cut short.
	if _, err := Open(dir, Options{LegacyGuard: true, Hook: func(p Phase) error {
		switch p {
		case PhaseCopy:
			probe("guard installed, before the commit")
		case PhaseBeforeManifest:
			return errInjected
		}
		return nil
	}}); err == nil {
		t.Fatal("the interrupted import committed")
	}
	// 2. A reopen in progress, rolling the import forward; 3. after the
	// commit with the engine and its runtime running; 4. the store closed.
	h := newHarness(t, func(h *harness) {
		h.dir = dir
		h.storeOpt = Options{LegacyGuard: true, Hook: func(p Phase) error {
			if p == PhaseCopy {
				probe("a reopen in progress")
			}
			return nil
		}}
	})
	if names := h.factory.callNames(); !slices.Contains(names, "reconcile") {
		t.Fatalf("the runtime is not running: %v", names)
	}
	probe("after the commit, engine running")
	h.stop()
	probe("store closed, service stopped")

	for _, v := range versions {
		if excluded[v] == 4 {
			t.Logf("%s: excluded at all 4 phases", v)
		}
	}

	t.Run("sensitivity: an unflagged guard is detected", func(t *testing.T) {
		plain := t.TempDir()
		for _, name := range guardTargets {
			write(t, filepath.Join(plain, name), guardBytes, 0o600)
		}
		for v, out := range readers(t, plain) {
			if !strings.Contains(out, "save ok") {
				t.Errorf("%s did not save over an unflagged guard, so the probe cannot see a missing flag:\n%s", v, out)
				continue
			}
			t.Logf("%s saved over the unflagged guard: detected", v)
		}
	})
}
