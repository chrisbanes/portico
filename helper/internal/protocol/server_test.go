package protocol

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestServeCorrelatesHandshakeResponse(t *testing.T) {
	input := bytes.NewBufferString(`{"version":1,"requestId":"request-1","command":"handshake","payload":{}}` + "\n")
	var output bytes.Buffer
	var diagnostics bytes.Buffer

	exitCode := Serve(input, &output, &diagnostics)

	if exitCode != 0 {
		t.Fatalf("Serve() exit code = %d, want 0", exitCode)
	}
	var response struct {
		Version   int             `json:"version"`
		RequestID string          `json:"requestId"`
		Result    json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Version != 1 || response.RequestID != "request-1" || string(response.Result) != `{"protocolVersion":1}` {
		t.Fatalf("response = %+v, want correlated version-one handshake", response)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("diagnostics = %q, want empty", diagnostics.String())
	}
}

func TestServeAcknowledgesShutdownAndStops(t *testing.T) {
	input := bytes.NewBufferString(
		`{"version":1,"requestId":"shutdown-1","command":"shutdown","payload":{}}` + "\n" +
			`{"version":1,"requestId":"ignored","command":"handshake","payload":{}}` + "\n",
	)
	var output bytes.Buffer
	var diagnostics bytes.Buffer

	exitCode := Serve(input, &output, &diagnostics)

	if exitCode != 0 {
		t.Fatalf("Serve() exit code = %d, want 0", exitCode)
	}
	const want = "{\"version\":1,\"requestId\":\"shutdown-1\",\"result\":{\"accepted\":true}}\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("diagnostics = %q, want empty", diagnostics.String())
	}
}

func TestServeReturnsCorrelatedErrorForUnknownCommand(t *testing.T) {
	input := bytes.NewBufferString(`{"version":1,"requestId":"unknown-1","command":"surprise","payload":{}}` + "\n")
	var output bytes.Buffer
	var diagnostics bytes.Buffer

	exitCode := Serve(input, &output, &diagnostics)

	if exitCode != 0 {
		t.Fatalf("Serve() exit code = %d, want 0", exitCode)
	}
	const want = "{\"version\":1,\"requestId\":\"unknown-1\",\"error\":{\"code\":\"unknownCommand\",\"message\":\"unsupported command\"}}\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("diagnostics = %q, want empty", diagnostics.String())
	}
}

func TestServeReturnsCorrelatedErrorForUnsupportedVersion(t *testing.T) {
	input := bytes.NewBufferString(`{"version":2,"requestId":"version-1","command":"handshake","payload":{}}` + "\n")
	var output bytes.Buffer
	var diagnostics bytes.Buffer

	exitCode := Serve(input, &output, &diagnostics)

	if exitCode != 0 {
		t.Fatalf("Serve() exit code = %d, want 0", exitCode)
	}
	const want = "{\"version\":1,\"requestId\":\"version-1\",\"error\":{\"code\":\"unsupportedVersion\",\"message\":\"unsupported protocol version\"}}\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("diagnostics = %q, want empty", diagnostics.String())
	}
}

func TestServeRejectsMalformedInputWithoutLeakingIt(t *testing.T) {
	const secret = "token=do-not-copy"
	input := bytes.NewBufferString(`{"version":1,"requestId":"` + secret + `"` + "\n")
	var output bytes.Buffer
	var diagnostics bytes.Buffer

	exitCode := Serve(input, &output, &diagnostics)

	if exitCode == 0 {
		t.Fatal("Serve() exit code = 0, want unsuccessful")
	}
	if output.Len() != 0 {
		t.Fatalf("output = %q, want empty", output.String())
	}
	const wantDiagnostic = "portico-helper: invalid request\n"
	if diagnostics.String() != wantDiagnostic {
		t.Fatalf("diagnostics = %q, want %q", diagnostics.String(), wantDiagnostic)
	}
	if strings.Contains(output.String(), secret) || strings.Contains(diagnostics.String(), secret) {
		t.Fatal("untrusted input was copied to a protocol or diagnostic stream")
	}
}

func TestServeRejectsTrailingContentAfterRequest(t *testing.T) {
	input := bytes.NewBufferString(`{"version":1,"requestId":"request-1","command":"handshake","payload":{}} trailing` + "\n")
	var output bytes.Buffer
	var diagnostics bytes.Buffer

	exitCode := Serve(input, &output, &diagnostics)

	if exitCode == 0 || output.Len() != 0 || diagnostics.String() != "portico-helper: invalid request\n" {
		t.Fatalf("Serve() = (exit %d, output %q, diagnostics %q), want unsuccessful, empty protocol output, fixed diagnostic", exitCode, output.String(), diagnostics.String())
	}
}

func TestServeRejectsStructurallyInvalidRequests(t *testing.T) {
	fixtures := map[string]string{
		"missing version":    `{"requestId":"request-1","command":"handshake","payload":{}}`,
		"missing request ID": `{"version":1,"command":"handshake","payload":{}}`,
		"missing command":    `{"version":1,"requestId":"request-1","payload":{}}`,
		"missing payload":    `{"version":1,"requestId":"request-1","command":"handshake"}`,
		"non-object payload": `{"version":1,"requestId":"request-1","command":"handshake","payload":[]}`,
		"non-empty payload":  `{"version":1,"requestId":"request-1","command":"handshake","payload":{"unexpected":true}}`,
		"unknown field":      `{"version":1,"requestId":"request-1","command":"handshake","payload":{},"extra":true}`,
	}

	for name, fixture := range fixtures {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			var diagnostics bytes.Buffer

			exitCode := Serve(bytes.NewBufferString(fixture+"\n"), &output, &diagnostics)

			if exitCode == 0 || output.Len() != 0 || diagnostics.String() != "portico-helper: invalid request\n" {
				t.Fatalf("Serve() = (exit %d, output %q, diagnostics %q), want unsuccessful, empty protocol output, fixed diagnostic", exitCode, output.String(), diagnostics.String())
			}
		})
	}
}

func TestServeExitsCleanlyOnEOF(t *testing.T) {
	var output bytes.Buffer
	var diagnostics bytes.Buffer

	exitCode := Serve(bytes.NewReader(nil), &output, &diagnostics)

	if exitCode != 0 || output.Len() != 0 || diagnostics.Len() != 0 {
		t.Fatalf("Serve() = (exit %d, output %q, diagnostics %q), want clean EOF", exitCode, output.String(), diagnostics.String())
	}
}
