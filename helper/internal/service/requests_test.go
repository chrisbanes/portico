package service

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chrisbanes/portico/helper/internal/installation"
	"github.com/chrisbanes/portico/helper/internal/portal"
)

var hermesParams = CreatePortalParams{Name: "hermes", Destination: localApp(8787), PublicAccess: portal.PublicAccessModePrivate}

func TestCreateAckLostAcrossRestart(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	req := h.req()
	original, err := h.session.CreatePortal(req, hermesParams)
	if err != nil {
		t.Fatal(err)
	}
	// The reply is lost; the service restarts before the client retries.
	h.restart()
	retried, err := h.session.CreatePortal(req, hermesParams)
	if err != nil || retried != original {
		t.Fatalf("retry = %+v, %v; want the original receipt %+v", retried, err, original)
	}
	h.settle()
	if portals := h.disk().Installation.Portals; len(portals) != 1 || portals[0].ID != original.PortalID {
		t.Fatalf("records = %+v, want exactly the original Portal", portals)
	}
	rt := h.factory.runtime()
	if n := rt.reconcileCount(); n != 1 || len(rt.lastReconcile()) != 1 || rt.lastReconcile()[0].ID != original.PortalID {
		t.Fatalf("runtime after restart and retry: %d reconciles of %+v, want one start of the original", n, rt.lastReconcile())
	}
}

func TestReceiptConflictExpiryAndCapacity(t *testing.T) {
	t.Run("same ID with another payload conflicts, including after a no-op", func(t *testing.T) {
		h := newHarness(t)
		h.chooseLogging(installation.LoggingEnabled)
		req := h.req()
		if _, err := h.session.CreatePortal(req, hermesParams); err != nil {
			t.Fatal(err)
		}
		other := hermesParams
		other.Name = "atlas"
		_, err := h.session.CreatePortal(req, other)
		wantCode(t, err, CodeRequestConflict)
		// The canonical identity includes expectedRevision and issuedAt.
		moved := req
		moved.ExpectedRevision++
		_, err = h.session.CreatePortal(moved, hermesParams)
		wantCode(t, err, CodeRequestConflict)
		moved = req
		moved.IssuedAt = moved.IssuedAt.Add(time.Second)
		_, err = h.session.CreatePortal(moved, hermesParams)
		wantCode(t, err, CodeRequestConflict)

		noOpReq := h.req()
		if r, err := h.session.SetOperationalLogging(noOpReq, SetOperationalLoggingParams{Logging: installation.LoggingEnabled}); err != nil || r.Outcome != OutcomeNoOp {
			t.Fatalf("no-op = %+v, %v", r, err)
		}
		_, err = h.session.SetOperationalLogging(noOpReq, SetOperationalLoggingParams{Logging: installation.LoggingDisabled})
		wantCode(t, err, CodeRequestConflict)
	})
	t.Run("an equivalent issuedAt in another zone is the same request", func(t *testing.T) {
		h := newHarness(t)
		h.chooseLogging(installation.LoggingEnabled)
		req := h.req()
		first, err := h.session.CreatePortal(req, hermesParams)
		if err != nil {
			t.Fatal(err)
		}
		req.IssuedAt = req.IssuedAt.In(time.FixedZone("x", 3600))
		again, err := h.session.CreatePortal(req, hermesParams)
		if err != nil || again != first {
			t.Fatalf("retry = %+v, %v", again, err)
		}
	})
	t.Run("concurrent same-ID retries create one record", func(t *testing.T) {
		h := newHarness(t)
		h.chooseLogging(installation.LoggingEnabled)
		req := h.req()
		var wg sync.WaitGroup
		receipts := make([]Receipt, 8)
		for i := range receipts {
			wg.Go(func() {
				r, err := h.session.CreatePortal(req, hermesParams)
				if err != nil {
					t.Error(err)
				}
				receipts[i] = r
			})
		}
		wg.Wait()
		for _, r := range receipts[1:] {
			if r != receipts[0] {
				t.Fatalf("receipts differ: %+v vs %+v", r, receipts[0])
			}
		}
		if n := len(h.disk().Installation.Portals); n != 1 {
			t.Fatalf("records = %d", n)
		}
	})
	t.Run("expired and future requests never execute", func(t *testing.T) {
		h := newHarness(t)
		h.chooseLogging(installation.LoggingEnabled)
		old := h.req()
		old.IssuedAt = h.clock.Now().Add(-recoveryWindow - time.Second)
		_, err := h.session.CreatePortal(old, hermesParams)
		wantCode(t, err, CodeResultExpired)
		_, err = h.session.GetRequestResult(old.ID, old.IssuedAt)
		wantCode(t, err, CodeResultExpired)
		future := h.req()
		future.IssuedAt = h.clock.Now().Add(futureAllowance + time.Second)
		_, err = h.session.CreatePortal(future, hermesParams)
		wantCode(t, err, CodeRequestNotYetValid)
		edge := h.req()
		edge.IssuedAt = h.clock.Now().Add(futureAllowance)
		if _, err := h.session.CreatePortal(edge, hermesParams); err != nil {
			t.Fatalf("a request at the future allowance was refused: %v", err)
		}
		if n := len(h.disk().Installation.Portals); n != 1 {
			t.Fatalf("records = %d, want only the admissible request", n)
		}
	})
	t.Run("unseen admissible requests are notAccepted", func(t *testing.T) {
		h := newHarness(t)
		req := h.req()
		result, err := h.session.GetRequestResult(req.ID, req.IssuedAt)
		if err != nil || !result.NotAccepted || result.Receipt != nil {
			t.Fatalf("GetRequestResult = %+v, %v", result, err)
		}
	})
	t.Run("recovery window and retention", func(t *testing.T) {
		h := newHarness(t)
		h.chooseLogging(installation.LoggingEnabled)
		req := h.req()
		receipt, err := h.session.CreatePortal(req, hermesParams)
		if err != nil {
			t.Fatal(err)
		}
		h.restart()
		result, err := h.session.GetRequestResult(req.ID, req.IssuedAt)
		if err != nil || result.Receipt == nil || *result.Receipt != receipt {
			t.Fatalf("receipt after restart = %+v, %v", result, err)
		}
		h.clock.set(receipt.RecoverUntil.Add(time.Second))
		_, err = h.session.GetRequestResult(req.ID, req.IssuedAt)
		wantCode(t, err, CodeResultExpired)
		_, err = h.session.CreatePortal(req, hermesParams)
		wantCode(t, err, CodeResultExpired)
		if n := len(h.disk().Installation.Portals); n != 1 {
			t.Fatal("an expired retry executed")
		}
	})
	t.Run("clock rollback cannot reopen an expired window", func(t *testing.T) {
		h := newHarness(t)
		h.chooseLogging(installation.LoggingEnabled)
		accepted := h.clock.Now()
		h.clock.set(accepted.Add(-48 * time.Hour))
		stale := h.req()
		stale.IssuedAt = accepted.Add(-30 * time.Hour)
		_, err := h.session.CreatePortal(stale, hermesParams)
		wantCode(t, err, CodeResultExpired)
		h.restart()
		_, err = h.session.CreatePortal(stale, hermesParams)
		wantCode(t, err, CodeResultExpired)
		if !h.disk().ClockHighWater.Equal(accepted) {
			t.Fatalf("high-water = %v, want %v", h.disk().ClockHighWater, accepted)
		}
	})
	t.Run("capacity rejects new requests but still recovers duplicates", func(t *testing.T) {
		defer func(limit int) { maxReceipts = limit }(maxReceipts)
		maxReceipts = 2
		h := newHarness(t)
		h.chooseLogging(installation.LoggingEnabled)
		first := h.req()
		receipt, err := h.session.CreatePortal(first, hermesParams)
		if err != nil {
			t.Fatal(err)
		}
		_, err = h.session.CreatePortal(h.req(), hermesParams)
		wantCode(t, err, CodeCapacityExceeded)
		again, err := h.session.CreatePortal(first, hermesParams)
		if err != nil || again != receipt {
			t.Fatalf("duplicate at capacity = %+v, %v", again, err)
		}
		// Expired records are compacted away, which frees capacity.
		h.clock.set(receipt.RetainUntil.Add(time.Second))
		if _, err := h.session.CreatePortal(h.req(), hermesParams); err != nil {
			t.Fatalf("create after retention passed: %v", err)
		}
	})
	t.Run("an oversized snapshot is refused before acceptance", func(t *testing.T) {
		defer func(limit int) { maxStateSize = limit }(maxStateSize)
		h := newHarness(t)
		h.chooseLogging(installation.LoggingEnabled)
		data, _ := json.Marshal(h.session.GetSnapshot())
		maxStateSize = len(data) + 64
		_, err := h.session.CreatePortal(h.req(), hermesParams)
		wantCode(t, err, CodeCapacityExceeded)
		if len(h.disk().Installation.Portals) != 0 {
			t.Fatal("an oversized install was accepted")
		}
	})
}

func TestConcurrentCommandsAtOneRevision(t *testing.T) {
	h := newHarness(t)
	h.chooseLogging(installation.LoggingEnabled)
	revision := h.session.GetSnapshot().Revision
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Go(func() {
			req := Request{ID: newUUID(), IssuedAt: h.clock.Now(), ExpectedRevision: revision}
			_, errs[i] = h.session.CreatePortal(req, hermesParams)
		})
	}
	wg.Wait()
	accepted, conflicts := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			accepted++
		case IsCode(err, CodeRevisionConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if accepted != 1 || conflicts != 1 || len(h.disk().Installation.Portals) != 1 {
		t.Fatalf("accepted %d, conflicts %d", accepted, conflicts)
	}
}

func TestRequestValidation(t *testing.T) {
	h := newHarness(t)
	for _, req := range []Request{
		{ID: "not-a-uuid", IssuedAt: h.clock.Now()},
		{ID: strings.ToUpper(newUUID()), IssuedAt: h.clock.Now()},
		{ID: newUUID()},
	} {
		_, err := h.session.SetOperationalLogging(req, SetOperationalLoggingParams{Logging: installation.LoggingEnabled})
		wantCode(t, err, CodeInvalidRequest)
	}
}

// TestReceiptFormat pins the persisted receipt and action keys, which strict
// decoding would otherwise reject after a rename.
func TestReceiptFormat(t *testing.T) {
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	data, err := json.Marshal(State{
		Schema:   stateSchema,
		Requests: []Receipt{{RequestID: "r", Hash: "h", IssuedAt: at, AcceptedAt: at, RecoverUntil: at, RetainUntil: at, AcceptedRevision: 1, Operation: OpCreatePortal, PortalID: "p", Outcome: OutcomeAccepted}},
		Actions:  []Action{{RequestID: "r", Operation: OpRetryRuntime, AcceptedAt: at}},
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = `"requests":[{"requestID":"r","hash":"h","issuedAt":"2026-11-15T12:00:00Z","acceptedAt":"2026-11-15T12:00:00Z","recoverUntil":"2026-11-15T12:00:00Z","retainUntil":"2026-11-15T12:00:00Z","acceptedRevision":1,"operation":"createPortal","portalID":"p","outcome":"accepted"}],"actions":[{"requestID":"r","operation":"retryRuntime","acceptedAt":"2026-11-15T12:00:00Z"}]`
	if !strings.Contains(string(data), want) {
		t.Fatalf("state = %s", data)
	}
}
