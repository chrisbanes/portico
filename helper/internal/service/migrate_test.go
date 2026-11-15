package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/chrisbanes/portico/helper/internal/installation"
)

const (
	hermesID       = "9f55ca93-d7b3-4eab-a871-310ea576005a"
	hermesOriginal = "9F55CA93-D7B3-4EAB-A871-310EA576005A"
	unknownID      = "5ea74329-3144-4ba2-925f-138d14d61fcc"
)

var errInjected = errors.New("injected fault")

func failAt(phase Phase) Options {
	return Options{Hook: func(p Phase) error {
		if p == phase {
			return errInjected
		}
		return nil
	}}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "installation", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func write(t *testing.T, name string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, mode); err != nil {
		t.Fatal(err)
	}
}

// writeIdentities creates a historical tsnet tree: hermes's identity under its
// uppercase name with nested files, plus children that must not be copied.
func writeIdentities(t *testing.T, root string) {
	t.Helper()
	identity := filepath.Join(root, "tsnet", hermesOriginal)
	write(t, filepath.Join(identity, "tailscaled.state"), []byte("node-key"), 0o600)
	write(t, filepath.Join(identity, "logs", "tailscaled.log.conf"), []byte("log-id"), 0o644)
	write(t, filepath.Join(root, "tsnet", "notes", "readme"), []byte("not a portal"), 0o600)
	write(t, filepath.Join(root, "tsnet", unknownID, "tailscaled.state"), []byte("orphan"), 0o600)
}

// snapshot records every entry's mode and content (or link target) under dir.
func snapshot(t *testing.T, dir string, skip func(rel string) bool) map[string]string {
	t.Helper()
	entries := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if skip != nil && skip(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		value := info.Mode().String()
		switch {
		case info.Mode().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				value += " unreadable"
				break
			}
			sum := sha256.Sum256(data)
			value += " " + hex.EncodeToString(sum[:])
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			value += " -> " + target
		}
		entries[rel] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func originals(t *testing.T, dir string) map[string]string {
	return snapshot(t, dir, func(rel string) bool { return rel == serviceDir || rel == lockFile })
}

func readManifest(t *testing.T, dir string) manifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, manifestFile))
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func mustOpen(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func assertGone(t *testing.T, dir, rel string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(dir, rel)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s exists (err %v)", rel, err)
	}
}

func TestImportVersionsOneThroughFive(t *testing.T) {
	for _, tc := range []struct{ fixture, file string }{
		{"v1.json", "portal-v1.json"},
		{"v2-pristine.json", "installation-v2.json"},
		{"v2.json", "installation-v2.json"},
		{"v3.json", "installation-v3.json"},
		{"v4-remote-app.json", "installation-v4.json"},
		{"v5-public-local-app.json", "installation-v5.json"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			dir := t.TempDir()
			source := fixture(t, tc.fixture)
			write(t, filepath.Join(dir, tc.file), source, 0o600)
			writeIdentities(t, dir)
			before := originals(t, dir)
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			want, _, err := installation.Import(root)
			root.Close()
			if err != nil {
				t.Fatal(err)
			}

			s := mustOpen(t, dir)
			got := s.State()
			s.Close()
			if !reflect.DeepEqual(got.Installation, want) || got.Migration.Source != tc.file || got.Revision != 0 {
				t.Fatalf("state = %+v, want installation %+v from %s", got, want, tc.file)
			}
			if after := originals(t, dir); !reflect.DeepEqual(after, before) {
				t.Fatalf("originals changed: %v -> %v", before, after)
			}

			m := readManifest(t, dir)
			generation := filepath.Join(dir, generationsDir, m.Generation)
			checkpoint := filepath.Join(dir, checkpointsDir, m.Generation)
			if data, err := os.ReadFile(filepath.Join(checkpoint, tc.file)); err != nil || string(data) != string(source) {
				t.Fatalf("checkpoint of %s = %q, %v", tc.file, data, err)
			}
			sum := sha256.Sum256(source)
			if m.Sources[tc.file] != hex.EncodeToString(sum[:]) {
				t.Fatalf("manifest sources = %v", m.Sources)
			}

			hasHermes := len(want.Portals) > 0
			original := contentHashes(t, filepath.Join(dir, "tsnet", hermesOriginal))
			for _, copyRoot := range []string{generation, checkpoint} {
				copied := filepath.Join(copyRoot, "tsnet", hermesID)
				if !hasHermes {
					assertGone(t, copyRoot, filepath.Join("tsnet", hermesID))
					continue
				}
				if got := contentHashes(t, copied); !reflect.DeepEqual(got, original) {
					t.Fatalf("%s identity = %v, want %v", copyRoot, got, original)
				}
				assertPrivateModes(t, filepath.Join(copyRoot, "tsnet"))
				assertGone(t, copyRoot, filepath.Join("tsnet", "notes"))
				assertGone(t, copyRoot, filepath.Join("tsnet", unknownID))
			}
			if hasHermes && m.Sources["tsnet/"+hermesID+"/logs/tailscaled.log.conf"] == "" {
				t.Fatalf("manifest lacks identity hashes: %v", m.Sources)
			}

			// A committed generation reopens unchanged.
			again := mustOpen(t, dir)
			defer again.Close()
			if !reflect.DeepEqual(again.State(), got) || readManifest(t, dir).Generation != m.Generation {
				t.Fatal("reopen changed the committed generation")
			}
		})
	}
}

// contentHashes maps each path under dir to its type and content hash.
func contentHashes(t *testing.T, dir string) map[string]string {
	t.Helper()
	return snapshotContent(snapshot(t, dir, nil))
}

func snapshotContent(entries map[string]string) map[string]string {
	content := map[string]string{}
	for rel, value := range entries {
		// Drop the mode: copies are deliberately tightened to 0700/0600.
		mode, rest, _ := strings.Cut(value, " ")
		content[rel] = mode[:1] + " " + rest
	}
	return content
}

func assertPrivateModes(t *testing.T, dir string) {
	t.Helper()
	for rel, value := range snapshot(t, dir, nil) {
		if !strings.HasPrefix(value, "drwx------") && !strings.HasPrefix(value, "-rw-------") {
			t.Fatalf("%s mode %s, want 0700 directory or 0600 file", rel, value)
		}
	}
}

func TestMigrationCrashBoundaries(t *testing.T) {
	validSource := func(t *testing.T, dir string) {
		write(t, filepath.Join(dir, "installation-v3.json"), fixture(t, "v3.json"), 0o600)
		writeIdentities(t, dir)
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  error
	}{
		{"unreadable", func(t *testing.T, dir string) {
			if os.Geteuid() == 0 {
				t.Skip("root bypasses file permissions")
			}
			write(t, filepath.Join(dir, "installation-v5.json"), fixture(t, "v5-public-local-app.json"), 0o000)
		}, ErrPersistenceUnavailable},
		{"unsupported", func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "installation-v5.json"), []byte(`{"version":6,"portals":[],"alerts":[]}`), 0o600)
		}, ErrUnsupportedState},
		{"corrupt", func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "installation-v4.json"), []byte("not-json"), 0o600)
			validSource(t, dir)
		}, ErrUnsupportedState},
		{"unsafe symlink in an identity", func(t *testing.T, dir string) {
			validSource(t, dir)
			outside := filepath.Join(t.TempDir(), "outside")
			write(t, outside, []byte("secret"), 0o600)
			if err := os.Symlink(outside, filepath.Join(dir, "tsnet", hermesOriginal, "escape")); err != nil {
				t.Fatal(err)
			}
		}, ErrUnsupportedState},
		{"symlinked identity directory", func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "installation-v3.json"), fixture(t, "v3.json"), 0o600)
			elsewhere := t.TempDir()
			write(t, filepath.Join(elsewhere, "tailscaled.state"), []byte("node-key"), 0o600)
			write(t, filepath.Join(dir, "tsnet", "keep"), nil, 0o600)
			if err := os.Symlink(elsewhere, filepath.Join(dir, "tsnet", hermesID)); err != nil {
				t.Fatal(err)
			}
		}, ErrUnsupportedState},
		{"two identity directories for one Portal", func(t *testing.T, dir string) {
			validSource(t, dir)
			write(t, filepath.Join(dir, "tsnet", hermesID, "tailscaled.state"), []byte("other"), 0o600)
			if _, err := os.Stat(filepath.Join(dir, "tsnet", hermesID, "logs")); err == nil {
				t.Skip("case-insensitive filesystem cannot hold both names")
			}
		}, ErrUnsupportedState},
		{"group-writable source", func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "installation-v3.json"), fixture(t, "v3.json"), 0o660)
		}, ErrUnsupportedState},
		{"group-writable identity file", func(t *testing.T, dir string) {
			validSource(t, dir)
			if err := os.Chmod(filepath.Join(dir, "tsnet", hermesOriginal, "tailscaled.state"), 0o666); err != nil {
				t.Fatal(err)
			}
		}, ErrUnsupportedState},
		{"service directory linked into the originals", func(t *testing.T, dir string) {
			validSource(t, dir)
			write(t, filepath.Join(dir, "tsnet", hermesOriginal, "staging", "x"), []byte("original"), 0o600)
			if err := os.Symlink(filepath.Join("tsnet", hermesOriginal), filepath.Join(dir, serviceDir)); err != nil {
				t.Fatal(err)
			}
		}, ErrUnsupportedState},
		{"group-writable root", func(t *testing.T, dir string) {
			validSource(t, dir)
			if err := os.Chmod(dir, 0o770); err != nil {
				t.Fatal(err)
			}
		}, ErrUnsupportedState},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)
			before := originals(t, dir)
			if _, err := Open(dir, Options{}); !errors.Is(err, tc.want) {
				t.Fatalf("Open() error = %v, want %v", err, tc.want)
			}
			if after := originals(t, dir); !reflect.DeepEqual(after, before) {
				t.Fatalf("originals changed: %v -> %v", before, after)
			}
			assertGone(t, dir, manifestFile)
			assertGone(t, dir, generationsDir)
		})
	}

	for _, phase := range []Phase{PhaseCopy, PhaseWrite, PhaseFsync, PhaseBeforeManifest} {
		t.Run("fail before commit at "+string(phase), func(t *testing.T) {
			dir := t.TempDir()
			validSource(t, dir)
			before := originals(t, dir)
			if _, err := Open(dir, failAt(phase)); !errors.Is(err, ErrPersistenceUnavailable) || !errors.Is(err, errInjected) {
				t.Fatalf("Open() error = %v", err)
			}
			assertGone(t, dir, manifestFile)
			if entries, _ := os.ReadDir(filepath.Join(dir, stagingDir)); len(entries) != 0 {
				t.Fatalf("failed import left staging behind: %v", entries)
			}
			if after := originals(t, dir); !reflect.DeepEqual(after, before) {
				t.Fatalf("originals changed: %v -> %v", before, after)
			}
			s := mustOpen(t, dir)
			defer s.Close()
			assertGone(t, dir, stagingDir)
			if entries, _ := os.ReadDir(filepath.Join(dir, generationsDir)); len(entries) != 1 {
				t.Fatalf("generations = %v, want exactly one", entries)
			}
			if s.State().Migration.Source != "installation-v3.json" {
				t.Fatalf("state = %+v", s.State())
			}
		})
	}

	t.Run("crash leftovers in staging are discarded before the retry", func(t *testing.T) {
		dir := t.TempDir()
		validSource(t, dir)
		write(t, filepath.Join(dir, stagingDir, "0123456789abcdef0123456789abcdef", "generation", "state.json"), []byte("partial"), 0o600)
		s, err := Open(dir, Options{Hook: func(p Phase) error {
			if entries, _ := os.ReadDir(filepath.Join(dir, stagingDir)); p == PhaseCopy && len(entries) != 1 {
				return errors.New("stale staging survived until the copy")
			}
			return nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
	})

	t.Run("identity changed during the copy is not committed", func(t *testing.T) {
		dir := t.TempDir()
		validSource(t, dir)
		_, err := Open(dir, Options{Hook: func(p Phase) error {
			if p == PhaseCopy {
				write(t, filepath.Join(dir, "tsnet", hermesOriginal, "tailscaled.state"), []byte("rotated-key"), 0o600)
			}
			return nil
		}})
		if !errors.Is(err, ErrPersistenceUnavailable) || !strings.Contains(err.Error(), "changed while copying") {
			t.Fatalf("Open() error = %v", err)
		}
		assertGone(t, dir, manifestFile)
	})

	t.Run("crash after manifest rolls the committed generation forward", func(t *testing.T) {
		dir := t.TempDir()
		validSource(t, dir)
		if _, err := Open(dir, failAt(PhaseAfterManifest)); !errors.Is(err, errInjected) {
			t.Fatalf("Open() error = %v", err)
		}
		committed := readManifest(t, dir).Generation
		s := mustOpen(t, dir)
		defer s.Close()
		if readManifest(t, dir).Generation != committed || s.State().Migration.Source != "installation-v3.json" {
			t.Fatal("reopen did not use the committed generation")
		}
		assertGone(t, dir, stagingDir)
		if _, err := os.Stat(filepath.Join(dir, generationsDir, committed, "tsnet", hermesID, "tailscaled.state")); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("committed generation wins over later-edited originals", func(t *testing.T) {
		dir := t.TempDir()
		validSource(t, dir)
		mustOpen(t, dir).Close()
		write(t, filepath.Join(dir, "installation-v5.json"), fixture(t, "v5-public-local-app.json"), 0o600)
		s := mustOpen(t, dir)
		defer s.Close()
		if s.State().Migration.Source != "installation-v3.json" {
			t.Fatalf("state = %+v, want the committed v3 import", s.State())
		}
	})
}

func TestCorruptStateFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		damage func(t *testing.T, dir string, m manifest)
	}{
		{"corrupt state document", func(t *testing.T, dir string, m manifest) {
			write(t, filepath.Join(dir, generationsDir, m.Generation, stateFileName), []byte("not-json"), 0o600)
		}},
		{"unsupported state schema", func(t *testing.T, dir string, m manifest) {
			write(t, filepath.Join(dir, generationsDir, m.Generation, stateFileName), []byte(`{"schema":2,"revision":0,"installation":{},"migration":{}}`), 0o600)
		}},
		{"unknown state field", func(t *testing.T, dir string, m manifest) {
			name := filepath.Join(dir, generationsDir, m.Generation, stateFileName)
			data, _ := os.ReadFile(name)
			write(t, name, []byte(strings.Replace(string(data), `{"schema":1`, `{"future":true,"schema":1`, 1)), 0o600)
		}},
		{"missing state document", func(t *testing.T, dir string, m manifest) {
			os.Remove(filepath.Join(dir, generationsDir, m.Generation, stateFileName))
		}},
		{"corrupt manifest", func(t *testing.T, dir string, m manifest) {
			write(t, filepath.Join(dir, manifestFile), []byte("not-json"), 0o600)
		}},
		{"unsupported manifest schema", func(t *testing.T, dir string, m manifest) {
			write(t, filepath.Join(dir, manifestFile), []byte(`{"schema":2,"generation":"`+m.Generation+`","phase":"committed","sources":{}}`), 0o600)
		}},
		{"manifest naming a missing generation", func(t *testing.T, dir string, m manifest) {
			write(t, filepath.Join(dir, manifestFile), []byte(`{"schema":1,"generation":"00000000000000000000000000000000","phase":"committed","sources":{}}`), 0o600)
		}},
		{"generation without a manifest", func(t *testing.T, dir string, m manifest) {
			os.Remove(filepath.Join(dir, manifestFile))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, "installation-v3.json"), fixture(t, "v3.json"), 0o600)
			mustOpen(t, dir).Close()
			tc.damage(t, dir, readManifest(t, dir))
			before := snapshot(t, dir, nil)
			if _, err := Open(dir, Options{}); !errors.Is(err, ErrUnsupportedState) {
				t.Fatalf("Open() error = %v, want unsupportedState", err)
			}
			// No re-import and no empty fallback: nothing on disk changes.
			if after := snapshot(t, dir, nil); !reflect.DeepEqual(after, before) {
				t.Fatalf("root changed: %v -> %v", before, after)
			}
		})
	}
}

func TestInitialSaveFailure(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(dir, failAt(PhaseBeforeManifest)); !errors.Is(err, ErrPersistenceUnavailable) {
		t.Fatalf("Open() error = %v, want persistenceUnavailable", err)
	}
	assertGone(t, dir, manifestFile)

	s := mustOpen(t, dir)
	defer s.Close()
	want := State{Schema: stateSchema, Installation: installation.New()}
	if !reflect.DeepEqual(s.State(), want) {
		t.Fatalf("fresh state = %+v, want %+v", s.State(), want)
	}
}

func TestOwnershipBusy(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "installation-v3.json"), fixture(t, "v3.json"), 0o600)
	first := mustOpen(t, dir)
	before := snapshot(t, dir, nil)
	if _, err := Open(dir, Options{}); !errors.Is(err, ErrOwnershipBusy) {
		t.Fatalf("second Open() error = %v, want ownershipBusy", err)
	}
	if after := snapshot(t, dir, nil); !reflect.DeepEqual(after, before) {
		t.Fatalf("busy Open changed the root: %v -> %v", before, after)
	}
	first.Close()
	mustOpen(t, dir).Close()
}

func TestFreeSpaceCheckRejectsAnImpossibleCopy(t *testing.T) {
	file, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := checkFreeSpace(file, math.MaxInt64); !errors.Is(err, ErrPersistenceUnavailable) {
		t.Fatalf("checkFreeSpace() error = %v", err)
	}
}

// TestStateDocumentFormat pins the schema-v1 bytes: the document is decoded
// strictly, so an unnoticed field rename would make committed state unreadable.
func TestStateDocumentFormat(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "installation-v4.json"), fixture(t, "v4-remote-app.json"), 0o600)
	s := mustOpen(t, dir)
	defer s.Close()
	data, err := os.ReadFile(filepath.Join(dir, s.generation, stateFileName))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"schema":1,"revision":0,"installation":{"Binding":{"Name":"opaque-tailnet-id","MagicDNSSuffix":"one.ts.net"},"Portals":[{"ID":"9f55ca93-d7b3-4eab-a871-310ea576005a","Name":"hermes","Destination":{"kind":"remoteApp","scheme":"https","host":"app.example.com","port":8443},"PublicAccess":"private","CreatedAt":"2026-08-06T07:06:40Z","DesiredState":"stopped","Lifecycle":"pendingRemoval","RemovalAssignedName":"hermes-1"}],"Alerts":[{"ID":"1f93e456-69ec-445a-8374-d7fc5558d0c7","Kind":"crossTailnetRejection","PortalName":"hermes","AssignedName":"hermes-1","ExpectedMagicDNSSuffix":"one.ts.net","RejectedMagicDNSSuffix":"two.ts.net","CreatedAt":"2026-08-06T07:08:20Z"}],"Logging":"disabled","LaunchAtLoginOffer":"accepted"},"migration":{"source":"installation-v4.json","sourceVersion":4}}`
	if string(data) != want {
		t.Fatalf("state document = %s", data)
	}
}
