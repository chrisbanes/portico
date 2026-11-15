package management

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chrisbanes/portico/helper/internal/portal"
	"github.com/chrisbanes/portico/helper/internal/service"
)

// setVar sets a package variable for one test. Call it before the harness
// starts, so the server's goroutines only ever see the test value.
func setVar[T any](t *testing.T, v *T, value T) {
	old := *v
	*v = value
	t.Cleanup(func() { *v = old })
}

var web = service.CreatePortalParams{Name: "web", Destination: localApp(8080), PublicAccess: portal.PublicAccessModePrivate}

func TestConcurrentCreatesAtOneRevision(t *testing.T) {
	h := newHarness(t)
	a, b := h.dial(), h.dial()
	chooseLogging(t, a)
	revision := snapshot(t, a).Revision
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, c := range []*Client{a, b} {
		wg.Go(func() {
			m := &Mutation{RequestID: newUUID(), IssuedAt: time.Now(), ExpectedRevision: revision}
			errs[i] = c.Call(ctx(t), "createPortal", m, web, nil)
		})
	}
	wg.Wait()
	accepted, conflicts := 0, 0
	for _, err := range errs {
		var e *Error
		switch {
		case err == nil:
			accepted++
		case errors.As(err, &e) && e.Code == "revisionConflict":
			conflicts++
		default:
			t.Fatalf("create: %v", err)
		}
	}
	if accepted != 1 || conflicts != 1 {
		t.Fatalf("%d accepted and %d revisionConflict, want one of each", accepted, conflicts)
	}
	if n := len(snapshot(t, a).Portals); n != 1 {
		t.Fatalf("%d Portals, want 1", n)
	}
}

// TestLostReplyRecoversTheOriginalReceiptAfterRestart drops the client after
// the commit and before the reply, restarts over the same root and retries
// the same request, whose expectedRevision is stale by then.
func TestLostReplyRecoversTheOriginalReceiptAfterRestart(t *testing.T) {
	var armed atomic.Bool
	var victim *Client
	setVar(t, &afterMutation, func() {
		if armed.CompareAndSwap(true, false) {
			victim.conn.Close()
		}
	})
	h := newHarness(t)
	observer := h.dial()
	chooseLogging(t, observer)
	victim = h.dial()
	m := newMutation(t, victim)
	armed.Store(true)
	if err := victim.Call(ctx(t), "createPortal", m, web, nil); !errors.Is(err, ErrUncertain) {
		t.Fatalf("create with a lost reply = %v, want ErrUncertain", err)
	}
	s := snapshot(t, observer)
	if len(s.Portals) != 1 {
		t.Fatalf("%d Portals after the commit, want 1", len(s.Portals))
	}
	id := s.Portals[0].ID
	waitFor(t, "the first runtime to start the Portal", func() bool { return h.factory.starts(id) == 1 })

	h.restart()
	c := h.dial()
	waitFor(t, "the restarted runtime to start the Portal", func() bool { return h.factory.builds() == 2 && h.factory.starts(id) == 2 })
	reconciles := h.factory.runtime().reconcileCount()
	if snapshot(t, c).Revision == m.ExpectedRevision {
		t.Fatal("the retry's expectedRevision is not stale")
	}
	var retried Receipt
	if err := c.Call(ctx(t), "createPortal", m, web, &retried); err != nil {
		t.Fatalf("same-request retry after restart: %v", err)
	}
	var recovered RequestResult
	if err := c.Call(ctx(t), "getRequestResult", nil, resultParams(m.RequestID, m.IssuedAt), &recovered); err != nil || recovered.Receipt == nil {
		t.Fatalf("getRequestResult = %+v, %v", recovered, err)
	}
	if retried != *recovered.Receipt || retried.PortalID != id || retried.Outcome != service.OutcomeAccepted || retried.AcceptedRevision != m.ExpectedRevision+1 {
		t.Fatalf("retry receipt = %+v, want the original %+v for Portal %s", retried, *recovered.Receipt, id)
	}
	if s := snapshot(t, c); len(s.Portals) != 1 || s.Portals[0].ID != id {
		t.Fatalf("Portals after the retry = %+v, want only %s", s.Portals, id)
	}
	if h.factory.builds() != 2 || h.factory.runtime().reconcileCount() != reconciles {
		t.Fatal("the retried request started runtime work")
	}
}

func TestSameRequestIDWithAnotherPayloadConflicts(t *testing.T) {
	h := newHarness(t)
	c := h.dial()
	chooseLogging(t, c)
	m := newMutation(t, c)
	if err := c.Call(ctx(t), "createPortal", m, web, nil); err != nil {
		t.Fatal(err)
	}
	other := web
	other.Name = "other"
	wantCode(t, c.Call(ctx(t), "createPortal", m, other, nil), service.CodeRequestConflict, "")
	if n := len(snapshot(t, c).Portals); n != 1 {
		t.Fatalf("%d Portals, want 1", n)
	}
}

// slowClock stalls the engine, which reads the clock under its lock, once.
type slowClock struct{ armed atomic.Bool }

const stall = time.Second

func (c *slowClock) now() time.Time {
	if c.armed.CompareAndSwap(true, false) {
		time.Sleep(stall)
	}
	return time.Now()
}

func TestTimedOutMutationIsUncertainAndRecoverable(t *testing.T) {
	setVar(t, &callTimeout, 200*time.Millisecond)
	clock := &slowClock{}
	h := newHarness(t, func(h *harness) { h.opts.Clock = clock.now })
	c := h.dial()
	chooseLogging(t, c)
	m := newMutation(t, c)
	clock.armed.Store(true)
	start := time.Now()
	if err := c.Call(ctx(t), "createPortal", m, web, nil); !errors.Is(err, ErrUncertain) {
		t.Fatalf("slow create = %v, want ErrUncertain", err)
	}
	if elapsed := time.Since(start); elapsed >= stall {
		t.Fatalf("the uncertain reply took %v, not the call deadline", elapsed)
	}
	// The engine is still busy with the create, so a read times out too.
	wantCode(t, c.Call(ctx(t), "getSnapshot", nil, nil, nil), service.CodeRuntimeUnavailable, "")

	var result RequestResult
	waitFor(t, "the create's receipt", func() bool {
		return c.Call(ctx(t), "getRequestResult", nil, resultParams(m.RequestID, m.IssuedAt), &result) == nil && result.Receipt != nil
	})
	if result.Receipt.Operation != "createPortal" || findPortal(snapshot(t, c), result.Receipt.PortalID) == nil {
		t.Fatalf("recovered receipt %+v does not name the created Portal", result.Receipt)
	}
}

// TestMutationWhoseContextEndsIsUncertain ends the caller's context after
// the frame is written and while the engine is busy with it.
func TestMutationWhoseContextEndsIsUncertain(t *testing.T) {
	clock := &slowClock{}
	h := newHarness(t, func(h *harness) { h.opts.Clock = clock.now })
	c := h.dial()
	chooseLogging(t, c)
	m := newMutation(t, c)
	clock.armed.Store(true)
	short, cancel := context.WithTimeout(ctx(t), 100*time.Millisecond)
	defer cancel()
	if err := c.Call(short, "createPortal", m, web, nil); !errors.Is(err, ErrUncertain) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("create past the caller's deadline = %v, want ErrUncertain and DeadlineExceeded", err)
	}
}
