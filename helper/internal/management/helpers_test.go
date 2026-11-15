package management

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/chrisbanes/portico/helper/internal/discovery"
	"github.com/chrisbanes/portico/helper/internal/installation"
	"github.com/chrisbanes/portico/helper/internal/ipc"
	"github.com/chrisbanes/portico/helper/internal/portal"
	"github.com/chrisbanes/portico/helper/internal/service"
)

// fakeFactory builds fake runtimes and counts them.
type fakeFactory struct {
	mu        sync.Mutex
	runtimes  []*fakeRuntime
	buildErr  error
	removeErr error
}

func (f *fakeFactory) build(string, installation.Logging) (service.Runtime, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.buildErr != nil {
		return nil, f.buildErr
	}
	r := &fakeRuntime{f: f}
	f.runtimes = append(f.runtimes, r)
	return r, nil
}

func (f *fakeFactory) set(fn func(*fakeFactory)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeFactory) builds() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.runtimes)
}

func (f *fakeFactory) runtime() *fakeRuntime {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.runtimes) == 0 {
		return nil
	}
	return f.runtimes[len(f.runtimes)-1]
}

// starts counts the runtimes whose reconciles started the Portal.
func (f *fakeFactory) starts(id string) int {
	f.mu.Lock()
	runtimes := slices.Clone(f.runtimes)
	f.mu.Unlock()
	n := 0
	for _, r := range runtimes {
		if r.configured(id) {
			n++
		}
	}
	return n
}

// fakeRuntime records Reconcile configs and login attempts, and keeps the
// emitter so tests can report status and authentication URLs.
type fakeRuntime struct {
	f *fakeFactory

	mu         sync.Mutex
	reconciles [][]portal.Config
	emit       func(portal.Event)
	attempts   map[string]string // Portal ID -> latest attempt nonce
}

func (r *fakeRuntime) Reconcile(_ context.Context, configs []portal.Config, emit func(portal.Event)) ([]portal.ReconcileEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reconciles = append(r.reconciles, slices.Clone(configs))
	r.emit = emit
	var entries []portal.ReconcileEntry
	for _, c := range configs {
		entries = append(entries, portal.ReconcileEntry{PortalID: c.ID, Outcome: portal.OutcomeConverged})
	}
	return entries, nil
}

func (r *fakeRuntime) AuthenticateAttempt(_ context.Context, id, attempt string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.attempts == nil {
		r.attempts = map[string]string{}
	}
	r.attempts[id] = attempt
	return nil
}

func (r *fakeRuntime) CleanupRejectedPortal(context.Context, string) error { return nil }

func (r *fakeRuntime) RemovePortal(context.Context, string) error {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	return r.f.removeErr
}

func (r *fakeRuntime) Close(context.Context) error { return nil }

func (r *fakeRuntime) configured(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, configs := range r.reconciles {
		if slices.ContainsFunc(configs, func(c portal.Config) bool { return c.ID == id }) {
			return true
		}
	}
	return false
}

func (r *fakeRuntime) reconcileCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reconciles)
}

func (r *fakeRuntime) attempt(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts[id]
}

func (r *fakeRuntime) status(id string, state portal.State) {
	r.mu.Lock()
	emit := r.emit
	r.mu.Unlock()
	emit(portal.Event{PortalID: id, Status: &portal.StatusEvent{State: state}})
}

func (r *fakeRuntime) url(id, attempt, rawURL string) {
	r.mu.Lock()
	emit := r.emit
	r.mu.Unlock()
	emit(portal.Event{PortalID: id, AuthenticationURL: rawURL, AuthenticationAttempt: attempt})
}

type unreachable struct{}

func (unreachable) Reachable(context.Context, uint16) bool { return false }

type fakeDiscoverer struct{}

func (fakeDiscoverer) Discover(context.Context) ([]discovery.Candidate, error) {
	return []discovery.Candidate{{LocalAppPort: 3000, ProcessLabel: "node", SuggestedPortalName: "web"}}, nil
}

// harness runs a real engine on a temporary store behind a real socket.
type harness struct {
	t        *testing.T
	root     string // state root
	base     string // endpoint base directory
	endpoint ipc.Endpoint
	lock     *os.File
	limits   ipc.Limits
	opts     service.EngineOptions
	factory  *fakeFactory
	seed     string // installation-v5.json written before the first start

	store    *service.Store
	engine   *service.Engine
	listener *ipc.Listener
	served   chan struct{}
}

func newHarness(t *testing.T, configure ...func(*harness)) *harness {
	t.Helper()
	h := &harness{t: t, root: t.TempDir(), base: shortTemp(t), limits: ipc.DefaultLimits, factory: &fakeFactory{}}
	h.limits.FrameTimeout = time.Second
	h.opts = service.EngineOptions{RuntimeFactory: h.factory.build, Prober: unreachable{}, LaunchAtLoginOfferCapable: true}
	for _, c := range configure {
		c(h)
	}
	if h.seed != "" {
		if err := os.WriteFile(filepath.Join(h.root, "installation-v5.json"), []byte(h.seed), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	endpoint, err := ipc.EndpointIn(h.base, ipc.Test)
	if err != nil {
		t.Fatal(err)
	}
	h.endpoint = endpoint
	h.lock, err = os.OpenFile(filepath.Join(t.TempDir(), "service-owner.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.lock.Close() })
	h.start()
	t.Cleanup(h.stop)
	return h
}

// shortTemp is a base directory short enough for sun_path, unlike t.TempDir.
func shortTemp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "mgmt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func (h *harness) start() {
	h.t.Helper()
	store, err := service.Open(h.root, service.Options{})
	if err != nil {
		h.t.Fatal(err)
	}
	engine, err := service.New(store, h.opts)
	if err != nil {
		h.t.Fatal(err)
	}
	listener, err := ipc.Listen(h.endpoint, h.lock, h.limits)
	if err != nil {
		h.t.Fatal(err)
	}
	h.store, h.engine, h.listener, h.served = store, engine, listener, make(chan struct{})
	go func() {
		defer close(h.served)
		if err := Serve(listener, h.limits, engine); err != nil {
			h.t.Errorf("Serve: %v", err)
		}
	}()
}

// stop shuts down in host order: the transport, then the engine, then the store.
func (h *harness) stop() {
	if h.engine == nil {
		return
	}
	h.listener.Close()
	<-h.served
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.engine.Close(ctx); err != nil {
		h.t.Errorf("Engine.Close: %v", err)
	}
	h.store.Close()
	h.engine = nil
}

func (h *harness) restart() {
	h.t.Helper()
	h.stop()
	h.start()
}

func (h *harness) dial() *Client { return h.dialRecording(nil) }

func (h *harness) dialRecording(record func([]byte)) *Client {
	h.t.Helper()
	c, err := dial(h.t.Context(), h.endpoint, record)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { c.Close() })
	return c
}

func (h *harness) raw(t *testing.T) *net.UnixConn {
	t.Helper()
	c, err := ipc.Dial(t.Context(), h.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

func snapshot(t *testing.T, c *Client) service.Snapshot {
	t.Helper()
	var s service.Snapshot
	if err := c.Call(ctx(t), "getSnapshot", nil, nil, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func newMutation(t *testing.T, c *Client) *Mutation {
	t.Helper()
	return &Mutation{RequestID: newUUID(), IssuedAt: time.Now(), ExpectedRevision: snapshot(t, c).Revision}
}

func mutate(t *testing.T, c *Client, method string, params any) (Receipt, error) {
	t.Helper()
	var r Receipt
	err := c.Call(ctx(t), method, newMutation(t, c), params, &r)
	return r, err
}

// tryMutate is mutate for goroutines other than the test's: it never fails t.
func tryMutate(t *testing.T, c *Client, method string, params any) error {
	var s service.Snapshot
	if err := c.Call(ctx(t), "getSnapshot", nil, nil, &s); err != nil {
		return err
	}
	return c.Call(ctx(t), method, &Mutation{RequestID: newUUID(), IssuedAt: time.Now(), ExpectedRevision: s.Revision}, params, nil)
}

func mustMutate(t *testing.T, c *Client, method string, params any) Receipt {
	t.Helper()
	r, err := mutate(t, c, method, params)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return r
}

func wantCode(t *testing.T, err error, code service.Code, field string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || string(e.Code) != string(code) || e.Field != field {
		t.Fatalf("error = %v, want %s (field %q)", err, code, field)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitSnapshot(t *testing.T, c *Client, what string, cond func(service.Snapshot) bool) service.Snapshot {
	t.Helper()
	var s service.Snapshot
	waitFor(t, what, func() bool { s = snapshot(t, c); return cond(s) })
	return s
}

func findPortal(s service.Snapshot, id string) *service.PortalSnapshot {
	for i := range s.Portals {
		if s.Portals[i].ID == id {
			return &s.Portals[i]
		}
	}
	return nil
}

func chooseLogging(t *testing.T, c *Client) {
	t.Helper()
	mustMutate(t, c, "setOperationalLogging", service.SetOperationalLoggingParams{Logging: installation.LoggingEnabled})
}

func localApp(port uint16) portal.Destination {
	return portal.Destination{Kind: portal.DestinationLocalApp, Port: port}
}

func createPortal(t *testing.T, c *Client, name string) string {
	t.Helper()
	return mustMutate(t, c, "createPortal", service.CreatePortalParams{Name: name, Destination: localApp(8080), PublicAccess: portal.PublicAccessModePrivate}).PortalID
}

// frame is a decoded raw server frame.
type frame map[string]any

func writeJSON(t *testing.T, c net.Conn, v any) {
	t.Helper()
	payload, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := ipc.WriteFrame(c, payload); err != nil {
		t.Fatal(err)
	}
}

func readRaw(t *testing.T, c net.Conn) (frame, error) {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	payload, err := ipc.ReadFrame(c, ipc.MaxFrame)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			t.Fatal("timed out reading a frame")
		}
		return nil, err
	}
	var f frame
	if err := json.Unmarshal(payload, &f); err != nil {
		t.Fatal(err)
	}
	return f, nil
}

func readJSON(t *testing.T, c net.Conn) frame {
	t.Helper()
	f, err := readRaw(t, c)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// readUntilClosed returns every frame until the server closes c.
func readUntilClosed(t *testing.T, c net.Conn) []frame {
	t.Helper()
	var frames []frame
	for {
		f, err := readRaw(t, c)
		if err != nil {
			return frames
		}
		frames = append(frames, f)
	}
}

func rawHello(t *testing.T, c net.Conn) {
	t.Helper()
	writeJSON(t, c, map[string]any{"type": "hello", "version": 1})
	if f := readJSON(t, c); f["type"] != "hello" {
		t.Fatalf("hello reply = %v", f)
	}
}

func rawCall(method string, params any) map[string]any {
	return map[string]any{"type": "call", "callID": newUUID(), "method": method, "params": params}
}

func rawMutation(method string, params any, revision uint64) map[string]any {
	call := rawCall(method, params)
	call["requestID"] = newUUID()
	call["issuedAt"] = time.Now().UTC().Format(time.RFC3339)
	call["expectedRevision"] = revision
	return call
}

// errorCode is a reply's or error frame's error code.
func errorCode(f frame) string {
	e, _ := f["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}
