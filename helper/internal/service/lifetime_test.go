package service

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"testing"

	"github.com/chrisbanes/portico/helper/internal/installation"
	"github.com/chrisbanes/portico/helper/internal/portal"
)

// lifetimeNode is a portal.Node that records the context portal.Runtime
// gives its watcher, so the test checks the real runtime's lifetime rules.
type lifetimeNode struct {
	mu       sync.Mutex
	watchCtx context.Context
}

func (n *lifetimeNode) Start() error { return nil }
func (n *lifetimeNode) Up(ctx context.Context) (portal.Status, error) {
	return portal.Status{}, errors.New("not online")
}
func (n *lifetimeNode) Status(context.Context) (portal.Status, error) {
	return portal.Status{BackendState: "NeedsLogin"}, nil
}
func (n *lifetimeNode) Watch(ctx context.Context) (portal.Watcher, error) {
	n.mu.Lock()
	n.watchCtx = ctx
	n.mu.Unlock()
	return lifetimeWatcher{ctx: ctx}, nil
}
func (n *lifetimeNode) StartLoginInteractive(context.Context) error { return nil }
func (n *lifetimeNode) Listen(string, string) (net.Listener, error) {
	return nil, errors.New("not listening")
}
func (n *lifetimeNode) TLSConfig() *tls.Config { return nil }
func (n *lifetimeNode) Close() error           { return nil }

func (n *lifetimeNode) watching() context.Context {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.watchCtx
}

type lifetimeWatcher struct{ ctx context.Context }

func (w lifetimeWatcher) Next() (portal.Notification, error) {
	<-w.ctx.Done()
	return portal.Notification{}, w.ctx.Err()
}
func (w lifetimeWatcher) Close() error { return nil }

// TestRealRuntimePortalsOutliveTheReconcileThatStartedThem drives the real
// portal.Runtime: a Portal it starts must keep watching after the reconcile
// returns, and stop only when the engine closes.
func TestRealRuntimePortalsOutliveTheReconcileThatStartedThem(t *testing.T) {
	node := &lifetimeNode{}
	h := newHarness(t, func(h *harness) {
		h.opts.RuntimeFactory = func(stateRoot string, _ installation.Logging) (Runtime, error) {
			return portal.NewRuntime(stateRoot, func(string, string) portal.Node { return node }), nil
		}
	})
	h.chooseLogging(installation.LoggingEnabled)
	h.create("hermes", localApp(8787))
	ctx := node.watching()
	if ctx == nil {
		t.Fatal("the runtime did not start the Portal")
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("the Portal's watcher context ended with its reconcile: %v", err)
	}
	h.stop()
	if ctx.Err() == nil {
		t.Fatal("the Portal's context outlived the engine")
	}
}
