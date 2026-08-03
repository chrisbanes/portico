package protocol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"

	"github.com/chrisbanes/portico/helper/internal/portal"
)

const Version = 1

const invalidRequestDiagnostic = "portico-helper: invalid request\n"

type PortalRuntime interface {
	Start(context.Context, portal.Config, func(portal.Event)) error
	Authenticate(context.Context, string) error
	Close() error
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

type startPortalPayload struct {
	PortalID     string `json:"portalId"`
	PortalName   string `json:"portalName"`
	LocalAppPort uint16 `json:"localAppPort"`
}

type authenticatePortalPayload struct {
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
	if runtime != nil {
		defer runtime.Close()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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
		case "startPortal":
			var payload startPortalPayload
			if runtime == nil || decodePayload(request.Payload, &payload) != nil {
				if writer.write(errorResponse(request.RequestID, "invalidPayload", "invalid portal request")) != nil {
					return 1
				}
				continue
			}
			config := portal.Config{ID: strings.ToLower(payload.PortalID), Name: payload.PortalName, Port: payload.LocalAppPort}
			if err := config.Validate(); err != nil {
				if writer.write(errorResponse(request.RequestID, "invalidPayload", "invalid portal request")) != nil {
					return 1
				}
				continue
			}
			if err := runtime.Start(ctx, config, emit); err != nil {
				if writer.write(errorResponse(request.RequestID, "runtimeFailure", "portal runtime failed")) != nil {
					return 1
				}
				continue
			}
			if writer.write(response{Version: Version, RequestID: request.RequestID, Result: acceptedResult{Accepted: true}}) != nil {
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
			portalID := strings.ToLower(payload.PortalID)
			if err := (portal.Config{ID: portalID, Name: "a", Port: 1}).Validate(); err != nil {
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
		default:
			if writer.write(errorResponse(request.RequestID, "unknownCommand", "unsupported command")) != nil {
				return 1
			}
		}
	}
	if scanner.Err() != nil {
		_, _ = io.WriteString(diagnostics, invalidRequestDiagnostic)
		return 1
	}
	return 0
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
