package service

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chrisbanes/portico/helper/internal/installation"
)

type fakeProber struct {
	mu        sync.Mutex
	reachable map[uint16]bool
	probed    []uint16
	// gate, when set, holds every probe until it is closed; gates holds
	// probes of one port.
	gate  chan struct{}
	gates map[uint16]chan struct{}
}

func (p *fakeProber) Reachable(ctx context.Context, port uint16) bool {
	p.mu.Lock()
	gate := p.gate
	if g, ok := p.gates[port]; ok {
		gate = g
	}
	p.probed = append(p.probed, port)
	p.mu.Unlock()
	if gate != nil {
		<-gate
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reachable[port]
}

func (p *fakeProber) set(port uint16, reachable bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reachable[port] = reachable
}

func TestLocalAppReachability(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	// Hold probes so the state before any result is observable.
	gate := make(chan struct{})
	h.prober.mu.Lock()
	h.prober.gate = gate
	h.prober.mu.Unlock()
	local := h.create("local", localApp(8787))
	remote := h.create("remote", remoteApp("app.example.com", 443))
	if got := h.portal(local).Reachability; got != ReachabilityUnknown {
		t.Fatalf("new Local App reachability = %q, want unknown before a probe", got)
	}
	h.prober.mu.Lock()
	h.prober.gate = nil
	h.prober.mu.Unlock()
	close(gate)
	if got := h.portal(remote).Reachability; got != "" {
		t.Fatalf("Remote App reachability = %q, want none", got)
	}

	h.prober.set(8787, true)
	h.engine.syncReachability()
	if got := h.portal(local).Reachability; got != ReachabilityReachable {
		t.Fatalf("reachability = %q, want reachable", got)
	}
	h.prober.set(8787, false)
	h.engine.syncReachability()
	if got := h.portal(local).Reachability; got != ReachabilityUnavailable {
		t.Fatalf("reachability = %q, want unavailable", got)
	}
	report, _ := h.session.GetDiagnostics(local)
	if !strings.Contains(report, "Reachability: unavailable") {
		t.Fatalf("diagnostics = %s", report)
	}

	if _, err := h.session.SetDestination(h.req(), SetDestinationParams{PortalID: local, Destination: localApp(9000)}); err != nil {
		t.Fatal(err)
	}
	h.prober.set(9000, true)
	h.engine.syncReachability()
	if got := h.portal(local).Reachability; got != ReachabilityReachable {
		t.Fatalf("reachability of the new port = %q", got)
	}

	if _, err := h.session.RemovePortal(h.req(), RemovePortalParams{PortalID: local, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	if got := h.portal(local).Reachability; got != "" {
		t.Fatalf("removing Portal reachability = %q, want none", got)
	}
}

func TestReachabilityResultAppliesOnlyToTheProbedPort(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	oldPort, newPort := make(chan struct{}), make(chan struct{})
	h.prober.mu.Lock()
	h.prober.gates = map[uint16]chan struct{}{8787: oldPort, 9000: newPort}
	h.prober.reachable[8787] = true
	h.prober.mu.Unlock()
	t.Cleanup(func() { close(newPort) })

	id := h.create("local", localApp(8787))
	// Creating the Portal must start a probe of its port; then move it.
	waitForProbes(t, h.prober, 1)
	if _, err := h.session.SetDestination(h.req(), SetDestinationParams{PortalID: id, Destination: localApp(9000)}); err != nil {
		t.Fatal(err)
	}
	close(oldPort)
	// The port change starts a new cycle once the old one has finished; the
	// new port's probe stays held.
	waitForProbes(t, h.prober, 2)
	if got := h.portal(id).Reachability; got != ReachabilityUnknown {
		t.Fatalf("reachability = %q; the old port's result was applied to the new one", got)
	}
}

func TestLoopbackProber(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	ctx, cancel := context.WithTimeout(context.Background(), reachabilityProbeTimeout)
	defer cancel()
	if !(LoopbackProber{}).Reachable(ctx, port) {
		t.Fatal("a listening loopback port is unreachable")
	}
	listener.Close()
	if (LoopbackProber{}).Reachable(ctx, port) {
		t.Fatal("a closed loopback port is reachable")
	}
	if (LoopbackProber{}).Reachable(ctx, 0) {
		t.Fatal("port 0 is reachable")
	}
}

// waitForProbes fails unless n probes start well before the periodic tick.
func waitForProbes(t *testing.T, p *fakeProber, n int) {
	t.Helper()
	deadline := time.Now().Add(reachabilityInterval / 4)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		started := len(p.probed)
		p.mu.Unlock()
		if started >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("fewer than %d probes started; a change did not trigger one", n)
}
