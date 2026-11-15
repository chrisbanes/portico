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
	"time"

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

// failingCloseNode is Online but cannot close until closable is set.
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
	// Runs before the harness's Close, so shutdown can succeed.
	t.Cleanup(func() { node.closable.Store(true) })
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
}

func TestStartFailureAgesFacts(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	id := h.create("hermes", localApp(1))
	h.factory.runtime().emitStatus(id, online("hermes", "one.ts.net", "tailnet-a"))
	h.factory.set(func(f *fakeFactory) { f.outcomes[id] = portal.OutcomeStartFailed })
	if _, err := h.session.SetDestination(h.req(), SetDestinationParams{PortalID: id, Destination: localApp(2)}); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if p := h.portal(id); p.Problem != ProblemStartFailed || p.Availability[OpOpenPortalURL].Available {
		t.Fatalf("after a start failure: problem %q, open %+v", p.Problem, p.Availability[OpOpenPortalURL])
	}
}

func TestFailedReplacementAgesFacts(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	id := h.create("hermes", localApp(1))
	h.factory.runtime().emitStatus(id, online("hermes", "one.ts.net", "tailnet-a"))
	h.factory.set(func(f *fakeFactory) { f.closeErr = errors.New("still running") })
	h.chooseLogging(installation.LoggingDisabled)
	if p := h.portal(id); p.Connection == nil || p.Connection.Freshness != freshnessLastKnown || p.Availability[OpOpenPortalURL].Available {
		t.Fatalf("after a failed replacement: %+v", p.Connection)
	}
}

// blockingCloseNode is Online, and its Close blocks until release closes.
type blockingCloseNode struct {
	onlineNode
	release chan struct{}
}

func (n *blockingCloseNode) Close() error {
	<-n.release
	return nil
}

// settleWithin fails the test if one convergence pass outlasts limit.
func (h *harness) settleWithin(limit time.Duration) {
	h.t.Helper()
	done := make(chan struct{})
	go func() {
		h.settle()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(limit):
		h.t.Fatalf("a convergence pass outlasted %v", limit)
	}
}

// TestRealRuntimeHungCloseReportsWithinTheReconcileDeadline: when the real
// runtime's Node.Close hangs during a stop or a removal, the reconcile ends at
// its deadline, the snapshot reports the failure, and no new node starts for
// that UUID until the close completes.
func TestRealRuntimeHungCloseReportsWithinTheReconcileDeadline(t *testing.T) {
	timeout := runtimeCallTimeout
	t.Cleanup(func() { runtimeCallTimeout = timeout })
	runtimeCallTimeout = 100 * time.Millisecond
	for _, removal := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop", true: "removal"}[removal], func(t *testing.T) {
			var nodes atomic.Int32
			node := &blockingCloseNode{release: make(chan struct{})}
			h := newHarness(t, func(h *harness) {
				h.opts.RuntimeFactory = func(stateRoot string, _ installation.Logging) (Runtime, error) {
					return portal.NewRuntime(stateRoot, func(string, string) portal.Node {
						nodes.Add(1)
						return node
					}), nil
				}
			})
			// Runs before the harness's Close, so shutdown can succeed.
			t.Cleanup(func() { close(node.release) })
			h.chooseLogging(installation.LoggingEnabled)
			id := h.create("hermes", localApp(8787))
			var err error
			if removal {
				_, err = h.session.RemovePortal(h.req(), RemovePortalParams{PortalID: id, Confirmed: true})
			} else {
				_, err = h.session.SetDesiredState(h.req(), SetDesiredStateParams{PortalID: id, State: portal.DesiredStateStopped})
			}
			if err != nil {
				t.Fatal(err)
			}
			h.settleWithin(2 * time.Second)
			want := ProblemCloseFailed
			if removal {
				want = ProblemRemovalFailed
			}
			if p := h.portal(id); p == nil || p.Problem != want {
				t.Fatalf("Portal after a hung close = %+v, want problem %q", p, want)
			}
			if removal {
				return
			}
			if _, err := h.session.SetDesiredState(h.req(), SetDesiredStateParams{PortalID: id, State: portal.DesiredStateEnabled}); err != nil {
				t.Fatal(err)
			}
			h.settleWithin(2 * time.Second)
			if n := nodes.Load(); n != 1 {
				t.Fatalf("%d nodes started for the UUID before its close completed, want 1", n)
			}
		})
	}
}

// switchableCloseNode is Online; once hang is set, Close blocks until
// release closes.
type switchableCloseNode struct {
	onlineNode
	hang    atomic.Bool
	release chan struct{}
}

func (n *switchableCloseNode) Close() error {
	if n.hang.Load() {
		<-n.release
	}
	return nil
}

// TestRealRuntimeHungCloseDoesNotFailItsSiblings: a hung close that uses up
// a reconcile's deadline does not leave a running Portal sorted after it
// reported as failed.
func TestRealRuntimeHungCloseDoesNotFailItsSiblings(t *testing.T) {
	timeout := runtimeCallTimeout
	t.Cleanup(func() { runtimeCallTimeout = timeout })
	runtimeCallTimeout = 100 * time.Millisecond
	release := make(chan struct{})
	var mu sync.Mutex
	nodes := map[string]*switchableCloseNode{}
	h := newHarness(t, func(h *harness) {
		h.opts.RuntimeFactory = func(stateRoot string, _ installation.Logging) (Runtime, error) {
			return portal.NewRuntime(stateRoot, func(_, name string) portal.Node {
				mu.Lock()
				defer mu.Unlock()
				nodes[name] = &switchableCloseNode{release: release}
				return nodes[name]
			}), nil
		}
	})
	t.Cleanup(func() { close(release) })
	h.chooseLogging(installation.LoggingEnabled)
	ids := map[string]string{"a": h.create("a", localApp(1)), "b": h.create("b", localApp(2))}
	hung, sibling := "a", "b"
	if ids["b"] < ids["a"] {
		hung, sibling = "b", "a"
	}
	mu.Lock()
	nodes[hung].hang.Store(true)
	mu.Unlock()
	if _, err := h.session.SetDesiredState(h.req(), SetDesiredStateParams{PortalID: ids[hung], State: portal.DesiredStateStopped}); err != nil {
		t.Fatal(err)
	}
	h.settleWithin(2 * time.Second)
	if p := h.portal(ids[hung]); p.Problem != ProblemCloseFailed {
		t.Fatalf("hung Portal problem = %q, want closeFailed", p.Problem)
	}
	if p := h.portal(ids[sibling]); p.Problem != "" {
		t.Fatalf("running sibling problem = %q, want none", p.Problem)
	}
}
