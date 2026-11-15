package service

import (
	"encoding/json"
	"errors"
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
	if p == nil || p.DesiredState != portal.DesiredStateEnabled || p.Enrolment.Status != enrolmentInterrupted {
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

	// 5. Restart: the action is interrupted, a retry recovers the receipt with
	// no URL and no new Authenticate, and the old runtime's late URL is dropped.
	oldRuntime := h.factory.runtime()
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
	oldRuntime.emitURL(id, enrolmentURL)
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
	report, _ := h.session.GetDiagnostics("")
	exposed = append(exposed, report)
	for _, snapshots := range []*[]Snapshot{aSnapshots, observerSnapshots} {
		record(*snapshots)
	}
	_, err = b2.ResumeEnrolment(h.req(), ResumeEnrolmentParams{PortalID: newUUID()})
	exposed = append(exposed, err.Error())
	for _, text := range exposed {
		if strings.Contains(text, enrolmentURL) || strings.Contains(text, "login.tailscale.com") {
			t.Fatalf("the auth URL leaked: %s", text)
		}
	}
}

func TestEnrolmentRejectsUnsafeURLs(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	id := h.create("hermes", localApp(1))
	h.authenticating(id)
	s, events, _ := h.connect()
	if _, err := s.ResumeEnrolment(h.req(), ResumeEnrolmentParams{PortalID: id}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"http://login.tailscale.com/a/1", "https:///a/1", "https://user@login.tailscale.com/a/1", "javascript:alert(1)", "not a url"} {
		h.factory.runtime().emitURL(id, bad)
	}
	if len(events.all()) != 0 {
		t.Fatalf("unsafe URLs were delivered: %+v", events.all())
	}
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
	h.chooseLogging(installation.LoggingDisabled)
	current := h.factory.runtime()
	if current == old {
		t.Fatal("the runtime was not replaced")
	}
	h.authenticating(id)
	s, events, _ := h.connect()
	if _, err := s.ResumeEnrolment(h.req(), ResumeEnrolmentParams{PortalID: id}); err != nil {
		t.Fatal(err)
	}
	old.emitURL(id, enrolmentURL)
	if len(events.of(EnrolmentAuthenticationURL)) != 0 {
		t.Fatal("a replaced runtime's URL was delivered")
	}
	current.emitURL(id, enrolmentURL)
	if len(events.of(EnrolmentAuthenticationURL)) != 1 {
		t.Fatal("the current runtime's URL was not delivered")
	}
}
