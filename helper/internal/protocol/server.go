package protocol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/chrisbanes/portico/helper/internal/discovery"
	"github.com/chrisbanes/portico/helper/internal/portal"
)

const Version = 2

const invalidRequestDiagnostic = "portico-helper: invalid request\n"

type PortalRuntime interface {
	Reconcile(context.Context, []portal.Config, func(portal.Event)) ([]portal.ReconcileEntry, error)
	Authenticate(context.Context, string) error
	CleanupRejectedPortal(string) error
	Close() error
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
	LocalAppPort uint16              `json:"localAppPort"`
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
	mu      sync.Mutex
	encoder *json.Encoder
}

func (w *messageWriter) write(value any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.encoder.Encode(value)
}

func Serve(input io.Reader, output, diagnostics io.Writer) int {
	return ServeWithRuntime(input, output, diagnostics, nil)
}

func ServeWithRuntime(input io.Reader, output, diagnostics io.Writer, runtime PortalRuntime) int {
	return ServeWithServices(input, output, diagnostics, Services{PortalRuntime: runtime})
}

func ServeWithServices(input io.Reader, output, diagnostics io.Writer, services Services) int {
	runtime := services.PortalRuntime
	if runtime != nil {
		defer runtime.Close()
	}
	ctx, cancel := context.WithCancel(context.Background())
	discoveryContext, cancelDiscovery := context.WithCancel(ctx)
	var outputFailed atomic.Bool
	failOutput := func() {
		if outputFailed.CompareAndSwap(false, true) {
			cancelDiscovery()
			cancel()
			if closer, ok := input.(io.Closer); ok {
				_ = closer.Close()
			}
		}
	}
	var discoveryGroup sync.WaitGroup
	discoveryGate := make(chan struct{}, 1)
	cancelBeforeExit := true
	defer func() {
		if cancelBeforeExit {
			cancelDiscovery()
			cancel()
		}
		discoveryGroup.Wait()
		cancelDiscovery()
		cancel()
	}()
	writer := &messageWriter{encoder: json.NewEncoder(output)}
	emit := func(event portal.Event) {
		if event.Status != nil {
			_ = writer.write(eventMessage{Version: Version, Event: "portalStatus", PortalID: event.PortalID, Payload: event.Status})
		}
		if event.AuthenticationURL != "" {
			_ = writer.write(eventMessage{
				Version: Version, Event: "authenticationURL", PortalID: event.PortalID,
				Payload: authenticationURLPayload{URL: event.AuthenticationURL},
			})
		}
	}

	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		var request request
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF || !request.isStructurallyValid() {
			_, _ = io.WriteString(diagnostics, invalidRequestDiagnostic)
			return 1
		}
		if requestVersion := *request.Version; requestVersion != Version {
			if writer.write(errorResponse(request.RequestID, "unsupportedVersion", "unsupported protocol version")) != nil {
				return 1
			}
			continue
		}

		switch request.Command {
		case "handshake":
			if !isEmptyPayload(request.Payload) {
				_, _ = io.WriteString(diagnostics, invalidRequestDiagnostic)
				return 1
			}
			if writer.write(response{Version: Version, RequestID: request.RequestID, Result: handshakeResult{ProtocolVersion: Version}}) != nil {
				return 1
			}
		case "shutdown":
			if !isEmptyPayload(request.Payload) {
				_, _ = io.WriteString(diagnostics, invalidRequestDiagnostic)
				return 1
			}
			cancelDiscovery()
			discoveryGroup.Wait()
			if runtime != nil {
				if err := runtime.Close(); err != nil {
					_ = writer.write(errorResponse(request.RequestID, "runtimeFailure", "portal runtime failed"))
					return 1
				}
			}
			if writer.write(response{Version: Version, RequestID: request.RequestID, Result: acceptedResult{Accepted: true}}) != nil {
				return 1
			}
			return 0
		case "discoverLocalApps":
			if !isEmptyPayload(request.Payload) {
				_, _ = io.WriteString(diagnostics, invalidRequestDiagnostic)
				return 1
			}
			if services.LocalAppDiscoverer == nil {
				if writer.write(errorResponse(request.RequestID, "discoveryFailure", "local app discovery failed")) != nil {
					return 1
				}
				continue
			}
			requestID := request.RequestID
			discoveryGroup.Add(1)
			go func() {
				defer discoveryGroup.Done()
				select {
				case discoveryGate <- struct{}{}:
					defer func() { <-discoveryGate }()
				case <-discoveryContext.Done():
					return
				}
				candidates, err := services.LocalAppDiscoverer.Discover(discoveryContext)
				if discoveryContext.Err() != nil {
					return
				}
				if err != nil {
					if writer.write(errorResponse(requestID, "discoveryFailure", "local app discovery failed")) != nil {
						failOutput()
					}
					return
				}
				if writer.write(response{
					Version: Version, RequestID: requestID,
					Result: discoverLocalAppsResult{Candidates: canonicalCandidates(candidates)},
				}) != nil {
					failOutput()
				}
			}()
		case "reconcilePortals":
			configs, err := decodeReconcilePortalsPayload(request.Payload)
			if runtime == nil || err != nil {
				if writer.write(errorResponse(request.RequestID, "invalidPayload", "invalid portal request")) != nil {
					return 1
				}
				continue
			}
			entries, err := runtime.Reconcile(ctx, configs, emit)
			if err != nil {
				if writer.write(errorResponse(request.RequestID, "invalidPayload", "invalid portal request")) != nil {
					return 1
				}
				continue
			}
			if writer.write(response{
				Version: Version, RequestID: request.RequestID,
				Result: reconcilePortalsResult{Entries: entries},
			}) != nil {
				return 1
			}
		case "authenticatePortal":
			var payload authenticatePortalPayload
			if runtime == nil || decodePayload(request.Payload, &payload) != nil {
				if writer.write(errorResponse(request.RequestID, "invalidPayload", "invalid portal request")) != nil {
					return 1
				}
				continue
			}
			portalID, valid := validatedPortalID(payload.PortalID)
			if !valid {
				if writer.write(errorResponse(request.RequestID, "invalidPayload", "invalid portal request")) != nil {
					return 1
				}
				continue
			}
			if err := runtime.Authenticate(ctx, portalID); err != nil {
				if writer.write(errorResponse(request.RequestID, "runtimeFailure", "portal runtime failed")) != nil {
					return 1
				}
				continue
			}
			if writer.write(response{Version: Version, RequestID: request.RequestID, Result: acceptedResult{Accepted: true}}) != nil {
				return 1
			}
		case "cleanupRejectedPortal":
			var payload cleanupRejectedPortalPayload
			if runtime == nil || decodePayload(request.Payload, &payload) != nil {
				if writer.write(errorResponse(request.RequestID, "invalidPayload", "invalid portal request")) != nil {
					return 1
				}
				continue
			}
			portalID, valid := validatedPortalID(payload.PortalID)
			if !valid {
				if writer.write(errorResponse(request.RequestID, "invalidPayload", "invalid portal request")) != nil {
					return 1
				}
				continue
			}
			if err := runtime.CleanupRejectedPortal(portalID); err != nil {
				if writer.write(errorResponse(request.RequestID, "runtimeFailure", "portal runtime failed")) != nil {
					return 1
				}
				continue
			}
			if writer.write(response{Version: Version, RequestID: request.RequestID, Result: acceptedResult{Accepted: true}}) != nil {
				return 1
			}
		default:
			if writer.write(errorResponse(request.RequestID, "unknownCommand", "unsupported command")) != nil {
				return 1
			}
		}
	}
	if scannerError := scanner.Err(); scannerError != nil {
		cancelDiscovery()
		cancel()
		discoveryGroup.Wait()
		if !outputFailed.Load() {
			_, _ = io.WriteString(diagnostics, invalidRequestDiagnostic)
		}
		return 1
	}
	discoveryGroup.Wait()
	if outputFailed.Load() {
		return 1
	}
	cancelBeforeExit = false
	return 0
}

func validatedPortalID(raw string) (string, bool) {
	portalID := strings.ToLower(raw)
	err := (portal.Config{ID: portalID, Name: "a", Port: 1}).Validate()
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
			Port:         requested.LocalAppPort,
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
