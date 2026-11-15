// Package service is the Portal-management authority: it owns an isolated
// state root, imports the historical installation into a service generation
// and commits durable intent through one transactional state document.
// It builds on unix only (flock and file ownership), the macOS and Linux targets.
package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"slices"
	"time"

	"github.com/chrisbanes/portico/helper/internal/installation"
)

// Errors map onto the management contract's error codes.
var (
	ErrOwnershipBusy          = errors.New("ownershipBusy")
	ErrPersistenceUnavailable = errors.New("persistenceUnavailable")
	ErrUnsupportedState       = errors.New("unsupportedState")
	ErrCapacityExceeded       = errors.New("capacityExceeded")
)

func persistenceErr(err error) error { return fmt.Errorf("%w: %w", ErrPersistenceUnavailable, err) }
func unsupportedErr(err error) error { return fmt.Errorf("%w: %w", ErrUnsupportedState, err) }
func unsafeErr(name string) error {
	return fmt.Errorf("%w: unsafe %s", ErrUnsupportedState, name)
}

const stateSchema = 1

// maxStateSize is the contract's 64 MiB state-document limit.
var maxStateSize = 64 << 20

// State is the schema-v1 service-state document.
type State struct {
	Schema       int                       `json:"schema"`
	Revision     uint64                    `json:"revision"`
	Installation installation.Installation `json:"installation"`
	// Requests holds the retained receipts of accepted mutations.
	Requests []Receipt `json:"requests"`
	// Actions holds durable markers for accepted runtime actions that a
	// restart interrupts; resumeEnrolment writes them.
	Actions []Action `json:"actions"`
	// ClockHighWater is the latest clock reading any commit used, so a
	// clock rollback cannot reopen an expired admission window.
	ClockHighWater time.Time `json:"clockHighWater"`
	// Rejections keeps the sanitized evidence of each pending tailnet
	// rejection, committed with the rejection, so the alert that replaces the
	// Portal still names the rejected node after a restart.
	Rejections []Rejection `json:"rejections"`
	Migration  Migration   `json:"migration"`
}

// Rejection is the evidence for a Portal pending tailnet rejection.
type Rejection struct {
	PortalID               string `json:"portalID"`
	AssignedName           string `json:"assignedName,omitempty"`
	RejectedMagicDNSSuffix string `json:"rejectedMagicDNSSuffix,omitempty"`
}

// Migration records where the generation came from.
type Migration struct {
	// Source is the historical file imported; empty for a fresh install.
	Source        string `json:"source,omitempty"`
	SourceVersion int    `json:"sourceVersion,omitempty"`
	// FirstMutationRequestID is the first request accepted after the import.
	// Once set, restoring the original checkpoint would lose accepted changes.
	FirstMutationRequestID string `json:"firstMutationRequestID,omitempty"`
}

// Action is a durable marker for an accepted runtime action.
type Action struct {
	RequestID  string    `json:"requestID"`
	Operation  Operation `json:"operation"`
	PortalID   string    `json:"portalID,omitempty"`
	AcceptedAt time.Time `json:"acceptedAt"`
}

// clone copies the slices a mutation may change. Pointer fields are treated
// as immutable and replaced rather than modified.
func (s State) clone() State {
	s.Installation.Portals = slices.Clone(s.Installation.Portals)
	s.Installation.Alerts = slices.Clone(s.Installation.Alerts)
	s.Requests = slices.Clone(s.Requests)
	s.Actions = slices.Clone(s.Actions)
	s.Rejections = slices.Clone(s.Rejections)
	return s
}

// Store is the committed service generation. It is not safe for concurrent
// use; the engine serializes access.
type Store struct {
	root       *os.Root
	lock       *os.File
	opts       Options
	generation string
	// identityRoot is the absolute path of the generation's tsnet directory,
	// which becomes the runtime state root.
	identityRoot string
	state        State
	// failed is set when a commit's outcome is uncertain; writes stop until
	// the store is reopened.
	failed bool
}

// State returns a copy of the committed document.
func (s *Store) State() State { return s.state.clone() }

// IdentityRoot is the absolute path of the committed generation's identity
// directory, the runtime state root.
func (s *Store) IdentityRoot() string { return s.identityRoot }

// Commit applies mutate to a copy of the state and durably replaces the
// document: temp file, fsync, rename, directory fsync. On error the
// in-memory state is unchanged.
func (s *Store) Commit(mutate func(*State) error) error {
	if s.failed {
		return persistenceErr(errors.New("an earlier commit failed; reopen the store"))
	}
	next := s.state.clone()
	if err := mutate(&next); err != nil {
		return err
	}
	data, err := encodeState(next)
	if err != nil {
		return err
	}
	if err := s.opts.hook(PhaseCommitWrite); err != nil {
		s.failed = true
		return err
	}
	name := path.Join(s.generation, stateFileName)
	temp := name + ".tmp"
	if err := s.root.Remove(temp); err != nil && !os.IsNotExist(err) {
		s.failed = true
		return persistenceErr(err)
	}
	if err := writeFileSync(s.root, temp, data); err != nil {
		s.failed = true
		return err
	}
	if err := s.opts.hook(PhaseCommitRename); err != nil {
		s.failed = true
		return err
	}
	if err := s.root.Rename(temp, name); err != nil {
		s.failed = true
		return persistenceErr(err)
	}
	if err := s.opts.hook(PhaseCommitFsync); err != nil {
		s.failed = true
		return err
	}
	if err := syncDir(s.root, s.generation); err != nil {
		s.failed = true
		return err
	}
	// Retain a decoded copy of the committed bytes, not next itself: mutate
	// may have stored pointers or slices its caller still holds.
	committed, err := decodeState(data)
	if err != nil {
		s.failed = true
		return err
	}
	s.state = committed
	return nil
}

// Close releases ownership of the state root.
func (s *Store) Close() error {
	return errors.Join(s.lock.Close(), s.root.Close())
}

// encodeState validates state and rejects a document over the size limit
// before anything is written.
func encodeState(state State) ([]byte, error) {
	if state.Schema != stateSchema {
		return nil, unsupportedErr(fmt.Errorf("state schema %d", state.Schema))
	}
	if err := installation.Validate(state.Installation); err != nil {
		return nil, fmt.Errorf("invalid installation: %w", err)
	}
	if err := validateRejections(state); err != nil {
		return nil, err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return nil, persistenceErr(err)
	}
	if len(data) > maxStateSize {
		return nil, ErrCapacityExceeded
	}
	return data, nil
}

// decodeState reads a committed document, failing closed on anything it does
// not fully understand.
func decodeState(data []byte) (State, error) {
	var state State
	if err := decodeStrict(data, &state); err != nil {
		return State{}, unsupportedErr(err)
	}
	if state.Schema != stateSchema {
		return State{}, unsupportedErr(fmt.Errorf("state schema %d", state.Schema))
	}
	if err := installation.Validate(state.Installation); err != nil {
		return State{}, unsupportedErr(err)
	}
	if err := validateRejections(state); err != nil {
		return State{}, unsupportedErr(err)
	}
	return state, nil
}

// validateRejections allows at most one entry per Portal, only for a Portal
// pending rejection, holding only sanitized facts.
func validateRejections(state State) error {
	seen := map[string]bool{}
	for _, r := range state.Rejections {
		i := portalIndex(&state, r.PortalID)
		if seen[r.PortalID] || i < 0 || state.Installation.Portals[i].Lifecycle != installation.LifecyclePendingTailnetRejection ||
			(r.AssignedName != "" && !dnsLabelPattern.MatchString(r.AssignedName)) ||
			(r.RejectedMagicDNSSuffix != "" && !isMagicDNSSuffix(r.RejectedMagicDNSSuffix)) {
			return errors.New("invalid rejection evidence")
		}
		seen[r.PortalID] = true
	}
	return nil
}
