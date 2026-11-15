package portal

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRuntimeDoesNotHoldRegistryDuringAnotherPortalStartup(t *testing.T) {
	blockedStart := make(chan struct{})
	releaseStart := make(chan struct{})
	factory := &lifecycleFactory{blockedStart: blockedStart, releaseStart: releaseStart}
	runtime := NewRuntime(t.TempDir(), factory.new)
	initial := Config{PublicAccess: PublicAccessModePrivate, ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled}
	if _, err := runtime.Reconcile(context.Background(), []Config{initial}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	slow := Config{PublicAccess: PublicAccessModePrivate, ID: secondPortalID, Name: "atlas", Destination: localAppDestination(8788), DesiredState: DesiredStateEnabled}
	done := make(chan struct{})
	go func() {
		_, _ = runtime.Reconcile(context.Background(), []Config{initial, slow}, func(Event) {})
		close(done)
	}()
	select {
	case <-blockedStart:
	case <-time.After(time.Second):
		t.Fatal("slow Portal startup did not begin")
	}
	authenticated := make(chan error, 1)
	go func() { authenticated <- runtime.Authenticate(context.Background(), initial.ID) }()
	select {
	case err := <-authenticated:
		if err != nil {
			t.Fatalf("Authenticate = %v, want existing Portal available", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("slow Portal startup held the Runtime registry")
	}
	close(releaseStart)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reconciliation did not finish after startup release")
	}
	_ = runtime.Close(context.Background())
}

func TestRuntimeDoesNotAdmitPortalsAfterCancellationOrCloseSnapshot(t *testing.T) {
	initial := &fakeNode{
		watcher:      newFakeWatcher(),
		status:       Status{BackendState: "Starting"},
		closeEntered: make(chan struct{}),
		releaseClose: make(chan struct{}),
	}
	created := make(chan string, 2)
	var factoryCalls int
	var factoryMu sync.Mutex
	runtime := NewRuntime(t.TempDir(), func(_, name string) Node {
		factoryMu.Lock()
		factoryCalls++
		call := factoryCalls
		factoryMu.Unlock()
		if call == 1 {
			return initial
		}
		created <- name
		return &fakeNode{watcher: newFakeWatcher(), status: Status{BackendState: "Starting"}}
	})
	first := Config{PublicAccess: PublicAccessModePrivate, ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled}
	if _, err := runtime.Reconcile(context.Background(), []Config{first}, func(Event) {}); err != nil {
		t.Fatalf("initial Reconcile: %v", err)
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- runtime.Close(context.Background()) }()
	waitForSignal(t, initial.closeEntered, "Runtime.Close snapshot")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runtime.Reconcile(cancelled, []Config{{PublicAccess: PublicAccessModePrivate,
		ID: secondPortalID, Name: "atlas", Destination: localAppDestination(8788), DesiredState: DesiredStateEnabled,
	}}, func(Event) {}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled Reconcile = %v, want context cancellation", err)
	}
	select {
	case name := <-created:
		t.Errorf("cancelled Reconcile started %q after root cancellation", name)
	default:
	}

	liveReconcile := make(chan error, 1)
	go func() {
		_, err := runtime.Reconcile(context.Background(), []Config{{PublicAccess: PublicAccessModePrivate,
			ID: "7ea74329-3144-4ba2-925f-138d14d61fcc", Name: "selina", Destination: localAppDestination(8789), DesiredState: DesiredStateEnabled,
		}}, func(Event) {})
		liveReconcile <- err
	}()
	select {
	case name := <-created:
		t.Errorf("Reconcile started %q after Runtime.Close snapped the registry", name)
	case err := <-liveReconcile:
		if err == nil {
			t.Error("Reconcile succeeded after Runtime.Close snapped the registry")
		}
	}

	close(initial.releaseClose)
	if err := <-closeDone; err != nil {
		t.Fatalf("Runtime.Close: %v", err)
	}
	select {
	case err := <-liveReconcile:
		if err == nil {
			t.Error("Reconcile succeeded after Runtime.Close snapped the registry")
		}
	default:
	}
	_ = runtime.Close(context.Background())
}

type lifecycleFactory struct {
	mu           sync.Mutex
	created      int
	blockedStart chan struct{}
	releaseStart chan struct{}
}

func (f *lifecycleFactory) new(_, _ string) Node {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created++
	return &lifecycleNode{block: f.created == 2, blockedStart: f.blockedStart, releaseStart: f.releaseStart, watcher: newFakeWatcher()}
}

type lifecycleNode struct {
	block        bool
	blockedStart chan struct{}
	releaseStart chan struct{}
	watcher      *fakeWatcher
}

func (n *lifecycleNode) Start() error {
	if n.block {
		close(n.blockedStart)
		<-n.releaseStart
	}
	return nil
}
func (*lifecycleNode) Up(context.Context) (Status, error)          { return lifecycleStatus(), nil }
func (*lifecycleNode) Status(context.Context) (Status, error)      { return lifecycleStatus(), nil }
func (n *lifecycleNode) Watch(context.Context) (Watcher, error)    { return n.watcher, nil }
func (*lifecycleNode) StartLoginInteractive(context.Context) error { return nil }
func (*lifecycleNode) Listen(string, string) (net.Listener, error) {
	return nil, nil
}
func (*lifecycleNode) TLSConfig() *tls.Config { return nil }
func (*lifecycleNode) Close() error           { return nil }

func lifecycleStatus() Status { return Status{BackendState: "Running"} }

// hungCloseFactory starts a fresh recording node per call; the first node
// for hungID blocks in Close, or in its watcher's Close when hangWatcher is
// set, until release closes.
type hungCloseFactory struct {
	mu          sync.Mutex
	hungID      string
	hangWatcher bool
	release     chan struct{}
	started     map[string]int
	nodes       []*fakeNode
}

func newHungCloseFactory(hungID string) *hungCloseFactory {
	return &hungCloseFactory{hungID: hungID, release: make(chan struct{}), started: map[string]int{}}
}

func (f *hungCloseFactory) new(dir, _ string) Node {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := filepath.Base(dir)
	f.started[id]++
	node := &fakeNode{watcher: newFakeWatcher(), status: Status{BackendState: "Starting"}}
	f.nodes = append(f.nodes, node)
	if id == f.hungID && f.started[id] == 1 {
		if f.hangWatcher {
			watcher := newBlockedCloseWatcher()
			watcher.releaseClose = f.release
			node.watcherOverride = watcher
			return node
		}
		return &hungCloseNode{fakeNode: node, release: f.release}
	}
	return node
}

// hungCloseNode blocks in its first Close until release closes and, like
// tsnet, fails every later Close.
type hungCloseNode struct {
	*fakeNode
	release chan struct{}
	once    sync.Once
}

func (n *hungCloseNode) Close() error {
	err := net.ErrClosed
	n.once.Do(func() {
		<-n.release
		err = n.fakeNode.Close()
	})
	return err
}

func (f *hungCloseFactory) count(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started[id]
}

func reconcileWithin(t *testing.T, runtime *Runtime, deadline time.Duration, configs ...Config) map[string]ReconcileOutcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	started := time.Now()
	entries, err := runtime.Reconcile(ctx, configs, func(Event) {})
	if elapsed := time.Since(started); elapsed > deadline+time.Second {
		t.Fatalf("Reconcile took %v, past its %v deadline", elapsed, deadline)
	}
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	outcomes := map[string]ReconcileOutcome{}
	for _, entry := range entries {
		outcomes[entry.PortalID] = entry.Outcome
	}
	return outcomes
}

func enabledConfig(id, name string, port uint16) Config {
	return Config{PublicAccess: PublicAccessModePrivate, ID: id, Name: name, Destination: localAppDestination(port), DesiredState: DesiredStateEnabled}
}

// TestReconcileDeadlineBoundsHungNodeClose: a Node.Close that blocks during a
// stop or a removal ends that Reconcile at its deadline with closeFailed, and
// no new node starts for the UUID until the close completes.
func TestReconcileDeadlineBoundsHungNodeClose(t *testing.T) {
	for _, removal := range []bool{false, true} {
		factory := newHungCloseFactory(testPortalID)
		runtime := NewRuntime(t.TempDir(), factory.new)
		enabled := enabledConfig(testPortalID, "hermes", 8787)
		if got := reconcileWithin(t, runtime, time.Second, enabled); got[testPortalID] != OutcomeConverged {
			t.Fatalf("start = %v", got)
		}
		var configs []Config
		if !removal {
			stopped := enabled
			stopped.DesiredState = DesiredStateStopped
			configs = []Config{stopped}
		}
		if got := reconcileWithin(t, runtime, 50*time.Millisecond, configs...); got[testPortalID] != OutcomeCloseFailed {
			t.Fatalf("hung close (removal=%v) = %v, want closeFailed", removal, got)
		}
		if got := reconcileWithin(t, runtime, 50*time.Millisecond, enabled); got[testPortalID] != OutcomeStartFailed || factory.count(testPortalID) != 1 {
			t.Fatalf("restart before the close completed = %v with %d nodes, want startFailed and 1", got, factory.count(testPortalID))
		}
		close(factory.release)
		// The late close hands the gate back once it completes.
		portal := runtime.portal(testPortalID)
		if err := portal.acquire(context.Background()); err != nil {
			t.Fatal(err)
		}
		portal.release()
		if got := reconcileWithin(t, runtime, time.Second, enabled); got[testPortalID] != OutcomeConverged || factory.count(testPortalID) != 2 {
			t.Fatalf("restart after the close completed = %v with %d nodes", got, factory.count(testPortalID))
		}
		if err := runtime.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
}

// TestHungPortalDoesNotStopAnotherPortalConverging: while Portal A's node or
// watcher close hangs, Portal B, sorted after it, converges on its next pass.
func TestHungPortalDoesNotStopAnotherPortalConverging(t *testing.T) {
	for _, hangWatcher := range []bool{false, true} {
		hung, other := secondPortalID, testPortalID // A sorts first
		factory := newHungCloseFactory(hung)
		factory.hangWatcher = hangWatcher
		runtime := NewRuntime(t.TempDir(), factory.new)
		t.Cleanup(func() { _ = runtime.Close(context.Background()) })
		t.Cleanup(func() { close(factory.release) })
		a := enabledConfig(hung, "atlas", 8788)
		if got := reconcileWithin(t, runtime, time.Second, a); got[hung] != OutcomeConverged {
			t.Fatalf("start A = %v", got)
		}
		a.DesiredState = DesiredStateStopped
		if got := reconcileWithin(t, runtime, 50*time.Millisecond, a); got[hung] != OutcomeCloseFailed {
			t.Fatalf("hung close of A (hangWatcher=%v) = %v", hangWatcher, got)
		}
		b := enabledConfig(other, "hermes", 8787)
		got := reconcileWithin(t, runtime, 200*time.Millisecond, a, b)
		if got[other] != OutcomeConverged || got[hung] != OutcomeCloseFailed {
			t.Fatalf("next pass = %v, want B converged while A's close is unproven", got)
		}
		// Re-enabling A is skipped too, so a third Portal still converges.
		a.DesiredState = DesiredStateEnabled
		third := enabledConfig("7ea74329-3144-4ba2-925f-138d14d61fcc", "selina", 8789)
		got = reconcileWithin(t, runtime, 200*time.Millisecond, a, b, third)
		if got[third.ID] != OutcomeConverged || got[hung] != OutcomeStartFailed || factory.count(hung) != 1 {
			t.Fatalf("re-enable pass = %v with %d nodes for A, want the third Portal converged", got, factory.count(hung))
		}
	}
}

// TestRuntimeCloseClosesNodesThenEndsLifetime: Close closes every node before
// cancelling the lifetime, a second Close is a no-op, and a Reconcile racing
// Close leaves no Portal running.
func TestRuntimeCloseClosesNodesThenEndsLifetime(t *testing.T) {
	var runtime *Runtime
	var lifetimeEndedFirst bool
	node := &fakeNode{watcher: newFakeWatcher(), status: Status{BackendState: "Starting"}}
	node.observeClose = func() { lifetimeEndedFirst = runtime.lifetime.Err() != nil }
	runtime = NewRuntime(t.TempDir(), func(_, _ string) Node { return node })
	if got := reconcileWithin(t, runtime, time.Second, enabledConfig(testPortalID, "hermes", 8787)); got[testPortalID] != OutcomeConverged {
		t.Fatalf("start = %v", got)
	}
	for range 2 {
		if err := runtime.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	if !node.closed || lifetimeEndedFirst || runtime.lifetime.Err() == nil {
		t.Fatalf("node closed %v, lifetime ended before node close %v, lifetime ended %v", node.closed, lifetimeEndedFirst, runtime.lifetime.Err() != nil)
	}

	for range 20 {
		factory := newHungCloseFactory("")
		runtime := NewRuntime(t.TempDir(), factory.new)
		reconciled := make(chan struct{})
		go func() {
			defer close(reconciled)
			_, _ = runtime.Reconcile(context.Background(), []Config{enabledConfig(testPortalID, "hermes", 8787), enabledConfig(secondPortalID, "atlas", 8788)}, func(Event) {})
		}()
		if err := runtime.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
		<-reconciled
		factory.mu.Lock()
		for _, node := range factory.nodes {
			node.mu.Lock()
			if !node.closed || node.watchContext != nil && node.watchContext.Err() == nil {
				t.Errorf("a Portal started by a racing Reconcile outlived Close")
			}
			node.mu.Unlock()
		}
		factory.mu.Unlock()
	}
}

// TestCloseRetryDoesNotCloseAClosedNodeAgain: after a close fails on the
// listener but closes the node, a retry finishes without a second
// Node.Close, which tsnet would fail.
func TestCloseRetryDoesNotCloseAClosedNodeAgain(t *testing.T) {
	factory := newFakeFactory()
	factory.status = Status{BackendState: "Running", DNSName: "hermes.example.ts.net.", CertDomains: []string{"hermes.example.ts.net"}}
	runtime := NewRuntime(t.TempDir(), factory.New)
	if err := reconcileOne(runtime, enabledConfig(testPortalID, "hermes", 8787), func(Event) {}); err != nil {
		t.Fatal(err)
	}
	listener := factory.node.listener
	setCloseErr := func(err error) {
		listener.mu.Lock()
		listener.closeErr = err
		listener.mu.Unlock()
	}
	setCloseErr(errors.New("close failed"))
	if err := runtime.Close(context.Background()); err == nil {
		t.Fatal("Close = nil, want the listener failure")
	}
	setCloseErr(nil)
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatalf("Close retry: %v", err)
	}
	if calls := factory.node.closeCalls; calls != 1 {
		t.Fatalf("Node.Close calls = %d, want 1", calls)
	}
}
