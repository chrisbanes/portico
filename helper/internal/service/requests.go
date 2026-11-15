package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Operation names a management v1 method, or an availability entry derived
// from one (start, stop, copyPortalURL, openPortalURL, selectRemoteApp).
type Operation string

const (
	OpCreatePortal          Operation = "createPortal"
	OpSetDesiredState       Operation = "setDesiredState"
	OpSetDestination        Operation = "setDestination"
	OpSetLaunchAtLoginOffer Operation = "setLaunchAtLoginOffer"
	OpSetOperationalLogging Operation = "setOperationalLogging"
	OpRemovePortal          Operation = "removePortal"
	OpRetryRemoval          Operation = "retryRemoval"
	OpDismissAlert          Operation = "dismissAlert"
	OpResetTailnet          Operation = "resetTailnet"
	OpRetryRuntime          Operation = "retryRuntime"
	OpDiscoverLocalApps     Operation = "discoverLocalApps"

	OpStart           Operation = "start"
	OpStop            Operation = "stop"
	OpSelectRemoteApp Operation = "selectRemoteApp"
	OpCopyPortalURL   Operation = "copyPortalURL"
	OpOpenPortalURL   Operation = "openPortalURL"
)

// Code is a management contract error code.
type Code string

const (
	CodeInvalidRequest         Code = "invalidRequest"
	CodeNotFound               Code = "notFound"
	CodeNotAvailable           Code = "notAvailable"
	CodeRevisionConflict       Code = "revisionConflict"
	CodeLoggingChoiceRequired  Code = "loggingChoiceRequired"
	CodeConfirmationRequired   Code = "confirmationRequired"
	CodeCapabilityUnavailable  Code = "capabilityUnavailable"
	CodeRequestConflict        Code = "requestConflict"
	CodeRequestNotYetValid     Code = "requestNotYetValid"
	CodeResultExpired          Code = "resultExpired"
	CodeCapacityExceeded       Code = "capacityExceeded"
	CodePersistenceUnavailable Code = "persistenceUnavailable"
	CodeUnsupportedState       Code = "unsupportedState"
	CodeRuntimeUnavailable     Code = "runtimeUnavailable"
)

// Error is a structured management error. It carries no free text, so it
// can never leak a URL, path or credential.
type Error struct {
	Code     Code
	Field    string
	PortalID string
}

func (e *Error) Error() string {
	switch {
	case e.Field != "":
		return fmt.Sprintf("%s (%s)", e.Code, e.Field)
	case e.PortalID != "":
		return fmt.Sprintf("%s (portal %s)", e.Code, e.PortalID)
	}
	return string(e.Code)
}

// Retryable reports whether the same request may be retried as is. It never
// means "allocate a new request ID".
func (e *Error) Retryable() bool {
	return e.Code == CodePersistenceUnavailable || e.Code == CodeRuntimeUnavailable
}

func codeErr(code Code) *Error { return &Error{Code: code} }

// IsCode reports whether err is a management error with code.
func IsCode(err error, code Code) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// Request is the durable identity every mutation carries.
type Request struct {
	ID               string
	IssuedAt         time.Time
	ExpectedRevision uint64
}

type Outcome string

const (
	OutcomeAccepted Outcome = "accepted"
	OutcomeNoOp     Outcome = "noOp"
)

// Receipt is the durable result of an accepted mutation.
type Receipt struct {
	RequestID        string    `json:"requestID"`
	Hash             string    `json:"hash"`
	IssuedAt         time.Time `json:"issuedAt"`
	AcceptedAt       time.Time `json:"acceptedAt"`
	RecoverUntil     time.Time `json:"recoverUntil"`
	RetainUntil      time.Time `json:"retainUntil"`
	AcceptedRevision uint64    `json:"acceptedRevision"`
	Operation        Operation `json:"operation"`
	PortalID         string    `json:"portalID,omitempty"`
	Outcome          Outcome   `json:"outcome"`
}

// RequestResult answers getRequestResult: a receipt, or NotAccepted for an
// unrecorded request that is still admissible and therefore safe to retry.
type RequestResult struct {
	Receipt     *Receipt
	NotAccepted bool
}

const (
	managementVersion = 1
	recoveryWindow    = 24 * time.Hour
	futureAllowance   = 5 * time.Minute
)

// maxReceipts is the contract's retained-receipt limit; tests lower it.
var maxReceipts = 10_000

var requestIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// requestHash is the canonical identity of a request: version, method,
// issuedAt, expectedRevision and typed params. Call IDs and JSON formatting
// are not part of it. Params must be a struct, which encodes deterministically.
func requestHash(op Operation, req Request, params any) (string, error) {
	data, err := json.Marshal(struct {
		Version          int       `json:"version"`
		Method           Operation `json:"method"`
		IssuedAt         string    `json:"issuedAt"`
		ExpectedRevision uint64    `json:"expectedRevision"`
		Params           any       `json:"params"`
	}{managementVersion, op, req.IssuedAt.UTC().Format(time.RFC3339Nano), req.ExpectedRevision, params})
	if err != nil {
		return "", codeErr(CodeInvalidRequest)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// effectiveNow is the clock reading clamped to the durable high-water mark.
func effectiveNow(clock time.Time, state State) time.Time {
	clock = clock.UTC()
	if clock.Before(state.ClockHighWater) {
		return state.ClockHighWater
	}
	return clock
}

func findReceipt(state State, id string) *Receipt {
	for i := range state.Requests {
		if state.Requests[i].RequestID == id {
			return &state.Requests[i]
		}
	}
	return nil
}

// recall applies the retry rules to a request ID. It returns the existing
// receipt for a duplicate, or nil when the request is new and admissible.
func recall(state State, now time.Time, req Request, hash string) (*Receipt, error) {
	if !requestIDPattern.MatchString(req.ID) || req.IssuedAt.IsZero() {
		return nil, &Error{Code: CodeInvalidRequest, Field: "requestID"}
	}
	if r := retained(state, now, req.ID); r != nil {
		if r.Hash != hash {
			return nil, codeErr(CodeRequestConflict)
		}
		return recoverable(now, r)
	}
	return nil, admissible(now, req.IssuedAt)
}

// retained finds a receipt that is still within its retention period.
func retained(state State, now time.Time, id string) *Receipt {
	if r := findReceipt(state, id); r != nil && now.Before(r.RetainUntil) {
		return r
	}
	return nil
}

// recoverable returns a copy of r while it is inside its recovery window.
func recoverable(now time.Time, r *Receipt) (*Receipt, error) {
	if now.After(r.RecoverUntil) {
		return nil, codeErr(CodeResultExpired)
	}
	receipt := *r
	return &receipt, nil
}

func admissible(now, issuedAt time.Time) error {
	if issuedAt.Before(now.Add(-recoveryWindow)) {
		return codeErr(CodeResultExpired)
	}
	if issuedAt.After(now.Add(futureAllowance)) {
		return codeErr(CodeRequestNotYetValid)
	}
	return nil
}

// newReceipt records an accepted request, dropping receipts past retention.
// Retention runs until the later of acceptedAt+24h and issuedAt+24h+5m, so an
// unexpired timestamp can never become a new operation after compaction.
func newReceipt(state *State, now time.Time, req Request, hash string, op Operation, portalID string, outcome Outcome) Receipt {
	retain := now.Add(recoveryWindow)
	if late := req.IssuedAt.UTC().Add(recoveryWindow + futureAllowance); late.After(retain) {
		retain = late
	}
	receipt := Receipt{
		RequestID:        req.ID,
		Hash:             hash,
		IssuedAt:         req.IssuedAt.UTC(),
		AcceptedAt:       now,
		RecoverUntil:     now.Add(recoveryWindow),
		RetainUntil:      retain,
		AcceptedRevision: state.Revision,
		Operation:        op,
		PortalID:         portalID,
		Outcome:          outcome,
	}
	kept := state.Requests[:0]
	for _, r := range state.Requests {
		if now.Before(r.RetainUntil) {
			kept = append(kept, r)
		}
	}
	state.Requests = append(kept, receipt)
	return receipt
}

func retainedReceipts(state State, now time.Time) int {
	n := 0
	for _, r := range state.Requests {
		if now.Before(r.RetainUntil) {
			n++
		}
	}
	return n
}
