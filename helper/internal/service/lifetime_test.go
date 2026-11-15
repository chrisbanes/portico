package service

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
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

// onlineNode is a portal.Node that reports itself Online with a Portal URL,
// serving the Portal's HTTPS proxy on a loopback listener.
type onlineNode struct{ lifetimeNode }

func (*onlineNode) status() portal.Status {
	return portal.Status{
		BackendState:   "Running",
		DNSName:        "hermes.one.ts.net.",
		CertDomains:    []string{"hermes.one.ts.net"},
		Addresses:      []string{"100.64.0.1"},
		TailnetName:    "tailnet-a",
		MagicDNSSuffix: "one.ts.net",
	}
}

func (n *onlineNode) Up(context.Context) (portal.Status, error)     { return n.status(), nil }
func (n *onlineNode) Status(context.Context) (portal.Status, error) { return n.status(), nil }
func (n *onlineNode) Listen(string, string) (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

// TestRealRuntimeStopLeavesNoCurrentOnlineFacts stops an Online Portal through
// the real portal.Runtime, which closes the node without a final status: the
// snapshot must not keep claiming a current Online connection.
func TestRealRuntimeStopLeavesNoCurrentOnlineFacts(t *testing.T) {
	node := &onlineNode{}
	h := newHarness(t, func(h *harness) {
		h.opts.RuntimeFactory = func(stateRoot string, _ installation.Logging) (Runtime, error) {
			return portal.NewRuntime(stateRoot, func(string, string) portal.Node { return node }), nil
		}
	})
	h.chooseLogging(installation.LoggingEnabled)
	id := h.create("hermes", localApp(8787))
	// Startup facts arrive within the reconcile that create waits for.
	if p := h.portal(id); p.Connection == nil || p.Connection.State != portal.StateOnline || !p.Availability[OpOpenPortalURL].Available {
		t.Fatalf("Portal before the stop = %+v", p)
	}
	if _, err := h.session.SetDesiredState(h.req(), SetDesiredStateParams{PortalID: id, State: portal.DesiredStateStopped}); err != nil {
		t.Fatal(err)
	}
	h.settle()
	p := h.portal(id)
	if c := p.Connection; c == nil || c.State != portal.StateStopped || c.Freshness != freshnessLastKnown || c.PortalURL == "" {
		t.Fatalf("connection after a stop = %+v, want last-known facts in state stopped", c)
	}
	if p.Availability[OpOpenPortalURL].Available || !p.Availability[OpCopyPortalURL].Available {
		t.Fatalf("URL actions after a stop = %+v", p.Availability)
	}
	report, err := h.session.GetDiagnostics(id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "Tailscale: stopped") || !strings.Contains(report, "Facts: lastKnown") {
		t.Fatalf("diagnostics after a stop:\n%s", report)
	}
}

// failingCloseNode is Online but cannot close until allowClose.
type failingCloseNode struct {
	onlineNode
	closable atomic.Bool
}

func (n *failingCloseNode) Close() error {
	if !n.closable.Load() {
		return errors.New("node still running")
	}
	return nil
}

// TestRealRuntimeFailedCloseLeavesNoCurrentOnlineFacts: when the real runtime
// stops watching a Portal but cannot close its node, the facts are no longer
// current, so the snapshot must not offer to open the Portal URL.
func TestRealRuntimeFailedCloseLeavesNoCurrentOnlineFacts(t *testing.T) {
	node := &failingCloseNode{}
	h := newHarness(t, func(h *harness) {
		h.opts.RuntimeFactory = func(stateRoot string, _ installation.Logging) (Runtime, error) {
			return portal.NewRuntime(stateRoot, func(string, string) portal.Node { return node }), nil
		}
	})
	h.chooseLogging(installation.LoggingEnabled)
	id := h.create("hermes", localApp(8787))
	if p := h.portal(id); !p.Availability[OpOpenPortalURL].Available {
		t.Fatalf("Portal before the stop = %+v", p)
	}
	if _, err := h.session.SetDesiredState(h.req(), SetDesiredStateParams{PortalID: id, State: portal.DesiredStateStopped}); err != nil {
		t.Fatal(err)
	}
	h.settle()
	p := h.portal(id)
	if p.Problem != ProblemCloseFailed || p.Connection == nil || p.Connection.Freshness != freshnessLastKnown || p.Availability[OpOpenPortalURL].Available {
		t.Fatalf("Portal after a failed close = problem %q, connection %+v, open %+v", p.Problem, p.Connection, p.Availability[OpOpenPortalURL])
	}
	node.closable.Store(true)
}
