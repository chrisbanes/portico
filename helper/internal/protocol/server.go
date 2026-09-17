package protocol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chrisbanes/portico/helper/internal/discovery"
	"github.com/chrisbanes/portico/helper/internal/portal"
)

const Version = 5

const invalidRequestDiagnostic = "portico-helper: invalid request\n"

type PortalRuntime interface {
	Reconcile(context.Context, []portal.Config, func(portal.Event)) ([]portal.ReconcileEntry, error)
	Authenticate(context.Context, string) error
	CleanupRejectedPortal(context.Context, string) error
	RemovePortal(context.Context, string) error
	Close(context.Context) error
}

type LocalAppDiscoverer interface {
	Discover(context.Context) ([]discovery.Candidate, error)
}

type Services struct {
	PortalRuntime      PortalRuntime
	LocalAppDiscoverer LocalAppDiscoverer
}

type request struct {
	Version   *int            `json:"version"`
	RequestID string          `json:"requestId"`
	Command   string          `json:"command"`
	Payload   json.RawMessage `json:"payload"`
}

type response struct {
	Version   int            `json:"version"`
	RequestID string         `json:"requestId"`
	Result    any            `json:"result,omitempty"`
	Error     *protocolError `json:"error,omitempty"`
}

type protocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type handshakeResult struct {
	ProtocolVersion int `json:"protocolVersion"`
}

type acceptedResult struct {
	Accepted bool `json:"accepted"`
}

type discoverLocalAppsResult struct {
	Candidates []discovery.Candidate `json:"candidates"`
}

type reconcilePortalPayload struct {
	PortalID     string              `json:"portalId"`
	PortalName   string              `json:"portalName"`
	Destination  portal.Destination  `json:"destination"`
	DesiredState portal.DesiredState `json:"desiredState"`
}

type reconcilePortalsPayload struct {
	Portals *[]reconcilePortalPayload `json:"portals"`
}

type reconcilePortalsResult struct {
	Entries []portal.ReconcileEntry `json:"entries"`
}

type authenticatePortalPayload struct {
	PortalID string `json:"portalId"`
}

type cleanupRejectedPortalPayload struct {
	PortalID string `json:"portalId"`
}

type removePortalPayload struct {
	PortalID string `json:"portalId"`
}

type eventMessage struct {
	Version  int    `json:"version"`
	Event    string `json:"event"`
	PortalID string `json:"portalId"`
	Payload  any    `json:"payload"`
}

type authenticationURLPayload struct {
	URL string `json:"url"`
}

type messageWriter struct {
	ctx      context.Context
	queue    chan outputFrame
	failed   chan struct{}
	stop     chan struct{}
	stopped  chan struct{}
	fail     func()
	once     sync.Once
	stopOnce sync.Once
}

type outputFrame struct {
	value   any
	flushed chan struct{}
}

type inputTerminal struct {
	invalid bool
}

type operationResult struct {
	err     error
	invalid bool
}

var errInvalidRequest = errors.New("invalid request")

func (w *messageWriter) write(value any) error {
	return w.writeContext(w.ctx, value)
}

func (w *messageWriter) writeContext(ctx context.Context, value any) error {
	select {
	case <-w.failed:
		return errors.New("write protocol output")
	default:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-w.failed:
		return errors.New("write protocol output")
	case <-ctx.Done():
		return ctx.Err()
	case w.queue <- outputFrame{value: value}:
		return nil
	}
}

func (w *messageWriter) flush(ctx context.Context) error {
	flushed := make(chan struct{})
	select {
	case <-w.failed:
		return errors.New("write protocol output")
	case <-ctx.Done():
		return ctx.Err()
	case w.queue <- outputFrame{flushed: flushed}:
	}
	select {
	case <-w.failed:
		return errors.New("write protocol output")
	case <-ctx.Done():
		return ctx.Err()
	case <-flushed:
		return nil
	}
}

func (w *messageWriter) stopContext(ctx context.Context) {
	w.stopOnce.Do(func() { close(w.stop) })
	select {
	case <-w.stopped:
	case <-ctx.Done():
	}
}

func newMessageWriter(ctx context.Context, output io.Writer, fail func()) *messageWriter {
	w := &messageWriter{
		ctx:     ctx,
		queue:   make(chan outputFrame, 16),
		failed:  make(chan struct{}),
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
		fail:    fail,
	}
	go func() {
		defer close(w.stopped)
		encoder := json.NewEncoder(output)
		for {
			select {
			case <-w.stop:
				return
			default:
			}
			var frame outputFrame
			select {
			case <-w.stop:
				return
			case frame = <-w.queue:
			}
			if frame.flushed != nil {
				close(frame.flushed)
				continue
			}
			if err := encoder.Encode(frame.value); err != nil {
				w.once.Do(func() {
					close(w.failed)
					w.fail()
				})
				return
			}
		}
	}()
	return w
}

func Serve(input io.Reader, output, diagnostics io.Writer) int {
	return ServeWithRuntime(input, output, diagnostics, nil)
}

func ServeWithRuntime(input io.Reader, output, diagnostics io.Writer, runtime PortalRuntime) int {
	return ServeWithServices(input, output, diagnostics, Services{PortalRuntime: runtime})
}

func ServeWithServices(input io.Reader, output, diagnostics io.Writer, services Services) int {
	runtime := services.PortalRuntime
	var closeOnce sync.Once
	var closeErr error
	closeRuntime := func(ctx context.Context) error {
		if runtime == nil {
			return nil
		}
		closeOnce.Do(func() { closeErr = runtime.Close(ctx) })
		return closeErr
	}
	root, cancelRoot := context.WithCancel(context.Background())
	failOutput := func() {
		cancelRoot()
		if closer, ok := input.(io.Closer); ok {
			_ = closer.Close()
		}
	}
	defer cancelRoot()
	writer := newMessageWriter(root, output, failOutput)
	emit := func(event portal.Event) {
		if event.Status != nil {
			_ = writer.writeContext(root, eventMessage{Version: Version, Event: "portalStatus", PortalID: event.PortalID, Payload: event.Status})
		}
		if event.AuthenticationURL != "" {
			_ = writer.writeContext(root, eventMessage{
				Version: Version, Event: "authenticationURL", PortalID: event.PortalID,
				Payload: authenticationURLPayload{URL: event.AuthenticationURL},
			})
		}
	}

	ordinary := make(chan request, 1)
	admissions := make(chan struct{}, 2)
	shutdown := make(chan request, 1)
	terminal := make(chan inputTerminal, 1)
	go readRequests(root, input, admissions, ordinary, shutdown, terminal)

	finish := func(shutdownRequest *request, invalidInput bool) int {
		// Cancellation comes before close so runtime work and discovery cannot
		// outlive the single teardown deadline.
		cancelRoot()
		if closer, ok := input.(io.Closer); ok {
			_ = closer.Close()
		}
		closeContext, closeCancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer closeCancel()
		defer writer.stopContext(closeContext)
		if err := closeRuntime(closeContext); err != nil {
			if shutdownRequest != nil {
				_ = writer.writeContext(closeContext, errorResponse(shutdownRequest.RequestID, "runtimeFailure", "portal runtime failed"))
				_ = writer.flush(closeContext)
			}
			return 1
		}
		if shutdownRequest != nil {
			if writer.writeContext(closeContext, response{Version: Version, RequestID: shutdownRequest.RequestID, Result: acceptedResult{Accepted: true}}) != nil || writer.flush(closeContext) != nil {
				return 1
			}
			return 0
		}
		flushed := writer.flush(closeContext)
		if invalidInput {
			select {
			case <-writer.failed:
			default:
				_, _ = io.WriteString(diagnostics, invalidRequestDiagnostic)
			}
			return 1
		}
		if flushed != nil {
			return 1
		}
		select {
		case <-writer.failed:
			return 1
		default:
			return 0
		}
	}

	var active bool
	var activeDone <-chan operationResult
	start := func(next request) {
		done := make(chan operationResult, 1)
		active = true
		activeDone = done
		go func() {
			err := serveRequest(root, next, runtime, services.LocalAppDiscoverer, writer, emit)
			done <- operationResult{err: err, invalid: errors.Is(err, errInvalidRequest)}
		}()
	}

	for {
		// A direct shutdown must win over a staged ordinary request whenever it
		// is already available.
		select {
		case shutdownRequest := <-shutdown:
			return finish(&shutdownRequest, false)
		default:
		}
		select {
		case inputEnd := <-terminal:
			return finish(nil, inputEnd.invalid)
		default:
		}
		if !active {
			select {
			case shutdownRequest := <-shutdown:
				return finish(&shutdownRequest, false)
			case inputEnd := <-terminal:
				return finish(nil, inputEnd.invalid)
			case next := <-ordinary:
				// If terminal input became ready in the same scheduler turn as a
				// staged request, terminal handling wins and the request is dropped.
				select {
				case shutdownRequest := <-shutdown:
					return finish(&shutdownRequest, false)
				default:
				}
				select {
				case inputEnd := <-terminal:
					return finish(nil, inputEnd.invalid)
				default:
				}
				if root.Err() != nil {
					return finish(nil, false)
				}
				start(next)
			case <-root.Done():
				return finish(nil, false)
			}
			continue
		}

		select {
		case shutdownRequest := <-shutdown:
			return finish(&shutdownRequest, false)
		case inputEnd := <-terminal:
			return finish(nil, inputEnd.invalid)
		case result := <-activeDone:
			active = false
			activeDone = nil
			<-admissions
			if result.err != nil && root.Err() == nil {
				return finish(nil, result.invalid)
			}
		case <-root.Done():
			return finish(nil, false)
		}
	}
}

func readRequests(ctx context.Context, input io.Reader, admissions chan struct{}, ordinary chan<- request, shutdown chan<- request, terminal chan<- inputTerminal) {
	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		select {
		case admissions <- struct{}{}:
		case <-ctx.Done():
			return
		}
		var next request
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&next); err != nil || decoder.Decode(&struct{}{}) != io.EOF || !next.isStructurallyValid() {
			select {
			case terminal <- inputTerminal{invalid: true}:
			case <-ctx.Done():
			}
			return
		}
		if *next.Version == Version && (next.Command == "handshake" || next.Command == "discoverLocalApps" || next.Command == "shutdown") && !isEmptyPayload(next.Payload) {
			select {
			case terminal <- inputTerminal{invalid: true}:
			case <-ctx.Done():
			}
			return
		}
		if *next.Version == Version && next.Command == "shutdown" && isEmptyPayload(next.Payload) {
			<-admissions
			select {
			case shutdown <- next:
			case <-ctx.Done():
			}
			return
		}
		select {
		case ordinary <- next:
		case <-ctx.Done():
			return
		}
	}
	select {
	case terminal <- inputTerminal{invalid: scanner.Err() != nil}:
	case <-ctx.Done():
	}
}

func serveRequest(ctx context.Context, request request, runtime PortalRuntime, discoverer LocalAppDiscoverer, writer *messageWriter, emit func(portal.Event)) error {
	write := func(value any) error { return writer.writeContext(ctx, value) }
	if requestVersion := *request.Version; requestVersion != Version {
		return write(errorResponse(request.RequestID, "unsupportedVersion", "unsupported protocol version"))
	}
	switch request.Command {
	case "handshake":
		if !isEmptyPayload(request.Payload) {
			return errInvalidRequest
		}
		return write(response{Version: Version, RequestID: request.RequestID, Result: handshakeResult{ProtocolVersion: Version}})
	case "shutdown":
		return errInvalidRequest
	case "discoverLocalApps":
		if !isEmptyPayload(request.Payload) {
			return errInvalidRequest
		}
		if discoverer == nil {
			return write(errorResponse(request.RequestID, "discoveryFailure", "local app discovery failed"))
		}
		requestContext, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		candidates, err := discoverer.Discover(requestContext)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || requestContext.Err() != nil {
			return write(errorResponse(request.RequestID, "discoveryFailure", "local app discovery failed"))
		}
		result := response{Version: Version, RequestID: request.RequestID, Result: discoverLocalAppsResult{Candidates: canonicalCandidates(candidates)}}
		encoded, marshalErr := json.Marshal(result)
		if marshalErr != nil || len(encoded) > 256*1024 {
			result = errorResponse(request.RequestID, "discoveryFailure", "local app discovery failed")
		}
		return write(result)
	case "reconcilePortals":
		configs, err := decodeReconcilePortalsPayload(request.Payload)
		if runtime == nil || err != nil {
			return write(errorResponse(request.RequestID, "invalidPayload", "invalid portal request"))
		}
		entries, err := runtime.Reconcile(ctx, configs, emit)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return write(errorResponse(request.RequestID, "invalidPayload", "invalid portal request"))
		}
		return write(response{Version: Version, RequestID: request.RequestID, Result: reconcilePortalsResult{Entries: entries}})
	case "authenticatePortal":
		var payload authenticatePortalPayload
		if runtime == nil || decodePayload(request.Payload, &payload) != nil {
			return write(errorResponse(request.RequestID, "invalidPayload", "invalid portal request"))
		}
		portalID, valid := validatedPortalID(payload.PortalID)
		if !valid {
			return write(errorResponse(request.RequestID, "invalidPayload", "invalid portal request"))
		}
		if err := runtime.Authenticate(ctx, portalID); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return write(errorResponse(request.RequestID, "runtimeFailure", "portal runtime failed"))
		}
		return write(response{Version: Version, RequestID: request.RequestID, Result: acceptedResult{Accepted: true}})
	case "cleanupRejectedPortal":
		var payload cleanupRejectedPortalPayload
		if runtime == nil || decodePayload(request.Payload, &payload) != nil {
			return write(errorResponse(request.RequestID, "invalidPayload", "invalid portal request"))
		}
		portalID, valid := validatedPortalID(payload.PortalID)
		if !valid {
			return write(errorResponse(request.RequestID, "invalidPayload", "invalid portal request"))
		}
		if err := runtime.CleanupRejectedPortal(ctx, portalID); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return write(errorResponse(request.RequestID, "runtimeFailure", "portal runtime failed"))
		}
		return write(response{Version: Version, RequestID: request.RequestID, Result: acceptedResult{Accepted: true}})
	case "removePortal":
		var payload removePortalPayload
		if runtime == nil || decodePayload(request.Payload, &payload) != nil {
			return write(errorResponse(request.RequestID, "invalidPayload", "invalid portal request"))
		}
		portalID, valid := validatedPortalID(payload.PortalID)
		if !valid {
			return write(errorResponse(request.RequestID, "invalidPayload", "invalid portal request"))
		}
		if err := runtime.RemovePortal(ctx, portalID); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return write(errorResponse(request.RequestID, "runtimeFailure", "portal runtime failed"))
		}
		return write(response{Version: Version, RequestID: request.RequestID, Result: acceptedResult{Accepted: true}})
	default:
		return write(errorResponse(request.RequestID, "unknownCommand", "unsupported command"))
	}
}

func validatedPortalID(raw string) (string, bool) {
	portalID := strings.ToLower(raw)
	err := (portal.Config{
		ID: portalID, Name: "a", Destination: portal.Destination{Kind: portal.DestinationLocalApp, Port: 1},
	}).Validate()
	return portalID, err == nil
}

func decodeReconcilePortalsPayload(raw json.RawMessage) ([]portal.Config, error) {
	var payload reconcilePortalsPayload
	if err := decodePayload(raw, &payload); err != nil || payload.Portals == nil {
		return nil, fmt.Errorf("invalid reconcile payload")
	}
	configs := make([]portal.Config, 0, len(*payload.Portals))
	seen := make(map[string]struct{}, len(*payload.Portals))
	for _, requested := range *payload.Portals {
		config := portal.Config{
			ID:           strings.ToLower(requested.PortalID),
			Name:         requested.PortalName,
			Destination:  requested.Destination,
			DesiredState: requested.DesiredState,
		}
		if err := config.Validate(); err != nil {
			return nil, fmt.Errorf("invalid reconcile payload")
		}
		if config.DesiredState != portal.DesiredStateEnabled && config.DesiredState != portal.DesiredStateStopped {
			return nil, fmt.Errorf("invalid reconcile payload")
		}
		if _, exists := seen[config.ID]; exists {
			return nil, fmt.Errorf("invalid reconcile payload")
		}
		seen[config.ID] = struct{}{}
		configs = append(configs, config)
	}
	return configs, nil
}

func canonicalCandidates(candidates []discovery.Candidate) []discovery.Candidate {
	byPort := make(map[uint16]discovery.Candidate, len(candidates))
	disagreed := make(map[uint16]bool)
	for _, candidate := range candidates {
		if candidate.LocalAppPort == 0 || candidate.ProcessLabel == "" {
			continue
		}
		if previous, exists := byPort[candidate.LocalAppPort]; !exists {
			byPort[candidate.LocalAppPort] = candidate
		} else if previous != candidate {
			disagreed[candidate.LocalAppPort] = true
		}
	}
	ports := make([]uint16, 0, len(byPort))
	for port := range byPort {
		ports = append(ports, port)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	result := make([]discovery.Candidate, 0, len(ports))
	for _, port := range ports {
		if disagreed[port] {
			result = append(result, discovery.Candidate{LocalAppPort: port, ProcessLabel: fmt.Sprintf("Port %d", port)})
		} else {
			result = append(result, byPort[port])
		}
	}
	return result
}

func (request request) isStructurallyValid() bool {
	if request.Version == nil || request.RequestID == "" || request.Command == "" || len(request.Payload) == 0 {
		return false
	}
	var payload map[string]json.RawMessage
	return json.Unmarshal(request.Payload, &payload) == nil && payload != nil
}

func isEmptyPayload(payload json.RawMessage) bool {
	var fields map[string]json.RawMessage
	return json.Unmarshal(payload, &fields) == nil && fields != nil && len(fields) == 0
}

func decodePayload(payload json.RawMessage, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func errorResponse(requestID, code, message string) response {
	return response{Version: Version, RequestID: requestID, Error: &protocolError{Code: code, Message: message}}
}
