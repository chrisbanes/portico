package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/chrisbanes/portico/helper/internal/installation"
	"github.com/chrisbanes/portico/helper/internal/portal"
)

// Runtime is the in-process Portal runtime. *portal.Runtime satisfies it;
// tests use a fake.
type Runtime interface {
	Reconcile(ctx context.Context, configs []portal.Config, emit func(portal.Event)) ([]portal.ReconcileEntry, error)
	Authenticate(ctx context.Context, portalID string) error
	CleanupRejectedPortal(ctx context.Context, portalID string) error
	RemovePortal(ctx context.Context, portalID string) error
	Close(ctx context.Context) error
}

// RuntimeFactory builds a runtime rooted at the committed generation's
// identity directory, configured for the given logging choice.
type RuntimeFactory func(stateRoot string, logging installation.Logging) (Runtime, error)

// runtimeCallTimeout bounds each runtime call the engine makes; tests lower it.
var runtimeCallTimeout = 30 * time.Second

// runtimeFailure is why no usable runtime exists. Each is recoverable through
// retryRuntime; persistence and ownership failures are not runtime failures.
type runtimeFailure string

const (
	runtimeFailureNone  runtimeFailure = ""
	runtimeFailureStart runtimeFailure = "startFailed"
	// runtimeFailureClose keeps the old runtime: replacement waits until it
	// is proven closed.
	runtimeFailureClose runtimeFailure = "closeFailed"
)

// Problem is a structured per-Portal failure shown in snapshots.
type Problem string

const (
	ProblemStartFailed   Problem = "startFailed"
	ProblemCloseFailed   Problem = "closeFailed"
	ProblemRemovalFailed Problem = "removalFailed"
	ProblemCleanupFailed Problem = "cleanupFailed"
)

// convergedAt records the revision and runtime generation last reconciled.
type convergedAt struct {
	revision, runtimeGen uint64
}

type rejectionEvidence struct {
	assignedName, magicDNSSuffix string
}

// run is the single runtime worker: it converges the runtime on the latest
// committed state whenever woken, so every runtime side effect follows the
// durable commit that requested it.
func (e *Engine) run() {
	defer close(e.stopped)
	for {
		select {
		case <-e.stop:
			return
		case <-e.kick:
			e.converge()
		case reply := <-e.syncReq:
			e.converge()
			close(reply)
		}
	}
}

func (e *Engine) wake() {
	select {
	case e.kick <- struct{}{}:
	default:
	}
}

func (e *Engine) converge() {
	e.mu.Lock()
	state := e.store.State()
	logging := state.Installation.Logging
	if e.closing || logging == installation.LoggingUndecided || e.failure != runtimeFailureNone {
		e.mu.Unlock()
		return
	}
	rt, gen := e.runtime, e.runtimeGen
	replace := rt != nil && (e.closePending || e.runtimeLogging != logging)
	// Every change that needs runtime work commits a new revision, so an
	// unchanged revision on the same runtime has nothing to converge.
	if !replace && rt != nil && e.converged == (convergedAt{state.Revision, gen}) {
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()

	if replace {
		// The new logging choice is already committed; close the whole runtime
		// within the deadline, then build a fresh one, like the helper restart.
		ctx, cancel := context.WithTimeout(context.Background(), runtimeCallTimeout)
		err := rt.Close(ctx)
		cancel()
		e.mu.Lock()
		// Either way the old runtime has stopped reporting.
		e.markLastKnownLocked()
		if err != nil {
			e.failure = runtimeFailureClose
			e.closePending = true
		} else {
			e.runtime, rt, e.closePending = nil, nil, false
		}
		e.publishLocked()
		e.mu.Unlock()
		if err != nil {
			return
		}
	}
	if rt == nil {
		created, err := e.opts.RuntimeFactory(e.store.IdentityRoot(), logging)
		e.mu.Lock()
		if err != nil || e.closing {
			if err != nil {
				e.failure = runtimeFailureStart
			}
			e.publishLocked()
			e.mu.Unlock()
			if err == nil {
				_ = created.Close(context.Background())
			}
			return
		}
		e.runtimeGen++
		e.runtime, e.runtimeLogging, gen, rt = created, logging, e.runtimeGen, created
		e.publishLocked()
		e.mu.Unlock()
	}

	// Not a per-call deadline: Portals started here live on this context.
	// shortcut: a Portal close that hangs during stop or removal blocks all
	// convergence, with no closeFailed reported, until Engine.Close. Fixing it
	// needs separate operation and lifetime contexts in portal.Runtime; do that
	// before #104 hosts the engine.
	entries, err := rt.Reconcile(e.lifetime, activeConfigs(state), e.emitter(gen))
	outcomes := map[string]portal.ReconcileOutcome{}
	for _, entry := range entries {
		outcomes[strings.ToLower(entry.PortalID)] = entry.Outcome
	}
	e.mu.Lock()
	for _, p := range state.Installation.Portals {
		switch {
		case err != nil && p.Lifecycle == installation.LifecycleActive:
			e.problems[p.ID] = ProblemStartFailed
		case err != nil && p.Lifecycle == installation.LifecyclePendingRemoval:
			// The node may not be closed, so this removal attempt has failed;
			// retryRemoval runs it again.
			e.removalFailed[p.ID] = true
		case outcomes[p.ID] == portal.OutcomeStartFailed:
			e.problems[p.ID] = ProblemStartFailed
			e.ageFactsLocked(p.ID)
		case outcomes[p.ID] == portal.OutcomeCloseFailed:
			// The runtime stopped watching before the close failed.
			e.problems[p.ID] = ProblemCloseFailed
			e.ageFactsLocked(p.ID)
		default:
			delete(e.problems, p.ID)
			if p.Lifecycle == installation.LifecycleActive && p.DesiredState == portal.DesiredStateStopped {
				e.markStoppedLocked(p.ID)
			}
		}
	}
	if err == nil {
		e.converged = convergedAt{state.Revision, gen}
	}
	e.publishLocked()
	e.mu.Unlock()
	if err != nil {
		return
	}

	for _, p := range state.Installation.Portals {
		switch p.Lifecycle {
		case installation.LifecyclePendingRemoval:
			e.mu.Lock()
			skip := e.removalFailed[p.ID]
			e.mu.Unlock()
			if skip {
				continue
			}
			// The reconcile above excluded the Portal; delete its identity only
			// once its node is proven closed.
			if outcomes[p.ID] == portal.OutcomeCloseFailed {
				e.failRemoval(p.ID)
				continue
			}
			if err := callRuntime(func(ctx context.Context) error { return rt.RemovePortal(ctx, p.ID) }); err != nil {
				e.failRemoval(p.ID)
				continue
			}
			e.finishRemoval(p.ID)
		case installation.LifecyclePendingTailnetRejection:
			e.mu.Lock()
			skip := e.cleanupFailed[p.ID]
			e.mu.Unlock()
			if skip {
				continue
			}
			if err := callRuntime(func(ctx context.Context) error { return rt.CleanupRejectedPortal(ctx, p.ID) }); err != nil {
				e.mu.Lock()
				e.cleanupFailed[p.ID] = true
				e.publishLocked()
				e.mu.Unlock()
				continue
			}
			e.finishRejection(p.ID)
		}
	}
}

// callRuntime gives each runtime call its own deadline.
func callRuntime(call func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), runtimeCallTimeout)
	defer cancel()
	return call(ctx)
}

// activeConfigs is the desired runtime set: active records only, enabled or
// stopped. Removing and rejected Portals are excluded so their nodes close.
func activeConfigs(state State) []portal.Config {
	var configs []portal.Config
	for _, p := range state.Installation.Portals {
		if p.Lifecycle != installation.LifecycleActive {
			continue
		}
		configs = append(configs, portal.Config{
			ID:           p.ID,
			Name:         p.Name,
			Destination:  p.Destination,
			DesiredState: p.DesiredState,
			PublicAccess: p.PublicAccess,
		})
	}
	return configs
}

func (e *Engine) failRemoval(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.removalFailed[id] = true
	e.publishLocked()
}

// finishRemoval drops the record only after the identity is gone, in its own
// durable commit. A failed save keeps the record and offers retryRemoval.
func (e *Engine) finishRemoval(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	err := e.commitInternal(func(st *State, _ time.Time) error {
		i := portalIndex(st, id)
		if i < 0 || st.Installation.Portals[i].Lifecycle != installation.LifecyclePendingRemoval {
			return errUnchanged
		}
		st.Installation.Portals = append(st.Installation.Portals[:i], st.Installation.Portals[i+1:]...)
		return nil
	})
	if err != nil && !errors.Is(err, errUnchanged) {
		e.removalFailed[id] = true
	} else {
		delete(e.removalFailed, id)
		delete(e.live, id)
		delete(e.problems, id)
	}
	e.publishLocked()
}

// finishRejection replaces a cleaned-up rejected Portal with an alert built
// from sanitized evidence.
func (e *Engine) finishRejection(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	evidence := e.evidence[id]
	err := e.commitInternal(func(st *State, now time.Time) error {
		i := portalIndex(st, id)
		if i < 0 || st.Installation.Portals[i].Lifecycle != installation.LifecyclePendingTailnetRejection {
			return errUnchanged
		}
		alert := installation.Alert{
			ID:                     newUUID(),
			Kind:                   installation.AlertCrossTailnetRejection,
			PortalName:             st.Installation.Portals[i].Name,
			AssignedName:           optional(evidence.assignedName),
			RejectedMagicDNSSuffix: optional(evidence.magicDNSSuffix),
			CreatedAt:              now,
		}
		if b := st.Installation.Binding; b != nil {
			alert.ExpectedMagicDNSSuffix = optional(b.MagicDNSSuffix)
		}
		st.Installation.Portals = append(st.Installation.Portals[:i], st.Installation.Portals[i+1:]...)
		st.Installation.Alerts = append(st.Installation.Alerts, alert)
		return nil
	})
	if err != nil && !errors.Is(err, errUnchanged) {
		e.cleanupFailed[id] = true
	} else {
		delete(e.cleanupFailed, id)
		delete(e.evidence, id)
		delete(e.live, id)
		delete(e.problems, id)
	}
	e.publishLocked()
}

// emitter fences runtime events to the runtime generation that produced them:
// events from a superseded runtime, or for Portals that are not active, are
// dropped. Authentication URLs are never stored or published.
func (e *Engine) emitter(gen uint64) func(portal.Event) {
	return func(event portal.Event) {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.closing || gen != e.runtimeGen || event.Status == nil {
			return
		}
		id := strings.ToLower(event.PortalID)
		state := e.store.state
		i := portalIndex(&state, id)
		if i < 0 || state.Installation.Portals[i].Lifecycle != installation.LifecycleActive {
			return
		}
		connection := sanitizeStatus(*event.Status)
		e.live[id] = connection
		if connection.State == portal.StateOnline && event.Status.TailnetName != "" {
			e.observeTailnet(id, connection, event.Status.TailnetName)
		}
		e.publishLocked()
	}
}

// observeTailnet ports the Swift binding rules: the first Online status binds
// the installation; a different tailnet persists the rejection before any
// cleanup; a new suffix for the same tailnet refreshes the binding.
func (e *Engine) observeTailnet(id string, c Connection, tailnetName string) {
	binding := e.store.state.Installation.Binding
	switch {
	case binding == nil:
		if c.MagicDNSSuffix == "" {
			return
		}
		_ = e.commitInternal(func(st *State, _ time.Time) error {
			st.Installation.Binding = &installation.TailnetBinding{Name: tailnetName, MagicDNSSuffix: c.MagicDNSSuffix}
			return nil
		})
	case binding.Name != tailnetName:
		err := e.commitInternal(func(st *State, _ time.Time) error {
			i := portalIndex(st, id)
			if i < 0 || st.Installation.Portals[i].Lifecycle != installation.LifecycleActive {
				return errUnchanged
			}
			st.Installation.Portals[i].Lifecycle = installation.LifecyclePendingTailnetRejection
			return nil
		})
		if err == nil {
			e.evidence[id] = rejectionEvidence{assignedName: c.AssignedName, magicDNSSuffix: c.MagicDNSSuffix}
			delete(e.live, id)
			e.wake()
		}
	case c.MagicDNSSuffix != "" && c.MagicDNSSuffix != binding.MagicDNSSuffix:
		_ = e.commitInternal(func(st *State, _ time.Time) error {
			st.Installation.Binding = &installation.TailnetBinding{Name: binding.Name, MagicDNSSuffix: c.MagicDNSSuffix}
			return nil
		})
	}
}

// markStoppedLocked records a converged stop. portal.Runtime closes the node
// without a final status event, so the engine updates the facts itself; the
// address and URL facts it keeps are last-known.
func (e *Engine) markStoppedLocked(id string) {
	if c, ok := e.live[id]; ok {
		c.State, c.Freshness = portal.StateStopped, freshnessLastKnown
		e.live[id] = c
	}
}

// ageFactsLocked marks one Portal's facts last-known: nothing reports them now.
func (e *Engine) ageFactsLocked(id string) {
	if c, ok := e.live[id]; ok {
		c.Freshness = freshnessLastKnown
		e.live[id] = c
	}
}

// markLastKnownLocked marks facts from a runtime that has gone.
func (e *Engine) markLastKnownLocked() {
	for id := range e.live {
		e.ageFactsLocked(id)
	}
}

func portalIndex(st *State, id string) int {
	for i, p := range st.Installation.Portals {
		if p.ID == id {
			return i
		}
	}
	return -1
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
