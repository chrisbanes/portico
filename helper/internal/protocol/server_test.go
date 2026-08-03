package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/chrisbanes/portico/helper/internal/portal"
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

func TestServeStartsPortalAndSerializesStructuredStatusEvent(t *testing.T) {
	runtime := &fakeRuntime{}
	input := bytes.NewBufferString(
		`{"version":1,"requestId":"start-1","command":"startPortal","payload":{"portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A","portalName":"hermes","localAppPort":8787}}` + "\n",
	)
	var output bytes.Buffer
	var diagnostics bytes.Buffer

	exitCode := ServeWithRuntime(input, &output, &diagnostics, runtime)

	if exitCode != 0 || diagnostics.Len() != 0 {
		t.Fatalf("ServeWithRuntime = (exit %d, diagnostics %q), want success", exitCode, diagnostics.String())
	}
	if runtime.started.ID != "9f55ca93-d7b3-4eab-a871-310ea576005a" || runtime.started.Name != "hermes" || runtime.started.Port != 8787 {
		t.Fatalf("started = %+v, want normalized typed configuration", runtime.started)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("output = %q, want event and response", output.String())
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatal(err)
	}
	if event["event"] != "portalStatus" || event["portalId"] != "9f55ca93-d7b3-4eab-a871-310ea576005a" {
		t.Fatalf("event = %+v, want structured portal status", event)
	}
	const wantResponse = `{"version":1,"requestId":"start-1","result":{"accepted":true}}`
	if lines[1] != wantResponse {
		t.Fatalf("response = %q, want %q", lines[1], wantResponse)
	}
}

func TestServeAuthenticatesCorrelatedPortalAndEmitsTransientURL(t *testing.T) {
	runtime := &fakeRuntime{}
	input := bytes.NewBufferString(
		`{"version":1,"requestId":"start-1","command":"startPortal","payload":{"portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A","portalName":"hermes","localAppPort":8787}}` + "\n" +
			`{"version":1,"requestId":"auth-1","command":"authenticatePortal","payload":{"portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A"}}` + "\n",
	)
	var output bytes.Buffer
	var diagnostics bytes.Buffer

	exitCode := ServeWithRuntime(input, &output, &diagnostics, runtime)

	if exitCode != 0 || diagnostics.Len() != 0 || runtime.authenticated != "9f55ca93-d7b3-4eab-a871-310ea576005a" {
		t.Fatalf("ServeWithRuntime = (exit %d, auth %q, diagnostics %q), want correlated authentication", exitCode, runtime.authenticated, diagnostics.String())
	}
	if !strings.Contains(output.String(), `"event":"authenticationURL"`) || !strings.Contains(output.String(), `"url":"https://login.tailscale.com/a/transient"`) {
		t.Fatalf("output = %q, want transient authentication event", output.String())
	}
	if !strings.Contains(output.String(), `"requestId":"auth-1","result":{"accepted":true}`) {
		t.Fatalf("output = %q, want correlated accepted response", output.String())
	}
}

func TestServeRejectsInvalidPortalPayloadWithoutLeakingIt(t *testing.T) {
	const secret = "https://login.tailscale.com/a/do-not-copy"
	input := bytes.NewBufferString(
		`{"version":1,"requestId":"start-1","command":"startPortal","payload":{"portalId":"../` + secret + `","portalName":"hermes","localAppPort":8787}}` + "\n",
	)
	var output bytes.Buffer
	var diagnostics bytes.Buffer

	exitCode := ServeWithRuntime(input, &output, &diagnostics, &fakeRuntime{})

	if exitCode != 0 || !strings.Contains(output.String(), `"code":"invalidPayload"`) {
		t.Fatalf("ServeWithRuntime = (exit %d, output %q), want correlated invalid-payload error", exitCode, output.String())
	}
	if strings.Contains(output.String(), secret) || strings.Contains(diagnostics.String(), secret) {
		t.Fatal("invalid submitted value leaked to protocol or diagnostic output")
	}
}

func TestServeRejectsDestinationFields(t *testing.T) {
	for _, field := range []string{"host", "scheme", "path", "url"} {
		t.Run(field, func(t *testing.T) {
			const secret = "untrusted-destination-do-not-copy"
			input := bytes.NewBufferString(
				`{"version":1,"requestId":"start-1","command":"startPortal","payload":{"portalId":"9f55ca93-d7b3-4eab-a871-310ea576005a","portalName":"hermes","localAppPort":8787,"` + field + `":"` + secret + `"}}` + "\n",
			)
			var output bytes.Buffer
			var diagnostics bytes.Buffer

			exitCode := ServeWithRuntime(input, &output, &diagnostics, &fakeRuntime{})

			if exitCode != 0 || !strings.Contains(output.String(), `"code":"invalidPayload"`) {
				t.Fatalf("ServeWithRuntime = (exit %d, output %q), want invalid-payload response", exitCode, output.String())
			}
			if strings.Contains(output.String(), secret) || strings.Contains(diagnostics.String(), secret) {
				t.Fatal("untrusted destination leaked to protocol or diagnostic output")
			}
		})
	}
}

func TestServeClosesRuntimeOnShutdown(t *testing.T) {
	runtime := &fakeRuntime{}
	input := bytes.NewBufferString(`{"version":1,"requestId":"shutdown-1","command":"shutdown","payload":{}}` + "\n")
	var output bytes.Buffer

	if exitCode := ServeWithRuntime(input, &output, &bytes.Buffer{}, runtime); exitCode != 0 || !runtime.closed {
		t.Fatalf("ServeWithRuntime = (exit %d, closed %v), want orderly close", exitCode, runtime.closed)
	}
}

func TestServeAcknowledgesShutdownAfterRuntimeCloseCompletes(t *testing.T) {
	runtime := &fakeRuntime{closeEntered: make(chan struct{}), releaseClose: make(chan struct{})}
	input := bytes.NewBufferString(`{"version":1,"requestId":"shutdown-1","command":"shutdown","payload":{}}` + "\n")
	var output bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- ServeWithRuntime(input, &output, &bytes.Buffer{}, runtime) }()

	<-runtime.closeEntered
	if output.Len() != 0 {
		t.Fatalf("output = %q, want no shutdown acknowledgement before close completes", output.String())
	}
	close(runtime.releaseClose)
	if exitCode := <-done; exitCode != 0 {
		t.Fatalf("ServeWithRuntime exit code = %d, want success", exitCode)
	}
	if !strings.Contains(output.String(), `"requestId":"shutdown-1","result":{"accepted":true}`) {
		t.Fatalf("output = %q, want shutdown acknowledgement after close", output.String())
	}
}

type fakeRuntime struct {
	started       portal.Config
	authenticated string
	closed        bool
	emit          func(portal.Event)
	closeEntered  chan struct{}
	releaseClose  chan struct{}
	closeOnce     sync.Once
}

func (r *fakeRuntime) Start(_ context.Context, config portal.Config, emit func(portal.Event)) error {
	r.started = config
	r.emit = emit
	emit(portal.Event{PortalID: config.ID, Status: &portal.StatusEvent{State: portal.StateConnecting, Addresses: []string{}}})
	return nil
}

func (r *fakeRuntime) Authenticate(_ context.Context, portalID string) error {
	r.authenticated = strings.ToLower(portalID)
	if r.emit != nil {
		r.emit(portal.Event{PortalID: r.authenticated, AuthenticationURL: "https://login.tailscale.com/a/transient"})
	}
	return nil
}

func (r *fakeRuntime) Close() error {
	r.closeOnce.Do(func() {
		if r.closeEntered != nil {
			close(r.closeEntered)
			<-r.releaseClose
		}
	})
	r.closed = true
	return nil
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
