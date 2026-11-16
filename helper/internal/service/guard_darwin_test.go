package service

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/chrisbanes/portico/helper/internal/installation"
)

var guarded = Options{LegacyGuard: true}

// guardTemp is a temporary root whose uchg flags are cleared before the
// directory is removed.
func guardTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() { clearFlags(t, dir) })
	return dir
}

func clearFlags(t *testing.T, dir string) {
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Type().IsRegular() && immutable(t, p) {
			if err := unix.Chflags(p, 0); err != nil {
				t.Errorf("clear flags: %v", err)
			}
		}
		return nil
	})
}

func immutable(t *testing.T, name string) bool {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Lstat(name, &st); err != nil {
		return false
	}
	return st.Flags&unix.UF_IMMUTABLE != 0
}

// failAtNth fails the nth (from 1) time phase is reached.
func failAtNth(phase Phase, n int) Options {
	seen := 0
	return Options{LegacyGuard: true, Hook: func(p Phase) error {
		if p == phase {
			if seen++; seen == n {
				return errInjected
			}
		}
		return nil
	}}
}

func assertGuarded(t *testing.T, dir string) {
	t.Helper()
	for _, name := range guardTargets {
		p := filepath.Join(dir, name)
		data, err := os.ReadFile(p)
		info, statErr := os.Lstat(p)
		if err != nil || statErr != nil || string(data) != string(guardBytes) || info.Mode() != 0o600 || !immutable(t, p) {
			t.Fatalf("%s is not the immutable guard: %q, %v, %v", name, data, info, err)
		}
	}
}

// targetsNotFlagged fails if any guard target is immutable.
func assertNotFlagged(t *testing.T, dir string) {
	t.Helper()
	for _, name := range guardTargets {
		if immutable(t, filepath.Join(dir, name)) {
			t.Fatalf("%s is still immutable", name)
		}
	}
}

func importOriginals(t *testing.T, dir string) installation.Installation {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	want, _, err := installation.Import(root)
	if err != nil {
		t.Fatal(err)
	}
	return want
}

// guardedSource seeds v5 as the source, a stale v4 beside it at another
// mode, and the identity tree.
func guardedSource(t *testing.T, dir string) {
	write(t, filepath.Join(dir, "installation-v5.json"), fixture(t, "v5-public-local-app.json"), 0o600)
	write(t, filepath.Join(dir, "installation-v4.json"), fixture(t, "v4-remote-app.json"), 0o640)
	writeIdentities(t, dir)
}

func TestLegacyGuardCrashBoundaries(t *testing.T) {
	for _, phase := range []Phase{PhaseGuardCheckpoint, PhaseGuardIntent} {
		t.Run("fault before the intent at "+string(phase), func(t *testing.T) {
			dir := guardTemp(t)
			guardedSource(t, dir)
			before := originals(t, dir)
			want := importOriginals(t, dir)
			if _, err := Open(dir, failAtNth(phase, 1)); !errors.Is(err, errInjected) {
				t.Fatalf("Open() error = %v", err)
			}
			if after := originals(t, dir); !reflect.DeepEqual(after, before) {
				t.Fatalf("originals changed: %v -> %v", before, after)
			}
			assertNotFlagged(t, dir)
			assertGone(t, dir, guardIntentFile)

			s, err := Open(dir, guarded)
			if err != nil {
				t.Fatalf("retry: %v", err)
			}
			defer s.Close()
			assertGuarded(t, dir)
			if !reflect.DeepEqual(s.State().Installation, want) {
				t.Fatalf("retried import = %+v, want %+v", s.State().Installation, want)
			}
		})
	}

	for _, tc := range []struct {
		phase Phase
		n     int
	}{
		{PhaseGuardInstall, 1}, {PhaseGuardFlag, 1}, {PhaseGuardInstall, 2}, {PhaseGuardFlag, 2},
		{PhaseCopy, 1}, {PhaseWrite, 1}, {PhaseFsync, 1}, {PhaseBeforeManifest, 1},
	} {
		t.Run("fault after the intent rolls forward at "+string(tc.phase)+string(rune('0'+tc.n)), func(t *testing.T) {
			dir := guardTemp(t)
			guardedSource(t, dir)
			source := fixture(t, "v5-public-local-app.json")
			want := importOriginals(t, dir)
			identity := contentHashes(t, filepath.Join(dir, "tsnet", hermesOriginal))
			if _, err := Open(dir, failAtNth(tc.phase, tc.n)); !errors.Is(err, errInjected) {
				t.Fatalf("Open() error = %v", err)
			}
			assertGone(t, dir, manifestFile)
			assertGone(t, dir, migrationFailedFile)
			// Each target is the guard or still its original; nothing is removed.
			wasGuard := map[string]bool{}
			for _, name := range guardTargets {
				data, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				wasGuard[name] = string(data) == string(guardBytes)
			}
			if tc.phase != PhaseGuardInstall && tc.phase != PhaseGuardFlag {
				assertGuarded(t, dir)
			}

			s, err := Open(dir, Options{LegacyGuard: true, Hook: func(Phase) error {
				for name, was := range wasGuard {
					if data, _ := os.ReadFile(filepath.Join(dir, name)); was && string(data) != string(guardBytes) {
						t.Errorf("the roll-forward removed the guard at %s", name)
					}
				}
				return nil
			}})
			if err != nil {
				t.Fatalf("roll forward: %v", err)
			}
			defer s.Close()
			assertGuarded(t, dir)
			// The guard replaced the source, so this came from the checkpoint.
			if !reflect.DeepEqual(s.State().Installation, want) || s.State().Migration.Source != "installation-v5.json" {
				t.Fatalf("rolled-forward state = %+v", s.State())
			}
			m := readManifest(t, dir)
			if data, err := os.ReadFile(filepath.Join(dir, checkpointsDir, m.Generation, "installation-v5.json")); err != nil || string(data) != string(source) {
				t.Fatalf("generation checkpoint = %q, %v", data, err)
			}
			if got := contentHashes(t, filepath.Join(dir, generationsDir, m.Generation, "tsnet", hermesID)); !reflect.DeepEqual(got, identity) {
				t.Fatalf("working identity = %v, want %v", got, identity)
			}
		})
	}

	for _, tc := range []struct {
		name   string
		damage func(t *testing.T, dir string)
	}{
		{"intent missing", func(t *testing.T, dir string) { os.Remove(filepath.Join(dir, guardIntentFile)) }},
		{"checkpoint missing", func(t *testing.T, dir string) { os.Remove(filepath.Join(dir, guardCheckpointFile)) }},
		{"checkpoint corrupt", func(t *testing.T, dir string) {
			name := filepath.Join(dir, guardCheckpointFile)
			data, _ := os.ReadFile(name)
			write(t, name, append(data, ' '), 0o600)
		}},
		{"source copy corrupt", func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, guardCopiesDir, "installation-v5.json"), []byte(`{"version":5,"portals":[],"alerts":[]}`), 0o600)
		}},
		// Not imported, but restored on a terminal failure.
		{"other copy corrupt", func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, guardCopiesDir, "installation-v4.json"), []byte(`{"version":4}`), 0o600)
		}},
		{"checkpoint copy missing", func(t *testing.T, dir string) {
			os.Remove(filepath.Join(dir, guardCopiesDir, "installation-v4.json"))
		}},
	} {
		t.Run("guard with "+tc.name+" fails closed", func(t *testing.T) {
			dir := guardTemp(t)
			guardedSource(t, dir)
			if _, err := Open(dir, failAtNth(PhaseCopy, 1)); !errors.Is(err, errInjected) {
				t.Fatalf("Open() error = %v", err)
			}
			tc.damage(t, dir)
			// Open discards incomplete staging first, as it always has.
			noStaging := func(rel string) bool { return rel == stagingDir }
			before := snapshot(t, dir, noStaging)
			for range 2 {
				_, err := Open(dir, guarded)
				if !errors.Is(err, ErrUnsupportedState) || errors.Is(err, ErrMigrationFailed) {
					t.Fatalf("Open() error = %v, want unsupportedState", err)
				}
			}
			// Nothing touched: the guard stays, nothing imported or restored.
			if after := snapshot(t, dir, noStaging); !reflect.DeepEqual(after, before) {
				t.Fatalf("root changed: %v -> %v", before, after)
			}
			assertGuarded(t, dir)
			assertGone(t, dir, manifestFile)
			assertGone(t, dir, generationsDir)
		})
	}

	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{"present targets", guardedSource},
		{"absent targets", func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "installation-v3.json"), fixture(t, "v3.json"), 0o600)
			writeIdentities(t, dir)
		}},
	} {
		t.Run("terminal failure restores the originals with "+tc.name, func(t *testing.T) {
			dir := guardTemp(t)
			tc.setup(t, dir)
			before := originals(t, dir)
			unsafe := filepath.Join(dir, "tsnet", hermesOriginal, "tailscaled.state")
			// An identity made unsafe after the guard went in cannot be
			// imported however often the service retries.
			_, err := Open(dir, Options{LegacyGuard: true, Hook: func(p Phase) error {
				if p == PhaseCopy {
					assertGuarded(t, dir)
					if err := os.Chmod(unsafe, 0o666); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			}})
			if !errors.Is(err, ErrMigrationFailed) {
				t.Fatalf("Open() error = %v, want migrationFailed", err)
			}
			if err := os.Chmod(unsafe, 0o600); err != nil {
				t.Fatal(err)
			}
			if after := originals(t, dir); !reflect.DeepEqual(after, before) {
				t.Fatalf("originals not restored exactly: %v -> %v", before, after)
			}
			assertNotFlagged(t, dir)
			if _, err := os.Stat(filepath.Join(dir, migrationFailedFile)); err != nil {
				t.Fatalf("no failure marker: %v", err)
			}
			// Later starts stay failed and never guard again, guarded or not.
			for _, opts := range []Options{guarded, guarded, {}} {
				if _, err := Open(dir, opts); !errors.Is(err, ErrMigrationFailed) {
					t.Fatalf("later Open() error = %v, want migrationFailed", err)
				}
				if after := originals(t, dir); !reflect.DeepEqual(after, before) {
					t.Fatalf("a later Open changed the originals: %v -> %v", before, after)
				}
			}
			assertGone(t, dir, manifestFile)
		})
	}

	unflag := func(t *testing.T, dir, name string) {
		if err := unix.Chflags(filepath.Join(dir, name), 0); err != nil {
			t.Fatal(err)
		}
	}
	// Each case alters one target and leaves the other guarded.
	for _, tc := range []struct {
		name  string
		alter func(t *testing.T, dir string)
	}{
		{"guard flag cleared", func(t *testing.T, dir string) { unflag(t, dir, "installation-v5.json") }},
		{"guard removed", func(t *testing.T, dir string) {
			unflag(t, dir, "installation-v4.json")
			os.Remove(filepath.Join(dir, "installation-v4.json"))
		}},
		{"guard overwritten", func(t *testing.T, dir string) {
			unflag(t, dir, "installation-v5.json")
			write(t, filepath.Join(dir, "installation-v5.json"), fixture(t, "v5-public-local-app.json"), 0o600)
		}},
		{"guard replaced by a symlink to an immutable guard", func(t *testing.T, dir string) {
			name := filepath.Join(dir, "installation-v4.json")
			unflag(t, dir, "installation-v4.json")
			write(t, filepath.Join(dir, "elsewhere"), guardBytes, 0o600)
			os.Remove(name)
			if err := os.Symlink("elsewhere", name); err != nil {
				t.Fatal(err)
			}
			if err := unix.Chflags(filepath.Join(dir, "elsewhere"), unix.UF_IMMUTABLE); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run("after the commit "+tc.name+" is migrationGuardMissing", func(t *testing.T) {
			dir := guardTemp(t)
			guardedSource(t, dir)
			mustOpenGuarded(t, dir).Close()
			tc.alter(t, dir)
			flags := func() map[string]bool {
				got := map[string]bool{}
				for _, name := range guardTargets {
					got[name] = immutable(t, filepath.Join(dir, name))
				}
				return got
			}
			before, beforeFlags := snapshot(t, dir, nil), flags()
			if _, err := Open(dir, guarded); !errors.Is(err, ErrMigrationGuardMissing) {
				t.Fatalf("Open() error = %v, want migrationGuardMissing", err)
			}
			// No automatic re-guard, and nothing else changed.
			if after := snapshot(t, dir, nil); !reflect.DeepEqual(after, before) {
				t.Fatalf("root changed: %v -> %v", before, after)
			}
			if after := flags(); !reflect.DeepEqual(after, beforeFlags) {
				t.Fatalf("flags changed: %v -> %v", beforeFlags, after)
			}
		})
	}

	t.Run("a record changed after its checkpoint is never overwritten", func(t *testing.T) {
		dir := guardTemp(t)
		guardedSource(t, dir)
		if _, err := Open(dir, failAtNth(PhaseGuardInstall, 1)); !errors.Is(err, errInjected) {
			t.Fatalf("Open() error = %v", err)
		}
		changed := []byte(`{"version":5,"portals":[],"alerts":[]}`)
		write(t, filepath.Join(dir, "installation-v5.json"), changed, 0o600)
		if _, err := Open(dir, guarded); !errors.Is(err, ErrMigrationFailed) {
			t.Fatalf("Open() error = %v, want migrationFailed", err)
		}
		if data, _ := os.ReadFile(filepath.Join(dir, "installation-v5.json")); string(data) != string(changed) {
			t.Fatalf("the changed record was replaced: %q", data)
		}
		assertNotFlagged(t, dir)
	})

	t.Run("an unguarded commit is refused once the guard is required", func(t *testing.T) {
		dir := guardTemp(t)
		guardedSource(t, dir)
		mustOpen(t, dir).Close()
		if _, err := Open(dir, guarded); !errors.Is(err, ErrMigrationGuardMissing) {
			t.Fatalf("Open() error = %v, want migrationGuardMissing", err)
		}
	})
}

func mustOpenGuarded(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir, guarded)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLegacyGuardCoversBothTargetsForEveryVersion(t *testing.T) {
	for _, tc := range []struct{ fixture, file string }{
		{"", ""},
		{"v1.json", "portal-v1.json"},
		{"v2-pristine.json", "installation-v2.json"},
		{"v2.json", "installation-v2.json"},
		{"v3.json", "installation-v3.json"},
		{"v4-remote-app.json", "installation-v4.json"},
		{"v5-public-local-app.json", "installation-v5.json"},
	} {
		t.Run("source "+tc.file, func(t *testing.T) {
			dir := guardTemp(t)
			var source []byte
			if tc.file != "" {
				source = fixture(t, tc.fixture)
				write(t, filepath.Join(dir, tc.file), source, 0o600)
			}
			writeIdentities(t, dir)
			want := importOriginals(t, dir)
			s := mustOpenGuarded(t, dir)
			got := s.State()
			s.Close()
			assertGuarded(t, dir)
			if !reflect.DeepEqual(got.Installation, want) || got.Migration.Source != tc.file {
				t.Fatalf("state = %+v, want %+v from %q", got, want, tc.file)
			}
			// The checkpoint holds the source's bytes; reopening verifies the guard.
			if tc.file != "" {
				if data, err := os.ReadFile(filepath.Join(dir, guardCopiesDir, tc.file)); err != nil || string(data) != string(source) {
					t.Fatalf("checkpoint copy = %q, %v", data, err)
				}
			}
			mustOpenGuarded(t, dir).Close()
		})
	}
}
