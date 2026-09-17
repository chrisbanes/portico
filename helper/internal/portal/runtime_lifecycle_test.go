package portal

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

func TestRuntimeDoesNotHoldRegistryDuringAnotherPortalStartup(t *testing.T) {
	blockedStart := make(chan struct{})
	releaseStart := make(chan struct{})
	factory := &lifecycleFactory{blockedStart: blockedStart, releaseStart: releaseStart}
	runtime := NewRuntime(t.TempDir(), factory.new)
	initial := Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled}
	if _, err := runtime.Reconcile(context.Background(), []Config{initial}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	slow := Config{ID: secondPortalID, Name: "atlas", Destination: localAppDestination(8788), DesiredState: DesiredStateEnabled}
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
	first := Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled}
	if _, err := runtime.Reconcile(context.Background(), []Config{first}, func(Event) {}); err != nil {
		t.Fatalf("initial Reconcile: %v", err)
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- runtime.Close(context.Background()) }()
	waitForSignal(t, initial.closeEntered, "Runtime.Close snapshot")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runtime.Reconcile(cancelled, []Config{{
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
		_, err := runtime.Reconcile(context.Background(), []Config{{
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
