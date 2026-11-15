package management

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chrisbanes/portico/helper/internal/service"
)

// authURL has characters encoding/json v1 escapes, so a leak in either form is caught.
const authURL = "https://login.tailscale.com/a/5f0e9c2d7b1a?x=1&y=<2>"

// recorder keeps every frame one client reads.
type recorder struct {
	mu     sync.Mutex
	frames [][]byte
}

func (r *recorder) record(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, bytes.Clone(p))
}

func (r *recorder) all() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.frames...)
}

type enrolment struct {
	id               string
	receipt          Receipt
	event            service.EnrolmentEvent
	requester, other *Client
	requesterFrames  *recorder
	otherFrames      *recorder
	setupFrames      *recorder
	otherSnapshots   func() []service.Snapshot
}

// enrol has one client resume an authenticating Portal while another
// subscribes, and delivers the runtime's URL for the attempt.
func enrol(t *testing.T, h *harness) enrolment {
	t.Helper()
	e := enrolment{requesterFrames: &recorder{}, otherFrames: &recorder{}, setupFrames: &recorder{}}
	setup := h.dialRecording(e.setupFrames.record)
	chooseLogging(t, setup)
	e.id = createPortal(t, setup, "web")
	authenticating(t, h, setup, e.id)

	e.requester = h.dialRecording(e.requesterFrames.record)
	e.other = h.dialRecording(e.otherFrames.record)
	subscribe(t, e.requester)
	e.otherSnapshots = subscribe(t, e.other)
	e.receipt = mustMutate(t, e.requester, "resumeEnrolment", service.ResumeEnrolmentParams{PortalID: e.id})
	rt := h.factory.runtime()
	waitFor(t, "the login attempt", func() bool { return rt.attempt(e.id) != "" })
	rt.url(e.id, rt.attempt(e.id), authURL)
	select {
	case e.event = <-e.requester.Enrolment():
	case <-time.After(10 * time.Second):
		t.Fatal("the requester got no enrolment event")
	}
	return e
}

func TestEnrolmentURLReachesOnlyTheRequester(t *testing.T) {
	h := newHarness(t)
	e := enrol(t, h)
	want := service.EnrolmentEvent{Kind: service.EnrolmentAuthenticationURL, RequestID: e.receipt.RequestID, PortalID: e.id, Nonce: h.factory.runtime().attempt(e.id), URL: authURL}
	if e.event != want {
		t.Fatalf("event = %v, want %v", e.event, want)
	}
	select {
	case event := <-e.other.Enrolment():
		t.Fatalf("another subscriber got %v", event)
	case <-time.After(200 * time.Millisecond):
	}

	// Disconnecting the requester ends its lease; the Portal stays.
	e.requester.Close()
	waitFor(t, "the enrolment interrupted", func() bool {
		s := e.otherSnapshots()
		if len(s) == 0 {
			return false
		}
		p := findPortal(s[len(s)-1], e.id)
		return p != nil && p.Enrolment != nil && p.Enrolment.Status == "interrupted"
	})
	for _, f := range e.otherFrames.all() {
		if bytes.Contains(f, []byte(`"type":"enrolment"`)) {
			t.Fatalf("another client got an enrolment frame: %s", f)
		}
	}
}

// TestNoFrameExposesPathsOrTheURL scans every frame of an enrolment, with
// diagnostics and errors, for the state root, identity root, endpoint and
// URL. Only the requester's own authenticationURL event may hold the URL.
func TestNoFrameExposesPathsOrTheURL(t *testing.T) {
	h := newHarness(t)
	e := enrol(t, h)
	for _, c := range []*Client{e.requester, e.other} {
		var report DiagnosticsResult
		if err := c.Call(ctx(t), "getDiagnostics", nil, nil, &report); err != nil {
			t.Fatal(err)
		}
		_ = c.Call(ctx(t), "getDiagnostics", nil, DiagnosticsParams{PortalID: newUUID()}, nil)
		_, _ = mutate(t, c, "resumeEnrolment", service.ResumeEnrolmentParams{PortalID: newUUID()})
	}
	_ = snapshot(t, e.requester)

	var secrets []string
	for _, path := range []string{h.root, h.store.IdentityRoot(), h.base} {
		secrets = append(secrets, path)
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			secrets = append(secrets, resolved)
		}
	}
	secrets = append(secrets, "management.sock")
	escaped, _ := json.Marshal(authURL)
	urlForms := [][]byte{[]byte(authURL), bytes.Trim(escaped, `"`)}
	if bytes.Equal(urlForms[0], urlForms[1]) {
		t.Fatal("authURL has nothing for encoding/json to escape")
	}
	urls := 0
	for name, r := range map[string]*recorder{"requester": e.requesterFrames, "other": e.otherFrames, "setup": e.setupFrames} {
		frames := r.all()
		if len(frames) == 0 {
			t.Fatalf("no frames recorded for %s", name)
		}
		for _, f := range frames {
			for _, secret := range secrets {
				if strings.Contains(string(f), secret) {
					t.Fatalf("a %s frame exposes %q: %s", name, secret, f)
				}
			}
			if bytes.Contains(f, urlForms[0]) || bytes.Contains(f, urlForms[1]) {
				if name != "requester" || !bytes.Contains(f, []byte(`"type":"enrolment"`)) || !bytes.Contains(f, []byte(`"event":"authenticationURL"`)) {
					t.Fatalf("a %s frame exposes the URL: %s", name, f)
				}
				urls++
			}
		}
	}
	if urls != 1 {
		t.Fatalf("the URL was delivered %d times, want once", urls)
	}
}

// TestResumeEnrolmentReplyPrecedesItsEvent has the login fail at once, so
// the engine emits the failed event while the call is still replying.
func TestResumeEnrolmentReplyPrecedesItsEvent(t *testing.T) {
	h := newHarness(t)
	setup := h.dial()
	chooseLogging(t, setup)
	id := createPortal(t, setup, "web")
	authenticating(t, h, setup, id)
	h.factory.set(func(f *fakeFactory) { f.authErr = errBuild })
	frames := &recorder{}
	c := h.dialRecording(frames.record)
	seen := 0
	for i := range 200 {
		r := mustMutate(t, c, "resumeEnrolment", service.ResumeEnrolmentParams{PortalID: id})
		select {
		case e := <-c.Enrolment():
			if e.Kind != service.EnrolmentFailed || e.RequestID != r.RequestID {
				t.Fatalf("iteration %d: event %v for request %s", i, e, r.RequestID)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("iteration %d: no enrolment event", i)
		}
		all := frames.all()
		for _, f := range all[seen:] {
			if bytes.Contains(f, []byte(r.RequestID)) {
				if !bytes.Contains(f, []byte(`"type":"reply"`)) {
					t.Fatalf("iteration %d: %s came before the reply", i, f)
				}
				break
			}
		}
		seen = len(all)
	}
}
