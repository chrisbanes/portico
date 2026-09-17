package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chrisbanes/portico/helper/internal/discovery"
	"github.com/chrisbanes/portico/helper/internal/portal"
)

type frameBuffer struct {
	mu      sync.Mutex
	Buffer  bytes.Buffer
	want    int
	frames  int
	reached chan struct{}
	written chan struct{}
	once    sync.Once
}

func newFrameBuffer(want int) *frameBuffer {
	return &frameBuffer{want: want, reached: make(chan struct{}), written: make(chan struct{}, 32)}
}

func (b *frameBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	n, err := b.Buffer.Write(data)
	b.frames += bytes.Count(data, []byte{'\n'})
	reached := b.frames >= b.want
	b.mu.Unlock()
	if reached {
		b.once.Do(func() { close(b.reached) })
	}
	for range bytes.Count(data, []byte{'\n'}) {
		b.written <- struct{}{}
	}
	return n, err
}

func (b *frameBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func (b *frameBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Len()
}

func serveOpenInput(t *testing.T, line string, frames int, serve func(io.Reader, io.Writer, io.Writer) int) (int, string, string) {
	t.Helper()
	reader, input := io.Pipe()
	output := newFrameBuffer(frames)
	var diagnostics bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- serve(reader, output, &diagnostics) }()
	if _, err := io.WriteString(input, line+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-output.reached:
	case <-time.After(6 * time.Second):
		t.Fatalf("timed out waiting for %d protocol frames", frames)
	}
	_ = input.Close()
	select {
	case exitCode := <-done:
		return exitCode, output.String(), diagnostics.String()
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for helper exit")
		return 0, "", ""
	}
}

func TestServeReconcilesLiteralProtocolVersionFiveSnapshot(t *testing.T) {
	runtime := &fakeRuntime{reconcileEntries: []portal.ReconcileEntry{
		{PortalID: "5ea74329-3144-4ba2-925f-138d14d61fcc", Outcome: portal.OutcomeConverged},
		{PortalID: "9f55ca93-d7b3-4eab-a871-310ea576005a", Outcome: portal.OutcomeStartFailed},
	}}
	line :=
		`{"version":5,"requestId":"reconcile-1","command":"reconcilePortals","payload":{"portals":[` +
			`{"portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A","portalName":"hermes","destination":{"kind":"localApp","port":8787},"desiredState":"enabled"},` +
			`{"portalId":"5EA74329-3144-4BA2-925F-138D14D61FCC","portalName":"atlas","destination":{"kind":"localApp","port":8788},"desiredState":"stopped"}` +
			`]}}`
	exitCode, output, diagnostics := serveOpenInput(t, line, 1, func(input io.Reader, output, diagnostics io.Writer) int {
		return ServeWithRuntime(input, output, diagnostics, runtime)
	})

	if exitCode != 0 || diagnostics != "" {
		t.Fatalf("ServeWithRuntime = (exit %d, diagnostics %q), want success", exitCode, diagnostics)
	}
	if len(runtime.reconciled) != 2 || runtime.reconciled[0].ID != "9f55ca93-d7b3-4eab-a871-310ea576005a" || runtime.reconciled[1].DesiredState != portal.DesiredStateStopped {
		t.Fatalf("reconciled = %+v, want normalized complete snapshot", runtime.reconciled)
	}
	const want = `{"version":5,"requestId":"reconcile-1","result":{"entries":[{"portalId":"5ea74329-3144-4ba2-925f-138d14d61fcc","outcome":"converged"},{"portalId":"9f55ca93-d7b3-4eab-a871-310ea576005a","outcome":"startFailed"}]}}` + "\n"
	if output != want {
		t.Fatalf("output = %q, want exact protocol-v5 result %q", output, want)
	}
}

func TestDiscoverLocalAppsReturnsOnlyStableSanitizedCandidates(t *testing.T) {
	discoverer := fakeDiscoverer{candidates: []discovery.Candidate{
		{LocalAppPort: 9000, ProcessLabel: "hermes", SuggestedPortalName: "hermes"},
		{LocalAppPort: 8000, ProcessLabel: "atlas", SuggestedPortalName: "atlas"},
		{LocalAppPort: 9000, ProcessLabel: "hermes", SuggestedPortalName: "hermes"},
		{LocalAppPort: 7000, ProcessLabel: "first", SuggestedPortalName: "first"},
	}}
	exitCode, output, diagnostics := serveOpenInput(t, `{"version":5,"requestId":"discover-1","command":"discoverLocalApps","payload":{}}`, 1, func(input io.Reader, output, diagnostics io.Writer) int {
		return ServeWithServices(input, output, diagnostics, Services{LocalAppDiscoverer: discoverer})
	})

	if exitCode != 0 || diagnostics != "" {
		t.Fatalf("ServeWithServices = (exit %d, diagnostics %q), want success", exitCode, diagnostics)
	}
	const want = `{"version":5,"requestId":"discover-1","result":{"candidates":[{"localAppPort":7000,"processLabel":"first","suggestedPortalName":"first"},{"localAppPort":8000,"processLabel":"atlas","suggestedPortalName":"atlas"},{"localAppPort":9000,"processLabel":"hermes","suggestedPortalName":"hermes"}]}}` + "\n"
	if output != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
}

func TestDiscoverLocalAppsCollapsesDisagreeingDuplicateOwners(t *testing.T) {
	discoverer := fakeDiscoverer{candidates: []discovery.Candidate{
		{LocalAppPort: 8787, ProcessLabel: "python3", SuggestedPortalName: "hermes"},
		{LocalAppPort: 8787, ProcessLabel: "node", SuggestedPortalName: "atlas"},
	}}
	exitCode, output, _ := serveOpenInput(t, `{"version":5,"requestId":"discover-1","command":"discoverLocalApps","payload":{}}`, 1, func(input io.Reader, output, diagnostics io.Writer) int {
		return ServeWithServices(input, output, diagnostics, Services{LocalAppDiscoverer: discoverer})
	})
	if exitCode != 0 {
		t.Fatalf("ServeWithServices exit code = %d, want success", exitCode)
	}
	const want = `{"version":5,"requestId":"discover-1","result":{"candidates":[{"localAppPort":8787,"processLabel":"Port 8787"}]}}` + "\n"
	if output != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
}

func TestDiscoverLocalAppsRequiresEmptyPayload(t *testing.T) {
	const secret = "do-not-copy"
	input := bytes.NewBufferString(`{"version":5,"requestId":"discover-1","command":"discoverLocalApps","payload":{"unexpected":"` + secret + `"}}` + "\n")
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
	exitCode, output, diagnostics := serveOpenInput(t, `{"version":5,"requestId":"discover-1","command":"discoverLocalApps","payload":{}}`, 1, func(input io.Reader, output, diagnostics io.Writer) int {
		return ServeWithServices(input, output, diagnostics, Services{LocalAppDiscoverer: fakeDiscoverer{err: errors.New(secret)}})
	})

	if exitCode != 0 || diagnostics != "" {
		t.Fatalf("ServeWithServices = (exit %d, diagnostics %q), want correlated failure", exitCode, diagnostics)
	}
	const want = `{"version":5,"requestId":"discover-1","error":{"code":"discoveryFailure","message":"local app discovery failed"}}` + "\n"
	if output != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
	if strings.Contains(output, secret) || strings.Contains(diagnostics, secret) {
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

	_, _ = io.WriteString(input, `{"version":5,"requestId":"discover-1","command":"discoverLocalApps","payload":{}}`+"\n")
	<-discoverer.started
	go func() {
		_, _ = io.WriteString(input, `{"version":5,"requestId":"shutdown-1","command":"shutdown","payload":{}}`+"\n")
		_ = input.Close()
	}()

	if exitCode := <-done; exitCode != 0 {
		t.Fatalf("ServeWithServices exit code = %d, want success", exitCode)
	}
	waitForProtocolSignal(t, discoverer.canceled, "in-flight discovery cancellation")
	const want = `{"version":5,"requestId":"shutdown-1","result":{"accepted":true}}` + "\n"
	if output.String() != want || diagnostics.Len() != 0 {
		t.Fatalf("ServeWithServices = (output %q, diagnostics %q), want only post-close shutdown acknowledgement", output.String(), diagnostics.String())
	}
}

func TestDiscoverLocalAppsFailsServeWhenResponseCannotBeWritten(t *testing.T) {
	reader, input := io.Pipe()
	done := make(chan int, 1)
	go func() {
		done <- ServeWithServices(reader, errorWriter{}, &bytes.Buffer{}, Services{LocalAppDiscoverer: fakeDiscoverer{}})
	}()
	_, _ = io.WriteString(input, `{"version":5,"requestId":"discover-1","command":"discoverLocalApps","payload":{}}`+"\n")
	exitCode := <-done
	_ = input.Close()

	if exitCode == 0 {
		t.Fatal("ServeWithServices exit code = 0, want response write failure")
	}
}

func TestWriterFailureCancelsActiveRuntimeOperation(t *testing.T) {
	reader, input := io.Pipe()
	runtime := &cancellationAwareRuntime{started: make(chan struct{}), canceled: make(chan struct{})}
	done := make(chan int, 1)
	go func() {
		done <- ServeWithRuntime(reader, errorWriter{}, &bytes.Buffer{}, runtime)
	}()
	_, _ = io.WriteString(input, `{"version":5,"requestId":"reconcile-1","command":"reconcilePortals","payload":{"portals":[]}}`+"\n")
	waitForProtocolSignal(t, runtime.started, "runtime reconciliation")
	waitForProtocolSignal(t, runtime.canceled, "runtime cancellation after writer failure")
	if exitCode := <-done; exitCode == 0 {
		t.Fatal("ServeWithRuntime exit code = 0, want output failure")
	}
	_ = input.Close()
}

func TestShutdownPreemptsBlockedOutputAndActiveOperation(t *testing.T) {
	reader, input := io.Pipe()
	output := &blockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	runtime := &shutdownPreemptionRuntime{
		started: make(chan struct{}), canceled: make(chan struct{}), closeEntered: make(chan struct{}),
	}
	done := make(chan int, 1)
	go func() { done <- ServeWithRuntime(reader, output, &bytes.Buffer{}, runtime) }()
	_, _ = io.WriteString(input, `{"version":5,"requestId":"reconcile-1","command":"reconcilePortals","payload":{"portals":[]}}`+"\n")
	waitForProtocolSignal(t, runtime.started, "runtime reconciliation")
	waitForProtocolSignal(t, output.entered, "blocked protocol output")
	go func() {
		_, _ = io.WriteString(input, `{"version":5,"requestId":"shutdown-1","command":"shutdown","payload":{}}`+"\n")
	}()
	waitForProtocolSignal(t, runtime.canceled, "active runtime cancellation")
	waitForProtocolSignal(t, runtime.closeEntered, "runtime close before output writer release")
	close(output.release)
	_ = input.Close()
	if exitCode := <-done; exitCode != 0 {
		t.Fatalf("ServeWithRuntime exit code = %d, want success", exitCode)
	}
}

func TestShutdownWithPermanentlyBlockedWriterReturnsAfterCloseDeadline(t *testing.T) {
	reader, input := io.Pipe()
	output := &blockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	runtime := &shutdownPreemptionRuntime{started: make(chan struct{}), canceled: make(chan struct{}), closeEntered: make(chan struct{})}
	done := make(chan int, 1)
	go func() { done <- ServeWithRuntime(reader, output, &bytes.Buffer{}, runtime) }()
	_, _ = io.WriteString(input, `{"version":5,"requestId":"reconcile-1","command":"reconcilePortals","payload":{"portals":[]}}`+"\n")
	waitForProtocolSignal(t, output.entered, "blocked protocol output")
	go func() {
		_, _ = io.WriteString(input, `{"version":5,"requestId":"shutdown-1","command":"shutdown","payload":{}}`+"\n")
	}()
	waitForProtocolSignal(t, runtime.closeEntered, "runtime close")
	select {
	case exitCode := <-done:
		if exitCode == 0 {
			t.Fatal("ServeWithRuntime exit code = 0, want bounded acknowledgement failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown waited for permanently blocked writer")
	}
	close(output.release)
	_ = input.Close()
}

func TestMessageWriterDrainsMoreThanCapacityInFIFOOrder(t *testing.T) {
	var output bytes.Buffer
	writer := newMessageWriter(context.Background(), &output, func() {})
	for i := 0; i < 17; i++ {
		if err := writer.write(response{Version: Version, RequestID: fmt.Sprintf("event-%d", i), Result: acceptedResult{Accepted: true}}); err != nil {
			t.Fatalf("enqueue frame %d: %v", i, err)
		}
	}
	if err := writer.write(response{Version: Version, RequestID: "result", Result: acceptedResult{Accepted: true}}); err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := writer.flush(deadline); err != nil {
		t.Fatal(err)
	}
	writer.stopContext(deadline)
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 18 || !strings.Contains(lines[0], `"requestId":"event-0"`) || !strings.Contains(lines[16], `"requestId":"event-16"`) || !strings.Contains(lines[17], `"requestId":"result"`) {
		t.Fatalf("frames = %q, want 17 events followed by result", output.String())
	}
}

func TestMessageWriterRejectsCanceledContextWithWritableQueue(t *testing.T) {
	for attempt := 0; attempt < 64; attempt++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		writer := newMessageWriter(context.Background(), io.Discard, func() {})
		if err := writer.writeContext(ctx, response{Version: Version, RequestID: "canceled", Result: acceptedResult{Accepted: true}}); !errors.Is(err, context.Canceled) {
			writer.stopContext(context.Background())
			t.Fatalf("attempt %d writeContext = %v, want context cancellation before queue admission", attempt, err)
		}
		writer.stopContext(context.Background())
	}
}

func TestServeDrainsMoreThanOutputCapacityBeforeCorrelatedResponse(t *testing.T) {
	runtime := &burstRuntime{}
	line := `{"version":5,"requestId":"reconcile-1","command":"reconcilePortals","payload":{"portals":[]}}`
	exitCode, output, diagnostics := serveOpenInput(t, line, 18, func(input io.Reader, output, diagnostics io.Writer) int {
		return ServeWithRuntime(input, output, diagnostics, runtime)
	})
	if exitCode != 0 || diagnostics != "" {
		t.Fatalf("ServeWithRuntime = (%d, %q), want success", exitCode, diagnostics)
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 18 || !strings.Contains(lines[0], `"portalStatus"`) || !strings.Contains(lines[17], `"requestId":"reconcile-1"`) {
		t.Fatalf("output = %q, want 17 events followed by response", output)
	}
}

func TestShutdownStartsCloseWithoutWaitingForCancellationResistantActiveRequest(t *testing.T) {
	reader, input := io.Pipe()
	runtime := &closeUnblocksRuntime{started: make(chan struct{}), closeStarted: make(chan struct{}), release: make(chan struct{})}
	done := make(chan int, 1)
	go func() { done <- ServeWithRuntime(reader, &bytes.Buffer{}, &bytes.Buffer{}, runtime) }()
	_, _ = io.WriteString(input, `{"version":5,"requestId":"reconcile-1","command":"reconcilePortals","payload":{"portals":[]}}`+"\n")
	waitForProtocolSignal(t, runtime.started, "active reconciliation")
	go func() {
		_, _ = io.WriteString(input, `{"version":5,"requestId":"shutdown-1","command":"shutdown","payload":{}}`+"\n")
	}()
	waitForProtocolSignal(t, runtime.closeStarted, "runtime close")
	close(runtime.release)
	_ = input.Close()
	if exitCode := <-done; exitCode != 0 {
		t.Fatalf("ServeWithRuntime exit code = %d, want success", exitCode)
	}
}

func TestEOFCancelsActiveAndDropsStagedRequestWithoutResponse(t *testing.T) {
	reader, input := io.Pipe()
	runtime := &eofRuntime{started: make(chan struct{}), canceled: make(chan struct{}), closeStarted: make(chan struct{})}
	output := newFrameBuffer(1)
	done := make(chan int, 1)
	go func() { done <- ServeWithRuntime(reader, output, &bytes.Buffer{}, runtime) }()
	_, _ = io.WriteString(input, `{"version":5,"requestId":"reconcile-1","command":"reconcilePortals","payload":{"portals":[]}}`+"\n")
	waitForProtocolSignal(t, runtime.started, "active reconciliation")
	_, _ = io.WriteString(input, `{"version":5,"requestId":"handshake-2","command":"handshake","payload":{}}`+"\n")
	_ = input.Close()
	waitForProtocolSignal(t, runtime.canceled, "active cancellation on EOF")
	waitForProtocolSignal(t, runtime.closeStarted, "runtime close on EOF")
	if exitCode := <-done; exitCode != 0 {
		t.Fatalf("ServeWithRuntime exit code = %d, want clean EOF", exitCode)
	}
	if output.Len() != 0 {
		t.Fatalf("output = %q, want no fabricated responses", output.String())
	}
}

func TestShutdownAndEOFCancelActiveDeletionCommands(t *testing.T) {
	commands := []struct {
		name    string
		request string
	}{
		{name: "cleanup", request: `{"version":5,"requestId":"cleanup-1","command":"cleanupRejectedPortal","payload":{"portalId":"9f55ca93-d7b3-4eab-a871-310ea576005a"}}`},
		{name: "remove", request: `{"version":5,"requestId":"remove-1","command":"removePortal","payload":{"portalId":"9f55ca93-d7b3-4eab-a871-310ea576005a"}}`},
	}
	for _, command := range commands {
		for _, terminal := range []string{"shutdown", "eof"} {
			t.Run(command.name+"/"+terminal, func(t *testing.T) {
				reader, input := io.Pipe()
				runtime := &cancellableDeletionRuntime{started: make(chan struct{}), canceled: make(chan struct{}), closeStarted: make(chan struct{})}
				var output bytes.Buffer
				done := make(chan int, 1)
				go func() { done <- ServeWithRuntime(reader, &output, &bytes.Buffer{}, runtime) }()
				_, _ = io.WriteString(input, command.request+"\n")
				waitForProtocolSignal(t, runtime.started, "active deletion command")
				if terminal == "shutdown" {
					_, _ = io.WriteString(input, `{"version":5,"requestId":"shutdown-1","command":"shutdown","payload":{}}`+"\n")
				} else {
					_ = input.Close()
				}
				waitForProtocolSignal(t, runtime.canceled, "deletion command cancellation")
				waitForProtocolSignal(t, runtime.closeStarted, "runtime close after deletion cancellation")
				if terminal == "shutdown" {
					_ = input.Close()
				}
				if exitCode := <-done; exitCode != 0 {
					t.Fatalf("ServeWithRuntime exit code = %d, want success", exitCode)
				}
				if terminal == "shutdown" {
					const want = `{"version":5,"requestId":"shutdown-1","result":{"accepted":true}}` + "\n"
					if output.String() != want {
						t.Fatalf("output = %q, want only shutdown acknowledgement", output.String())
					}
				} else if output.Len() != 0 {
					t.Fatalf("output = %q, want no deletion response", output.String())
				}
			})
		}
	}
}

func TestOrdinaryRequestsRunOneAtATimeInFIFOOrder(t *testing.T) {
	reader, input := io.Pipe()
	runtime := &sequencingRuntime{
		firstStarted:  make(chan struct{}),
		secondStarted: make(chan struct{}),
		releaseFirst:  make(chan struct{}),
		releaseSecond: make(chan struct{}),
	}
	output := newFrameBuffer(2)
	done := make(chan int, 1)
	go func() { done <- ServeWithRuntime(reader, output, &bytes.Buffer{}, runtime) }()

	_, _ = io.WriteString(input, `{"version":5,"requestId":"first","command":"authenticatePortal","payload":{"portalId":"9f55ca93-d7b3-4eab-a871-310ea576005a"}}`+"\n")
	waitForProtocolSignal(t, runtime.firstStarted, "first ordinary request")
	_, _ = io.WriteString(input, `{"version":5,"requestId":"second","command":"authenticatePortal","payload":{"portalId":"5ea74329-3144-4ba2-925f-138d14d61fcc"}}`+"\n")
	select {
	case <-runtime.secondStarted:
		t.Fatal("staged ordinary request started while the first was active")
	default:
	}

	close(runtime.releaseFirst)
	waitForProtocolSignal(t, runtime.secondStarted, "second ordinary request after first completion")
	close(runtime.releaseSecond)
	select {
	case <-output.reached:
	case <-time.After(time.Second):
		t.Fatal("ordinary responses were not written")
	}
	_ = input.Close()
	if exitCode := <-done; exitCode != 0 {
		t.Fatalf("ServeWithRuntime exit code = %d, want success", exitCode)
	}
	const want = "{\"version\":5,\"requestId\":\"first\",\"result\":{\"accepted\":true}}\n" +
		"{\"version\":5,\"requestId\":\"second\",\"result\":{\"accepted\":true}}\n"
	if output.String() != want {
		t.Fatalf("output = %q, want FIFO responses %q", output.String(), want)
	}
}

func TestThirdOrdinaryFrameIsNotDecodedBeforeAnAdmissionIsAvailable(t *testing.T) {
	reader, input := io.Pipe()
	runtime := &boundedAdmissionRuntime{
		firstStarted:  make(chan struct{}),
		secondStarted: make(chan struct{}),
		closeStarted:  make(chan struct{}),
		releaseFirst:  make(chan struct{}),
	}
	done := make(chan int, 1)
	go func() { done <- ServeWithRuntime(reader, io.Discard, &bytes.Buffer{}, runtime) }()

	_, _ = io.WriteString(input, `{"version":5,"requestId":"first","command":"authenticatePortal","payload":{"portalId":"9f55ca93-d7b3-4eab-a871-310ea576005a"}}`+"\n")
	waitForProtocolSignal(t, runtime.firstStarted, "first ordinary request")
	_, _ = io.WriteString(input, `{"version":5,"requestId":"second","command":"authenticatePortal","payload":{"portalId":"5ea74329-3144-4ba2-925f-138d14d61fcc"}}`+"\n")
	thirdWritten := make(chan struct{})
	go func() {
		_, _ = io.WriteString(input, `not-json`+"\n")
		close(thirdWritten)
	}()
	waitForProtocolSignal(t, thirdWritten, "third raw frame read")
	select {
	case <-runtime.closeStarted:
		t.Fatal("third frame was decoded before an admission was available")
	default:
	}

	close(runtime.releaseFirst)
	waitForProtocolSignal(t, runtime.closeStarted, "third-frame terminal handling")
	if exitCode := <-done; exitCode == 0 {
		t.Fatal("ServeWithRuntime exit code = 0, want invalid third frame failure")
	}
}

func TestScannerErrorCancelsActiveAndDropsStagedRequestWithoutResponse(t *testing.T) {
	runtime := &eofRuntime{started: make(chan struct{}), canceled: make(chan struct{}), closeStarted: make(chan struct{})}
	input := &gatedScannerErrorReader{
		first:       []byte(`{"version":5,"requestId":"reconcile-1","command":"reconcilePortals","payload":{"portals":[]}}` + "\n"),
		tail:        []byte(`{"version":5,"requestId":"handshake-2","command":"handshake","payload":{}}` + "\n"),
		releaseTail: make(chan struct{}),
	}
	output := newFrameBuffer(1)
	var diagnostics bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- ServeWithRuntime(input, output, &diagnostics, runtime) }()
	waitForProtocolSignal(t, runtime.started, "active reconciliation before scanner error")
	close(input.releaseTail)
	if exitCode := <-done; exitCode == 0 {
		t.Fatal("ServeWithRuntime exit code = 0, want scanner failure")
	}
	if diagnostics.String() != invalidRequestDiagnostic || output.Len() != 0 {
		t.Fatalf("diagnostics/output = (%q, %q), want fixed diagnostic and no responses", diagnostics.String(), output.String())
	}
	select {
	case <-runtime.canceled:
	case <-time.After(time.Second):
		t.Fatal("active request was not canceled")
	}
}

type burstRuntime struct{}

func (*burstRuntime) Reconcile(_ context.Context, _ []portal.Config, emit func(portal.Event)) ([]portal.ReconcileEntry, error) {
	for i := 0; i < 17; i++ {
		emit(portal.Event{PortalID: fmt.Sprintf("00000000-0000-0000-0000-%012d", i), Status: &portal.StatusEvent{State: portal.StateConnecting, Addresses: []string{}}})
	}
	return []portal.ReconcileEntry{}, nil
}
func (*burstRuntime) Authenticate(context.Context, string) error          { return nil }
func (*burstRuntime) CleanupRejectedPortal(context.Context, string) error { return nil }
func (*burstRuntime) RemovePortal(context.Context, string) error          { return nil }
func (*burstRuntime) Close(context.Context) error                         { return nil }

type closeUnblocksRuntime struct{ started, closeStarted, release chan struct{} }

func (r *closeUnblocksRuntime) Reconcile(context.Context, []portal.Config, func(portal.Event)) ([]portal.ReconcileEntry, error) {
	close(r.started)
	<-r.release
	return nil, nil
}
func (*closeUnblocksRuntime) Authenticate(context.Context, string) error          { return nil }
func (*closeUnblocksRuntime) CleanupRejectedPortal(context.Context, string) error { return nil }
func (*closeUnblocksRuntime) RemovePortal(context.Context, string) error          { return nil }
func (r *closeUnblocksRuntime) Close(context.Context) error                       { close(r.closeStarted); return nil }

type eofRuntime struct{ started, canceled, closeStarted chan struct{} }

func (r *eofRuntime) Reconcile(ctx context.Context, _ []portal.Config, _ func(portal.Event)) ([]portal.ReconcileEntry, error) {
	close(r.started)
	<-ctx.Done()
	close(r.canceled)
	return nil, ctx.Err()
}
func (*eofRuntime) Authenticate(context.Context, string) error          { return nil }
func (*eofRuntime) CleanupRejectedPortal(context.Context, string) error { return nil }
func (*eofRuntime) RemovePortal(context.Context, string) error          { return nil }
func (r *eofRuntime) Close(context.Context) error                       { close(r.closeStarted); return nil }

type cancellableDeletionRuntime struct {
	started, canceled, closeStarted  chan struct{}
	startOnce, cancelOnce, closeOnce sync.Once
}

func (*cancellableDeletionRuntime) Reconcile(context.Context, []portal.Config, func(portal.Event)) ([]portal.ReconcileEntry, error) {
	return nil, nil
}
func (*cancellableDeletionRuntime) Authenticate(context.Context, string) error { return nil }
func (r *cancellableDeletionRuntime) CleanupRejectedPortal(ctx context.Context, _ string) error {
	return r.wait(ctx)
}
func (r *cancellableDeletionRuntime) RemovePortal(ctx context.Context, _ string) error {
	return r.wait(ctx)
}
func (r *cancellableDeletionRuntime) wait(ctx context.Context) error {
	r.startOnce.Do(func() { close(r.started) })
	<-ctx.Done()
	r.cancelOnce.Do(func() { close(r.canceled) })
	return ctx.Err()
}
func (r *cancellableDeletionRuntime) Close(context.Context) error {
	r.closeOnce.Do(func() { close(r.closeStarted) })
	return nil
}

type sequencingRuntime struct {
	firstStarted, secondStarted, releaseFirst, releaseSecond chan struct{}
}

func (*sequencingRuntime) Reconcile(context.Context, []portal.Config, func(portal.Event)) ([]portal.ReconcileEntry, error) {
	return nil, nil
}
func (r *sequencingRuntime) Authenticate(ctx context.Context, portalID string) error {
	switch portalID {
	case "9f55ca93-d7b3-4eab-a871-310ea576005a":
		close(r.firstStarted)
		select {
		case <-r.releaseFirst:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	case "5ea74329-3144-4ba2-925f-138d14d61fcc":
		close(r.secondStarted)
		select {
		case <-r.releaseSecond:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	default:
		return errors.New("unexpected Portal ID")
	}
}
func (*sequencingRuntime) CleanupRejectedPortal(context.Context, string) error { return nil }
func (*sequencingRuntime) RemovePortal(context.Context, string) error          { return nil }
func (*sequencingRuntime) Close(context.Context) error                         { return nil }

type boundedAdmissionRuntime struct {
	firstStarted, secondStarted, closeStarted, releaseFirst chan struct{}
	closeOnce                                               sync.Once
}

func (*boundedAdmissionRuntime) Reconcile(context.Context, []portal.Config, func(portal.Event)) ([]portal.ReconcileEntry, error) {
	return nil, nil
}
func (r *boundedAdmissionRuntime) Authenticate(ctx context.Context, portalID string) error {
	switch portalID {
	case "9f55ca93-d7b3-4eab-a871-310ea576005a":
		close(r.firstStarted)
		select {
		case <-r.releaseFirst:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	case "5ea74329-3144-4ba2-925f-138d14d61fcc":
		close(r.secondStarted)
		<-ctx.Done()
		return ctx.Err()
	default:
		return errors.New("unexpected Portal ID")
	}
}
func (*boundedAdmissionRuntime) CleanupRejectedPortal(context.Context, string) error { return nil }
func (*boundedAdmissionRuntime) RemovePortal(context.Context, string) error          { return nil }
func (r *boundedAdmissionRuntime) Close(context.Context) error {
	r.closeOnce.Do(func() { close(r.closeStarted) })
	return nil
}

type gatedScannerErrorReader struct {
	first, tail []byte
	releaseTail chan struct{}
	firstRead   bool
	tailRead    bool
}

func (r *gatedScannerErrorReader) Read(destination []byte) (int, error) {
	if !r.firstRead {
		r.firstRead = true
		return copy(destination, r.first), nil
	}
	if !r.tailRead {
		<-r.releaseTail
		r.tailRead = true
		return copy(destination, r.tail), nil
	}
	return 0, errors.New("scanner read failure")
}

type fakeDiscoverer struct {
	candidates []discovery.Candidate
	err        error
}

func TestDiscoveryDeadlineReturnsFailureWithoutStoppingHelper(t *testing.T) {
	exitCode, output, diagnostics := serveOpenInput(t, `{"version":5,"requestId":"discover-budget","command":"discoverLocalApps","payload":{}}`, 1, func(input io.Reader, output, diagnostics io.Writer) int {
		return ServeWithServices(input, output, diagnostics, Services{LocalAppDiscoverer: deadlineDiscoverer{t: t}})
	})
	const want = `{"version":5,"requestId":"discover-budget","error":{"code":"discoveryFailure","message":"local app discovery failed"}}` + "\n"
	if exitCode != 0 || diagnostics != "" || output != want {
		t.Fatalf("ServeWithServices = (%d, %q, %q), want correlated discovery failure only", exitCode, output, diagnostics)
	}
}

func TestOversizedDiscoveryReturnsFailureWithoutSendingPartialCandidates(t *testing.T) {
	candidates := make([]discovery.Candidate, 3000)
	for i := range candidates {
		candidates[i] = discovery.Candidate{
			LocalAppPort: uint16(i + 1), ProcessLabel: strings.Repeat("a", 64),
		}
	}
	exitCode, output, diagnostics := serveOpenInput(t, `{"version":5,"requestId":"discover-large","command":"discoverLocalApps","payload":{}}`, 1, func(input io.Reader, output, diagnostics io.Writer) int {
		return ServeWithServices(input, output, diagnostics, Services{LocalAppDiscoverer: fakeDiscoverer{candidates: candidates}})
	})
	const want = `{"version":5,"requestId":"discover-large","error":{"code":"discoveryFailure","message":"local app discovery failed"}}` + "\n"
	if exitCode != 0 || diagnostics != "" || output != want {
		t.Fatalf("ServeWithServices = (exit %d, output %d bytes, diagnostics %q), want only the correlated sanitized failure", exitCode, len(output), diagnostics)
	}
}

type deadlineDiscoverer struct{ t *testing.T }

func (d deadlineDiscoverer) Discover(ctx context.Context) ([]discovery.Candidate, error) {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 4*time.Second {
		d.t.Error("discovery must have a budget leaving time before the Swift five-second deadline")
		return nil, context.DeadlineExceeded
	}
	<-ctx.Done()
	// A discoverer returning partial candidates on expiry must not produce success.
	return []discovery.Candidate{{LocalAppPort: 8000, ProcessLabel: "partial"}}, nil
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

type cancellationAwareRuntime struct {
	started  chan struct{}
	canceled chan struct{}
}

type blockingWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingWriter) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(data), nil
}

type shutdownPreemptionRuntime struct {
	started      chan struct{}
	canceled     chan struct{}
	closeEntered chan struct{}
}

func (r *shutdownPreemptionRuntime) Reconcile(ctx context.Context, _ []portal.Config, emit func(portal.Event)) ([]portal.ReconcileEntry, error) {
	close(r.started)
	emit(portal.Event{PortalID: "9f55ca93-d7b3-4eab-a871-310ea576005a", Status: &portal.StatusEvent{State: portal.StateConnecting, Addresses: []string{}}})
	<-ctx.Done()
	close(r.canceled)
	return nil, ctx.Err()
}

func (*shutdownPreemptionRuntime) Authenticate(context.Context, string) error          { return nil }
func (*shutdownPreemptionRuntime) CleanupRejectedPortal(context.Context, string) error { return nil }
func (*shutdownPreemptionRuntime) RemovePortal(context.Context, string) error          { return nil }
func (r *shutdownPreemptionRuntime) Close(context.Context) error {
	close(r.closeEntered)
	return nil
}

func (r *cancellationAwareRuntime) Reconcile(ctx context.Context, _ []portal.Config, emit func(portal.Event)) ([]portal.ReconcileEntry, error) {
	close(r.started)
	emit(portal.Event{PortalID: "9f55ca93-d7b3-4eab-a871-310ea576005a", Status: &portal.StatusEvent{State: portal.StateConnecting, Addresses: []string{}}})
	<-ctx.Done()
	close(r.canceled)
	return nil, ctx.Err()
}

func (*cancellationAwareRuntime) Authenticate(context.Context, string) error          { return nil }
func (*cancellationAwareRuntime) CleanupRejectedPortal(context.Context, string) error { return nil }
func (*cancellationAwareRuntime) RemovePortal(context.Context, string) error          { return nil }
func (*cancellationAwareRuntime) Close(context.Context) error                         { return nil }

func waitForProtocolSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func (*shutdownOrderingRuntime) Reconcile(context.Context, []portal.Config, func(portal.Event)) ([]portal.ReconcileEntry, error) {
	return []portal.ReconcileEntry{}, nil
}

func (*shutdownOrderingRuntime) Authenticate(context.Context, string) error { return nil }

func (*shutdownOrderingRuntime) CleanupRejectedPortal(context.Context, string) error { return nil }

func (*shutdownOrderingRuntime) RemovePortal(context.Context, string) error { return nil }

func (r *shutdownOrderingRuntime) Close(context.Context) error {
	select {
	case <-r.discoveryCanceled:
		r.closedAfterDiscoveryCancellation = true
	default:
	}
	return nil
}

func TestServeCorrelatesHandshakeResponse(t *testing.T) {
	exitCode, output, diagnostics := serveOpenInput(t, `{"version":5,"requestId":"request-1","command":"handshake","payload":{}}`, 1, Serve)

	if exitCode != 0 {
		t.Fatalf("Serve() exit code = %d, want 0", exitCode)
	}
	var response struct {
		Version   int             `json:"version"`
		RequestID string          `json:"requestId"`
		Result    json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(strings.NewReader(output)).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Version != 5 || response.RequestID != "request-1" || string(response.Result) != `{"protocolVersion":5}` {
		t.Fatalf("response = %+v, want correlated version-five handshake", response)
	}
	if diagnostics != "" {
		t.Fatalf("diagnostics = %q, want empty", diagnostics)
	}
}

func TestServeReconciliationSerializesStructuredStatusEvent(t *testing.T) {
	runtime := &fakeRuntime{emitStatus: true, reconcileEntries: []portal.ReconcileEntry{{
		PortalID: "9f55ca93-d7b3-4eab-a871-310ea576005a", Outcome: portal.OutcomeConverged,
	}}}
	line := `{"version":5,"requestId":"reconcile-1","command":"reconcilePortals","payload":{"portals":[{"portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A","portalName":"hermes","destination":{"kind":"localApp","port":8787},"desiredState":"enabled"}]}}`
	exitCode, output, diagnostics := serveOpenInput(t, strings.TrimSpace(line), 2, func(input io.Reader, output, diagnostics io.Writer) int {
		return ServeWithRuntime(input, output, diagnostics, runtime)
	})

	if exitCode != 0 || diagnostics != "" {
		t.Fatalf("ServeWithRuntime = (exit %d, diagnostics %q), want success", exitCode, diagnostics)
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 2 {
		t.Fatalf("output = %q, want event and response", output)
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
	const wantResponse = `{"version":5,"requestId":"reconcile-1","result":{"entries":[{"portalId":"9f55ca93-d7b3-4eab-a871-310ea576005a","outcome":"converged"}]}}`
	if lines[1] != wantResponse {
		t.Fatalf("response = %q, want %q", lines[1], wantResponse)
	}
}

func TestServeAuthenticatesCorrelatedPortalAndEmitsTransientURL(t *testing.T) {
	runtime := &fakeRuntime{reconcileEntries: []portal.ReconcileEntry{{
		PortalID: "9f55ca93-d7b3-4eab-a871-310ea576005a", Outcome: portal.OutcomeConverged,
	}}}
	reader, input := io.Pipe()
	output := newFrameBuffer(3)
	var diagnostics bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- ServeWithRuntime(reader, output, &diagnostics, runtime) }()
	_, _ = io.WriteString(input, `{"version":5,"requestId":"reconcile-1","command":"reconcilePortals","payload":{"portals":[{"portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A","portalName":"hermes","destination":{"kind":"localApp","port":8787},"desiredState":"enabled"}]}}`+"\n")
	select {
	case <-output.written:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reconcile response")
	}
	_, _ = io.WriteString(input, `{"version":5,"requestId":"auth-1","command":"authenticatePortal","payload":{"portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A"}}`+"\n")
	select {
	case <-output.reached:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for authentication frames")
	}
	_ = input.Close()
	exitCode := <-done

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
	exitCode, output, diagnostics := serveOpenInput(t, `{"version":5,"requestId":"cleanup-1","command":"cleanupRejectedPortal","payload":{"portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A"}}`, 1, func(input io.Reader, output, diagnostics io.Writer) int {
		return ServeWithRuntime(input, output, diagnostics, runtime)
	})

	if exitCode != 0 || diagnostics != "" || runtime.cleaned != "9f55ca93-d7b3-4eab-a871-310ea576005a" {
		t.Fatalf("ServeWithRuntime = (exit %d, cleaned %q, diagnostics %q), want correlated cleanup", exitCode, runtime.cleaned, diagnostics)
	}
	const want = `{"version":5,"requestId":"cleanup-1","result":{"accepted":true}}` + "\n"
	if output != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
}

func TestServeRemovesOnlyCorrelatedPortalWithProtocolVersionFive(t *testing.T) {
	runtime := &fakeRuntime{}
	exitCode, output, diagnostics := serveOpenInput(t, `{"version":5,"requestId":"remove-1","command":"removePortal","payload":{"portalId":"9F55CA93-D7B3-4EAB-A871-310EA576005A"}}`, 1, func(input io.Reader, output, diagnostics io.Writer) int {
		return ServeWithRuntime(input, output, diagnostics, runtime)
	})

	if exitCode != 0 || diagnostics != "" || runtime.removed != "9f55ca93-d7b3-4eab-a871-310ea576005a" {
		t.Fatalf("ServeWithRuntime = (exit %d, removed %q, diagnostics %q), want correlated removal", exitCode, runtime.removed, diagnostics)
	}
	const want = `{"version":5,"requestId":"remove-1","result":{"accepted":true}}` + "\n"
	if output != want {
		t.Fatalf("output = %q, want %q", output, want)
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
			exitCode, output, _ := serveOpenInput(t, `{"version":5,"requestId":"remove-1","command":"removePortal","payload":`+payload+`}`, 1, func(input io.Reader, output, diagnostics io.Writer) int {
				return ServeWithRuntime(input, output, diagnostics, runtime)
			})
			if exitCode != 0 {
				t.Fatalf("ServeWithRuntime exit code = %d, want correlated rejection", exitCode)
			}
			if runtime.removeCalls != 0 || !strings.Contains(output, `"code":"invalidPayload"`) {
				t.Fatalf("remove calls = %d, output = %q, want zero-call invalid payload", runtime.removeCalls, output)
			}
		})
	}
}

func TestServeMapsRemovalFailureToFixedRuntimeError(t *testing.T) {
	const secret = "path=/private/secret"
	runtime := &fakeRuntime{removeErr: errors.New(secret)}
	exitCode, output, _ := serveOpenInput(t, `{"version":5,"requestId":"remove-1","command":"removePortal","payload":{"portalId":"9f55ca93-d7b3-4eab-a871-310ea576005a"}}`, 1, func(input io.Reader, output, diagnostics io.Writer) int {
		return ServeWithRuntime(input, output, diagnostics, runtime)
	})
	if exitCode != 0 {
		t.Fatalf("ServeWithRuntime exit code = %d, want correlated failure", exitCode)
	}
	const want = `{"version":5,"requestId":"remove-1","error":{"code":"runtimeFailure","message":"portal runtime failed"}}` + "\n"
	if output != want || strings.Contains(output, secret) {
		t.Fatalf("output = %q, want fixed secret-free runtime error", output)
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
			exitCode, output, diagnostics := serveOpenInput(t, `{"version":5,"requestId":"reconcile-1","command":"reconcilePortals","payload":`+payload+`}`, 1, func(input io.Reader, output, diagnostics io.Writer) int {
				return ServeWithRuntime(input, output, diagnostics, runtime)
			})

			if exitCode != 0 || !strings.Contains(output, `"code":"invalidPayload"`) || len(runtime.reconciled) != 0 {
				t.Fatalf("ServeWithRuntime = (exit %d, output %q, reconciled %+v), want zero-call invalid payload", exitCode, output, runtime.reconciled)
			}
			if strings.Contains(output, secret) || strings.Contains(diagnostics, secret) {
				t.Fatal("invalid submitted value leaked to protocol or diagnostic output")
			}
		})
	}
}

func TestServeRejectsImperativeStartPortalInProtocolVersionFive(t *testing.T) {
	exitCode, output, _ := serveOpenInput(t, `{"version":5,"requestId":"start-1","command":"startPortal","payload":{"portalId":"9f55ca93-d7b3-4eab-a871-310ea576005a","portalName":"hermes","destination":{"kind":"localApp","port":8787}}}`, 1, func(input io.Reader, output, diagnostics io.Writer) int {
		return ServeWithRuntime(input, output, diagnostics, &fakeRuntime{})
	})
	if exitCode != 0 {
		t.Fatalf("ServeWithRuntime exit code = %d", exitCode)
	}
	const want = `{"version":5,"requestId":"start-1","error":{"code":"unknownCommand","message":"unsupported command"}}` + "\n"
	if output != want {
		t.Fatalf("output = %q, want protocol-v1 lifecycle rejection", output)
	}
}

func TestServeClosesRuntimeOnShutdown(t *testing.T) {
	runtime := &fakeRuntime{}
	input := bytes.NewBufferString(`{"version":5,"requestId":"shutdown-1","command":"shutdown","payload":{}}` + "\n")
	var output bytes.Buffer

	if exitCode := ServeWithRuntime(input, &output, &bytes.Buffer{}, runtime); exitCode != 0 || !runtime.closed {
		t.Fatalf("ServeWithRuntime = (exit %d, closed %v), want orderly close", exitCode, runtime.closed)
	}
}

func TestServeAcknowledgesShutdownAfterRuntimeCloseCompletes(t *testing.T) {
	runtime := &fakeRuntime{closeEntered: make(chan struct{}), releaseClose: make(chan struct{})}
	input := bytes.NewBufferString(`{"version":5,"requestId":"shutdown-1","command":"shutdown","payload":{}}` + "\n")
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

func (r *fakeRuntime) CleanupRejectedPortal(_ context.Context, portalID string) error {
	r.cleaned = strings.ToLower(portalID)
	return nil
}

func (r *fakeRuntime) RemovePortal(_ context.Context, portalID string) error {
	r.removeCalls++
	r.removed = strings.ToLower(portalID)
	return r.removeErr
}

func (r *fakeRuntime) Close(context.Context) error {
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
		`{"version":5,"requestId":"shutdown-1","command":"shutdown","payload":{}}` + "\n" +
			`{"version":5,"requestId":"ignored","command":"handshake","payload":{}}` + "\n",
	)
	var output bytes.Buffer
	var diagnostics bytes.Buffer

	exitCode := Serve(input, &output, &diagnostics)

	if exitCode != 0 {
		t.Fatalf("Serve() exit code = %d, want 0", exitCode)
	}
	const want = "{\"version\":5,\"requestId\":\"shutdown-1\",\"result\":{\"accepted\":true}}\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("diagnostics = %q, want empty", diagnostics.String())
	}
}

func TestServeReturnsCorrelatedErrorForUnknownCommand(t *testing.T) {
	exitCode, output, diagnostics := serveOpenInput(t, `{"version":5,"requestId":"unknown-1","command":"surprise","payload":{}}`, 1, Serve)

	if exitCode != 0 {
		t.Fatalf("Serve() exit code = %d, want 0", exitCode)
	}
	const want = "{\"version\":5,\"requestId\":\"unknown-1\",\"error\":{\"code\":\"unknownCommand\",\"message\":\"unsupported command\"}}\n"
	if output != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
	if diagnostics != "" {
		t.Fatalf("diagnostics = %q, want empty", diagnostics)
	}
}

func TestServeRejectsVersionFourPeer(t *testing.T) {
	exitCode, output, diagnostics := serveOpenInput(t, `{"version":4,"requestId":"version-4","command":"handshake","payload":{}}`, 1, Serve)

	if exitCode != 0 {
		t.Fatalf("Serve() exit code = %d, want 0", exitCode)
	}
	const want = "{\"version\":5,\"requestId\":\"version-4\",\"error\":{\"code\":\"unsupportedVersion\",\"message\":\"unsupported protocol version\"}}\n"
	if output != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
	if diagnostics != "" {
		t.Fatalf("diagnostics = %q, want empty", diagnostics)
	}
}

func TestServeRejectsMalformedInputWithoutLeakingIt(t *testing.T) {
	const secret = "token=do-not-copy"
	input := bytes.NewBufferString(`{"version":5,"requestId":"` + secret + `"` + "\n")
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
	input := bytes.NewBufferString(`{"version":5,"requestId":"request-1","command":"handshake","payload":{}} trailing` + "\n")
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
		"missing request ID": `{"version":5,"command":"handshake","payload":{}}`,
		"missing command":    `{"version":5,"requestId":"request-1","payload":{}}`,
		"missing payload":    `{"version":5,"requestId":"request-1","command":"handshake"}`,
		"non-object payload": `{"version":5,"requestId":"request-1","command":"handshake","payload":[]}`,
		"non-empty payload":  `{"version":5,"requestId":"request-1","command":"handshake","payload":{"unexpected":true}}`,
		"unknown field":      `{"version":5,"requestId":"request-1","command":"handshake","payload":{},"extra":true}`,
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
