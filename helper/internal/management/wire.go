// Package management is the management v1 protocol over the ipc transport:
// the hello exchange, calls dispatched to a service.Session per connection,
// paged snapshots and private enrolment events, and a minimal typed client.
package management

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/go-json-experiment/json/jsontext"

	"github.com/chrisbanes/portico/helper/internal/discovery"
	"github.com/chrisbanes/portico/helper/internal/ipc"
	"github.com/chrisbanes/portico/helper/internal/service"
)

// Version is the management protocol version this package speaks.
const Version = 1

// Error is the wire error shape, for replies and connection-level errors.
type Error = ipc.Error

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// inbound is every member a client frame may carry. Pointers tell absent
// members from zero values.
type inbound struct {
	Type             string         `json:"type"`
	Version          *int           `json:"version"`
	CallID           *string        `json:"callID"`
	Method           *string        `json:"method"`
	RequestID        *string        `json:"requestID"`
	IssuedAt         *string        `json:"issuedAt"`
	ExpectedRevision *uint64        `json:"expectedRevision"`
	Params           jsontext.Value `json:"params"`
}

// Capabilities are hints, never authorization.
type Capabilities struct {
	// AutomaticDiscovery is "available" or "unavailable".
	AutomaticDiscovery string `json:"automaticDiscovery"`
}

type helloFrame struct {
	Type         string       `json:"type"`
	Version      int          `json:"version"`
	Epoch        string       `json:"epoch,omitempty"`
	Capabilities Capabilities `json:"capabilities"`
}

type callFrame struct {
	Type             string  `json:"type"`
	CallID           string  `json:"callID"`
	Method           string  `json:"method"`
	RequestID        *string `json:"requestID,omitempty"`
	IssuedAt         *string `json:"issuedAt,omitempty"`
	ExpectedRevision *uint64 `json:"expectedRevision,omitempty"`
	Params           any     `json:"params"`
}

type errorFrame struct {
	Type              string `json:"type"`
	Error             *Error `json:"error"`
	SupportedVersions []int  `json:"supportedVersions,omitempty"`
}

type replyFrame struct {
	Type   string `json:"type"`
	CallID string `json:"callID"`
	Result any    `json:"result,omitempty"`
	Error  *Error `json:"error,omitempty"`
	// Uncertain means the deadline passed with the outcome unknown; recover
	// it with getRequestResult and the same request ID.
	Uncertain bool `json:"uncertain,omitempty"`
}

type snapshotFrame struct {
	Type     string           `json:"type"`
	Snapshot service.Snapshot `json:"snapshot"`
}

type snapshotBegin struct {
	Type      string `json:"type"`
	CallID    string `json:"callID,omitempty"`
	Epoch     string `json:"epoch"`
	Sequence  uint64 `json:"sequence"`
	Revision  uint64 `json:"revision"`
	PageCount int    `json:"pageCount"`
}

type snapshotPage struct {
	Type      string `json:"type"`
	CallID    string `json:"callID,omitempty"`
	PageIndex int    `json:"pageIndex"`
	// Snapshot, on page 0 only, holds the installation fields with no Portals.
	Snapshot *service.Snapshot `json:"snapshot,omitempty"`
	Portals  []json.RawMessage `json:"portals"`
}

type snapshotEnd struct {
	Type     string `json:"type"`
	CallID   string `json:"callID,omitempty"`
	Sequence uint64 `json:"sequence"`
}

type enrolmentFrame struct {
	Type      string                     `json:"type"`
	Event     service.EnrolmentEventKind `json:"event"`
	RequestID string                     `json:"requestID"`
	PortalID  string                     `json:"portalID"`
	Nonce     string                     `json:"nonce"`
	URL       string                     `json:"url,omitempty"`
}

// Receipt is a mutation's durable result as clients see it; the request hash
// and retention time stay inside the service.
type Receipt struct {
	RequestID        string            `json:"requestID"`
	AcceptedAt       time.Time         `json:"acceptedAt"`
	RecoverUntil     time.Time         `json:"recoverUntil"`
	AcceptedRevision uint64            `json:"acceptedRevision"`
	Operation        service.Operation `json:"operation"`
	PortalID         string            `json:"portalID,omitempty"`
	Outcome          service.Outcome   `json:"outcome"`
}

func wireReceipt(r service.Receipt) Receipt {
	return Receipt{
		RequestID: r.RequestID, AcceptedAt: r.AcceptedAt, RecoverUntil: r.RecoverUntil,
		AcceptedRevision: r.AcceptedRevision, Operation: r.Operation, PortalID: r.PortalID, Outcome: r.Outcome,
	}
}

// RequestResult answers getRequestResult.
type RequestResult struct {
	Receipt     *Receipt `json:"receipt,omitempty"`
	NotAccepted bool     `json:"notAccepted,omitempty"`
}

type RequestResultParams struct {
	RequestID string `json:"requestID"`
	IssuedAt  string `json:"issuedAt"`
}

type DiscoverResult struct {
	Candidates []discovery.Candidate `json:"candidates"`
}

type DiagnosticsParams struct {
	PortalID string `json:"portalID,omitempty"`
}

type DiagnosticsResult struct {
	Report string `json:"report"`
}

// wireError maps engine and transport errors to the wire shape. Anything
// else carries no detail.
func wireError(err error) *Error {
	var se *service.Error
	var ie *Error
	switch {
	case errors.As(err, &se):
		return &Error{Code: ipc.Code(se.Code), Field: se.Field, PortalID: se.PortalID, Retryable: se.Retryable()}
	case errors.As(err, &ie):
		return ie
	}
	return &Error{Code: ipc.Code(service.CodeRuntimeUnavailable), Retryable: true}
}

func invalid(field string) *Error {
	return &Error{Code: ipc.Code(service.CodeInvalidRequest), Field: field}
}

// parseIssuedAt accepts only UTC RFC 3339 timestamps.
func parseIssuedAt(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	return t, err == nil && len(s) > 0 && s[len(s)-1] == 'Z'
}

// newUUID returns a lowercase random (version 4) UUID.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
