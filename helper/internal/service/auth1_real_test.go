package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"slices"
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
	identityRoot := store.IdentityRoot()
	identity := filepath.Join(identityRoot, id)
	authenticating(setup, id)
	before := identityFiles(t, identity)
	beforeKeys := nodeKeys(t, identity)

	// Client A resumes, its login starts, then it disconnects.
	a := engine.Connect()
	if _, err := a.ResumeEnrolment(request(a), ResumeEnrolmentParams{PortalID: id}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	a.Close()
	// Any URL A's attempt produces arrives now, with no lease, and is
	// dropped; B's URL below can then only come from B's own resume.
	time.Sleep(5 * time.Second)

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
	t.Logf("outcome: observer session received %d enrolment events", len(observerEvents.all()))
	if len(observerEvents.all()) != 0 {
		t.Fatal("a session other than the requester received the URL")
	}
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
	kept := store.IdentityRoot() == identityRoot
	for name := range before {
		if !after[name] {
			kept = false
		}
	}
	sameKeys := nodeKeys(t, identity) == beforeKeys
	t.Logf("outcome: same Portal UUID and identity directory with all %d entries: %v; same machine key and login profiles (compared in memory): %v", len(before), kept, sameKeys)
	if !kept || !sameKeys || findPortal(b2.GetSnapshot(), id) == nil {
		t.Fatal("the Portal's identity state changed")
	}

	report, err := b2.GetDiagnostics("")
	if err != nil {
		t.Fatal(err)
	}
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

// nodeKeys fingerprints the node's machine key and login profile keys in
// tailscaled.state, so a logout or reset is detectable without logging them.
func nodeKeys(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "tailscaled.state"))
	if err != nil {
		t.Fatalf("identity state: %v", err)
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal("identity state is not a JSON state store")
	}
	if len(state["_machinekey"]) == 0 {
		t.Fatal("identity state has no machine key")
	}
	var profiles []string
	for key := range state {
		if strings.HasPrefix(key, "profile-") {
			profiles = append(profiles, key)
		}
	}
	slices.Sort(profiles)
	sum := sha256.Sum256(append(append([]byte{}, state["_machinekey"]...), strings.Join(profiles, ",")...))
	return hex.EncodeToString(sum[:])
}

func findPortal(s Snapshot, id string) *PortalSnapshot {
	for i := range s.Portals {
		if s.Portals[i].ID == id {
			return &s.Portals[i]
		}
	}
	return nil
}
