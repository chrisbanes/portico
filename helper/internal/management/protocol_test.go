package management

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chrisbanes/portico/helper/internal/installation"
	"github.com/chrisbanes/portico/helper/internal/ipc"
	"github.com/chrisbanes/portico/helper/internal/portal"
	"github.com/chrisbanes/portico/helper/internal/service"
)

func TestVersionMismatchClosesWithoutReadingCommands(t *testing.T) {
	h := newHarness(t)
	c := h.raw(t)
	writeJSON(t, c, map[string]any{"type": "hello", "version": 2})
	// A command right behind the hello must never be read.
	writeJSON(t, c, rawMutation("setOperationalLogging", map[string]any{"logging": "enabled"}, 0))

	frames := readUntilClosed(t, c)
	if len(frames) != 1 || frames[0]["type"] != "error" || errorCode(frames[0]) != "incompatibleVersion" {
		t.Fatalf("frames = %v, want only incompatibleVersion", frames)
	}
	if e := frames[0]["error"].(map[string]any); e["retryable"] != false {
		t.Fatalf("incompatibleVersion is retryable: %v", e)
	}
	if v, _ := frames[0]["supportedVersions"].([]any); len(v) != 1 || v[0] != float64(1) {
		t.Fatalf("supportedVersions = %v, want [1]", frames[0]["supportedVersions"])
	}
	if s := snapshot(t, h.dial()); s.Revision != 0 || s.Logging != installation.LoggingUndecided {
		t.Fatalf("the command after the mismatch ran: revision %d, logging %s", s.Revision, s.Logging)
	}
}

func TestMalformedInputClosesTheConnection(t *testing.T) {
	h := newHarness(t)
	for name, tc := range map[string]struct {
		hello bool
		frame string
	}{
		"call before hello":     {false, `{"type":"call","callID":"11111111-1111-4111-8111-111111111111","method":"getSnapshot","params":{}}`},
		"hello extra member":    {false, `{"type":"hello","version":1,"extra":true}`},
		"not an object":         {false, `[1]`},
		"second hello":          {true, `{"type":"hello","version":1}`},
		"unknown type":          {true, `{"type":"ping"}`},
		"upper-case call ID":    {true, `{"type":"call","callID":"11111111-1111-4111-8111-11111111111A","method":"getSnapshot","params":{}}`},
		"duplicate member":      {true, `{"type":"call","type":"call","callID":"11111111-1111-4111-8111-111111111111","method":"getSnapshot","params":{}}`},
		"negative revision":     {true, `{"type":"call","callID":"11111111-1111-4111-8111-111111111111","method":"retryRuntime","requestID":"22222222-2222-4222-8222-222222222222","issuedAt":"2026-11-15T12:00:00Z","expectedRevision":-1,"params":{}}`},
		"unknown envelope name": {true, `{"type":"call","callID":"11111111-1111-4111-8111-111111111111","method":"getSnapshot","params":{},"x":1}`},
	} {
		t.Run(name, func(t *testing.T) {
			c := h.raw(t)
			if tc.hello {
				rawHello(t, c)
			}
			if err := ipc.WriteFrame(c, []byte(tc.frame)); err != nil {
				t.Fatal(err)
			}
			frames := readUntilClosed(t, c)
			if len(frames) != 1 || frames[0]["type"] != "error" || errorCode(frames[0]) != "invalidRequest" {
				t.Fatalf("frames = %v, want one invalidRequest error", frames)
			}
		})
	}
}

func TestEnvelopeValidation(t *testing.T) {
	h := newHarness(t)
	c := h.raw(t)
	rawHello(t, c)
	withField := func(call map[string]any, key string, value any) map[string]any {
		call[key] = value
		return call
	}
	without := func(call map[string]any, key string) map[string]any {
		delete(call, key)
		return call
	}
	for name, tc := range map[string]struct {
		call  map[string]any
		field string
	}{
		"unknown method":                {rawCall("explode", map[string]any{}), "method"},
		"read with requestID":           {withField(rawCall("getSnapshot", map[string]any{}), "requestID", newUUID()), "requestID"},
		"read with issuedAt":            {withField(rawCall("getSnapshot", map[string]any{}), "issuedAt", "2026-11-15T12:00:00Z"), "issuedAt"},
		"read with expectedRevision":    {withField(rawCall("getSnapshot", map[string]any{}), "expectedRevision", 0), "expectedRevision"},
		"read with params":              {rawCall("getSnapshot", map[string]any{"x": 1}), "params"},
		"missing params":                {without(rawCall("getSnapshot", nil), "params"), "params"},
		"mutation without requestID":    {without(rawMutation("retryRuntime", map[string]any{}, 0), "requestID"), "requestID"},
		"mutation upper-case requestID": {withField(rawMutation("retryRuntime", map[string]any{}, 0), "requestID", strings.ToUpper(newUUID())), "requestID"},
		"mutation offset issuedAt":      {withField(rawMutation("retryRuntime", map[string]any{}, 0), "issuedAt", "2026-11-15T13:00:00+01:00"), "issuedAt"},
		"mutation without issuedAt":     {without(rawMutation("retryRuntime", map[string]any{}, 0), "issuedAt"), "issuedAt"},
		"mutation without revision":     {without(rawMutation("retryRuntime", map[string]any{}, 0), "expectedRevision"), "expectedRevision"},
		"mutation unknown param":        {rawMutation("createPortal", map[string]any{"name": "web", "colour": "red"}, 0), "params"},
		"result with bad issuedAt":      {rawCall("getRequestResult", map[string]any{"requestID": newUUID(), "issuedAt": "yesterday"}), "issuedAt"},
	} {
		t.Run(name, func(t *testing.T) {
			writeJSON(t, c, tc.call)
			reply := readJSON(t, c)
			e, _ := reply["error"].(map[string]any)
			if reply["type"] != "reply" || reply["callID"] != tc.call["callID"] || e["code"] != "invalidRequest" || e["field"] != tc.field {
				t.Fatalf("reply = %v, want invalidRequest field %s", reply, tc.field)
			}
		})
	}
	if s := snapshot(t, h.dial()); s.Revision != 0 {
		t.Fatalf("an invalid envelope ran: revision %d", s.Revision)
	}
}

var errBuild = errors.New("fake runtime failure")

const everyMethodAlert = "5b3c2b1e-8f61-4a1e-9b6e-0c7f2d9a4e11"

// TestEveryMethodRoundTrips makes one successful and one failing call of
// each management v1 method over the socket.
func TestEveryMethodRoundTrips(t *testing.T) {
	h := newHarness(t, func(h *harness) {
		h.opts.Discoverer = fakeDiscoverer{}
		h.seed = `{"version":5,"portals":[],"alerts":[{"id":"` + everyMethodAlert + `","kind":"crossTailnetRejection","portalName":"old","createdAt":800000000}],` +
			`"operationalLogging":"undecided","launchAtLoginOffer":"notOffered","tailnetBinding":{"name":"tailnet-a","magicDNSSuffix":"example.ts.net"}}`
		h.factory.buildErr = errBuild
	})
	c := h.dial()
	if c.Capabilities.AutomaticDiscovery != "available" || c.Epoch == "" {
		t.Fatalf("hello = %q, %+v", c.Epoch, c.Capabilities)
	}

	// getSnapshot
	if s := snapshot(t, c); s.Epoch != c.Epoch || !s.TailnetBound {
		t.Fatalf("snapshot = %+v", s)
	}
	wantCode(t, c.Call(ctx(t), "getSnapshot", nil, map[string]any{"x": 1}, nil), service.CodeInvalidRequest, "params")

	// subscribe
	sub := h.dial()
	if err := sub.Call(ctx(t), "subscribe", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if s := <-sub.Snapshots(); s.Epoch != c.Epoch {
		t.Fatalf("initial snapshot = %+v", s)
	}
	wantCode(t, sub.Call(ctx(t), "subscribe", nil, nil, nil), service.CodeNotAvailable, "")

	// setOperationalLogging; the runtime then fails to start.
	_, err := mutate(t, c, "setOperationalLogging", map[string]any{"logging": "sometimes"})
	wantCode(t, err, service.CodeInvalidRequest, "logging")
	chooseLogging(t, c)
	waitSnapshot(t, c, "a failed runtime", func(s service.Snapshot) bool { return s.Service.Runtime == "unavailable" })

	// retryRuntime
	h.factory.set(func(f *fakeFactory) { f.buildErr = nil })
	if r := mustMutate(t, c, "retryRuntime", nil); r.Outcome != service.OutcomeAccepted || r.Operation != "retryRuntime" {
		t.Fatalf("retryRuntime receipt = %+v", r)
	}
	waitSnapshot(t, c, "a running runtime", func(s service.Snapshot) bool { return s.Service.Runtime == "running" })
	_, err = mutate(t, c, "retryRuntime", nil)
	wantCode(t, err, service.CodeNotAvailable, "")

	// dismissAlert
	_, err = mutate(t, c, "dismissAlert", service.DismissAlertParams{AlertID: newUUID()})
	wantCode(t, err, service.CodeNotFound, "alertID")
	mustMutate(t, c, "dismissAlert", service.DismissAlertParams{AlertID: everyMethodAlert})

	// resetTailnet
	_, err = mutate(t, c, "resetTailnet", service.ResetTailnetParams{})
	wantCode(t, err, service.CodeConfirmationRequired, "")
	mustMutate(t, c, "resetTailnet", service.ResetTailnetParams{Confirmed: true})

	// setLaunchAtLoginOffer
	_, err = mutate(t, c, "setLaunchAtLoginOffer", map[string]any{"state": "notOffered"})
	wantCode(t, err, service.CodeInvalidRequest, "state")
	mustMutate(t, c, "setLaunchAtLoginOffer", service.SetLaunchAtLoginOfferParams{State: installation.OfferPresented})

	// createPortal
	_, err = mutate(t, c, "createPortal", service.CreatePortalParams{Name: "Bad_Name", Destination: localApp(8080), PublicAccess: portal.PublicAccessModePrivate})
	wantCode(t, err, service.CodeInvalidRequest, "name")
	created := mustMutate(t, c, "createPortal", service.CreatePortalParams{Name: "web", Destination: localApp(8080), PublicAccess: portal.PublicAccessModePrivate})
	id := created.PortalID
	if created.Operation != "createPortal" || !uuidPattern.MatchString(id) || created.AcceptedAt.IsZero() || !created.RecoverUntil.After(created.AcceptedAt) {
		t.Fatalf("createPortal receipt = %+v", created)
	}

	// setDesiredState
	_, err = mutate(t, c, "setDesiredState", service.SetDesiredStateParams{PortalID: newUUID(), State: portal.DesiredStateStopped})
	wantCode(t, err, service.CodeNotFound, "")
	mustMutate(t, c, "setDesiredState", service.SetDesiredStateParams{PortalID: id, State: portal.DesiredStateStopped})

	// setDestination
	_, err = mutate(t, c, "setDestination", service.SetDestinationParams{PortalID: newUUID(), Destination: localApp(9090)})
	wantCode(t, err, service.CodeNotFound, "")
	mustMutate(t, c, "setDestination", service.SetDestinationParams{PortalID: id, Destination: localApp(9090)})

	// resumeEnrolment, which needs an Enabled Portal that is authenticating.
	_, err = mutate(t, c, "resumeEnrolment", service.ResumeEnrolmentParams{PortalID: id})
	wantCode(t, err, service.CodeNotAvailable, "")
	mustMutate(t, c, "setDesiredState", service.SetDesiredStateParams{PortalID: id, State: portal.DesiredStateEnabled})
	authenticating(t, h, c, id)
	if r := mustMutate(t, c, "resumeEnrolment", service.ResumeEnrolmentParams{PortalID: id}); r.PortalID != id {
		t.Fatalf("resumeEnrolment receipt = %+v", r)
	}

	// getDiagnostics
	var report DiagnosticsResult
	if err := c.Call(ctx(t), "getDiagnostics", nil, DiagnosticsParams{PortalID: id}, &report); err != nil || !strings.Contains(report.Report, "Portal Name: web") {
		t.Fatalf("getDiagnostics = %q, %v", report.Report, err)
	}
	wantCode(t, c.Call(ctx(t), "getDiagnostics", nil, DiagnosticsParams{PortalID: newUUID()}, nil), service.CodeNotFound, "")

	// getRequestResult
	var result RequestResult
	if err := c.Call(ctx(t), "getRequestResult", nil, resultParams(created.RequestID, time.Now()), &result); err != nil || result.Receipt == nil || *result.Receipt != created {
		t.Fatalf("getRequestResult = %+v, %v; want %+v", result.Receipt, err, created)
	}
	result = RequestResult{}
	if err := c.Call(ctx(t), "getRequestResult", nil, resultParams(newUUID(), time.Now()), &result); err != nil || !result.NotAccepted || result.Receipt != nil {
		t.Fatalf("getRequestResult for an unseen request = %+v, %v", result, err)
	}
	wantCode(t, c.Call(ctx(t), "getRequestResult", nil, resultParams(newUUID(), time.Now().Add(-48*time.Hour)), nil), service.CodeResultExpired, "")

	// discoverLocalApps, then on a service without discovery.
	var found DiscoverResult
	if err := c.Call(ctx(t), "discoverLocalApps", nil, nil, &found); err != nil || len(found.Candidates) != 1 || found.Candidates[0].LocalAppPort != 3000 {
		t.Fatalf("discoverLocalApps = %+v, %v", found, err)
	}
	wantCode(t, newHarness(t).dial().Call(ctx(t), "discoverLocalApps", nil, nil, nil), service.CodeCapabilityUnavailable, "")

	// removePortal; the removal then fails.
	_, err = mutate(t, c, "removePortal", service.RemovePortalParams{PortalID: id})
	wantCode(t, err, service.CodeConfirmationRequired, "")
	h.factory.set(func(f *fakeFactory) { f.removeErr = errBuild })
	mustMutate(t, c, "removePortal", service.RemovePortalParams{PortalID: id, Confirmed: true})
	waitSnapshot(t, c, "a failed removal", func(s service.Snapshot) bool {
		p := findPortal(s, id)
		return p != nil && p.Problem == service.ProblemRemovalFailed
	})

	// retryRemoval
	_, err = mutate(t, c, "retryRemoval", service.RetryRemovalParams{PortalID: newUUID()})
	wantCode(t, err, service.CodeNotFound, "")
	h.factory.set(func(f *fakeFactory) { f.removeErr = nil })
	mustMutate(t, c, "retryRemoval", service.RetryRemovalParams{PortalID: id})
	waitSnapshot(t, c, "the Portal removed", func(s service.Snapshot) bool { return findPortal(s, id) == nil })
}

func resultParams(id string, issuedAt time.Time) RequestResultParams {
	return RequestResultParams{RequestID: id, IssuedAt: issuedAt.UTC().Format(time.RFC3339Nano)}
}

// authenticating reports the Portal as needing login and waits until the
// engine offers resumeEnrolment.
func authenticating(t *testing.T, h *harness, c *Client, id string) {
	t.Helper()
	waitFor(t, "the Portal started", func() bool { return h.factory.runtime() != nil && h.factory.runtime().configured(id) })
	h.factory.runtime().status(id, portal.StateAuthenticating)
	waitSnapshot(t, c, "resumeEnrolment available", func(s service.Snapshot) bool {
		p := findPortal(s, id)
		return p != nil && p.Availability[service.OpResumeEnrolment].Available
	})
}

// TestIdentifierParamsMustBeCanonical keeps arbitrary client strings out of
// error.portalID: a portalID or alertID that is not a canonical UUID is
// refused before the engine sees it.
func TestIdentifierParamsMustBeCanonical(t *testing.T) {
	h := newHarness(t)
	c := h.dial()
	bad := "/tmp/" + strings.ToUpper(newUUID())
	for _, tc := range []struct {
		method string
		params map[string]any
		field  string
	}{
		{"getDiagnostics", map[string]any{"portalID": bad}, "portalID"},
		{"setDesiredState", map[string]any{"portalID": bad, "state": "stopped"}, "portalID"},
		{"setDestination", map[string]any{"portalID": bad, "destination": map[string]any{"kind": "localApp", "port": 8080}}, "portalID"},
		{"removePortal", map[string]any{"portalID": bad, "confirmed": true}, "portalID"},
		{"retryRemoval", map[string]any{"portalID": bad}, "portalID"},
		{"resumeEnrolment", map[string]any{"portalID": bad}, "portalID"},
		{"dismissAlert", map[string]any{"alertID": bad}, "alertID"},
	} {
		var m *Mutation
		if tc.method != "getDiagnostics" {
			m = newMutation(t, c)
		}
		err := c.Call(ctx(t), tc.method, m, tc.params, nil)
		var e *Error
		if !errors.As(err, &e) || e.Code != "invalidRequest" || e.Field != tc.field || e.PortalID != "" {
			t.Errorf("%s = %v, want invalidRequest field %s with no portalID", tc.method, err, tc.field)
		}
	}
}
