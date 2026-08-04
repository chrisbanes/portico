package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chrisbanes/portico/helper/internal/discovery"
	"github.com/chrisbanes/portico/helper/internal/portal"
)

func TestServeReconcilesLiteralProtocolVersionFourSnapshot(t *testing.T) {
	runtime := &fakeRuntime{reconcileEntries: []portal.ReconcileEntry{
		{PortalID: "5ea74329-3144-4ba2-925f-138d14d61fcc", Outcome: portal.OutcomeConverged},
		{PortalID: "9f55ca93-d7b3-4eab-a871-310ea576005a", Outcome: portal.OutcomeStartFailed},
	}}
	input := bytes.NewBufferString(
		`{"version":4,"requestId":"reconcile-1","command":"reconcilePortals","payload":{"portals":[` +
			`{"portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A","portalName":"hermes","destination":{"kind":"localApp","port":8787},"desiredState":"enabled"},` +
			`{"portalId":"5EA74329-3144-4BA2-925F-138D14D61FCC","portalName":"atlas","destination":{"kind":"localApp","port":8788},"desiredState":"stopped"}` +
			`]}}` + "\n",
	)
	var output bytes.Buffer
	var diagnostics bytes.Buffer

	exitCode := ServeWithRuntime(input, &output, &diagnostics, runtime)

	if exitCode != 0 || diagnostics.Len() != 0 {
		t.Fatalf("ServeWithRuntime = (exit %d, diagnostics %q), want success", exitCode, diagnostics.String())
	}
	if len(runtime.reconciled) != 2 || runtime.reconciled[0].ID != "9f55ca93-d7b3-4eab-a871-310ea576005a" || runtime.reconciled[1].DesiredState != portal.DesiredStateStopped {
		t.Fatalf("reconciled = %+v, want normalized complete snapshot", runtime.reconciled)
	}
	const want = `{"version":4,"requestId":"reconcile-1","result":{"entries":[{"portalId":"5ea74329-3144-4ba2-925f-138d14d61fcc","outcome":"converged"},{"portalId":"9f55ca93-d7b3-4eab-a871-310ea576005a","outcome":"startFailed"}]}}` + "\n"
	if output.String() != want {
		t.Fatalf("output = %q, want exact protocol-v4 result %q", output.String(), want)
	}
}

func TestDiscoverLocalAppsReturnsOnlyStableSanitizedCandidates(t *testing.T) {
	discoverer := fakeDiscoverer{candidates: []discovery.Candidate{
		{LocalAppPort: 9000, ProcessLabel: "hermes", SuggestedPortalName: "hermes"},
		{LocalAppPort: 8000, ProcessLabel: "atlas", SuggestedPortalName: "atlas"},
		{LocalAppPort: 9000, ProcessLabel: "hermes", SuggestedPortalName: "hermes"},
		{LocalAppPort: 7000, ProcessLabel: "first", SuggestedPortalName: "first"},
	}}
	input := bytes.NewBufferString(`{"version":4,"requestId":"discover-1","command":"discoverLocalApps","payload":{}}` + "\n")
	var output bytes.Buffer
	var diagnostics bytes.Buffer

	exitCode := ServeWithServices(input, &output, &diagnostics, Services{LocalAppDiscoverer: discoverer})

	if exitCode != 0 || diagnostics.Len() != 0 {
		t.Fatalf("ServeWithServices = (exit %d, diagnostics %q), want success", exitCode, diagnostics.String())
	}
	const want = `{"version":4,"requestId":"discover-1","result":{"candidates":[{"localAppPort":7000,"processLabel":"first","suggestedPortalName":"first"},{"localAppPort":8000,"processLabel":"atlas","suggestedPortalName":"atlas"},{"localAppPort":9000,"processLabel":"hermes","suggestedPortalName":"hermes"}]}}` + "\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestDiscoverLocalAppsCollapsesDisagreeingDuplicateOwners(t *testing.T) {
	discoverer := fakeDiscoverer{candidates: []discovery.Candidate{
		{LocalAppPort: 8787, ProcessLabel: "python3", SuggestedPortalName: "hermes"},
		{LocalAppPort: 8787, ProcessLabel: "node", SuggestedPortalName: "atlas"},
	}}
	input := bytes.NewBufferString(`{"version":4,"requestId":"discover-1","command":"discoverLocalApps","payload":{}}` + "\n")
	var output bytes.Buffer

	if exitCode := ServeWithServices(input, &output, &bytes.Buffer{}, Services{LocalAppDiscoverer: discoverer}); exitCode != 0 {
		t.Fatalf("ServeWithServices exit code = %d, want success", exitCode)
	}
	const want = `{"version":4,"requestId":"discover-1","result":{"candidates":[{"localAppPort":8787,"processLabel":"Port 8787"}]}}` + "\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestDiscoverLocalAppsRequiresEmptyPayload(t *testing.T) {
	const secret = "do-not-copy"
	input := bytes.NewBufferString(`{"version":4,"requestId":"discover-1","command":"discoverLocalApps","payload":{"unexpected":"` + secret + `"}}` + "\n")
	var output bytes.Buffer
	var diagnostics bytes.Buffer

	exitCode := ServeWithServices(input, &output, &diagnostics, Services{LocalAppDiscoverer: fakeDiscoverer{}})

	if exitCode == 0 || output.Len() != 0 || diagnostics.String() != invalidRequestDiagnostic {
		t.Fatalf("ServeWithServices = (exit %d, output %q, diagnostics %q), want fixed invalid request", exitCode, output.String(), diagnostics.String())
	}
	if strings.Contains(output.String(), secret) || strings.Contains(diagnostics.String(), secret) {
		t.Fatal("invalid discovery payload leaked")
	}
}

func TestDiscoverLocalAppsReturnsFixedSecretFreeFailure(t *testing.T) {
	const secret = "token=do-not-copy"
	input := bytes.NewBufferString(`{"version":4,"requestId":"discover-1","command":"discoverLocalApps","payload":{}}` + "\n")
	var output bytes.Buffer
	var diagnostics bytes.Buffer

	exitCode := ServeWithServices(input, &output, &diagnostics, Services{LocalAppDiscoverer: fakeDiscoverer{err: errors.New(secret)}})

	if exitCode != 0 || diagnostics.Len() != 0 {
		t.Fatalf("ServeWithServices = (exit %d, diagnostics %q), want correlated failure", exitCode, diagnostics.String())
	}
	const want = `{"version":4,"requestId":"discover-1","error":{"code":"discoveryFailure","message":"local app discovery failed"}}` + "\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	if strings.Contains(output.String(), secret) || strings.Contains(diagnostics.String(), secret) {
		t.Fatal("discovery failure leaked its underlying error")
	}
}

func TestDiscoverLocalAppsCancelsBeforeRuntimeCloseAndShutdownAcknowledgement(t *testing.T) {
	reader, input := io.Pipe()
	discoverer := &blockingDiscoverer{started: make(chan struct{}), canceled: make(chan struct{})}
	runtime := &shutdownOrderingRuntime{discoveryCanceled: discoverer.canceled}
	var output bytes.Buffer
	var diagnostics bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- ServeWithServices(reader, &output, &diagnostics, Services{
			PortalRuntime: runtime, LocalAppDiscoverer: discoverer,
		})
	}()

	_, _ = io.WriteString(input, `{"version":4,"requestId":"discover-1","command":"discoverLocalApps","payload":{}}`+"\n")
	<-discoverer.started
	go func() {
		_, _ = io.WriteString(input, `{"version":4,"requestId":"shutdown-1","command":"shutdown","payload":{}}`+"\n")
		_ = input.Close()
	}()

	if exitCode := <-done; exitCode != 0 {
		t.Fatalf("ServeWithServices exit code = %d, want success", exitCode)
	}
	if !runtime.closedAfterDiscoveryCancellation {
		t.Fatal("runtime closed before in-flight discovery observed cancellation")
	}
	const want = `{"version":4,"requestId":"shutdown-1","result":{"accepted":true}}` + "\n"
	if output.String() != want || diagnostics.Len() != 0 {
		t.Fatalf("ServeWithServices = (output %q, diagnostics %q), want only post-close shutdown acknowledgement", output.String(), diagnostics.String())
	}
}

func TestDiscoverLocalAppsFailsServeWhenResponseCannotBeWritten(t *testing.T) {
	input := bytes.NewBufferString(`{"version":4,"requestId":"discover-1","command":"discoverLocalApps","payload":{}}` + "\n")

	exitCode := ServeWithServices(input, errorWriter{}, &bytes.Buffer{}, Services{LocalAppDiscoverer: fakeDiscoverer{}})

	if exitCode == 0 {
		t.Fatal("ServeWithServices exit code = 0, want response write failure")
	}
}

type fakeDiscoverer struct {
	candidates []discovery.Candidate
	err        error
}

func (d fakeDiscoverer) Discover(context.Context) ([]discovery.Candidate, error) {
	return d.candidates, d.err
}

type blockingDiscoverer struct {
	started  chan struct{}
	canceled chan struct{}
}

func (d *blockingDiscoverer) Discover(ctx context.Context) ([]discovery.Candidate, error) {
	close(d.started)
	select {
	case <-ctx.Done():
		close(d.canceled)
		return nil, ctx.Err()
	case <-time.After(250 * time.Millisecond):
		return nil, errors.New("discovery did not observe cancellation")
	}
}

type shutdownOrderingRuntime struct {
	discoveryCanceled                <-chan struct{}
	closedAfterDiscoveryCancellation bool
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func (*shutdownOrderingRuntime) Reconcile(context.Context, []portal.Config, func(portal.Event)) ([]portal.ReconcileEntry, error) {
	return []portal.ReconcileEntry{}, nil
}

func (*shutdownOrderingRuntime) Authenticate(context.Context, string) error { return nil }

func (*shutdownOrderingRuntime) CleanupRejectedPortal(string) error { return nil }

func (*shutdownOrderingRuntime) RemovePortal(string) error { return nil }

func (r *shutdownOrderingRuntime) Close() error {
	select {
	case <-r.discoveryCanceled:
		r.closedAfterDiscoveryCancellation = true
	default:
	}
	return nil
}

func TestServeCorrelatesHandshakeResponse(t *testing.T) {
	input := bytes.NewBufferString(`{"version":4,"requestId":"request-1","command":"handshake","payload":{}}` + "\n")
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
	if response.Version != 4 || response.RequestID != "request-1" || string(response.Result) != `{"protocolVersion":4}` {
		t.Fatalf("response = %+v, want correlated version-four handshake", response)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("diagnostics = %q, want empty", diagnostics.String())
	}
}

func TestServeReconciliationSerializesStructuredStatusEvent(t *testing.T) {
	runtime := &fakeRuntime{emitStatus: true, reconcileEntries: []portal.ReconcileEntry{{
		PortalID: "9f55ca93-d7b3-4eab-a871-310ea576005a", Outcome: portal.OutcomeConverged,
	}}}
	input := bytes.NewBufferString(
		`{"version":4,"requestId":"reconcile-1","command":"reconcilePortals","payload":{"portals":[{"portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A","portalName":"hermes","destination":{"kind":"localApp","port":8787},"desiredState":"enabled"}]}}` + "\n",
	)
	var output bytes.Buffer
	var diagnostics bytes.Buffer

	exitCode := ServeWithRuntime(input, &output, &diagnostics, runtime)

	if exitCode != 0 || diagnostics.Len() != 0 {
		t.Fatalf("ServeWithRuntime = (exit %d, diagnostics %q), want success", exitCode, diagnostics.String())
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
	payload := event["payload"].(map[string]any)
	if payload["tailnetName"] != "opaque-identity-do-not-display" || payload["magicDNSSuffix"] != "example.ts.net" {
		t.Fatalf("payload = %+v, want exact identity and separate display suffix", payload)
	}
	const wantResponse = `{"version":4,"requestId":"reconcile-1","result":{"entries":[{"portalId":"9f55ca93-d7b3-4eab-a871-310ea576005a","outcome":"converged"}]}}`
	if lines[1] != wantResponse {
		t.Fatalf("response = %q, want %q", lines[1], wantResponse)
	}
}

func TestServeAuthenticatesCorrelatedPortalAndEmitsTransientURL(t *testing.T) {
	runtime := &fakeRuntime{reconcileEntries: []portal.ReconcileEntry{{
		PortalID: "9f55ca93-d7b3-4eab-a871-310ea576005a", Outcome: portal.OutcomeConverged,
	}}}
	input := bytes.NewBufferString(
		`{"version":4,"requestId":"reconcile-1","command":"reconcilePortals","payload":{"portals":[{"portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A","portalName":"hermes","destination":{"kind":"localApp","port":8787},"desiredState":"enabled"}]}}` + "\n" +
			`{"version":4,"requestId":"auth-1","command":"authenticatePortal","payload":{"portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A"}}` + "\n",
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

func TestServeCleansUpOnlyCorrelatedRejectedPortal(t *testing.T) {
	runtime := &fakeRuntime{}
	input := bytes.NewBufferString(
		`{"version":4,"requestId":"cleanup-1","command":"cleanupRejectedPortal","payload":{"portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A"}}` + "\n",
	)
	var output bytes.Buffer
	var diagnostics bytes.Buffer

	exitCode := ServeWithRuntime(input, &output, &diagnostics, runtime)

	if exitCode != 0 || diagnostics.Len() != 0 || runtime.cleaned != "9f55ca93-d7b3-4eab-a871-310ea576005a" {
		t.Fatalf("ServeWithRuntime = (exit %d, cleaned %q, diagnostics %q), want correlated cleanup", exitCode, runtime.cleaned, diagnostics.String())
	}
	const want = `{"version":4,"requestId":"cleanup-1","result":{"accepted":true}}` + "\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestServeRemovesOnlyCorrelatedPortalWithProtocolVersionFour(t *testing.T) {
	runtime := &fakeRuntime{}
	input := bytes.NewBufferString(
		`{"version":4,"requestId":"remove-1","command":"removePortal","payload":{"portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A"}}` + "\n",
	)
	var output bytes.Buffer
	var diagnostics bytes.Buffer

	exitCode := ServeWithRuntime(input, &output, &diagnostics, runtime)

	if exitCode != 0 || diagnostics.Len() != 0 || runtime.removed != "9f55ca93-d7b3-4eab-a871-310ea576005a" {
		t.Fatalf("ServeWithRuntime = (exit %d, removed %q, diagnostics %q), want correlated removal", exitCode, runtime.removed, diagnostics.String())
	}
	const want = `{"version":4,"requestId":"remove-1","result":{"accepted":true}}` + "\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestServeRejectsUntrustedRemovalPayloadWithoutRuntimeCall(t *testing.T) {
	fixtures := map[string]string{
		"missing ID": `{}`,
		"invalid ID": `{"portalId":"../outside"}`,
		"path":       `{"portalId":"9f55ca93-d7b3-4eab-a871-310ea576005a","path":"/private/secret"}`,
		"hostname":   `{"portalId":"9f55ca93-d7b3-4eab-a871-310ea576005a","hostname":"other"}`,
	}
	for name, payload := range fixtures {
		t.Run(name, func(t *testing.T) {
			runtime := &fakeRuntime{}
			input := bytes.NewBufferString(`{"version":4,"requestId":"remove-1","command":"removePortal","payload":` + payload + `}` + "\n")
			var output bytes.Buffer

			if exitCode := ServeWithRuntime(input, &output, &bytes.Buffer{}, runtime); exitCode != 0 {
				t.Fatalf("ServeWithRuntime exit code = %d, want correlated rejection", exitCode)
			}
			if runtime.removeCalls != 0 || !strings.Contains(output.String(), `"code":"invalidPayload"`) {
				t.Fatalf("remove calls = %d, output = %q, want zero-call invalid payload", runtime.removeCalls, output.String())
			}
		})
	}
}

func TestServeMapsRemovalFailureToFixedRuntimeError(t *testing.T) {
	const secret = "path=/private/secret"
	runtime := &fakeRuntime{removeErr: errors.New(secret)}
	input := bytes.NewBufferString(
		`{"version":4,"requestId":"remove-1","command":"removePortal","payload":{"portalId":"9f55ca93-d7b3-4eab-a871-310ea576005a"}}` + "\n",
	)
	var output bytes.Buffer

	if exitCode := ServeWithRuntime(input, &output, &bytes.Buffer{}, runtime); exitCode != 0 {
		t.Fatalf("ServeWithRuntime exit code = %d, want correlated failure", exitCode)
	}
	const want = `{"version":4,"requestId":"remove-1","error":{"code":"runtimeFailure","message":"portal runtime failed"}}` + "\n"
	if output.String() != want || strings.Contains(output.String(), secret) {
		t.Fatalf("output = %q, want fixed secret-free runtime error", output.String())
	}
}

func TestServeRejectsInvalidReconciliationWithoutRuntimeMutationOrLeaks(t *testing.T) {
	const secret = "untrusted-do-not-copy"
	fixtures := map[string]string{
		"missing portals": `{}`,
		"null portals":    `{"portals":null}`,
		"duplicate uuid": `{"portals":[` +
			`{"portalId":"9f55ca93-d7b3-4eab-a871-310ea576005a","portalName":"hermes","destination":{"kind":"localApp","port":8787},"desiredState":"enabled"},` +
			`{"portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A","portalName":"hermes","destination":{"kind":"localApp","port":8787},"desiredState":"stopped"}]}`,
		"invalid desired state": `{"portals":[{"portalId":"9f55ca93-d7b3-4eab-a871-310ea576005a","portalName":"hermes","destination":{"kind":"localApp","port":8787},"desiredState":"paused"}]}`,
		"unknown portal field":  `{"portals":[{"portalId":"9f55ca93-d7b3-4eab-a871-310ea576005a","portalName":"hermes","destination":{"kind":"localApp","port":8787},"desiredState":"enabled","url":"` + secret + `"}]}`,
	}
	for name, payload := range fixtures {
		t.Run(name, func(t *testing.T) {
			runtime := &fakeRuntime{}
			input := bytes.NewBufferString(`{"version":4,"requestId":"reconcile-1","command":"reconcilePortals","payload":` + payload + `}` + "\n")
			var output bytes.Buffer
			var diagnostics bytes.Buffer

			exitCode := ServeWithRuntime(input, &output, &diagnostics, runtime)

			if exitCode != 0 || !strings.Contains(output.String(), `"code":"invalidPayload"`) || len(runtime.reconciled) != 0 {
				t.Fatalf("ServeWithRuntime = (exit %d, output %q, reconciled %+v), want zero-call invalid payload", exitCode, output.String(), runtime.reconciled)
			}
			if strings.Contains(output.String(), secret) || strings.Contains(diagnostics.String(), secret) {
				t.Fatal("invalid submitted value leaked to protocol or diagnostic output")
			}
		})
	}
}

func TestServeRejectsImperativeStartPortalInProtocolVersionFour(t *testing.T) {
	input := bytes.NewBufferString(
		`{"version":4,"requestId":"start-1","command":"startPortal","payload":{"portalId":"9f55ca93-d7b3-4eab-a871-310ea576005a","portalName":"hermes","destination":{"kind":"localApp","port":8787}}}` + "\n",
	)
	var output bytes.Buffer

	if exitCode := ServeWithRuntime(input, &output, &bytes.Buffer{}, &fakeRuntime{}); exitCode != 0 {
		t.Fatalf("ServeWithRuntime exit code = %d", exitCode)
	}
	const want = `{"version":4,"requestId":"start-1","error":{"code":"unknownCommand","message":"unsupported command"}}` + "\n"
	if output.String() != want {
		t.Fatalf("output = %q, want protocol-v1 lifecycle rejection", output.String())
	}
}

func TestServeClosesRuntimeOnShutdown(t *testing.T) {
	runtime := &fakeRuntime{}
	input := bytes.NewBufferString(`{"version":4,"requestId":"shutdown-1","command":"shutdown","payload":{}}` + "\n")
	var output bytes.Buffer

	if exitCode := ServeWithRuntime(input, &output, &bytes.Buffer{}, runtime); exitCode != 0 || !runtime.closed {
		t.Fatalf("ServeWithRuntime = (exit %d, closed %v), want orderly close", exitCode, runtime.closed)
	}
}

func TestServeAcknowledgesShutdownAfterRuntimeCloseCompletes(t *testing.T) {
	runtime := &fakeRuntime{closeEntered: make(chan struct{}), releaseClose: make(chan struct{})}
	input := bytes.NewBufferString(`{"version":4,"requestId":"shutdown-1","command":"shutdown","payload":{}}` + "\n")
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
	reconciled       []portal.Config
	reconcileEntries []portal.ReconcileEntry
	emitStatus       bool
	authenticated    string
	cleaned          string
	removed          string
	removeCalls      int
	removeErr        error
	closed           bool
	emit             func(portal.Event)
	closeEntered     chan struct{}
	releaseClose     chan struct{}
	closeOnce        sync.Once
}

func (r *fakeRuntime) Reconcile(_ context.Context, configs []portal.Config, emit func(portal.Event)) ([]portal.ReconcileEntry, error) {
	r.reconciled = append([]portal.Config(nil), configs...)
	r.emit = emit
	if r.emitStatus && len(configs) > 0 {
		emit(portal.Event{PortalID: configs[0].ID, Status: &portal.StatusEvent{
			State: portal.StateConnecting, Addresses: []string{},
			TailnetName: "opaque-identity-do-not-display", MagicDNSSuffix: "example.ts.net",
		}})
	}
	return append([]portal.ReconcileEntry(nil), r.reconcileEntries...), nil
}

func (r *fakeRuntime) Authenticate(_ context.Context, portalID string) error {
	r.authenticated = strings.ToLower(portalID)
	if r.emit != nil {
		r.emit(portal.Event{PortalID: r.authenticated, AuthenticationURL: "https://login.tailscale.com/a/transient"})
	}
	return nil
}

func (r *fakeRuntime) CleanupRejectedPortal(portalID string) error {
	r.cleaned = strings.ToLower(portalID)
	return nil
}

func (r *fakeRuntime) RemovePortal(portalID string) error {
	r.removeCalls++
	r.removed = strings.ToLower(portalID)
	return r.removeErr
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
		`{"version":4,"requestId":"shutdown-1","command":"shutdown","payload":{}}` + "\n" +
			`{"version":4,"requestId":"ignored","command":"handshake","payload":{}}` + "\n",
	)
	var output bytes.Buffer
	var diagnostics bytes.Buffer

	exitCode := Serve(input, &output, &diagnostics)

	if exitCode != 0 {
		t.Fatalf("Serve() exit code = %d, want 0", exitCode)
	}
	const want = "{\"version\":4,\"requestId\":\"shutdown-1\",\"result\":{\"accepted\":true}}\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("diagnostics = %q, want empty", diagnostics.String())
	}
}

func TestServeReturnsCorrelatedErrorForUnknownCommand(t *testing.T) {
	input := bytes.NewBufferString(`{"version":4,"requestId":"unknown-1","command":"surprise","payload":{}}` + "\n")
	var output bytes.Buffer
	var diagnostics bytes.Buffer

	exitCode := Serve(input, &output, &diagnostics)

	if exitCode != 0 {
		t.Fatalf("Serve() exit code = %d, want 0", exitCode)
	}
	const want = "{\"version\":4,\"requestId\":\"unknown-1\",\"error\":{\"code\":\"unknownCommand\",\"message\":\"unsupported command\"}}\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("diagnostics = %q, want empty", diagnostics.String())
	}
}

func TestServeReturnsCorrelatedErrorForUnsupportedVersion(t *testing.T) {
	input := bytes.NewBufferString(`{"version":3,"requestId":"version-3","command":"handshake","payload":{}}` + "\n")
	var output bytes.Buffer
	var diagnostics bytes.Buffer

	exitCode := Serve(input, &output, &diagnostics)

	if exitCode != 0 {
		t.Fatalf("Serve() exit code = %d, want 0", exitCode)
	}
	const want = "{\"version\":4,\"requestId\":\"version-3\",\"error\":{\"code\":\"unsupportedVersion\",\"message\":\"unsupported protocol version\"}}\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("diagnostics = %q, want empty", diagnostics.String())
	}
}

func TestServeRejectsMalformedInputWithoutLeakingIt(t *testing.T) {
	const secret = "token=do-not-copy"
	input := bytes.NewBufferString(`{"version":4,"requestId":"` + secret + `"` + "\n")
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
	input := bytes.NewBufferString(`{"version":4,"requestId":"request-1","command":"handshake","payload":{}} trailing` + "\n")
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
		"missing request ID": `{"version":4,"command":"handshake","payload":{}}`,
		"missing command":    `{"version":4,"requestId":"request-1","payload":{}}`,
		"missing payload":    `{"version":4,"requestId":"request-1","command":"handshake"}`,
		"non-object payload": `{"version":4,"requestId":"request-1","command":"handshake","payload":[]}`,
		"non-empty payload":  `{"version":4,"requestId":"request-1","command":"handshake","payload":{"unexpected":true}}`,
		"unknown field":      `{"version":4,"requestId":"request-1","command":"handshake","payload":{},"extra":true}`,
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
