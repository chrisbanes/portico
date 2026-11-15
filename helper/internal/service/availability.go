package service

import (
	"github.com/chrisbanes/portico/helper/internal/installation"
	"github.com/chrisbanes/portico/helper/internal/portal"
)

// Availability says whether an operation can run now and, if not, why.
// Clients render it; every command rechecks the same rules server-side.
type Availability struct {
	Available bool `json:"available"`
	Reason    Code `json:"reason,omitempty"`
}

func availableIf(ok bool, reason Code) Availability {
	if ok {
		return Availability{Available: true}
	}
	return Availability{Reason: reason}
}

// mutations are the operations a persistence failure disables.
var mutations = map[Operation]bool{
	OpCreatePortal: true, OpSetDesiredState: true, OpSetDestination: true,
	OpSetLaunchAtLoginOffer: true, OpSetOperationalLogging: true, OpRemovePortal: true,
	OpRetryRemoval: true, OpDismissAlert: true, OpResetTailnet: true, OpRetryRuntime: true,
	OpStart: true, OpStop: true, OpSelectRemoteApp: true, OpResumeEnrolment: true,
}

func (e *Engine) withPersistence(ops map[Operation]Availability) map[Operation]Availability {
	if e.store.failed {
		for op := range ops {
			if mutations[op] {
				ops[op] = Availability{Reason: CodePersistenceUnavailable}
			}
		}
	}
	return ops
}

// installationAvailability extends the installation-wide rules of Swift's
// PortalActionAvailability with the contract's operations.
func (e *Engine) installationAvailability(state State) map[Operation]Availability {
	in := state.Installation
	return e.withPersistence(map[Operation]Availability{
		OpCreatePortal:          availableIf(in.Logging != installation.LoggingUndecided, CodeLoggingChoiceRequired),
		OpDiscoverLocalApps:     availableIf(e.opts.Discoverer != nil, CodeCapabilityUnavailable),
		OpSetOperationalLogging: availableIf(true, ""),
		OpSetLaunchAtLoginOffer: availableIf(e.opts.LaunchAtLoginOfferCapable, CodeCapabilityUnavailable),
		OpResetTailnet:          availableIf(in.Binding != nil && len(in.Portals) == 0, CodeNotAvailable),
		OpRetryRuntime:          availableIf(e.failure != runtimeFailureNone, CodeNotAvailable),
		OpDismissAlert:          availableIf(len(in.Alerts) > 0, CodeNotAvailable),
	})
}

// portalAvailability ports PortalActionAvailability's per-Portal matrix
// (start, stop, edit, Remote App choice, URL actions, remove, retry removal).
func (e *Engine) portalAvailability(state State, p installation.Portal, c *Connection) map[Operation]Availability {
	active := p.Lifecycle == installation.LifecycleActive
	hasURL := c != nil && c.PortalURL != ""
	retry := availableIf(p.Lifecycle == installation.LifecyclePendingRemoval && e.removalFailed[p.ID], CodeNotAvailable)
	if retry.Available && e.runtimeStatus(state) != runtimeRunning {
		retry = Availability{Reason: CodeRuntimeUnavailable}
	}
	return e.withPersistence(map[Operation]Availability{
		OpStart:           availableIf(active && p.DesiredState == portal.DesiredStateStopped, CodeNotAvailable),
		OpStop:            availableIf(active && p.DesiredState == portal.DesiredStateEnabled, CodeNotAvailable),
		OpSetDestination:  availableIf(active, CodeNotAvailable),
		OpSelectRemoteApp: availableIf(active && p.PublicAccess == portal.PublicAccessModePrivate, CodeNotAvailable),
		OpRemovePortal:    availableIf(active, CodeNotAvailable),
		OpRetryRemoval:    retry,
		OpCopyPortalURL:   availableIf(hasURL, CodeNotAvailable),
		OpOpenPortalURL:   availableIf(hasURL && c.Freshness == freshnessCurrent && c.State == portal.StateOnline, CodeNotAvailable),
		OpResumeEnrolment: e.enrolmentAvailability(state, p),
	})
}
