package service

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/chrisbanes/portico/helper/internal/installation"
	"github.com/chrisbanes/portico/helper/internal/portal"
)

// Reachability is whether a Local App accepts connections on loopback. It is
// separate from the Portal's Tailscale state, as in Swift's LocalAppReachability.
type Reachability string

const (
	ReachabilityUnknown     Reachability = "unknown"
	ReachabilityReachable   Reachability = "reachable"
	ReachabilityUnavailable Reachability = "unavailable"
)

// Prober checks whether 127.0.0.1:port accepts a TCP connection before ctx ends.
type Prober interface {
	Reachable(ctx context.Context, port uint16) bool
}

const (
	reachabilityProbeTimeout = 500 * time.Millisecond
	reachabilityInterval     = 10 * time.Second
)

// LoopbackProber dials 127.0.0.1, like Swift's LoopbackTCPProbe.
type LoopbackProber struct{}

func (LoopbackProber) Reachable(ctx context.Context, port uint16) bool {
	if port == 0 {
		return false
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

type reachEntry struct {
	port  uint16
	state Reachability
}

// reachLoop probes every active Local App Portal on each wake and every
// reachabilityInterval. One cycle runs at a time; wakes during a cycle
// coalesce into the next one.
func (e *Engine) reachLoop() {
	defer close(e.reachStopped)
	ticker := time.NewTicker(reachabilityInterval)
	defer ticker.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-ticker.C:
			e.probeReachability()
		case <-e.reachKick:
			e.probeReachability()
		case reply := <-e.reachSync:
			e.probeReachability()
			close(reply)
		}
	}
}

func (e *Engine) wakeReachability() {
	select {
	case e.reachKick <- struct{}{}:
	default:
	}
}

// syncReachability runs one probe cycle; tests use it.
func (e *Engine) syncReachability() {
	reply := make(chan struct{})
	select {
	case e.reachSync <- reply:
		<-reply
	case <-e.reachStopped:
	}
}

// trackReachabilityLocked matches the probed set to the active Local App
// Portals: a new Portal or a changed port starts unknown, others are dropped.
// It reports whether the set changed. Callers hold e.mu.
func (e *Engine) trackReachabilityLocked() bool {
	ports := map[string]uint16{}
	for _, p := range e.store.state.Installation.Portals {
		if p.Lifecycle == installation.LifecycleActive && p.Destination.Kind == portal.DestinationLocalApp {
			ports[p.ID] = p.Destination.Port
		}
	}
	changed := false
	for id := range e.reach {
		if _, ok := ports[id]; !ok {
			delete(e.reach, id)
			changed = true
		}
	}
	for id, port := range ports {
		if entry, ok := e.reach[id]; !ok || entry.port != port {
			e.reach[id] = reachEntry{port: port, state: ReachabilityUnknown}
			changed = true
		}
	}
	return changed
}

func (e *Engine) probeReachability() {
	e.mu.Lock()
	if e.closing {
		e.mu.Unlock()
		return
	}
	// publishLocked keeps e.reach in step with every commit.
	targets := map[string]uint16{}
	for id, entry := range e.reach {
		targets[id] = entry.port
	}
	e.mu.Unlock()

	results := make(map[string]bool, len(targets))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for id, port := range targets {
		wg.Go(func() {
			// Close cancels in-flight probes through e.probeCtx.
			ctx, cancel := context.WithTimeout(e.probeCtx, reachabilityProbeTimeout)
			defer cancel()
			reachable := e.opts.Prober.Reachable(ctx, port)
			mu.Lock()
			results[id] = reachable
			mu.Unlock()
		})
	}
	wg.Wait()

	e.mu.Lock()
	defer e.mu.Unlock()
	changed := false
	for id, reachable := range results {
		entry, ok := e.reach[id]
		// A result only applies to the port it probed.
		if !ok || entry.port != targets[id] {
			continue
		}
		state := ReachabilityUnavailable
		if reachable {
			state = ReachabilityReachable
		}
		if entry.state != state {
			e.reach[id] = reachEntry{port: entry.port, state: state}
			changed = true
		}
	}
	if changed && !e.closing {
		e.publishLocked()
	}
}
