package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chrisbanes/portico/helper/internal/installation"
	"github.com/chrisbanes/portico/helper/internal/portal"
)

// The fake runtime's URL; the test proves it reaches nothing but the
// requester's sink.
const enrolmentURL = "https://login.tailscale.com/a/0123456789abcdef"

type eventLog struct {
	mu     sync.Mutex
	events []EnrolmentEvent
}

func (l *eventLog) sink(e EnrolmentEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
}

func (l *eventLog) all() []EnrolmentEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]EnrolmentEvent(nil), l.events...)
}

func (l *eventLog) of(kind EnrolmentEventKind) []EnrolmentEvent {
	var matched []EnrolmentEvent
	for _, e := range l.all() {
		if e.Kind == kind {
			matched = append(matched, e)
		}
	}
	return matched
}

// connect opens a session that records its private events and snapshots.
func (h *harness) connect() (*Session, *eventLog, *[]Snapshot) {
	s := h.engine.Connect()
	events := &eventLog{}
	s.ReceiveEnrolmentEvents(events.sink)
	var snapshots []Snapshot
	var mu sync.Mutex
	s.Subscribe(func(snap Snapshot) {
		mu.Lock()
		snapshots = append(snapshots, snap)
		mu.Unlock()
	})
	return s, events, &snapshots
}

// waitFor polls until cond holds; runtime calls from the engine are async.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func (h *harness) authenticating(id string) {
	h.factory.runtime().emitStatus(id, portal.StatusEvent{State: portal.StateAuthenticating})
}

func TestEnrolmentResumeAcrossDisconnectAndRestart(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	id := h.create("hermes", localApp(8787))
	h.authenticating(id)
	identity := filepath.Join(h.store.IdentityRoot(), id)
	if err := os.MkdirAll(identity, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(identity, "tailscaled.state"), []byte("node-key"), 0o600)

	var exposed []string
	record := func(v any) {
		data, _ := json.Marshal(v)
		exposed = append(exposed, string(data))
	}

	// 1. Session A resumes and disconnects before the URL arrives.
	a, aEvents, aSnapshots := h.connect()
	_, observerEvents, observerSnapshots := h.connect()
	aReq := h.req()
	aReceipt, err := a.ResumeEnrolment(aReq, ResumeEnrolmentParams{PortalID: id})
	if err != nil {
		t.Fatal(err)
	}
	record(aReceipt)
	waitFor(t, "Authenticate for A", func() bool { return len(h.factory.callsNamed("auth:"+id)) == 1 })
	if call := h.factory.callsNamed("auth:" + id)[0]; findReceipt(call.state, aReq.ID) == nil {
		t.Fatal("Authenticate ran before the resume receipt was durable")
	}
	if p := h.portal(id); p.Enrolment == nil || p.Enrolment.Status != enrolmentActive {
		t.Fatalf("enrolment during A's attempt = %+v", p.Enrolment)
	}
	a.Close()

	// 2. The record and Enabled intent survive; the URL for A is dropped.
	h.factory.runtime().emitURL(id, enrolmentURL)
	p := h.portal(id)
	if p == nil || p.DesiredState != portal.DesiredStateEnabled || p.Enrolment == nil || p.Enrolment.Status != enrolmentInterrupted {
		t.Fatalf("Portal after A disconnected = %+v", p)
	}
	if len(aEvents.all()) != 0 {
		t.Fatalf("a disconnected session received %+v", aEvents.all())
	}

	// 3. Session B resumes; the runtime re-sends the same URL. Only B gets it, once.
	b, bEvents, _ := h.connect()
	bReq := h.req()
	bReceipt, err := b.ResumeEnrolment(bReq, ResumeEnrolmentParams{PortalID: id})
	if err != nil {
		t.Fatal(err)
	}
	record(bReceipt)
	waitFor(t, "Authenticate for B", func() bool { return len(h.factory.callsNamed("auth:"+id)) == 2 })
	h.factory.runtime().emitURL(id, enrolmentURL)
	h.factory.runtime().emitURL(id, enrolmentURL)
	urls := bEvents.of(EnrolmentAuthenticationURL)
	if len(urls) != 1 || urls[0].URL != enrolmentURL || urls[0].RequestID != bReq.ID || urls[0].PortalID != id || urls[0].Nonce == "" {
		t.Fatalf("B's events = %+v, want the URL once with its request, Portal and nonce", bEvents.all())
	}
	if len(observerEvents.all()) != 0 || len(aEvents.all()) != 0 {
		t.Fatal("a session other than the requester received an enrolment event")
	}

	// 4. A new resume supersedes B, and only B is told.
	c, cEvents, _ := h.connect()
	if _, err := c.ResumeEnrolment(h.req(), ResumeEnrolmentParams{PortalID: id}); err != nil {
		t.Fatal(err)
	}
	if cancelled := bEvents.of(EnrolmentCancelled); len(cancelled) != 1 || cancelled[0].Nonce != urls[0].Nonce {
		t.Fatalf("B's events after supersession = %+v", bEvents.all())
	}
	if len(observerEvents.all()) != 0 || len(cEvents.of(EnrolmentCancelled)) != 0 {
		t.Fatal("supersession notified a session other than the old requester")
	}
	waitFor(t, "Authenticate for C", func() bool { return len(h.factory.callsNamed("auth:"+id)) == 3 })

	// 5. Restart: the action is interrupted, and a retry recovers the receipt
	// with no URL and no new Authenticate. (A late URL from the old runtime
	// cannot reach the new engine at all; the replaced-runtime fence within one
	// engine is TestEnrolmentRejectsCallbacksFromAReplacedRuntime.)
	record(h.session.GetSnapshot())
	h.restart()
	h.authenticating(id)
	if p := h.portal(id); p.Enrolment == nil || p.Enrolment.Status != enrolmentInterrupted {
		t.Fatalf("enrolment after restart = %+v", p.Enrolment)
	}
	b2, b2Events, _ := h.connect()
	retried, err := b2.ResumeEnrolment(bReq, ResumeEnrolmentParams{PortalID: id})
	if err != nil || retried != bReceipt {
		t.Fatalf("retry after restart = %+v, %v; want B's receipt", retried, err)
	}
	result, err := b2.GetRequestResult(bReq.ID, bReq.IssuedAt)
	if err != nil || result.Receipt == nil || *result.Receipt != bReceipt {
		t.Fatalf("GetRequestResult = %+v, %v", result, err)
	}
	record(result)
	time.Sleep(20 * time.Millisecond)
	if len(h.factory.callsNamed("auth:"+id)) != 3 || len(b2Events.all()) != 0 {
		t.Fatalf("recovery started an attempt or replayed a URL: auth calls %d, events %+v", len(h.factory.callsNamed("auth:"+id)), b2Events.all())
	}

	// 6. A new explicit resume after the restart delivers again; identity is unchanged.
	newReq := h.req()
	if _, err := b2.ResumeEnrolment(newReq, ResumeEnrolmentParams{PortalID: id}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Authenticate after restart", func() bool { return len(h.factory.callsNamed("auth:"+id)) == 4 })
	h.factory.runtime().emitURL(id, enrolmentURL)
	if urls := b2Events.of(EnrolmentAuthenticationURL); len(urls) != 1 || urls[0].RequestID != newReq.ID {
		t.Fatalf("events after a new resume = %+v", b2Events.all())
	}
	if data, err := os.ReadFile(filepath.Join(h.store.IdentityRoot(), id, "tailscaled.state")); err != nil || string(data) != "node-key" || h.portal(id).ID != id {
		t.Fatal("enrolment changed the Portal's identity")
	}

	// 8. Nothing durable, observable or diagnostic contains the URL.
	disk, err := os.ReadFile(filepath.Join(h.dir, h.store.generation, stateFileName))
	if err != nil {
		t.Fatal(err)
	}
	exposed = append(exposed, string(disk))
	record(h.session.GetSnapshot())
	report, err := h.session.GetDiagnostics("")
	if err != nil {
		t.Fatal(err)
	}
	exposed = append(exposed, report)
	for _, snapshots := range []*[]Snapshot{aSnapshots, observerSnapshots} {
		record(*snapshots)
	}
	_, err = b2.ResumeEnrolment(h.req(), ResumeEnrolmentParams{PortalID: newUUID()})
	exposed = append(exposed, err.Error())
	for i, text := range exposed {
		if strings.Contains(text, enrolmentURL) || strings.Contains(text, "login.tailscale.com") {
			t.Fatalf("the auth URL leaked into exposed item %d", i)
		}
	}
}

func TestEnrolmentRejectsUnsafeURLs(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	id := h.create("hermes", localApp(1))
	h.authenticating(id)
	s, events, _ := h.connect()
	resume := func() {
		t.Helper()
		calls := len(h.factory.callsNamed("auth:" + id))
		if _, err := s.ResumeEnrolment(h.req(), ResumeEnrolmentParams{PortalID: id}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "Authenticate", func() bool { return len(h.factory.callsNamed("auth:"+id)) == calls+1 })
	}
	// The runtime emits one URL per login, so each unsafe URL gets its own attempt.
	for _, bad := range []string{"http://login.tailscale.com/a/1", "https:///a/1", "https://user@login.tailscale.com/a/1", "javascript:alert(1)", "not a url"} {
		resume()
		h.factory.runtime().emitURL(id, bad)
	}
	if len(events.of(EnrolmentAuthenticationURL)) != 0 {
		t.Fatalf("unsafe URLs were delivered: %v", events.all())
	}
	resume()
	h.factory.runtime().emitURL(id, enrolmentURL)
	if len(events.of(EnrolmentAuthenticationURL)) != 1 {
		t.Fatal("a valid URL after rejected ones was not delivered")
	}
}

func TestEnrolmentRefusals(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	online := h.create("online", localApp(1))
	stopped := h.create("stopped", localApp(2))
	removing := h.create("removing", localApp(3))
	h.factory.runtime().emitStatus(online, portal.StatusEvent{State: portal.StateOnline})
	h.authenticating(stopped)
	h.authenticating(removing)
	if _, err := h.session.SetDesiredState(h.req(), SetDesiredStateParams{PortalID: stopped, State: portal.DesiredStateStopped}); err != nil {
		t.Fatal(err)
	}
	h.factory.set(func(f *fakeFactory) { f.removeErr = errors.New("held") })
	if _, err := h.session.RemovePortal(h.req(), RemovePortalParams{PortalID: removing, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	h.settle()
	for _, tc := range []struct {
		id   string
		code Code
	}{
		{online, CodeNotAvailable},
		{stopped, CodeNotAvailable},
		{removing, CodeNotAvailable},
		{newUUID(), CodeNotFound},
	} {
		_, err := h.session.ResumeEnrolment(h.req(), ResumeEnrolmentParams{PortalID: tc.id})
		wantCode(t, err, tc.code)
		if p := h.portal(tc.id); p != nil && p.Availability[OpResumeEnrolment].Available {
			t.Fatalf("%s offers resumeEnrolment", p.Name)
		}
	}

	// Runtime unavailable: a factory failure after a logging change.
	waiting := h.create("waiting", localApp(4))
	h.authenticating(waiting)
	if !h.portal(waiting).Availability[OpResumeEnrolment].Available {
		t.Fatal("an authenticating Portal does not offer resumeEnrolment")
	}
	h.factory.set(func(f *fakeFactory) { f.buildErr = errors.New("no runtime") })
	h.chooseLogging(installation.LoggingDisabled)
	_, err := h.session.ResumeEnrolment(h.req(), ResumeEnrolmentParams{PortalID: waiting})
	wantCode(t, err, CodeRuntimeUnavailable)
}

func TestEnrolmentLeaseEndsOnStopRemovalAndRebuild(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(h *harness, id string)
	}{
		{"stop", func(h *harness, id string) {
			if _, err := h.session.SetDesiredState(h.req(), SetDesiredStateParams{PortalID: id, State: portal.DesiredStateStopped}); err != nil {
				h.t.Fatal(err)
			}
		}},
		{"removal", func(h *harness, id string) {
			if _, err := h.session.RemovePortal(h.req(), RemovePortalParams{PortalID: id, Confirmed: true}); err != nil {
				h.t.Fatal(err)
			}
		}},
		{"runtime rebuild", func(h *harness, id string) { h.chooseLogging(installation.LoggingDisabled) }},
		{"tailnet rejection", func(h *harness, id string) {
			keep := h.create("keep", localApp(2))
			rt := h.factory.runtime()
			rt.emitStatus(keep, online("keep", "one.ts.net", "tailnet-a"))
			rt.emitStatus(id, online("hermes", "two.ts.net", "tailnet-b"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.chooseLogging(installation.LoggingEnabled)
			id := h.create("hermes", localApp(1))
			h.authenticating(id)
			s, events, _ := h.connect()
			if _, err := s.ResumeEnrolment(h.req(), ResumeEnrolmentParams{PortalID: id}); err != nil {
				t.Fatal(err)
			}
			rt := h.factory.runtime()
			tc.end(h, id)
			if len(events.of(EnrolmentCancelled)) != 1 {
				t.Fatalf("events = %+v, want one cancellation", events.all())
			}
			rt.emitURL(id, enrolmentURL)
			if current := h.factory.runtime(); current != rt {
				current.emitURL(id, enrolmentURL)
			}
			if len(events.of(EnrolmentAuthenticationURL)) != 0 {
				t.Fatal("an ended attempt delivered a URL")
			}
			if tc.name == "removal" || tc.name == "tailnet rejection" {
				h.settle()
				for _, action := range h.disk().Actions {
					if action.PortalID == id {
						t.Fatal("the Portal's enrolment marker outlived it")
					}
				}
			}
		})
	}
}

func TestEnrolmentStartFailureIsReportedToTheRequester(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	id := h.create("hermes", localApp(1))
	h.authenticating(id)
	h.factory.set(func(f *fakeFactory) { f.authErr = errors.New("start interactive login") })
	s, events, _ := h.connect()
	if _, err := s.ResumeEnrolment(h.req(), ResumeEnrolmentParams{PortalID: id}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the failure event", func() bool { return len(events.of(EnrolmentFailed)) == 1 })
	h.factory.runtime().emitURL(id, enrolmentURL)
	if len(events.of(EnrolmentAuthenticationURL)) != 0 {
		t.Fatal("a failed attempt delivered a URL")
	}
	if p := h.portal(id); p.Enrolment == nil || p.Enrolment.Status != enrolmentInterrupted {
		t.Fatalf("enrolment after a failed start = %+v", p.Enrolment)
	}
}

// A URL from a runtime that was replaced is an obsolete callback, even when
// a new attempt for the same Portal is live on the new runtime.
func TestEnrolmentRejectsCallbacksFromAReplacedRuntime(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	id := h.create("hermes", localApp(1))
	old := h.factory.runtime()
	h.authenticating(id)
	s, events, _ := h.connect()
	// An attempt on the old runtime is still pending when it is replaced.
	if _, err := s.ResumeEnrolment(h.req(), ResumeEnrolmentParams{PortalID: id}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Authenticate on the old runtime", func() bool { return len(h.factory.callsNamed("auth:"+id)) == 1 })
	h.chooseLogging(installation.LoggingDisabled)
	current := h.factory.runtime()
	if current == old {
		t.Fatal("the runtime was not replaced")
	}
	h.authenticating(id)
	if _, err := s.ResumeEnrolment(h.req(), ResumeEnrolmentParams{PortalID: id}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Authenticate on the new runtime", func() bool { return len(h.factory.callsNamed("auth:"+id)) == 2 })
	old.emitURL(id, enrolmentURL)
	if len(events.of(EnrolmentAuthenticationURL)) != 0 {
		t.Fatal("a replaced runtime's URL was delivered")
	}
	current.emitURL(id, enrolmentURL)
	if len(events.of(EnrolmentAuthenticationURL)) != 1 {
		t.Fatal("the current runtime's URL was not delivered")
	}
}

// TestEnrolmentDropsASupersededAttemptsLateURL: A's login is pending when B
// resumes. A's URL, arriving before B's login starts, must not reach B or use
// up B's delivery; B then gets the URL answering its own attempt.
func TestEnrolmentDropsASupersededAttemptsLateURL(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	id := h.create("hermes", localApp(1))
	h.authenticating(id)
	a, aEvents, _ := h.connect()
	if _, err := a.ResumeEnrolment(h.req(), ResumeEnrolmentParams{PortalID: id}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "A's Authenticate", func() bool { return len(h.factory.callsNamed("auth:"+id)) == 1 })

	// Hold B's login so A's URL arrives first.
	gate := make(chan struct{})
	h.factory.set(func(f *fakeFactory) { f.authGate = gate })
	b, bEvents, _ := h.connect()
	bReq := h.req()
	if _, err := b.ResumeEnrolment(bReq, ResumeEnrolmentParams{PortalID: id}); err != nil {
		t.Fatal(err)
	}
	h.factory.runtime().emitURL(id, enrolmentURL)
	if len(bEvents.of(EnrolmentAuthenticationURL)) != 0 || len(aEvents.of(EnrolmentAuthenticationURL)) != 0 {
		t.Fatalf("A's late URL was delivered: A %v, B %v", aEvents.all(), bEvents.all())
	}

	h.factory.set(func(f *fakeFactory) { f.authGate = nil })
	close(gate)
	waitFor(t, "B's Authenticate", func() bool { return len(h.factory.callsNamed("auth:"+id)) == 2 })
	h.factory.runtime().emitURL(id, enrolmentURL)
	if urls := bEvents.of(EnrolmentAuthenticationURL); len(urls) != 1 || urls[0].RequestID != bReq.ID {
		t.Fatalf("B's events = %v, want its own URL once", bEvents.all())
	}
}

func TestEnrolmentMarkerFollowsItsPortal(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	a := h.create("a", localApp(1))
	b := h.create("b", localApp(2))
	h.authenticating(a)
	h.authenticating(b)
	s, _, _ := h.connect()
	for _, id := range []string{a, b} {
		if _, err := s.ResumeEnrolment(h.req(), ResumeEnrolmentParams{PortalID: id}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(h.disk().Actions); n != 2 {
		t.Fatalf("markers = %d, want one per Portal", n)
	}
	// A Portal past authentication no longer reports an enrolment.
	h.factory.runtime().emitStatus(a, portal.StatusEvent{State: portal.StateOnline})
	if p := h.portal(a); p.Enrolment != nil {
		t.Fatalf("an Online Portal still reports enrolment %+v", p.Enrolment)
	}
	if p := h.portal(b); p.Enrolment == nil || p.Enrolment.Status != enrolmentActive {
		t.Fatalf("the other Portal's enrolment = %+v", p.Enrolment)
	}
	if _, err := h.session.RemovePortal(h.req(), RemovePortalParams{PortalID: b, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	h.settle()
	for _, action := range h.disk().Actions {
		if action.PortalID == b {
			t.Fatal("a removed Portal kept its enrolment marker")
		}
	}
}

func TestEnrolmentRefusedOnAClosedSession(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	id := h.create("hermes", localApp(1))
	h.authenticating(id)
	s, _, _ := h.connect()
	s.Close()
	_, err := s.ResumeEnrolment(h.req(), ResumeEnrolmentParams{PortalID: id})
	wantCode(t, err, CodeNotAvailable)
	if h.portal(id).Enrolment != nil {
		t.Fatal("a closed session started an attempt")
	}
}

func TestEnrolmentEventFormattingRedactsTheURL(t *testing.T) {
	e := EnrolmentEvent{Kind: EnrolmentAuthenticationURL, RequestID: "r", PortalID: "p", Nonce: "n", URL: enrolmentURL}
	for _, text := range []string{fmt.Sprintf("%v", e), fmt.Sprintf("%+v", e), fmt.Sprintf("%#v", e), fmt.Sprint([]EnrolmentEvent{e})} {
		if strings.Contains(text, "login.tailscale.com") {
			t.Fatalf("formatting exposes the URL: %s", text)
		}
	}
}

func TestEnrolmentRefusedWhileTheRuntimeIsReplaced(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	id := h.create("hermes", localApp(1))
	h.authenticating(id)
	release := make(chan struct{})
	h.factory.set(func(f *fakeFactory) { f.closeBlock = release })
	if _, err := h.session.SetOperationalLogging(h.req(), SetOperationalLoggingParams{Logging: installation.LoggingDisabled}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the old runtime's close to start", func() bool { return len(h.factory.callsNamed("close")) == 1 })
	s, events, _ := h.connect()
	_, err := s.ResumeEnrolment(h.req(), ResumeEnrolmentParams{PortalID: id})
	wantCode(t, err, CodeRuntimeUnavailable)
	h.factory.set(func(f *fakeFactory) { close(release); f.closeBlock = nil })
	h.settle()
	if n := len(h.factory.callsNamed("auth:" + id)); n != 0 || len(events.all()) != 0 {
		t.Fatalf("an attempt started on a closing runtime: %d Authenticate calls, events %v", n, events.all())
	}
}

// TestEnrolmentDelayedSupersededStartCannotOverwriteTheCurrentAttempt: A's
// login start is delayed after its lease check; B supersedes A meanwhile. B's
// attempt must end up pending on the runtime, so B gets its URL.
func TestEnrolmentDelayedSupersededStartCannotOverwriteTheCurrentAttempt(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	id := h.create("hermes", localApp(1))
	h.authenticating(id)
	paused, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h.engine.mu.Lock()
	h.engine.authStartHook = func(stage, _ string) {
		if stage != "starting" {
			return
		}
		first := false
		once.Do(func() { first = true })
		if first {
			close(paused)
			<-release
		}
	}
	h.engine.mu.Unlock()

	a, _, _ := h.connect()
	if _, err := a.ResumeEnrolment(h.req(), ResumeEnrolmentParams{PortalID: id}); err != nil {
		t.Fatal(err)
	}
	<-paused // A passed its lease check and is about to call the runtime.
	b, bEvents, _ := h.connect()
	bReq := h.req()
	if _, err := b.ResumeEnrolment(bReq, ResumeEnrolmentParams{PortalID: id}); err != nil {
		t.Fatal(err)
	}
	// Give B's start every chance to run first, as it could without the
	// per-Portal boundary.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) && len(h.factory.callsNamed("auth:"+id)) == 0 {
		time.Sleep(time.Millisecond)
	}
	close(release)
	waitFor(t, "both login starts", func() bool { return len(h.factory.callsNamed("auth:"+id)) == 2 })
	h.factory.runtime().emitURL(id, enrolmentURL)
	if urls := bEvents.of(EnrolmentAuthenticationURL); len(urls) != 1 || urls[0].RequestID != bReq.ID {
		t.Fatalf("B's events = %v, want its URL", bEvents.all())
	}
}

// TestEnrolmentQueuedSupersededStartIsSkipped: A's start is delayed before the
// per-Portal boundary while B's start completes; A must then see it is no
// longer current and not call the runtime at all.
func TestEnrolmentQueuedSupersededStartIsSkipped(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	id := h.create("hermes", localApp(1))
	h.authenticating(id)
	paused, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h.engine.mu.Lock()
	h.engine.authStartHook = func(stage, _ string) {
		if stage != "queued" {
			return
		}
		first := false
		once.Do(func() { first = true })
		if first {
			close(paused)
			<-release
		}
	}
	h.engine.mu.Unlock()

	a, _, _ := h.connect()
	if _, err := a.ResumeEnrolment(h.req(), ResumeEnrolmentParams{PortalID: id}); err != nil {
		t.Fatal(err)
	}
	<-paused
	b, bEvents, _ := h.connect()
	bReq := h.req()
	if _, err := b.ResumeEnrolment(bReq, ResumeEnrolmentParams{PortalID: id}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "B's login start", func() bool { return len(h.factory.callsNamed("auth:"+id)) == 1 })
	close(release)
	time.Sleep(50 * time.Millisecond)
	if n := len(h.factory.callsNamed("auth:" + id)); n != 1 {
		t.Fatalf("login starts = %d; the superseded start ran after the current one", n)
	}
	h.factory.runtime().emitURL(id, enrolmentURL)
	if urls := bEvents.of(EnrolmentAuthenticationURL); len(urls) != 1 || urls[0].RequestID != bReq.ID {
		t.Fatalf("B's events = %v, want its URL", bEvents.all())
	}
}
