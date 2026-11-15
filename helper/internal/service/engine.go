package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/chrisbanes/portico/helper/internal/discovery"
	"github.com/chrisbanes/portico/helper/internal/installation"
	"github.com/chrisbanes/portico/helper/internal/portal"
)

// Discoverer finds Local App candidates. A nil Discoverer means automatic
// discovery is unavailable, as on Linux; explicit ports still work.
type Discoverer interface {
	Discover(ctx context.Context) ([]discovery.Candidate, error)
}

type EngineOptions struct {
	RuntimeFactory RuntimeFactory
	Discoverer     Discoverer
	// Prober checks Local App reachability; it defaults to LoopbackProber.
	Prober Prober
	// Clock defaults to time.Now.
	Clock func() time.Time
	// LaunchAtLoginOfferCapable is true only for the macOS production service.
	LaunchAtLoginOfferCapable bool
}

// Engine is the single serializing authority over a Store: commands commit
// durable intent under one lock, and one worker converges the runtime on it.
// The engine starts no runtime until a logging choice is committed.
type Engine struct {
	store *Store
	opts  EngineOptions
	epoch string

	mu      sync.Mutex
	closing bool
	// closed is closed once the first Close finishes; closeErr is its result.
	closed   chan struct{}
	closeErr error
	sequence uint64
	current  Snapshot
	sessions map[*Session]struct{}

	runtime        Runtime
	runtimeGen     uint64
	runtimeLogging installation.Logging
	failure        runtimeFailure
	// closePending holds a runtime whose close failed: it must be closed
	// before anything else uses or replaces it, even if the logging choice
	// that triggered the close is reverted.
	closePending  bool
	converged     convergedAt
	live          map[string]Connection
	problems      map[string]Problem
	removalFailed map[string]bool
	cleanupFailed map[string]bool
	evidence      map[string]rejectionEvidence
	reach         map[string]reachEntry

	kick    chan struct{}
	syncReq chan chan struct{}
	stop    chan struct{}
	stopped chan struct{}

	// lifetime ends once Close has tried to close the runtime. portal.Runtime
	// ties every Portal a Reconcile starts (watcher, run loop, HTTPS proxy) to
	// the Reconcile context, so reconciles use it, as do reachability probes.
	lifetime       context.Context
	cancelLifetime context.CancelFunc
	reachKick      chan struct{}
	reachSync      chan chan struct{}
	reachStopped   chan struct{}
}

// New starts an engine over an opened store. Enabled Portals are reconciled,
// pending cleanup and removal get one automatic attempt, and Stopped Portals
// stay down.
func New(store *Store, opts EngineOptions) (*Engine, error) {
	if opts.RuntimeFactory == nil {
		return nil, errors.New("service: RuntimeFactory is required")
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.Prober == nil {
		opts.Prober = LoopbackProber{}
	}
	e := &Engine{
		store:         store,
		opts:          opts,
		epoch:         newUUID(),
		sessions:      map[*Session]struct{}{},
		live:          map[string]Connection{},
		problems:      map[string]Problem{},
		removalFailed: map[string]bool{},
		cleanupFailed: map[string]bool{},
		evidence:      map[string]rejectionEvidence{},
		reach:         map[string]reachEntry{},
		reachKick:     make(chan struct{}, 1),
		reachSync:     make(chan chan struct{}),
		reachStopped:  make(chan struct{}),
		kick:          make(chan struct{}, 1),
		syncReq:       make(chan chan struct{}),
		stop:          make(chan struct{}),
		stopped:       make(chan struct{}),
		closed:        make(chan struct{}),
	}
	e.lifetime, e.cancelLifetime = context.WithCancel(context.Background())
	e.mu.Lock()
	e.publishLocked()
	e.mu.Unlock()
	go e.run()
	go e.reachLoop()
	e.wake()
	return e, nil
}

// Close stops the engine and closes the runtime within ctx's deadline. If the
// runtime does not close in time the error is returned; the caller must then
// keep the store (and its ownership lock) open, since replacement is unsafe
// until the old nodes are proven closed. A failed close is final: the host
// should exit while still holding the lock, which ends the in-process nodes.
//
// Every call reports the same shutdown: a later call waits for the first one
// to finish rather than reporting success while nodes may still run.
func (e *Engine) Close(ctx context.Context) error {
	e.mu.Lock()
	if !e.closing {
		e.closing = true
		rt := e.runtime
		for s := range e.sessions {
			s.subscribers = nil
		}
		close(e.stop)
		go func() {
			var err error
			if rt != nil {
				// The first caller's deadline bounds the runtime's own close.
				if err = rt.Close(ctx); err != nil {
					err = &Error{Code: CodeRuntimeUnavailable}
				}
			}
			// Portal contexts end only after the runtime has tried to close;
			// this also releases a reconcile or probe still in flight.
			e.cancelLifetime()
			<-e.stopped
			<-e.reachStopped
			e.closeErr = err
			close(e.closed)
		}()
	}
	e.mu.Unlock()
	select {
	case <-e.closed:
		return e.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// sync runs one convergence after everything committed so far; tests use it
// to wait for the runtime worker.
func (e *Engine) sync() {
	reply := make(chan struct{})
	select {
	case e.syncReq <- reply:
		<-reply
	case <-e.stopped:
	}
}

// Session is one client connection. Closing it drops its subscriptions.
type Session struct {
	engine      *Engine
	subscribers []func(Snapshot)
}

func (e *Engine) Connect() *Session {
	s := &Session{engine: e}
	e.mu.Lock()
	e.sessions[s] = struct{}{}
	e.mu.Unlock()
	return s
}

func (s *Session) Close() {
	s.engine.mu.Lock()
	defer s.engine.mu.Unlock()
	delete(s.engine.sessions, s)
	s.subscribers = nil
}

// Subscribe delivers the current snapshot, then every replacement in
// increasing sequence order. Registration and the initial snapshot are atomic
// with publication. fn is called with the engine locked and must not block or
// call back into the engine; the IPC layer queues.
func (s *Session) Subscribe(fn func(Snapshot)) {
	s.engine.mu.Lock()
	defer s.engine.mu.Unlock()
	if _, open := s.engine.sessions[s]; !open || s.engine.closing {
		return
	}
	s.subscribers = append(s.subscribers, fn)
	fn(s.engine.current)
}

func (s *Session) GetSnapshot() Snapshot {
	s.engine.mu.Lock()
	defer s.engine.mu.Unlock()
	return s.engine.current
}

// GetRequestResult recovers a mutation's outcome by request identity. It is
// serialized behind any in-flight acceptance, so NotAccepted is safe to act on.
func (s *Session) GetRequestResult(id string, issuedAt time.Time) (RequestResult, error) {
	e := s.engine
	e.mu.Lock()
	defer e.mu.Unlock()
	state := e.store.state
	now := effectiveNow(e.opts.Clock(), state)
	if r := retained(state, now, id); r != nil {
		receipt, err := recoverable(now, r)
		return RequestResult{Receipt: receipt}, err
	}
	if err := admissible(now, issuedAt); err != nil {
		return RequestResult{}, err
	}
	// After an uncertain commit the receipt may be durable but not in
	// memory, so "not accepted" would be a guess.
	if e.store.failed {
		return RequestResult{}, codeErr(CodePersistenceUnavailable)
	}
	return RequestResult{NotAccepted: true}, nil
}

// discoveryBudget is the existing total budget for Local App discovery.
const discoveryBudget = 4 * time.Second

func (s *Session) DiscoverLocalApps(ctx context.Context) ([]discovery.Candidate, error) {
	d := s.engine.opts.Discoverer
	if d == nil {
		return nil, codeErr(CodeCapabilityUnavailable)
	}
	ctx, cancel := context.WithTimeout(ctx, discoveryBudget)
	defer cancel()
	candidates, err := d.Discover(ctx)
	if err != nil {
		return nil, codeErr(CodeRuntimeUnavailable)
	}
	return candidates, nil
}

// GetDiagnostics returns the bounded, sanitized report, optionally for one Portal.
func (s *Session) GetDiagnostics(portalID string) (string, error) {
	snapshot := s.GetSnapshot()
	if portalID != "" {
		var only []PortalSnapshot
		for _, p := range snapshot.Portals {
			if p.ID == portalID {
				only = append(only, p)
			}
		}
		if only == nil {
			return "", &Error{Code: CodeNotFound, PortalID: portalID}
		}
		snapshot.Portals = only
	}
	return renderDiagnostics(snapshot), nil
}

// change is what a command's apply step decided.
type change struct {
	outcome  Outcome
	portalID string
	// after runs under the lock once the commit is durable.
	after func()
}

var errUnchanged = errors.New("unchanged")

// mutate runs one mutation: recover a duplicate by request identity first,
// then admit, revalidate and commit intent and receipt atomically. Runtime
// work is only woken after the commit is durable.
func (e *Engine) mutate(op Operation, req Request, params any, apply func(st *State, now time.Time) (change, error)) (Receipt, error) {
	hash, err := requestHash(op, req, params)
	if err != nil {
		return Receipt{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closing {
		return Receipt{}, codeErr(CodeRuntimeUnavailable)
	}
	state := e.store.state
	now := effectiveNow(e.opts.Clock(), state)
	if existing, err := recall(state, now, req, hash); err != nil || existing != nil {
		if err != nil {
			return Receipt{}, err
		}
		return *existing, nil
	}
	if e.store.failed {
		return Receipt{}, codeErr(CodePersistenceUnavailable)
	}
	if retainedReceipts(state, now) >= maxReceipts {
		return Receipt{}, codeErr(CodeCapacityExceeded)
	}
	if req.ExpectedRevision != state.Revision {
		return Receipt{}, codeErr(CodeRevisionConflict)
	}

	var receipt Receipt
	var result change
	err = e.store.Commit(func(st *State) error {
		var err error
		if result, err = apply(st, now); err != nil {
			return err
		}
		if result.outcome == OutcomeAccepted {
			st.Revision++
			if st.Migration.FirstMutationRequestID == "" {
				st.Migration.FirstMutationRequestID = req.ID
			}
		}
		st.ClockHighWater = now
		receipt = newReceipt(st, now, req, hash, op, result.portalID, result.outcome)
		// The snapshot of an accepted state must stay readable.
		if size, err := json.Marshal(e.buildSnapshot(*st)); err != nil || len(size) > maxStateSize {
			return codeErr(CodeCapacityExceeded)
		}
		return nil
	})
	if err != nil {
		e.publishLocked()
		return Receipt{}, commitError(err)
	}
	if result.after != nil {
		result.after()
	}
	e.publishLocked()
	e.wake()
	return receipt, nil
}

// commitInternal commits a service-initiated change (binding, rejection,
// removal completion) with no receipt. Callers hold e.mu.
func (e *Engine) commitInternal(apply func(st *State, now time.Time) error) error {
	if e.store.failed {
		return codeErr(CodePersistenceUnavailable)
	}
	now := effectiveNow(e.opts.Clock(), e.store.state)
	err := e.store.Commit(func(st *State) error {
		if err := apply(st, now); err != nil {
			return err
		}
		st.Revision++
		st.ClockHighWater = now
		return nil
	})
	if err != nil && !errors.Is(err, errUnchanged) {
		return commitError(err)
	}
	return err
}

func commitError(err error) error {
	var e *Error
	switch {
	case errors.As(err, &e), errors.Is(err, errUnchanged):
		return err
	case errors.Is(err, ErrCapacityExceeded):
		return codeErr(CodeCapacityExceeded)
	case errors.Is(err, ErrUnsupportedState):
		return codeErr(CodeUnsupportedState)
	case errors.Is(err, ErrPersistenceUnavailable):
		return codeErr(CodePersistenceUnavailable)
	}
	return codeErr(CodeInvalidRequest)
}

// Snapshot is the complete structured state a client renders. It holds no
// wording, effects, auth URLs, identity paths or opaque tailnet identifiers.
type Snapshot struct {
	Version            int                             `json:"version"`
	Epoch              string                          `json:"epoch"`
	Sequence           uint64                          `json:"sequence"`
	Revision           uint64                          `json:"revision"`
	Service            ServiceState                    `json:"service"`
	Logging            installation.Logging            `json:"operationalLogging"`
	LaunchAtLoginOffer installation.LaunchAtLoginOffer `json:"launchAtLoginOffer"`
	// TailnetMagicDNSSuffix is the bound tailnet's display suffix; the
	// binding's opaque name is never exposed.
	TailnetMagicDNSSuffix string                     `json:"tailnetMagicDNSSuffix,omitempty"`
	TailnetBound          bool                       `json:"tailnetBound"`
	Alerts                []installation.Alert       `json:"alerts"`
	Portals               []PortalSnapshot           `json:"portals"`
	Availability          map[Operation]Availability `json:"availability"`
}

type ServiceState struct {
	// Runtime is loggingChoiceRequired, starting, running or unavailable.
	Runtime            string `json:"runtime"`
	PersistenceHealthy bool   `json:"persistenceHealthy"`
	AutomaticDiscovery bool   `json:"automaticDiscovery"`
	LaunchAtLoginOffer bool   `json:"launchAtLoginOfferCapable"`
}

type PortalSnapshot struct {
	ID           string                  `json:"id"`
	Name         string                  `json:"name"`
	Destination  portal.Destination      `json:"destination"`
	PublicAccess portal.PublicAccessMode `json:"publicAccess"`
	DesiredState portal.DesiredState     `json:"desiredState"`
	Lifecycle    installation.Lifecycle  `json:"lifecycle"`
	Connection   *Connection             `json:"connection,omitempty"`
	Problem      Problem                 `json:"problem,omitempty"`
	// Reachability is set for active Local App Portals only.
	Reachability Reachability               `json:"reachability,omitempty"`
	Availability map[Operation]Availability `json:"availability"`
}

const (
	runtimeLoggingChoiceRequired = "loggingChoiceRequired"
	runtimeStarting              = "starting"
	runtimeRunning               = "running"
	runtimeUnavailable           = "unavailable"
)

func (e *Engine) runtimeStatus(state State) string {
	switch {
	case state.Installation.Logging == installation.LoggingUndecided:
		return runtimeLoggingChoiceRequired
	case e.failure != runtimeFailureNone:
		return runtimeUnavailable
	case e.runtime == nil:
		return runtimeStarting
	}
	return runtimeRunning
}

// buildSnapshot projects state and live runtime facts. Callers hold e.mu.
func (e *Engine) buildSnapshot(state State) Snapshot {
	in := state.Installation
	snapshot := Snapshot{
		Version:            managementVersion,
		Epoch:              e.epoch,
		Revision:           state.Revision,
		Logging:            in.Logging,
		LaunchAtLoginOffer: in.LaunchAtLoginOffer,
		Alerts:             append([]installation.Alert{}, in.Alerts...),
		Portals:            []PortalSnapshot{},
		Service: ServiceState{
			Runtime:            e.runtimeStatus(state),
			PersistenceHealthy: !e.store.failed,
			AutomaticDiscovery: e.opts.Discoverer != nil,
			LaunchAtLoginOffer: e.opts.LaunchAtLoginOfferCapable,
		},
		Availability: e.installationAvailability(state),
	}
	if in.Binding != nil {
		snapshot.TailnetBound = true
		snapshot.TailnetMagicDNSSuffix = in.Binding.MagicDNSSuffix
	}
	for _, p := range in.Portals {
		ps := PortalSnapshot{
			ID:           p.ID,
			Name:         p.Name,
			Destination:  p.Destination,
			PublicAccess: p.PublicAccess,
			DesiredState: p.DesiredState,
			Lifecycle:    p.Lifecycle,
			Problem:      e.portalProblem(p),
		}
		if entry, ok := e.reach[p.ID]; ok && p.Lifecycle == installation.LifecycleActive {
			ps.Reachability = entry.state
		}
		if c, ok := e.live[p.ID]; ok && p.Lifecycle == installation.LifecycleActive {
			c.Addresses = append([]string(nil), c.Addresses...)
			ps.Connection = &c
		}
		ps.Availability = e.portalAvailability(state, p, ps.Connection)
		snapshot.Portals = append(snapshot.Portals, ps)
	}
	return snapshot
}

func (e *Engine) portalProblem(p installation.Portal) Problem {
	switch {
	case e.removalFailed[p.ID]:
		return ProblemRemovalFailed
	case e.cleanupFailed[p.ID]:
		return ProblemCleanupFailed
	}
	return e.problems[p.ID]
}

// publishLocked builds the next snapshot and delivers it to subscribers.
func (e *Engine) publishLocked() {
	if e.trackReachabilityLocked() {
		e.wakeReachability()
	}
	e.sequence++
	snapshot := e.buildSnapshot(e.store.state)
	snapshot.Sequence = e.sequence
	e.current = snapshot
	for s := range e.sessions {
		for _, fn := range s.subscribers {
			fn(snapshot)
		}
	}
}

// newUUID returns a lowercase random (version 4) UUID.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
