package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
)

const Version = 1

const invalidRequestDiagnostic = "portico-helper: invalid request\n"

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

type shutdownResult struct {
	Accepted bool `json:"accepted"`
}

func Serve(input io.Reader, output, diagnostics io.Writer) int {
	scanner := bufio.NewScanner(input)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		var request request
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF || !request.isValid() {
			_, _ = io.WriteString(diagnostics, invalidRequestDiagnostic)
			return 1
		}
		if requestVersion := *request.Version; requestVersion != Version {
			if err := encoder.Encode(response{
				Version:   Version,
				RequestID: request.RequestID,
				Error: &protocolError{
					Code:    "unsupportedVersion",
					Message: "unsupported protocol version",
				},
			}); err != nil {
				return 1
			}
			continue
		}
		if request.Command == "handshake" {
			if err := encoder.Encode(response{
				Version:   Version,
				RequestID: request.RequestID,
				Result:    handshakeResult{ProtocolVersion: Version},
			}); err != nil {
				return 1
			}
		} else if request.Command == "shutdown" {
			if err := encoder.Encode(response{
				Version:   Version,
				RequestID: request.RequestID,
				Result:    shutdownResult{Accepted: true},
			}); err != nil {
				return 1
			}
			return 0
		} else if err := encoder.Encode(response{
			Version:   Version,
			RequestID: request.RequestID,
			Error: &protocolError{
				Code:    "unknownCommand",
				Message: "unsupported command",
			},
		}); err != nil {
			return 1
		}
	}
	if scanner.Err() != nil {
		_, _ = io.WriteString(diagnostics, invalidRequestDiagnostic)
		return 1
	}
	return 0
}

func (request request) isValid() bool {
	if request.Version == nil || request.RequestID == "" || request.Command == "" || len(request.Payload) == 0 {
		return false
	}

	var payload map[string]json.RawMessage
	return json.Unmarshal(request.Payload, &payload) == nil && payload != nil && len(payload) == 0
}
