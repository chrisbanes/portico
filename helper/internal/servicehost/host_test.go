package servicehost

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/chrisbanes/portico/helper/internal/installation"
	"github.com/chrisbanes/portico/helper/internal/ipc"
	"github.com/chrisbanes/portico/helper/internal/management"
	"github.com/chrisbanes/portico/helper/internal/portal"
	"github.com/chrisbanes/portico/helper/internal/service"
)

// runtimes counts the runtimes a host builds; each one's Close waits for
// release, so tests can hold the engine mid-shutdown.
type runtimes struct {
	mu      sync.Mutex
	built   int
	closing chan struct{} // closed when a runtime's Close starts
	release chan struct{} // closing it lets Close return
}

func newRuntimes() *runtimes {
	r := &runtimes{closing: make(chan struct{}), release: make(chan struct{})}
	close(r.release)
	return r
}

func (r *runtimes) factory(string, installation.Logging) (service.Runtime, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.built++
	return fakeRuntime{r}, nil
}

func (r *runtimes) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.built
}

type fakeRuntime struct{ r *runtimes }

func (fakeRuntime) Reconcile(_ context.Context, configs []portal.Config, _ func(portal.Event)) ([]portal.ReconcileEntry, error) {
	var entries []portal.ReconcileEntry
	for _, c := range configs {
		entries = append(entries, portal.ReconcileEntry{PortalID: c.ID, Outcome: portal.OutcomeConverged})
	}
	return entries, nil
}
func (fakeRuntime) AuthenticateAttempt(context.Context, string, string) error { return nil }
func (fakeRuntime) CleanupRejectedPortal(context.Context, string) error       { return nil }
func (fakeRuntime) RemovePortal(context.Context, string) error                { return nil }
func (f fakeRuntime) Close(context.Context) error {
	f.r.mu.Lock()
	select {
	case <-f.r.closing:
	default:
		close(f.r.closing)
	}
	f.r.mu.Unlock()
	<-f.r.release
	return nil
}

// testRoots is an isolated test-variant root, seeded with seed if set.
func testRoots(t *testing.T, seed []byte) Roots {
	t.Helper()
	roots, _ := testRootsIn(t, seed)
	return roots
}

// testRootsIn also returns the management socket's path.
func testRootsIn(t *testing.T, seed []byte) (Roots, string) {
	t.Helper()
	root := t.TempDir()
	if seed != nil {
		if err := os.WriteFile(filepath.Join(root, "installation-v5.json"), seed, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base := shortTemp(t)
	roots, err := Resolve(ipc.Test, root, base)
	if err != nil {
		t.Fatal(err)
	}
	return roots, filepath.Join(base, string(ipc.Test), "management.sock")
}

// shortTemp is a directory short enough for sun_path, unlike t.TempDir.
func shortTemp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func fixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../installation/testdata/v5-public-local-app.json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type host struct {
	cancel context.CancelFunc
	done   chan error
}

func runHost(t *testing.T, roots Roots, r *runtimes) *host {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h := &host{cancel: cancel, done: make(chan error, 1)}
	cfg := Config{Roots: roots, Engine: service.EngineOptions{RuntimeFactory: r.factory}, Limits: ipc.DefaultLimits}
	go func() { h.done <- Run(ctx, cfg) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.done:
		case <-time.After(15 * time.Second):
			t.Error("host did not stop")
		}
	})
	return h
}

func (h *host) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-h.done:
		h.done <- err // for Cleanup
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("host did not stop")
		return nil
	}
}

func (h *host) running() bool {
	select {
	case err := <-h.done:
		h.done <- err
		return false
	default:
		return true
	}
}

func dial(t *testing.T, endpoint ipc.Endpoint) *management.Client {
	t.Helper()
	var c *management.Client
	waitFor(t, "the endpoint", func() bool {
		var err error
		c, err = management.Dial(t.Context(), endpoint)
		return err == nil
	})
	t.Cleanup(func() { c.Close() })
	return c
}

func getSnapshot(t *testing.T, c *management.Client) service.Snapshot {
	t.Helper()
	var s service.Snapshot
	if err := c.Call(t.Context(), "getSnapshot", nil, nil, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func wantBusy(t *testing.T, root, when string) {
	t.Helper()
	owner, err := service.Acquire(root)
	if err == nil {
		owner.Close()
	}
	if !errors.Is(err, service.ErrOwnershipBusy) {
		t.Fatalf("a contender %s: Acquire() = %v, want ownershipBusy", when, err)
	}
}

func wantFree(t *testing.T, root, when string) {
	t.Helper()
	owner, err := service.Acquire(root)
	if err != nil {
		t.Fatalf("a contender %s: Acquire() = %v, want the lock released", when, err)
	}
	owner.Close()
}

// tree maps every entry under dir to its mode and contents.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data := ""
		if info.Mode().IsRegular() {
			// An unreadable file is compared by mode alone.
			b, err := os.ReadFile(p)
			if err != nil && !errors.Is(err, fs.ErrPermission) {
				return err
			}
			data = string(b)
		}
		entries[p] = info.Mode().String() + " " + data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestStartupFailureIsServedWithTheLockHeld(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   os.FileMode
		reason service.Code
	}{
		{"unsupported state", 0o600, service.CodeUnsupportedState},
		{"unreadable state", 0o000, service.CodePersistenceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			roots := testRoots(t, []byte(`{"version":6}`))
			if err := os.Chmod(filepath.Join(roots.StateRoot, "installation-v5.json"), tc.mode); err != nil {
				t.Fatal(err)
			}
			before := tree(t, roots.StateRoot)
			r := newRuntimes()
			h := runHost(t, roots, r)
			c := dial(t, roots.Endpoint)

			if s := getSnapshot(t, c); s.Service.StartupFailure != tc.reason || len(s.Portals) != 0 {
				t.Fatalf("snapshot service = %+v, %d portals", s.Service, len(s.Portals))
			}
			err := c.Call(t.Context(), "setOperationalLogging",
				&management.Mutation{RequestID: "22222222-2222-4222-8222-222222222222", IssuedAt: time.Now()},
				service.SetOperationalLoggingParams{Logging: installation.LoggingEnabled}, nil)
			var e *management.Error
			if !errors.As(err, &e) || string(e.Code) != string(tc.reason) {
				t.Fatalf("mutation error = %v, want %s", err, tc.reason)
			}
			// The lock stays held while the failure is served.
			wantBusy(t, roots.StateRoot, "while a startup failure is served")

			h.cancel()
			var failure *StartupFailure
			if err := h.wait(t); !errors.As(err, &failure) || failure.Reason != tc.reason {
				t.Fatalf("Run() = %v, want StartupFailure(%s)", err, tc.reason)
			}
			wantFree(t, roots.StateRoot, "after the failed host stops")
			if r.count() != 0 {
				t.Fatalf("%d runtimes built", r.count())
			}
			// No empty install: only the lock file is new.
			after := tree(t, roots.StateRoot)
			delete(after, filepath.Join(roots.StateRoot, "service-owner.lock"))
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("state root changed: %v -> %v", before, after)
			}
		})
	}
}

func TestOwnershipBusyLeavesTheLivePeerIntact(t *testing.T) {
	roots, socket := testRootsIn(t, fixture(t))
	runHost(t, roots, newRuntimes())
	peer := dial(t, roots.Endpoint)
	before, err := os.Lstat(socket)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = Run(ctx, Config{Roots: roots, Engine: service.EngineOptions{RuntimeFactory: newRuntimes().factory}, Limits: ipc.DefaultLimits})
	if !errors.Is(err, service.ErrOwnershipBusy) {
		t.Fatalf("second Run() = %v, want ownershipBusy", err)
	}
	after, err := os.Lstat(socket)
	if err != nil || !os.SameFile(before, after) || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("the live socket was replaced: %v", err)
	}
	getSnapshot(t, peer)
	dial(t, roots.Endpoint)
}

func TestShutdownReleasesTheLockOnlyAfterTheEngineCloses(t *testing.T) {
	roots := testRoots(t, fixture(t))
	r := newRuntimes()
	r.release = make(chan struct{})
	h := runHost(t, roots, r)
	dial(t, roots.Endpoint)
	waitFor(t, "the runtime", func() bool { return r.count() == 1 })

	h.cancel()
	<-r.closing
	// Accepting has stopped, but the engine is still closing: the lock holds.
	if _, err := ipc.Dial(t.Context(), roots.Endpoint); !errors.Is(err, ipc.ErrNotRunning) {
		t.Fatalf("Dial() while closing = %v, want notRunning", err)
	}
	wantBusy(t, roots.StateRoot, "while Engine.Close runs")
	if !h.running() {
		t.Fatal("Run returned before Engine.Close")
	}
	close(r.release)
	if err := h.wait(t); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	wantFree(t, roots.StateRoot, "after Run returns")
}

func TestFailedCloseKeepsTheLock(t *testing.T) {
	defer func(d time.Duration) { closeTimeout = d }(closeTimeout)
	closeTimeout = 50 * time.Millisecond
	roots := testRoots(t, fixture(t))
	r := newRuntimes()
	r.release = make(chan struct{})
	defer close(r.release)
	h := runHost(t, roots, r)
	dial(t, roots.Endpoint)
	waitFor(t, "the runtime", func() bool { return r.count() == 1 })

	h.cancel()
	if err := h.wait(t); err == nil {
		t.Fatal("Run() = nil after a failed close, want an error")
	}
	// Replacement is unsafe until the nodes are proven closed, so the lock
	// stays held until the process exits.
	wantBusy(t, roots.StateRoot, "after a failed close")
}
