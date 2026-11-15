package servicehost

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/chrisbanes/portico/helper/internal/ipc"
)

func TestParseVariant(t *testing.T) {
	for name, want := range map[string]ipc.Variant{"production": ipc.Production, "dev": ipc.Dev, "test": ipc.Test} {
		if got, err := ParseVariant(name); err != nil || got != want {
			t.Errorf("ParseVariant(%q) = %q, %v", name, got, err)
		}
	}
	for _, name := range []string{"", "Production", "portico", "staging"} {
		if _, err := ParseVariant(name); !errors.Is(err, ErrInvalidRoots) {
			t.Errorf("ParseVariant(%q) error = %v", name, err)
		}
	}
}

func TestDefaultStateRoots(t *testing.T) {
	for _, tc := range []struct {
		goos    string
		variant ipc.Variant
		xdg     string
		want    string
	}{
		{"darwin", ipc.Production, "", "/Users/u/Library/Application Support/Portico"},
		{"darwin", ipc.Dev, "", "/Users/u/Library/Application Support/Portico Dev"},
		// macOS ignores XDG entirely.
		{"darwin", ipc.Production, "/x", "/Users/u/Library/Application Support/Portico"},
		{"linux", ipc.Production, "", "/Users/u/.local/state/portico"},
		{"linux", ipc.Dev, "", "/Users/u/.local/state/portico-dev"},
		{"linux", ipc.Production, "/state", "/state/portico"},
		{"linux", ipc.Dev, "/state", "/state/portico-dev"},
	} {
		if got, err := defaultStateRoot(tc.goos, tc.variant, "/Users/u", tc.xdg); err != nil || got != tc.want {
			t.Errorf("defaultStateRoot(%s, %s, %q) = %q, %v; want %q", tc.goos, tc.variant, tc.xdg, got, err, tc.want)
		}
	}
	if _, err := defaultStateRoot("linux", ipc.Production, "/Users/u", "relative/state"); !errors.Is(err, ErrInvalidRoots) {
		t.Errorf("relative XDG_STATE_HOME: error = %v", err)
	}
	if _, err := defaultStateRoot("linux", ipc.Production, "", ""); !errors.Is(err, ErrInvalidRoots) {
		t.Errorf("no home: error = %v", err)
	}
}

// The macOS production and Dev roots may still have an older app writing
// them, so until T5's guard the host must refuse them before touching
// anything.
func TestMacOSProductionAndDevAreRefused(t *testing.T) {
	for _, tc := range []struct {
		goos    string
		variant ipc.Variant
		want    error
	}{
		{"darwin", ipc.Production, ErrUnguardedVariant},
		{"darwin", ipc.Dev, ErrUnguardedVariant},
		{"darwin", ipc.Test, nil},
		{"linux", ipc.Production, nil},
		{"linux", ipc.Dev, nil},
	} {
		if err := refuseUnguarded(tc.goos, tc.variant); !errors.Is(err, tc.want) {
			t.Errorf("refuseUnguarded(%s, %s) = %v, want %v", tc.goos, tc.variant, err, tc.want)
		}
	}
	// Resolve refuses first. The path flags also make production invalid, so
	// even without the refusal this never looks up the real roots.
	want := ErrInvalidRoots
	if runtime.GOOS == "darwin" {
		want = ErrUnguardedVariant
	}
	for _, variant := range []ipc.Variant{ipc.Production, ipc.Dev} {
		if _, err := Resolve(variant, t.TempDir(), shortTemp(t)); !errors.Is(err, want) {
			t.Errorf("Resolve(%s) = %v, want %v", variant, err, want)
		}
	}
}

func TestResolveTestVariant(t *testing.T) {
	base := shortTemp(t)
	root := filepath.Join(t.TempDir(), "missing", "state")
	roots, err := Resolve(ipc.Test, root, base)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(root)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("created root: %v, %v", info.Mode(), err)
	}
	if want, _ := filepath.EvalSymlinks(root); roots.StateRoot != want {
		t.Fatalf("StateRoot = %q, want %q", roots.StateRoot, want)
	}

	// A looser root this user owns is tightened, as the app does.
	loose := t.TempDir()
	os.Chmod(loose, 0o755)
	if _, err := Resolve(ipc.Test, loose, base); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Lstat(loose); info.Mode().Perm() != 0o700 {
		t.Fatalf("loose root mode = %v", info.Mode())
	}

	for _, tc := range []struct{ name, root, runtimeDir string }{
		{"no state root", "", base},
		{"no runtime dir", t.TempDir(), ""},
		{"relative state root", "state", base},
		{"relative runtime dir", t.TempDir(), "run"},
	} {
		if _, err := Resolve(ipc.Test, tc.root, tc.runtimeDir); !errors.Is(err, ErrInvalidRoots) {
			t.Errorf("%s: error = %v", tc.name, err)
		}
	}
	if _, err := Resolve("staging", t.TempDir(), base); !errors.Is(err, ErrInvalidRoots) {
		t.Errorf("unknown variant: error = %v", err)
	}
}

func TestResolveRejectsUnsafeRoots(t *testing.T) {
	base := shortTemp(t)
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, root := range map[string]string{"symlinked root": link, "file root": file, "root under a file": filepath.Join(file, "state")} {
		if _, err := Resolve(ipc.Test, root, base); !errors.Is(err, ErrInvalidRoots) {
			t.Errorf("%s: error = %v", name, err)
		}
	}

	// A runtime directory others can write lets them swap the socket
	// directory; nothing is created when it is refused.
	shared := shortTemp(t)
	if err := os.Chmod(shared, 0o777); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "state")
	if _, err := Resolve(ipc.Test, root, shared); !errors.Is(err, ErrInvalidRoots) {
		t.Errorf("shared runtime dir: error = %v", err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Errorf("a refused runtime dir still created the state root: %v", err)
	}

	// A socket directory that is not this user's private directory.
	for name, prepare := range map[string]func(dir string) error{
		"group-readable socket dir": func(dir string) error { return os.Mkdir(dir, 0o750) },
		"symlinked socket dir":      func(dir string) error { return os.Symlink(t.TempDir(), dir) },
	} {
		base := shortTemp(t)
		if err := prepare(filepath.Join(base, string(ipc.Test))); err != nil {
			t.Fatal(err)
		}
		roots, err := Resolve(ipc.Test, t.TempDir(), base)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ipc.Listen(roots.Endpoint, lockFile(t), ipc.DefaultLimits); !errors.Is(err, ipc.ErrUnsafeEndpoint) {
			t.Errorf("%s: Listen() = %v, want unsafe endpoint", name, err)
		}
	}
}

// XDG_RUNTIME_DIR locates the Linux production and Dev endpoints, so a
// missing, relative or shared one is refused before any state root exists.
func TestResolveRejectsUnsafeXDGRuntimeDir(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG_RUNTIME_DIR locates the endpoint on Linux only")
	}
	shared := shortTemp(t)
	if err := os.Chmod(shared, 0o777); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"unset": "", "relative": "run/user", "shared": shared} {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		t.Setenv("XDG_RUNTIME_DIR", value)
		if _, err := Resolve(ipc.Production, "", ""); !errors.Is(err, ErrInvalidRoots) {
			t.Errorf("%s XDG_RUNTIME_DIR: error = %v", name, err)
		}
		if _, err := os.Lstat(filepath.Join(state, "portico")); !os.IsNotExist(err) {
			t.Errorf("%s XDG_RUNTIME_DIR: the state root was created", name)
		}
	}
}

func lockFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(t.TempDir(), "service-owner.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}
