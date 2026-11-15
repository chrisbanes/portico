package service

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chrisbanes/portico/helper/internal/installation"
	"github.com/chrisbanes/portico/helper/internal/portal"
)

// TestRealRuntimeAuth1ResumeAcrossDisconnectAndRestart is the opt-in AUTH-1
// qualification against Tailscale's control plane: an isolated unenrolled
// tsnet node, no credentials. It needs network access, so it runs only with
// PORTICO_REAL_TSNET_AUTH1=1. It logs redacted outcomes only; the URL itself
// never reaches test output.
func TestRealRuntimeAuth1ResumeAcrossDisconnectAndRestart(t *testing.T) {
	if os.Getenv("PORTICO_REAL_TSNET_AUTH1") != "1" {
		t.Skip("set PORTICO_REAL_TSNET_AUTH1=1 to run against Tailscale's control plane")
	}
	dir := t.TempDir()
	opts := EngineOptions{RuntimeFactory: RealRuntimeFactory, LaunchAtLoginOfferCapable: false}
	open := func() (*Store, *Engine) {
		store, err := Open(dir, Options{})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		engine, err := New(store, opts)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return store, engine
	}
	closeEngine := func(store *Store, engine *Engine) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := engine.Close(ctx); err != nil {
			t.Fatalf("Engine.Close: %v", err)
		}
		store.Close()
	}
	request := func(s *Session) Request {
		return Request{ID: newUUID(), IssuedAt: time.Now(), ExpectedRevision: s.GetSnapshot().Revision}
	}
	authenticating := func(s *Session, id string) {
		deadline := time.Now().Add(2 * time.Minute)
		for time.Now().Before(deadline) {
			for _, p := range s.GetSnapshot().Portals {
				if p.ID == id && p.Availability[OpResumeEnrolment].Available {
					return
				}
			}
			time.Sleep(250 * time.Millisecond)
		}
		t.Fatal("the Portal never reported that it needs authentication")
	}
	var urls []string
	awaitURL := func(events *eventLog, requestID string) {
		deadline := time.Now().Add(time.Minute)
		for time.Now().Before(deadline) {
			for _, e := range events.of(EnrolmentAuthenticationURL) {
				if e.RequestID == requestID {
					urls = append(urls, e.URL)
					return
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("the requester received no authentication URL")
	}
	checkURL := func(raw string) {
		u, err := url.Parse(raw)
		safe := err == nil && u.Scheme == "https" && (u.Host == "tailscale.com" || strings.HasSuffix(u.Host, ".tailscale.com"))
		t.Logf("outcome: delivered URL is https on *.tailscale.com: %v", safe)
		if !safe {
			t.Fatal("the delivered URL is not an https *.tailscale.com URL")
		}
	}

	store, engine := open()
	setup := engine.Connect()
	if _, err := setup.SetOperationalLogging(request(setup), SetOperationalLoggingParams{Logging: installation.LoggingDisabled}); err != nil {
		t.Fatal(err)
	}
	receipt, err := setup.CreatePortal(request(setup), CreatePortalParams{Name: "auth1-qualification", Destination: portal.Destination{Kind: portal.DestinationLocalApp, Port: 9}, PublicAccess: portal.PublicAccessModePrivate})
	if err != nil {
		t.Fatal(err)
	}
	id := receipt.PortalID
	identity := filepath.Join(store.IdentityRoot(), id)
	authenticating(setup, id)

	// Client A resumes and disconnects; it must receive nothing.
	a := engine.Connect()
	aEvents := &eventLog{}
	a.ReceiveEnrolmentEvents(aEvents.sink)
	if _, err := a.ResumeEnrolment(request(a), ResumeEnrolmentParams{PortalID: id}); err != nil {
		t.Fatal(err)
	}
	a.Close()

	// Client B resumes and is the only one to receive the URL.
	observerEvents := &eventLog{}
	setup.ReceiveEnrolmentEvents(observerEvents.sink)
	b := engine.Connect()
	bEvents := &eventLog{}
	b.ReceiveEnrolmentEvents(bEvents.sink)
	authenticating(b, id)
	bReq := request(b)
	if _, err := b.ResumeEnrolment(bReq, ResumeEnrolmentParams{PortalID: id}); err != nil {
		t.Fatal(err)
	}
	awaitURL(bEvents, bReq.ID)
	checkURL(urls[0])
	t.Logf("outcome: client A received %d events after disconnecting; observer received %d", len(aEvents.of(EnrolmentAuthenticationURL)), len(observerEvents.all()))
	if len(aEvents.of(EnrolmentAuthenticationURL)) != 0 || len(observerEvents.all()) != 0 {
		t.Fatal("a session other than the requester received the URL")
	}
	before := identityFiles(t, identity)
	exposed := []any{setup.GetSnapshot()}
	closeEngine(store, engine)

	// Restart, then client B resumes again.
	store, engine = open()
	defer closeEngine(store, engine)
	b2 := engine.Connect()
	b2Events := &eventLog{}
	b2.ReceiveEnrolmentEvents(b2Events.sink)
	if p := findPortal(b2.GetSnapshot(), id); p == nil || p.Enrolment == nil || p.Enrolment.Status != enrolmentInterrupted {
		t.Fatal("after restart the Portal or its interrupted enrolment action is missing")
	}
	authenticating(b2, id)
	b2Req := request(b2)
	if _, err := b2.ResumeEnrolment(b2Req, ResumeEnrolmentParams{PortalID: id}); err != nil {
		t.Fatal(err)
	}
	awaitURL(b2Events, b2Req.ID)
	checkURL(urls[1])
	t.Logf("outcome: URL after restart equals the earlier one: %v (either is allowed)", urls[0] == urls[1])

	after := identityFiles(t, identity)
	kept := true
	for name := range before {
		if !after[name] {
			kept = false
		}
	}
	t.Logf("outcome: same Portal UUID and identity directory, %d entries before restart, all still present: %v", len(before), kept)
	if !kept || findPortal(b2.GetSnapshot(), id) == nil {
		t.Fatal("the Portal's identity state changed")
	}

	report, _ := b2.GetDiagnostics("")
	disk, err := os.ReadFile(filepath.Join(dir, store.generation, stateFileName))
	if err != nil {
		t.Fatal(err)
	}
	result, _ := b2.GetRequestResult(bReq.ID, bReq.IssuedAt)
	exposed = append(exposed, b2.GetSnapshot(), result, report, string(disk))
	leaked := false
	for _, v := range exposed {
		data, _ := json.Marshal(v)
		for _, u := range urls {
			if strings.Contains(string(data), u) {
				leaked = true
			}
		}
	}
	t.Logf("outcome: URL found in state, snapshots, receipts or diagnostics: %v", leaked)
	if leaked {
		t.Fatal("the authentication URL leaked")
	}
}

func identityFiles(t *testing.T, dir string) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	err := filepath.WalkDir(dir, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		names[rel] = true
		return nil
	})
	if err != nil {
		t.Fatalf("identity directory: %v", err)
	}
	return names
}

func findPortal(s Snapshot, id string) *PortalSnapshot {
	for i := range s.Portals {
		if s.Portals[i].ID == id {
			return &s.Portals[i]
		}
	}
	return nil
}
