package servicehost

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/chrisbanes/portico/helper/internal/installation"
	"github.com/chrisbanes/portico/helper/internal/management"
	"github.com/chrisbanes/portico/helper/internal/service"
)

var guardTargets = []string{"installation-v5.json", "installation-v4.json"}

// guardedRoots are test roots that require the guard; their uchg flags
// are cleared before the temporary directory is removed.
func guardedRoots(t *testing.T) Roots {
	t.Helper()
	roots := testRoots(t, fixture(t))
	roots.LegacyGuard = true
	t.Cleanup(func() {
		filepath.WalkDir(roots.StateRoot, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				unix.Chflags(p, 0)
			}
			return nil
		})
	})
	return roots
}

func flagged(t *testing.T, name string) bool {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Lstat(name, &st); err != nil {
		t.Fatal(err)
	}
	return st.Flags&unix.UF_IMMUTABLE != 0
}

// After the commit every start requires the guard: without it the host
// serves a startup failure, builds no runtime and does not guard again.
func TestMissingLegacyGuardIsAStartupFailure(t *testing.T) {
	roots := guardedRoots(t)
	r := newRuntimes()
	h := runHost(t, roots, r)
	c := dial(t, roots.Endpoint)
	if s := getSnapshot(t, c); s.Service.StartupFailure != "" || len(s.Portals) != 1 {
		t.Fatalf("guarded import: service %+v, %d portals", s.Service, len(s.Portals))
	}
	for _, name := range guardTargets {
		if !flagged(t, filepath.Join(roots.StateRoot, name)) {
			t.Fatalf("%s is not guarded", name)
		}
	}
	h.cancel()
	if err := h.wait(t); err != nil {
		t.Fatal(err)
	}
	built := r.count()

	// A legacy-style edit: the flag cleared and the record replaced.
	v5 := filepath.Join(roots.StateRoot, "installation-v5.json")
	if err := unix.Chflags(v5, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v5, fixture(t), 0o600); err != nil {
		t.Fatal(err)
	}
	h = runHost(t, roots, r)
	c = dial(t, roots.Endpoint)
	if s := getSnapshot(t, c); s.Service.StartupFailure != service.CodeUnsupportedState || len(s.Portals) != 0 {
		t.Fatalf("snapshot service = %+v, %d portals", s.Service, len(s.Portals))
	}
	err := c.Call(t.Context(), "setOperationalLogging",
		&management.Mutation{RequestID: "33333333-3333-4333-8333-333333333333", IssuedAt: time.Now()},
		service.SetOperationalLoggingParams{Logging: installation.LoggingEnabled}, nil)
	var e *management.Error
	if !errors.As(err, &e) || string(e.Code) != string(service.CodeUnsupportedState) {
		t.Fatalf("mutation error = %v, want unsupportedState", err)
	}
	h.cancel()
	var failure *StartupFailure
	if err := h.wait(t); !errors.As(err, &failure) || failure.Reason != service.CodeUnsupportedState {
		t.Fatalf("Run() = %v, want StartupFailure(unsupportedState)", err)
	}
	if r.count() != built {
		t.Fatalf("a runtime was built without the guard")
	}
	if flagged(t, v5) {
		t.Fatal("the host guarded the root again")
	}
	if got := Reason(service.ErrMigrationGuardMissing); got != "migrationGuardMissing" {
		t.Fatalf("Reason = %q", got)
	}
}
