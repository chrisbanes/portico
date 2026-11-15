package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chrisbanes/portico/helper/internal/discovery"
	"github.com/chrisbanes/portico/helper/internal/installation"
	"github.com/chrisbanes/portico/helper/internal/portal"
)

// fakeClock is a settable clock shared across engine restarts.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// fakeRuntime records every call together with the on-disk state at call
// time, so tests can prove intent was durable before any side effect. It
// models the portal.Runtime lifetime rules the engine depends on: Portals
// live on the context of the Reconcile that started them, so their events
// stop when it ends; and Close marks the runtime closing for good, even when
// it fails, after which Reconcile is refused.
type fakeRuntime struct {
	f       *fakeFactory
	logging installation.Logging

	mu         sync.Mutex
	reconciles [][]portal.Config
	emit       func(portal.Event)
	portalCtx  context.Context
	pending    map[string]string // Portal ID -> pending login attempt
	closing    bool
	closed     bool
}

var errFakeRuntimeClosing = errors.New("runtime closing")

func (r *fakeRuntime) Reconcile(ctx context.Context, configs []portal.Config, emit func(portal.Event)) ([]portal.ReconcileEntry, error) {
	r.f.record("reconcile")
	r.f.mu.Lock()
	delay := r.f.reconcileDelay
	r.f.mu.Unlock()
	time.Sleep(delay)
	r.mu.Lock()
	if r.closing {
		r.mu.Unlock()
		return nil, errFakeRuntimeClosing
	}
	r.reconciles = append(r.reconciles, slices.Clone(configs))
	r.emit = emit
	r.portalCtx = ctx
	r.mu.Unlock()
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	if r.f.reconcileErr != nil {
		return nil, r.f.reconcileErr
	}
	var entries []portal.ReconcileEntry
	for _, c := range configs {
		outcome := portal.OutcomeConverged
		if o, ok := r.f.outcomes[c.ID]; ok {
			outcome = o
		}
		entries = append(entries, portal.ReconcileEntry{PortalID: c.ID, Outcome: outcome})
	}
	for id, o := range r.f.outcomes {
		if !slices.ContainsFunc(configs, func(c portal.Config) bool { return c.ID == id }) {
			entries = append(entries, portal.ReconcileEntry{PortalID: id, Outcome: o})
		}
	}
	return entries, nil
}

// AuthenticateAttempt, like portal.Runtime, fails unless the Portal is
// running (started by the last Reconcile, Enabled, on a runtime that is not
// closing), and otherwise leaves one login pending, labelled with attempt.
func (r *fakeRuntime) AuthenticateAttempt(_ context.Context, id, attempt string) error {
	r.f.mu.Lock()
	gate, pause := r.f.authGate, r.f.authPause
	r.f.authPause = nil
	r.f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if pause != nil {
		pause()
	}
	r.mu.Lock()
	running := !r.closing && len(r.reconciles) > 0 && slices.ContainsFunc(r.reconciles[len(r.reconciles)-1], func(c portal.Config) bool {
		return c.ID == id && c.DesiredState == portal.DesiredStateEnabled
	})
	r.f.mu.Lock()
	err := r.f.authErr
	r.f.mu.Unlock()
	if running && err == nil {
		if r.pending == nil {
			r.pending = map[string]string{}
		}
		r.pending[id] = attempt
	}
	r.mu.Unlock()
	r.f.record("auth:" + id)
	if !running {
		return errors.New("portal is not running")
	}
	return err
}

// emitURL delivers a notification URL as portal.Runtime's watcher would: only
// while a login is pending, once, labelled with the pending attempt.
func (r *fakeRuntime) emitURL(id, rawURL string) {
	r.mu.Lock()
	emit, ctx := r.emit, r.portalCtx
	attempt, pending := r.pending[id]
	delete(r.pending, id)
	r.mu.Unlock()
	if ctx == nil || ctx.Err() != nil || !pending {
		return
	}
	emit(portal.Event{PortalID: id, AuthenticationURL: rawURL, AuthenticationAttempt: attempt})
}

func (r *fakeRuntime) CleanupRejectedPortal(ctx context.Context, id string) error {
	r.f.record("cleanup:" + id)
	r.f.noteCtx(ctx)
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	if r.f.onCleanup != nil {
		r.f.onCleanup()
	}
	return r.f.cleanupErr
}

func (r *fakeRuntime) RemovePortal(ctx context.Context, id string) error {
	r.f.record("remove:" + id)
	r.f.noteCtx(ctx)
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	if r.f.onRemove != nil {
		r.f.onRemove()
	}
	return r.f.removeErr
}

func (r *fakeRuntime) Close(ctx context.Context) error {
	r.f.record("close")
	r.mu.Lock()
	r.closing = true
	r.mu.Unlock()
	r.f.mu.Lock()
	block, err := r.f.closeBlock, r.f.closeErr
	r.f.mu.Unlock()
	if block != nil {
		// Like portal.Runtime.Close, give up when the deadline passes.
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	return nil
}

func (r *fakeRuntime) lastReconcile() []portal.Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.reconciles) == 0 {
		return nil
	}
	return r.reconciles[len(r.reconciles)-1]
}

func (r *fakeRuntime) reconcileCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reconciles)
}

// emitStatus sends a status event through the emitter the runtime was given,
// as a running Portal's watcher would. A Portal whose context ended has no
// watcher, so it sends nothing.
func (r *fakeRuntime) emitStatus(id string, status portal.StatusEvent) {
	r.mu.Lock()
	emit, ctx := r.emit, r.portalCtx
	r.mu.Unlock()
	if ctx == nil || ctx.Err() != nil {
		return
	}
	emit(portal.Event{PortalID: id, Status: &status})
}

type call struct {
	name  string
	state State // on-disk state when the call happened
}

type fakeFactory struct {
	t        *testing.T
	diskPath func() string

	mu           sync.Mutex
	calls        []call
	runtimes     []*fakeRuntime
	buildErr     error
	reconcileErr error
	removeErr    error
	cleanupErr   error
	closeErr     error
	authErr      error
	authGate     chan struct{} // holds AuthenticateAttempt until closed
	authPause    func()        // runs once inside the next AuthenticateAttempt
	closeBlock   chan struct{}
	outcomes     map[string]portal.ReconcileOutcome
	onRemove     func()
	onCleanup    func()
	// reconcileDelay slows Reconcile; expiredCalls counts runtime calls that
	// arrived with their deadline already passed.
	reconcileDelay time.Duration
	expiredCalls   int
}

func (f *fakeFactory) noteCtx(ctx context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ctx.Err() != nil {
		f.expiredCalls++
	}
}

func (f *fakeFactory) build(stateRoot string, logging installation.Logging) (Runtime, error) {
	f.record("build:" + string(logging))
	if info, err := os.Stat(stateRoot); err != nil || !info.IsDir() {
		f.t.Errorf("runtime state root %s is not the committed identity directory: %v", stateRoot, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.buildErr != nil {
		return nil, f.buildErr
	}
	r := &fakeRuntime{f: f, logging: logging}
	f.runtimes = append(f.runtimes, r)
	return r, nil
}

func (f *fakeFactory) record(name string) {
	var state State
	if data, err := os.ReadFile(f.diskPath()); err == nil {
		state, _ = decodeState(data)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{name, state})
}

func (f *fakeFactory) set(fn func(f *fakeFactory)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeFactory) runtime() *fakeRuntime {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.runtimes) == 0 {
		return nil
	}
	return f.runtimes[len(f.runtimes)-1]
}

func (f *fakeFactory) callNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for _, c := range f.calls {
		names = append(names, c.name)
	}
	return names
}

func (f *fakeFactory) callsNamed(name string) []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var matched []call
	for _, c := range f.calls {
		if c.name == name {
			matched = append(matched, c)
		}
	}
	return matched
}

type fakeDiscoverer struct {
	mu       sync.Mutex
	deadline time.Duration
}

func (d *fakeDiscoverer) Discover(ctx context.Context) ([]discovery.Candidate, error) {
	deadline, _ := ctx.Deadline()
	d.mu.Lock()
	d.deadline = time.Until(deadline)
	d.mu.Unlock()
	return []discovery.Candidate{{LocalAppPort: 3000, ProcessLabel: "node", SuggestedPortalName: "web"}}, nil
}

type harness struct {
	t        *testing.T
	dir      string
	clock    *fakeClock
	factory  *fakeFactory
	opts     EngineOptions
	storeOpt Options
	prober   *fakeProber
	store    *Store
	engine   *Engine
	session  *Session
}

var testStart = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

func newHarness(t *testing.T, configure ...func(h *harness)) *harness {
	t.Helper()
	h := &harness{t: t, dir: t.TempDir(), clock: &fakeClock{now: testStart}}
	h.factory = &fakeFactory{t: t, diskPath: func() string {
		return filepath.Join(h.dir, h.store.generation, stateFileName)
	}, outcomes: map[string]portal.ReconcileOutcome{}}
	h.prober = &fakeProber{reachable: map[uint16]bool{}}
	h.opts = EngineOptions{RuntimeFactory: h.factory.build, Clock: h.clock.Now, LaunchAtLoginOfferCapable: true, Prober: h.prober}
	for _, c := range configure {
		c(h)
	}
	h.start()
	t.Cleanup(h.stop)
	return h
}

func (h *harness) start() {
	h.t.Helper()
	store, err := Open(h.dir, h.storeOpt)
	if err != nil {
		h.t.Fatal(err)
	}
	h.store = store
	engine, err := New(store, h.opts)
	if err != nil {
		h.t.Fatal(err)
	}
	h.engine = engine
	h.session = engine.Connect()
	engine.sync()
}

func (h *harness) stop() {
	if h.engine == nil {
		return
	}
	h.factory.set(func(f *fakeFactory) {
		if f.closeBlock != nil {
			close(f.closeBlock)
			f.closeBlock = nil
		}
		f.closeErr = nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.engine.Close(ctx); err != nil {
		h.t.Errorf("Engine.Close: %v", err)
	}
	h.store.Close()
	h.engine = nil
}

// restart closes the engine and store and opens both again, like a service restart.
func (h *harness) restart() {
	h.t.Helper()
	h.stop()
	h.start()
}

func (h *harness) req() Request {
	return Request{ID: newUUID(), IssuedAt: h.clock.Now(), ExpectedRevision: h.session.GetSnapshot().Revision}
}

func (h *harness) settle() { h.engine.sync() }

func (h *harness) chooseLogging(logging installation.Logging) {
	h.t.Helper()
	if _, err := h.session.SetOperationalLogging(h.req(), SetOperationalLoggingParams{Logging: logging}); err != nil {
		h.t.Fatal(err)
	}
	h.settle()
}

func (h *harness) create(name string, destination portal.Destination) string {
	h.t.Helper()
	receipt, err := h.session.CreatePortal(h.req(), CreatePortalParams{Name: name, Destination: destination, PublicAccess: portal.PublicAccessModePrivate})
	if err != nil {
		h.t.Fatal(err)
	}
	h.settle()
	return receipt.PortalID
}

func (h *harness) disk() State {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.dir, h.store.generation, stateFileName))
	if err != nil {
		h.t.Fatal(err)
	}
	state, err := decodeState(data)
	if err != nil {
		h.t.Fatal(err)
	}
	return state
}

func (h *harness) portal(id string) *PortalSnapshot {
	for _, p := range h.session.GetSnapshot().Portals {
		if p.ID == id {
			return &p
		}
	}
	return nil
}

func wantCode(t *testing.T, err error, code Code) {
	t.Helper()
	if !IsCode(err, code) {
		t.Fatalf("error = %v, want %s", err, code)
	}
}

func localApp(port uint16) portal.Destination {
	return portal.Destination{Kind: portal.DestinationLocalApp, Port: port}
}

func remoteApp(host string, port uint16) portal.Destination {
	return portal.Destination{Kind: portal.DestinationRemoteApp, Scheme: "https", Host: host, Port: port}
}

func online(name, suffix, tailnet string) portal.StatusEvent {
	return portal.StatusEvent{
		State:          portal.StateOnline,
		StableNodeID:   "nStable123CNTRL",
		AssignedName:   name,
		PortalURL:      "https://" + name + "." + suffix + "/",
		Addresses:      []string{"100.64.0.1", "fd7a:115c:a1e0::1"},
		TailnetName:    tailnet,
		MagicDNSSuffix: suffix,
	}
}

func TestLoggingChoiceGatesCreate(t *testing.T) {
	h := newHarness(t)
	snapshot := h.session.GetSnapshot()
	if snapshot.Service.Runtime != runtimeLoggingChoiceRequired || snapshot.Availability[OpCreatePortal].Reason != CodeLoggingChoiceRequired {
		t.Fatalf("undecided snapshot = %+v", snapshot)
	}
	_, err := h.session.CreatePortal(h.req(), CreatePortalParams{Name: "hermes", Destination: localApp(8787), PublicAccess: portal.PublicAccessModePrivate})
	wantCode(t, err, CodeLoggingChoiceRequired)
	if names := h.factory.callNames(); len(names) != 0 {
		t.Fatalf("runtime touched before a logging choice: %v", names)
	}

	h.chooseLogging(installation.LoggingEnabled)
	if builds := h.factory.callsNamed("build:enabled"); len(builds) != 1 || builds[0].state.Installation.Logging != installation.LoggingEnabled {
		t.Fatalf("builds = %+v, want one after the committed choice", builds)
	}
	id := h.create("hermes", localApp(8787))
	if len(h.disk().Installation.Portals) != 1 || h.portal(id) == nil {
		t.Fatal("Portal not created after the logging choice")
	}
}

func TestLocalAndRemoteCreateEachCommitOneRecordAndReconcile(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	for _, destination := range []portal.Destination{localApp(8787), remoteApp("app.example.com", 8443)} {
		before := h.factory.runtime().reconcileCount()
		id := h.create("hermes", destination)
		rt := h.factory.runtime()
		if rt.reconcileCount() != before+1 {
			t.Fatalf("reconciles = %d, want %d", rt.reconcileCount(), before+1)
		}
		last := rt.lastReconcile()
		i := slices.IndexFunc(last, func(c portal.Config) bool { return c.ID == id })
		if i < 0 || last[i].Destination != destination || last[i].DesiredState != portal.DesiredStateEnabled {
			t.Fatalf("reconcile = %+v, want %s enabled at %+v", last, id, destination)
		}
		reconciled := h.factory.callsNamed("reconcile")
		if d := reconciled[len(reconciled)-1].state; portalIndex(&d, id) < 0 {
			t.Fatal("reconcile ran before the Portal was durable")
		}
	}
	if n := len(h.disk().Installation.Portals); n != 2 {
		t.Fatalf("records = %d, want 2", n)
	}
}

func TestValidateCreate(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	revision := h.session.GetSnapshot().Revision
	for _, tc := range []struct {
		name  string
		p     CreatePortalParams
		field string
	}{
		{"uppercase name", CreatePortalParams{Name: "Hermes", Destination: localApp(1), PublicAccess: portal.PublicAccessModePrivate}, "name"},
		{"empty name", CreatePortalParams{Name: "", Destination: localApp(1), PublicAccess: portal.PublicAccessModePrivate}, "name"},
		{"leading hyphen", CreatePortalParams{Name: "-x", Destination: localApp(1), PublicAccess: portal.PublicAccessModePrivate}, "name"},
		{"long name", CreatePortalParams{Name: strings.Repeat("a", 64), Destination: localApp(1), PublicAccess: portal.PublicAccessModePrivate}, "name"},
		{"unknown public access", CreatePortalParams{Name: "x", Destination: localApp(1), PublicAccess: "funnel"}, "publicAccess"},
		{"public Remote App", CreatePortalParams{Name: "x", Destination: remoteApp("app.example.com", 443), PublicAccess: portal.PublicAccessModePublic}, "publicAccess"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.session.CreatePortal(h.req(), tc.p)
			var e *Error
			if !errors.As(err, &e) || e.Code != CodeInvalidRequest || e.Field != tc.field {
				t.Fatalf("error = %v, want invalidRequest on %s", err, tc.field)
			}
		})
	}
	if h.session.GetSnapshot().Revision != revision || len(h.disk().Installation.Portals) != 0 {
		t.Fatal("an invalid create changed state")
	}
}

func TestInvalidDestinationRejected(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	reconciles := h.factory.runtime().reconcileCount()
	for _, d := range []portal.Destination{
		localApp(0),
		{Kind: "ftp", Port: 21},
		remoteApp("localhost", 80),
		remoteApp("127.0.0.1", 80),
		remoteApp("::1", 80),
		remoteApp("0.0.0.0", 80),
		remoteApp("App.Example.com", 80),
		remoteApp("app.example.com", 0),
		{Kind: portal.DestinationRemoteApp, Scheme: "ftp", Host: "app.example.com", Port: 21},
	} {
		_, err := h.session.CreatePortal(h.req(), CreatePortalParams{Name: "x", Destination: d, PublicAccess: portal.PublicAccessModePrivate})
		var e *Error
		if !errors.As(err, &e) || e.Code != CodeInvalidRequest || e.Field != "destination" {
			t.Fatalf("%+v: error = %v, want invalidRequest on destination", d, err)
		}
	}
	h.settle()
	if len(h.disk().Installation.Portals) != 0 || h.factory.runtime().reconcileCount() != reconciles {
		t.Fatal("an invalid destination reached state or the runtime")
	}
}

func TestDiscoveryAndManualCreate(t *testing.T) {
	t.Run("unavailable discovery leaves explicit ports working", func(t *testing.T) {
		h := newHarness(t)
		h.chooseLogging(installation.LoggingEnabled)
		_, err := h.session.DiscoverLocalApps(context.Background())
		wantCode(t, err, CodeCapabilityUnavailable)
		snapshot := h.session.GetSnapshot()
		if snapshot.Service.AutomaticDiscovery || snapshot.Availability[OpDiscoverLocalApps].Reason != CodeCapabilityUnavailable {
			t.Fatalf("snapshot = %+v", snapshot.Availability)
		}
		h.create("manual", localApp(9000))
	})
	t.Run("discovery is bounded and creates nothing", func(t *testing.T) {
		d := &fakeDiscoverer{}
		h := newHarness(t, func(h *harness) { h.opts.Discoverer = d })
		h.chooseLogging(installation.LoggingEnabled)
		revision := h.session.GetSnapshot().Revision
		candidates, err := h.session.DiscoverLocalApps(context.Background())
		if err != nil || len(candidates) != 1 || candidates[0].LocalAppPort != 3000 {
			t.Fatalf("DiscoverLocalApps() = %+v, %v", candidates, err)
		}
		if d.deadline <= 0 || d.deadline > discoveryBudget {
			t.Fatalf("discovery budget = %v, want at most %v", d.deadline, discoveryBudget)
		}
		// NoCreateBeforeSubmit: choosing a candidate is not a Create.
		if h.session.GetSnapshot().Revision != revision || len(h.disk().Installation.Portals) != 0 {
			t.Fatal("discovery changed state")
		}
	})
}

func TestNoCreateBeforeSubmit(t *testing.T) {
	d := &fakeDiscoverer{}
	h := newHarness(t, func(h *harness) { h.opts.Discoverer = d })
	h.chooseLogging(installation.LoggingEnabled)
	before := h.disk()
	for range 3 {
		if _, err := h.session.DiscoverLocalApps(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if after := h.disk(); after.Revision != before.Revision || len(after.Requests) != len(before.Requests) {
		t.Fatal("discovery recorded a mutation")
	}
}

func TestDurableAcceptanceBeforeRuntimeAndFailedCommit(t *testing.T) {
	failCommit := false
	var mu sync.Mutex
	h := newHarness(t, func(h *harness) {
		h.storeOpt = Options{Hook: func(p Phase) error {
			mu.Lock()
			defer mu.Unlock()
			if failCommit && p == PhaseCommitRename {
				return errors.New("injected")
			}
			return nil
		}}
	})
	h.chooseLogging(installation.LoggingEnabled)
	receipt, err := h.session.CreatePortal(h.req(), CreatePortalParams{Name: "hermes", Destination: localApp(8787), PublicAccess: portal.PublicAccessModePrivate})
	if err != nil {
		t.Fatal(err)
	}
	h.settle()
	reconciles := h.factory.callsNamed("reconcile")
	last := reconciles[len(reconciles)-1].state
	if last.Revision < receipt.AcceptedRevision || findReceipt(last, receipt.RequestID) == nil {
		t.Fatalf("runtime ran at disk revision %d before receipt revision %d was durable", last.Revision, receipt.AcceptedRevision)
	}

	mu.Lock()
	failCommit = true
	mu.Unlock()
	count := h.factory.runtime().reconcileCount()
	_, err = h.session.SetDesiredState(h.req(), SetDesiredStateParams{PortalID: receipt.PortalID, State: portal.DesiredStateStopped})
	wantCode(t, err, CodePersistenceUnavailable)
	h.settle()
	if h.factory.runtime().reconcileCount() != count {
		t.Fatal("a failed commit woke the runtime")
	}
	if last := h.factory.runtime().lastReconcile(); last[0].DesiredState != portal.DesiredStateEnabled {
		t.Fatal("an unacknowledged stop reached the runtime")
	}
	snapshot := h.session.GetSnapshot()
	if snapshot.Service.PersistenceHealthy || snapshot.Availability[OpCreatePortal].Reason != CodePersistenceUnavailable {
		t.Fatalf("snapshot after failed commit = %+v", snapshot.Service)
	}
	mu.Lock()
	failCommit = false
	mu.Unlock()
	_, err = h.session.SetDesiredState(h.req(), SetDesiredStateParams{PortalID: receipt.PortalID, State: portal.DesiredStateStopped})
	wantCode(t, err, CodePersistenceUnavailable)
	if p := h.portal(receipt.PortalID); p.Availability[OpStop].Reason != CodePersistenceUnavailable || p.Availability[OpRemovePortal].Reason != CodePersistenceUnavailable {
		t.Fatalf("Portal availability after a failed commit = %+v", p.Availability)
	}

	h.restart()
	if _, err := h.session.SetDesiredState(h.req(), SetDesiredStateParams{PortalID: receipt.PortalID, State: portal.DesiredStateStopped}); err != nil {
		t.Fatalf("writes did not resume after reload: %v", err)
	}
}

func TestSetDesiredStateIsIdempotentIntent(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	id := h.create("hermes", localApp(8787))
	for _, tc := range []struct {
		state   portal.DesiredState
		outcome Outcome
	}{
		{portal.DesiredStateStopped, OutcomeAccepted},
		{portal.DesiredStateStopped, OutcomeNoOp},
		{portal.DesiredStateEnabled, OutcomeAccepted},
		{portal.DesiredStateEnabled, OutcomeNoOp},
	} {
		receipt, err := h.session.SetDesiredState(h.req(), SetDesiredStateParams{PortalID: id, State: tc.state})
		if err != nil || receipt.Outcome != tc.outcome {
			t.Fatalf("SetDesiredState(%s) = %+v, %v; want %s", tc.state, receipt, err, tc.outcome)
		}
		h.settle()
		if last := h.factory.runtime().lastReconcile(); len(last) != 1 || last[0].DesiredState != tc.state {
			t.Fatalf("reconcile = %+v, want %s", last, tc.state)
		}
	}
	_, err := h.session.SetDesiredState(h.req(), SetDesiredStateParams{PortalID: id, State: "paused"})
	wantCode(t, err, CodeInvalidRequest)
	_, err = h.session.SetDesiredState(h.req(), SetDesiredStateParams{PortalID: newUUID(), State: portal.DesiredStateStopped})
	wantCode(t, err, CodeNotFound)
}

func TestEngineCloseHonoursDeadline(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	h.factory.set(func(f *fakeFactory) { f.closeBlock = make(chan struct{}) })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := h.engine.Close(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 2*time.Second {
		t.Fatalf("Close() = %v after %v, want the deadline", err, time.Since(started))
	}
	// Ownership stays with the failed engine's store.
	if _, err := Open(h.dir, Options{}); !errors.Is(err, ErrOwnershipBusy) {
		t.Fatalf("Open() after a failed close = %v, want ownershipBusy", err)
	}
	// A failed close is final: a retry never reports success.
	h.factory.set(func(f *fakeFactory) { close(f.closeBlock); f.closeBlock = nil })
	if err := h.engine.Close(context.Background()); !IsCode(err, CodeRuntimeUnavailable) {
		t.Fatalf("retried Close() = %v, want runtimeUnavailable", err)
	}
	h.store.Close()
	h.engine = nil
}

func TestSnapshotFactsAndAvailability(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	id := h.create("hermes", localApp(8787))
	rt := h.factory.runtime()

	rt.emitStatus(id, portal.StatusEvent{State: portal.StateAuthenticating})
	p := h.portal(id)
	if p.Connection == nil || p.Connection.State != portal.StateAuthenticating || p.Availability[OpCopyPortalURL].Available || p.Availability[OpOpenPortalURL].Available {
		t.Fatalf("authenticating Portal = %+v", p)
	}

	rt.emitStatus(id, online("hermes", "one.ts.net", "opaque-tailnet"))
	p = h.portal(id)
	c := p.Connection
	if c.State != portal.StateOnline || c.AssignedName != "hermes" || c.PortalURL != "https://hermes.one.ts.net/" ||
		len(c.Addresses) != 2 || c.Freshness != freshnessCurrent {
		t.Fatalf("online facts = %+v", c)
	}
	if !p.Availability[OpCopyPortalURL].Available || !p.Availability[OpOpenPortalURL].Available {
		t.Fatalf("online availability = %+v", p.Availability)
	}
	data, _ := json.Marshal(h.session.GetSnapshot())
	for _, secret := range []string{"nStable123CNTRL", "opaque-tailnet", h.dir} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("snapshot exposes %q: %s", secret, data)
		}
	}

	// A replaced runtime leaves last-known facts: copy stays, open does not.
	h.chooseLogging(installation.LoggingDisabled)
	p = h.portal(id)
	if p.Connection == nil || p.Connection.Freshness != freshnessLastKnown || !p.Availability[OpCopyPortalURL].Available || p.Availability[OpOpenPortalURL].Available {
		t.Fatalf("last-known Portal = %+v", p)
	}
	// The superseded runtime's events are fenced out.
	rt.emitStatus(id, portal.StatusEvent{State: portal.StateError})
	if h.portal(id).Connection.State != portal.StateOnline {
		t.Fatal("an event from a superseded runtime changed the snapshot")
	}
}

func TestHostileStatusFactsAreSanitized(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	id := h.create("hermes", localApp(8787))
	h.factory.runtime().emitStatus(id, portal.StatusEvent{
		State:          "pwned",
		StableNodeID:   "nSecret",
		AssignedName:   "Evil Name",
		PortalURL:      "https://user:pass@evil.example/?token=1",
		Addresses:      []string{"100.64.0.1", "not-an-ip", "1.2.3.4; rm -rf", "fe80::1%https://login.example/secret"},
		MagicDNSSuffix: "evil.example",
	})
	c := h.portal(id).Connection
	if c.State != portal.StateError || c.AssignedName != "" || c.PortalURL != "" || c.MagicDNSSuffix != "" || !slices.Equal(c.Addresses, []string{"100.64.0.1"}) {
		t.Fatalf("hostile facts reached the snapshot: %+v", c)
	}
	report, err := h.session.GetDiagnostics("")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"Evil", "evil.example", "user:pass", "token", "nSecret", "rm -rf", "secret", h.dir} {
		if strings.Contains(report, bad) {
			t.Fatalf("diagnostics contain %q:\n%s", bad, report)
		}
	}
	if !strings.Contains(report, "Portal Name: hermes") {
		t.Fatalf("diagnostics = %s", report)
	}
	if _, err := h.session.GetDiagnostics(newUUID()); !IsCode(err, CodeNotFound) {
		t.Fatalf("GetDiagnostics(unknown) = %v", err)
	}
}

func TestRestartRecovery(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	enabled := h.create("enabled", localApp(8001))
	stopped := h.create("stopped", localApp(8002))
	if _, err := h.session.SetDesiredState(h.req(), SetDesiredStateParams{PortalID: stopped, State: portal.DesiredStateStopped}); err != nil {
		t.Fatal(err)
	}
	h.settle()
	h.restart()
	rt := h.factory.runtime()
	first := rt.reconciles[0]
	states := map[string]portal.DesiredState{}
	for _, c := range first {
		states[c.ID] = c.DesiredState
	}
	if states[enabled] != portal.DesiredStateEnabled || states[stopped] != portal.DesiredStateStopped {
		t.Fatalf("first reconcile after restart = %+v", first)
	}
}

func TestMigrationMarkerSetByFirstAcceptedMutation(t *testing.T) {
	h := newHarness(t)
	if h.disk().Migration.FirstMutationRequestID != "" {
		t.Fatal("marker set before any mutation")
	}
	req := h.req()
	if _, err := h.session.SetOperationalLogging(req, SetOperationalLoggingParams{Logging: installation.LoggingEnabled}); err != nil {
		t.Fatal(err)
	}
	disk := h.disk()
	if disk.Migration.FirstMutationRequestID != req.ID || findReceipt(disk, req.ID) == nil {
		t.Fatalf("marker = %q, want %q in the same commit as its receipt", disk.Migration.FirstMutationRequestID, req.ID)
	}
	h.create("hermes", localApp(1))
	if h.disk().Migration.FirstMutationRequestID != req.ID {
		t.Fatal("a later mutation moved the marker")
	}
}

func TestSubscribeDeliversInitialSnapshotThenReplacements(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	var got []Snapshot
	h.session.Subscribe(func(s Snapshot) {
		mu.Lock()
		got = append(got, s)
		mu.Unlock()
	})
	h.chooseLogging(installation.LoggingEnabled)
	h.create("hermes", localApp(1))
	mu.Lock()
	defer mu.Unlock()
	if len(got) < 3 {
		t.Fatalf("snapshots = %d", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Sequence <= got[i-1].Sequence || got[i].Epoch != got[0].Epoch {
			t.Fatalf("snapshot %d out of order: %d after %d", i, got[i].Sequence, got[i-1].Sequence)
		}
	}
	if got[len(got)-1].Revision != h.session.GetSnapshot().Revision {
		t.Fatal("last delivered snapshot is not current")
	}
	h.session.Close()
	n := len(got)
	mu.Unlock()
	h.create("atlas", localApp(2))
	mu.Lock()
	if len(got) != n {
		t.Fatal("a closed session still received snapshots")
	}
}

func TestUncertainCommitIsNeverReportedNotAccepted(t *testing.T) {
	var fail bool
	h := newHarness(t, func(h *harness) {
		h.storeOpt = Options{Hook: func(p Phase) error {
			if fail && p == PhaseCommitFsync {
				return errors.New("injected")
			}
			return nil
		}}
	})
	h.chooseLogging(installation.LoggingEnabled)
	fail = true
	req := h.req()
	_, err := h.session.CreatePortal(req, hermesParams)
	wantCode(t, err, CodePersistenceUnavailable)
	// The renamed document may hold the receipt; only a reload can tell.
	_, err = h.session.GetRequestResult(req.ID, req.IssuedAt)
	wantCode(t, err, CodePersistenceUnavailable)
	fail = false
	h.restart()
	result, err := h.session.GetRequestResult(req.ID, req.IssuedAt)
	if err != nil || result.Receipt == nil {
		t.Fatalf("after reload = %+v, %v; want the durable receipt", result, err)
	}
}

func TestReconcileFailureFailsPendingRemoval(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	id := h.create("hermes", localApp(1))
	h.factory.set(func(f *fakeFactory) { f.reconcileErr = errors.New("runtime closing") })
	if _, err := h.session.RemovePortal(h.req(), RemovePortalParams{PortalID: id, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if len(h.factory.callsNamed("remove:"+id)) != 0 {
		t.Fatal("identity deleted although the reconcile that closes the node failed")
	}
	p := h.portal(id)
	if p.Problem != ProblemRemovalFailed || !p.Availability[OpRetryRemoval].Available {
		t.Fatalf("removing Portal after a failed reconcile = %+v", p)
	}
	h.factory.set(func(f *fakeFactory) { f.reconcileErr = nil })
	if _, err := h.session.RetryRemoval(h.req(), RetryRemovalParams{PortalID: id}); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if h.portal(id) != nil {
		t.Fatal("retryRemoval did not finish the removal")
	}
}

func TestSubscribeAfterCloseIsIgnored(t *testing.T) {
	h := newHarness(t)
	session := h.session
	h.stop()
	called := false
	session.Subscribe(func(Snapshot) { called = true })
	if called {
		t.Fatal("Subscribe delivered after Engine.Close")
	}
}

func TestEachRuntimeCallGetsItsOwnDeadline(t *testing.T) {
	// Restore after the harness's cleanup has stopped the engine.
	timeout := runtimeCallTimeout
	t.Cleanup(func() { runtimeCallTimeout = timeout })
	runtimeCallTimeout = 100 * time.Millisecond
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	a := h.create("a", localApp(1))
	b := h.create("b", localApp(2))
	h.factory.set(func(f *fakeFactory) { f.reconcileDelay = 150 * time.Millisecond })
	for _, id := range []string{a, b} {
		if _, err := h.session.RemovePortal(h.req(), RemovePortalParams{PortalID: id, Confirmed: true}); err != nil {
			t.Fatal(err)
		}
	}
	h.settle()
	h.factory.mu.Lock()
	expired := h.factory.expiredCalls
	h.factory.mu.Unlock()
	if expired != 0 || len(h.factory.callsNamed("remove:"+a))+len(h.factory.callsNamed("remove:"+b)) == 0 {
		t.Fatalf("%d removal calls arrived after a slow reconcile used up their deadline", expired)
	}
}
