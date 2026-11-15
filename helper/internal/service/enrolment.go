package service

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/chrisbanes/portico/helper/internal/installation"
	"github.com/chrisbanes/portico/helper/internal/portal"
)

// EnrolmentEventKind is the kind of a private enrolment event.
type EnrolmentEventKind string

const (
	// EnrolmentAuthenticationURL carries Tailscale's current valid URL for
	// this attempt. It is transient: never stored, logged or replayed.
	EnrolmentAuthenticationURL EnrolmentEventKind = "authenticationURL"
	// EnrolmentCancelled ends an attempt that will deliver nothing more:
	// superseded by another resume, stopped, removed, or a runtime rebuild.
	EnrolmentCancelled EnrolmentEventKind = "cancelled"
	// EnrolmentFailed means the runtime could not start the login.
	EnrolmentFailed EnrolmentEventKind = "failed"
)

// EnrolmentEvent goes only to the session that requested the attempt.
type EnrolmentEvent struct {
	Kind      EnrolmentEventKind
	RequestID string
	PortalID  string
	Nonce     string
	// URL is set only for EnrolmentAuthenticationURL.
	URL string
}

// String and GoString keep the URL out of any %v or %+v formatting.
func (e EnrolmentEvent) String() string {
	return fmt.Sprintf("{Kind:%s RequestID:%s PortalID:%s Nonce:%s URL:%s}", e.Kind, e.RequestID, e.PortalID, e.Nonce, redacted(e.URL))
}

func (e EnrolmentEvent) GoString() string { return e.String() }

func redacted(s string) string {
	if s == "" {
		return ""
	}
	return "<redacted>"
}

// Enrolment is a Portal's latest resumeEnrolment action as snapshots show it.
// It never contains the URL.
type Enrolment struct {
	RequestID string `json:"requestID"`
	// Status is active while its requester's attempt is live, or
	// interrupted once that attempt is gone (disconnect, rebuild, restart);
	// an interrupted attempt needs an explicit new resume.
	Status string `json:"status"`
}

const (
	enrolmentActive      = "active"
	enrolmentInterrupted = "interrupted"
)

const OpResumeEnrolment Operation = "resumeEnrolment"

type ResumeEnrolmentParams struct {
	PortalID string `json:"portalID"`
}

// lease is the single live attempt for a Portal: requester session, request,
// Portal, runtime generation and nonce. A URL is delivered through it at
// most once, and only while it is current.
type lease struct {
	session   *Session
	requestID string
	portalID  string
	gen       uint64
	nonce     string
	delivered bool
}

// ReceiveEnrolmentEvents sets this session's private sink. fn is called with
// the engine locked and must not block or call back into the engine.
func (s *Session) ReceiveEnrolmentEvents(fn func(EnrolmentEvent)) {
	s.engine.mu.Lock()
	defer s.engine.mu.Unlock()
	s.enrolmentSink = fn
}

// ResumeEnrolment starts an explicit login attempt for an Enabled Portal that
// needs authentication. The receipt is durable; the URL arrives later as a
// private event to this session only. A retried request returns its receipt
// and starts nothing.
func (s *Session) ResumeEnrolment(req Request, p ResumeEnrolmentParams) (Receipt, error) {
	e := s.engine
	return e.mutate(OpResumeEnrolment, req, p, func(st *State, now time.Time) (change, error) {
		i := portalIndex(st, p.PortalID)
		if i < 0 {
			return change{}, &Error{Code: CodeNotFound, PortalID: p.PortalID}
		}
		if avail := e.enrolmentAvailability(*st, st.Installation.Portals[i]); !avail.Available {
			return change{}, &Error{Code: avail.Reason, PortalID: p.PortalID}
		}
		if _, open := e.sessions[s]; !open {
			return change{}, &Error{Code: CodeNotAvailable, PortalID: p.PortalID}
		}
		dropActions(st, p.PortalID)
		st.Actions = append(st.Actions, Action{RequestID: req.ID, Operation: OpResumeEnrolment, PortalID: p.PortalID, AcceptedAt: now})
		c := accepted(p.PortalID)
		c.after = func() { e.startLeaseLocked(s, req.ID, p.PortalID) }
		return c, nil
	})
}

// enrolmentAvailability: an active, Enabled Portal whose current runtime
// reports it is authenticating.
func (e *Engine) enrolmentAvailability(state State, p installation.Portal) Availability {
	switch {
	case p.Lifecycle != installation.LifecycleActive || p.DesiredState != portal.DesiredStateEnabled:
		return Availability{Reason: CodeNotAvailable}
	// A runtime being replaced or awaiting a retried close still looks
	// running, but no new attempt may start on it.
	case e.runtimeStatus(state) != runtimeRunning, e.closePending, e.runtimeLogging != state.Installation.Logging:
		return Availability{Reason: CodeRuntimeUnavailable}
	}
	c, ok := e.live[p.ID]
	return availableIf(ok && c.Freshness == freshnessCurrent && c.State == portal.StateAuthenticating, CodeNotAvailable)
}

// startLeaseLocked supersedes any earlier attempt, then asks the runtime to
// start the login outside the lock. Callers hold e.mu, after the commit.
func (e *Engine) startLeaseLocked(s *Session, requestID, portalID string) {
	e.dropLeaseLocked(portalID)
	l := &lease{session: s, requestID: requestID, portalID: portalID, gen: e.runtimeGen, nonce: newUUID()}
	e.leases[portalID] = l
	rt := e.runtime
	go func() {
		e.mu.Lock()
		current := e.leases[portalID] == l
		e.mu.Unlock()
		if !current {
			// Superseded or disconnected before the login started.
			return
		}
		err := callRuntime(func(ctx context.Context) error { return rt.Authenticate(ctx, portalID) })
		if err == nil {
			return
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.leases[portalID] == l {
			delete(e.leases, portalID)
			l.send(EnrolmentFailed, "")
			e.publishLocked()
		}
	}()
}

// dropLeaseLocked ends a Portal's attempt and tells only its requester.
func (e *Engine) dropLeaseLocked(portalID string) {
	if l, ok := e.leases[portalID]; ok {
		delete(e.leases, portalID)
		l.send(EnrolmentCancelled, "")
	}
}

func (e *Engine) dropAllLeasesLocked() {
	for id := range e.leases {
		e.dropLeaseLocked(id)
	}
}

func (l *lease) send(kind EnrolmentEventKind, rawURL string) {
	if l.session.enrolmentSink == nil {
		return
	}
	l.session.enrolmentSink(EnrolmentEvent{Kind: kind, RequestID: l.requestID, PortalID: l.portalID, Nonce: l.nonce, URL: rawURL})
}

// deliverURLLocked routes a runtime URL to the Portal's current lease only.
// URLs with no lease, from another runtime generation, after delivery, or
// that are not https with a host are dropped. Nothing keeps the URL.
func (e *Engine) deliverURLLocked(gen uint64, portalID, rawURL string) {
	l, ok := e.leases[portalID]
	if !ok || l.gen != gen || l.delivered {
		return
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return
	}
	l.delivered = true
	l.send(EnrolmentAuthenticationURL, rawURL)
}

// portalEnrolment projects the durable action marker for snapshots while the
// Portal needs authentication, or before any runtime facts exist (as just
// after a restart). The marker stays durable, so it shows again if the
// Portal returns to authenticating.
func (e *Engine) portalEnrolment(state State, p installation.Portal) *Enrolment {
	if p.Lifecycle != installation.LifecycleActive {
		return nil
	}
	if c, ok := e.live[p.ID]; ok && c.State != portal.StateAuthenticating {
		return nil
	}
	for _, a := range state.Actions {
		if a.Operation != OpResumeEnrolment || a.PortalID != p.ID {
			continue
		}
		status := enrolmentInterrupted
		if l, ok := e.leases[p.ID]; ok && l.requestID == a.RequestID {
			status = enrolmentActive
		}
		return &Enrolment{RequestID: a.RequestID, Status: status}
	}
	return nil
}
