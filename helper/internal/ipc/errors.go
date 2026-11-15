// Package ipc is the management v1 transport: a same-user Unix-domain socket
// endpoint, kernel peer-credential checks in both directions, length-prefixed
// frames, strict JSON and connection limits. It knows nothing of the engine.
package ipc

import "errors"

// Code is a management error code the transport itself produces.
type Code string

const (
	CodeIncompatibleVersion Code = "incompatibleVersion"
	CodeSlowConsumer        Code = "slowConsumer"
	CodeCapacityExceeded    Code = "capacityExceeded"
	CodeOwnershipBusy       Code = "ownershipBusy"
)

// Error is the wire error shape: code, optional safe field and Portal UUID,
// and whether retrying can succeed.
type Error struct {
	Code      Code   `json:"code"`
	Field     string `json:"field,omitempty"`
	PortalID  string `json:"portalID,omitempty"`
	Retryable bool   `json:"retryable"`
}

func (e *Error) Error() string { return "ipc: " + string(e.Code) }

// Errors never carry endpoint paths: they may reach shared diagnostics.
var (
	ErrUnsafeEndpoint = errors.New("ipc: unsafe management endpoint")
	ErrPeerRejected   = errors.New("ipc: peer is not this user")
	ErrInvalidFrame   = errors.New("ipc: invalid frame")
	ErrInvalidJSON    = errors.New("ipc: invalid JSON")
	ErrNotRunning     = errors.New("ipc: management service is not running")
	ErrRoot           = errors.New("ipc: root may not use the management transport")
)
