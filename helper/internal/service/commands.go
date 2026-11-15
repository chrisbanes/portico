package service

import (
	"time"

	"github.com/chrisbanes/portico/helper/internal/installation"
	"github.com/chrisbanes/portico/helper/internal/portal"
)

// Typed params, one per mutation. They are also the canonical request-hash
// input, so field names and order are part of request identity.

type CreatePortalParams struct {
	Name         string                  `json:"name"`
	Destination  portal.Destination      `json:"destination"`
	PublicAccess portal.PublicAccessMode `json:"publicAccess"`
}

type SetDesiredStateParams struct {
	PortalID string              `json:"portalID"`
	State    portal.DesiredState `json:"state"`
}

type SetDestinationParams struct {
	PortalID    string             `json:"portalID"`
	Destination portal.Destination `json:"destination"`
}

type SetLaunchAtLoginOfferParams struct {
	State installation.LaunchAtLoginOffer `json:"state"`
}

type SetOperationalLoggingParams struct {
	Logging installation.Logging `json:"logging"`
}

type RemovePortalParams struct {
	PortalID  string `json:"portalID"`
	Confirmed bool   `json:"confirmed"`
}

type RetryRemovalParams struct {
	PortalID string `json:"portalID"`
}

type DismissAlertParams struct {
	AlertID string `json:"alertID"`
}

type ResetTailnetParams struct {
	Confirmed bool `json:"confirmed"`
}

type RetryRuntimeParams struct{}

func accepted(portalID string) change { return change{outcome: OutcomeAccepted, portalID: portalID} }
func noOp(portalID string) change     { return change{outcome: OutcomeNoOp, portalID: portalID} }

func invalid(field string) *Error { return &Error{Code: CodeInvalidRequest, Field: field} }

// activePortal finds an addressed Portal and requires it to be active.
func activePortal(st *State, id string) (int, error) {
	i := portalIndex(st, id)
	if i < 0 {
		return -1, &Error{Code: CodeNotFound, PortalID: id}
	}
	if st.Installation.Portals[i].Lifecycle != installation.LifecycleActive {
		return -1, &Error{Code: CodeNotAvailable, PortalID: id}
	}
	return i, nil
}

func (s *Session) CreatePortal(req Request, p CreatePortalParams) (Receipt, error) {
	return s.engine.mutate(OpCreatePortal, req, p, func(st *State, now time.Time) (change, error) {
		if st.Installation.Logging == installation.LoggingUndecided {
			return change{}, codeErr(CodeLoggingChoiceRequired)
		}
		if !dnsLabelPattern.MatchString(p.Name) {
			return change{}, invalid("name")
		}
		if p.Destination.Validate() != nil {
			return change{}, invalid("destination")
		}
		switch p.PublicAccess {
		case portal.PublicAccessModePrivate:
		case portal.PublicAccessModePublic:
			if p.Destination.Kind != portal.DestinationLocalApp {
				return change{}, invalid("publicAccess")
			}
		default:
			return change{}, invalid("publicAccess")
		}
		id := newUUID()
		st.Installation.Portals = append(st.Installation.Portals, installation.Portal{
			ID:           id,
			Name:         p.Name,
			Destination:  p.Destination,
			PublicAccess: p.PublicAccess,
			CreatedAt:    now,
			DesiredState: portal.DesiredStateEnabled,
			Lifecycle:    installation.LifecycleActive,
		})
		return accepted(id), nil
	})
}

func (s *Session) SetDesiredState(req Request, p SetDesiredStateParams) (Receipt, error) {
	return s.engine.mutate(OpSetDesiredState, req, p, func(st *State, _ time.Time) (change, error) {
		if p.State != portal.DesiredStateEnabled && p.State != portal.DesiredStateStopped {
			return change{}, invalid("state")
		}
		i, err := activePortal(st, p.PortalID)
		if err != nil {
			return change{}, err
		}
		if st.Installation.Portals[i].DesiredState == p.State {
			return noOp(p.PortalID), nil
		}
		st.Installation.Portals[i].DesiredState = p.State
		return accepted(p.PortalID), nil
	})
}

func (s *Session) SetDestination(req Request, p SetDestinationParams) (Receipt, error) {
	return s.engine.mutate(OpSetDestination, req, p, func(st *State, _ time.Time) (change, error) {
		if p.Destination.Validate() != nil {
			return change{}, invalid("destination")
		}
		i, err := activePortal(st, p.PortalID)
		if err != nil {
			return change{}, err
		}
		current := &st.Installation.Portals[i]
		if current.PublicAccess == portal.PublicAccessModePublic && p.Destination.Kind != portal.DestinationLocalApp {
			return change{}, &Error{Code: CodeNotAvailable, Field: "destination", PortalID: p.PortalID}
		}
		if current.Destination == p.Destination {
			return noOp(p.PortalID), nil
		}
		current.Destination = p.Destination
		return accepted(p.PortalID), nil
	})
}

// offerTransitions are the contract's launch-at-login offer moves.
var offerTransitions = map[installation.LaunchAtLoginOffer][]installation.LaunchAtLoginOffer{
	installation.OfferNotOffered: {installation.OfferPresented, installation.OfferAccepted},
	installation.OfferPresented:  {installation.OfferAccepted, installation.OfferDeclined},
	installation.OfferDeclined:   {installation.OfferAccepted},
}

func (s *Session) SetLaunchAtLoginOffer(req Request, p SetLaunchAtLoginOfferParams) (Receipt, error) {
	capable := s.engine.opts.LaunchAtLoginOfferCapable
	return s.engine.mutate(OpSetLaunchAtLoginOffer, req, p, func(st *State, _ time.Time) (change, error) {
		if !capable {
			return change{}, codeErr(CodeCapabilityUnavailable)
		}
		switch p.State {
		case installation.OfferPresented, installation.OfferAccepted, installation.OfferDeclined:
		default:
			return change{}, invalid("state")
		}
		current := st.Installation.LaunchAtLoginOffer
		if current == p.State {
			return noOp(""), nil
		}
		for _, next := range offerTransitions[current] {
			if next == p.State {
				st.Installation.LaunchAtLoginOffer = p.State
				return accepted(""), nil
			}
		}
		return change{}, codeErr(CodeNotAvailable)
	})
}

// SetOperationalLogging commits the preference first; the runtime worker then
// closes the whole runtime and builds a new one with the new setting.
func (s *Session) SetOperationalLogging(req Request, p SetOperationalLoggingParams) (Receipt, error) {
	return s.engine.mutate(OpSetOperationalLogging, req, p, func(st *State, _ time.Time) (change, error) {
		if p.Logging != installation.LoggingEnabled && p.Logging != installation.LoggingDisabled {
			return change{}, invalid("logging")
		}
		if st.Installation.Logging == p.Logging {
			return noOp(""), nil
		}
		st.Installation.Logging = p.Logging
		return accepted(""), nil
	})
}

// RemovePortal durably marks the Portal as removing before the runtime
// excludes it, closes its node and deletes its identity.
func (s *Session) RemovePortal(req Request, p RemovePortalParams) (Receipt, error) {
	e := s.engine
	return e.mutate(OpRemovePortal, req, p, func(st *State, _ time.Time) (change, error) {
		if !p.Confirmed {
			return change{}, codeErr(CodeConfirmationRequired)
		}
		i, err := activePortal(st, p.PortalID)
		if err != nil {
			return change{}, err
		}
		st.Installation.Portals[i].Lifecycle = installation.LifecyclePendingRemoval
		st.Installation.Portals[i].RemovalAssignedName = optional(e.live[p.PortalID].AssignedName)
		c := accepted(p.PortalID)
		c.after = func() { delete(e.live, p.PortalID) }
		return c, nil
	})
}

// RetryRemoval re-runs a failed removal. It never re-enables the Portal.
func (s *Session) RetryRemoval(req Request, p RetryRemovalParams) (Receipt, error) {
	e := s.engine
	return e.mutate(OpRetryRemoval, req, p, func(st *State, _ time.Time) (change, error) {
		i := portalIndex(st, p.PortalID)
		if i < 0 {
			return change{}, &Error{Code: CodeNotFound, PortalID: p.PortalID}
		}
		if avail := e.portalAvailability(*st, st.Installation.Portals[i], nil)[OpRetryRemoval]; !avail.Available {
			return change{}, &Error{Code: avail.Reason, PortalID: p.PortalID}
		}
		c := accepted(p.PortalID)
		c.after = func() { delete(e.removalFailed, p.PortalID) }
		return c, nil
	})
}

func (s *Session) DismissAlert(req Request, p DismissAlertParams) (Receipt, error) {
	return s.engine.mutate(OpDismissAlert, req, p, func(st *State, _ time.Time) (change, error) {
		for i, a := range st.Installation.Alerts {
			if a.ID == p.AlertID {
				st.Installation.Alerts = append(st.Installation.Alerts[:i], st.Installation.Alerts[i+1:]...)
				return accepted(""), nil
			}
		}
		return change{}, &Error{Code: CodeNotFound, Field: "alertID"}
	})
}

// ResetTailnet clears the binding only when no Portal records remain,
// including pending cleanup.
func (s *Session) ResetTailnet(req Request, p ResetTailnetParams) (Receipt, error) {
	return s.engine.mutate(OpResetTailnet, req, p, func(st *State, _ time.Time) (change, error) {
		if !p.Confirmed {
			return change{}, codeErr(CodeConfirmationRequired)
		}
		if st.Installation.Binding == nil || len(st.Installation.Portals) > 0 {
			return change{}, codeErr(CodeNotAvailable)
		}
		st.Installation.Binding = nil
		return accepted(""), nil
	})
}

// RetryRuntime clears a recoverable runtime failure so the worker tries again:
// a failed start builds a new runtime; a failed close retries the close first.
func (s *Session) RetryRuntime(req Request) (Receipt, error) {
	e := s.engine
	return e.mutate(OpRetryRuntime, req, RetryRuntimeParams{}, func(st *State, _ time.Time) (change, error) {
		if e.failure == runtimeFailureNone {
			return change{}, codeErr(CodeNotAvailable)
		}
		c := accepted("")
		c.after = func() { e.failure = runtimeFailureNone }
		return c, nil
	})
}
