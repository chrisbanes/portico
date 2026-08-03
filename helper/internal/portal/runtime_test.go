package portal

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const testPortalID = "9f55ca93-d7b3-4eab-a871-310ea576005a"

func TestRuntimeUsesUUIDStateDirectoryAndStableIdentity(t *testing.T) {
	factory := newFakeFactory()
	root := t.TempDir()
	config := Config{ID: testPortalID, Name: "hermes", Port: 8787}

	for range 2 {
		runtime := NewRuntime(root, factory.New)
		if err := runtime.Start(context.Background(), config, func(Event) {}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := runtime.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}

	wantDir := filepath.Join(root, testPortalID)
	if len(factory.created) != 2 || factory.created[0].dir != wantDir || factory.created[1].dir != wantDir {
		t.Fatalf("created dirs = %+v, want repeated %q", factory.created, wantDir)
	}
	if factory.created[0].hostname != "hermes" || factory.created[0].nodeID != factory.created[1].nodeID {
		t.Fatalf("created nodes = %+v, want requested hostname and stable identity", factory.created)
	}
}

func TestConfigRejectsUntrustedPathsAndDestinations(t *testing.T) {
	invalid := []Config{
		{ID: "../escape", Name: "hermes", Port: 8787},
		{ID: testPortalID, Name: "Hermes", Port: 8787},
		{ID: testPortalID, Name: "hermes", Port: 0},
	}
	for _, config := range invalid {
		if err := config.Validate(); err == nil {
			t.Fatalf("Validate(%+v) = nil, want error", config)
		}
	}
}

func TestRuntimeMapsStructuredStatus(t *testing.T) {
	tests := []struct {
		backend string
		want    State
	}{
		{"NeedsLogin", StateAuthenticating},
		{"NeedsMachineAuth", StateAwaitingApproval},
		{"Starting", StateConnecting},
		{"Running", StateOnline},
		{"Stopped", StateStopped},
		{"Unexpected", StateError},
	}
	for _, test := range tests {
		t.Run(test.backend, func(t *testing.T) {
			factory := newFakeFactory()
			factory.status = Status{
				BackendState: test.backend,
				StableNodeID: "node-1",
				DNSName:      "hermes-1.example.ts.net.",
				CertDomains:  []string{"other.example.ts.net", "hermes-1.example.ts.net"},
				Addresses:    []string{"100.64.0.1"},
			}
			var events []Event
			runtime := NewRuntime(t.TempDir(), factory.New)
			if err := runtime.Start(context.Background(), Config{ID: testPortalID, Name: "hermes", Port: 8787}, func(event Event) {
				events = append(events, event)
			}); err != nil {
				t.Fatalf("Start: %v", err)
			}
			event := events[0]
			if event.Status == nil || event.Status.State != test.want {
				t.Fatalf("status = %+v, want %q", event.Status, test.want)
			}
			if test.want == StateOnline {
				if event.Status.AssignedName != "hermes-1" || event.Status.PortalURL != "https://hermes-1.example.ts.net/" {
					t.Fatalf("online status = %+v, want structured assigned name and certificate URL", event.Status)
				}
			}
		})
	}
}

func TestMapStatusUsesAnEmptyAddressArray(t *testing.T) {
	mapped := mapStatus(Status{BackendState: "NeedsLogin"})
	if mapped.Addresses == nil {
		t.Fatal("Addresses = nil, want an empty JSON array")
	}
}

func TestAuthenticateEmitsTransientURLFromFreshNotification(t *testing.T) {
	factory := newFakeFactory()
	events := make(chan Event, 8)
	runtime := NewRuntime(t.TempDir(), factory.New)
	if err := runtime.Start(context.Background(), Config{ID: testPortalID, Name: "hermes", Port: 8787}, func(event Event) {
		events <- event
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer runtime.Close()
	<-events

	if err := runtime.Authenticate(context.Background(), testPortalID); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	factory.node.watcher.send(Notification{AuthURL: "https://login.tailscale.com/a/secret"})
	factory.node.watcher.send(Notification{})
	event := <-events

	if !factory.node.loginRequested {
		t.Fatal("StartLoginInteractive was not called")
	}
	if event.AuthenticationURL != "https://login.tailscale.com/a/secret" {
		t.Fatalf("event = %+v, want transient authentication URL event", event)
	}
}

func TestStartAndCloseAreSerialized(t *testing.T) {
	factory := newFakeFactory()
	factory.node.startEntered = make(chan struct{})
	factory.node.releaseStart = make(chan struct{})
	runtime := NewRuntime(t.TempDir(), factory.New)
	startDone := make(chan error, 1)
	go func() {
		startDone <- runtime.Start(context.Background(), Config{ID: testPortalID, Name: "hermes", Port: 8787}, func(Event) {})
	}()
	<-factory.node.startEntered
	closeDone := make(chan error, 1)
	go func() { closeDone <- runtime.Close() }()

	select {
	case <-closeDone:
		t.Fatal("Close completed while Start was in progress")
	default:
	}
	close(factory.node.releaseStart)
	if err := <-startDone; err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if factory.node.closedBeforeStartReturned {
		t.Fatal("node closed before Start returned")
	}
}

func TestOnlineRuntimeListensTLSAndClosesListenerBeforeNode(t *testing.T) {
	factory := newFakeFactory()
	factory.status = Status{
		BackendState: "Running",
		DNSName:      "hermes.example.ts.net.",
		CertDomains:  []string{"hermes.example.ts.net"},
	}
	runtime := NewRuntime(t.TempDir(), factory.New)
	if err := runtime.Start(context.Background(), Config{ID: testPortalID, Name: "hermes", Port: 8787}, func(Event) {}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if factory.node.listenNetwork != "tcp" || factory.node.listenAddress != ":443" {
		t.Fatalf("ListenTLS = (%q, %q), want (tcp, :443)", factory.node.listenNetwork, factory.node.listenAddress)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !factory.node.listener.closed || !factory.node.listenerClosedBeforeNode {
		t.Fatal("TLS listener was not closed before the tsnet node")
	}
}

func TestRuntimeCloseReturnsListenerFailure(t *testing.T) {
	factory := newFakeFactory()
	factory.status = Status{
		BackendState: "Running",
		DNSName:      "hermes.example.ts.net.",
		CertDomains:  []string{"hermes.example.ts.net"},
	}
	runtime := NewRuntime(t.TempDir(), factory.New)
	if err := runtime.Start(context.Background(), Config{ID: testPortalID, Name: "hermes", Port: 8787}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	factory.node.listener.closeErr = errors.New("close failed")

	if err := runtime.Close(); err == nil {
		t.Fatal("Runtime.Close error = nil, want listener shutdown failure")
	}
	if !factory.node.listenerClosedBeforeNode {
		t.Fatal("tsnet node did not close after the listener failure")
	}
}

func TestRuntimeCloseCancelsActiveRequest(t *testing.T) {
	requestEntered := make(chan struct{})
	handlerExited := make(chan struct{})
	runtime, proxyURL, factory := newOnlineRuntimeWithLocalApp(t, http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(requestEntered)
		<-request.Context().Done()
		close(handlerExited)
	}))
	factory.node.observeClose = func() {
		select {
		case <-handlerExited:
			factory.node.handlersExitedBeforeNode = true
		default:
		}
	}

	requestDone := make(chan error, 1)
	go func() {
		response, err := http.Get(proxyURL)
		if response != nil {
			_ = response.Body.Close()
		}
		requestDone <- err
	}()
	waitForSignal(t, requestEntered, "active Local App request")
	closeDone := make(chan error, 1)
	go func() { closeDone <- runtime.Close() }()
	waitForSignal(t, handlerExited, "active Local App handler exit")
	if err := waitForRuntimeClose(t, closeDone); err != nil {
		t.Fatal(err)
	}
	_ = waitForError(t, requestDone, "client request")
	if !factory.node.handlersExitedBeforeNode {
		t.Fatal("tsnet node closed before the active proxy handler exited")
	}
}

func TestRuntimeCloseClosesIdleConnection(t *testing.T) {
	runtime, proxyURL, factory := newOnlineRuntimeWithLocalApp(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "ok")
	}))
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	response, err := client.Get(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()

	closeDone := make(chan error, 1)
	go func() { closeDone <- runtime.Close() }()
	if err := waitForRuntimeClose(t, closeDone); err != nil {
		t.Fatal(err)
	}
	if response, err = client.Get(proxyURL); err == nil {
		_ = response.Body.Close()
		t.Fatal("idle client connection remained reusable after Runtime.Close")
	}
	if !factory.node.listenerClosedBeforeNode {
		t.Fatal("TLS listener was not closed before the tsnet node")
	}
}

func TestRuntimeCloseClosesWebSocket(t *testing.T) {
	handlerEntered := make(chan struct{})
	handlerExited := make(chan struct{})
	runtime, proxyURL, _ := newOnlineRuntimeWithLocalApp(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := websocket.Accept(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.CloseNow()
		close(handlerEntered)
		_, _, _ = connection.Read(request.Context())
		close(handlerExited)
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(proxyURL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	waitForSignal(t, handlerEntered, "Local App WebSocket handler")
	closeDone := make(chan error, 1)
	go func() { closeDone <- runtime.Close() }()
	waitForSignal(t, handlerExited, "Local App WebSocket handler exit")
	if _, _, err := connection.Read(ctx); err == nil {
		t.Fatal("client WebSocket remained open after Runtime.Close")
	}
	if err := waitForRuntimeClose(t, closeDone); err != nil {
		t.Fatal(err)
	}
}

func newOnlineRuntimeWithLocalApp(t *testing.T, handler http.Handler) (*Runtime, string, *fakeFactory) {
	t.Helper()
	localApp := httptest.NewServer(handler)
	t.Cleanup(localApp.Close)
	port, _ := localAppPort(t, localApp.URL)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	factory := newFakeFactory()
	factory.status = Status{
		BackendState: "Running",
		DNSName:      "hermes.example.ts.net.",
		CertDomains:  []string{"hermes.example.ts.net"},
	}
	factory.node.realListener = listener
	runtime := NewRuntime(t.TempDir(), factory.New)
	if err := runtime.Start(context.Background(), Config{ID: testPortalID, Name: "hermes", Port: uint16(port)}, func(Event) {}); err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime, "http://" + listener.Addr().String(), factory
}

func waitForSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForError(t *testing.T, result <-chan error, description string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
		return nil
	}
}

func waitForRuntimeClose(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(900 * time.Millisecond):
		t.Fatal("Runtime.Close exceeded the native supervisor's one-second grace period")
		return nil
	}
}

type fakeFactory struct {
	mu      sync.Mutex
	created []createdNode
	node    *fakeNode
	status  Status
	ids     map[string]string
}

type createdNode struct {
	dir      string
	hostname string
	nodeID   string
}

func newFakeFactory() *fakeFactory {
	return &fakeFactory{node: &fakeNode{watcher: newFakeWatcher()}, ids: make(map[string]string)}
}

func (f *fakeFactory) New(dir, hostname string) Node {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.ids[dir]
	if id == "" {
		id = "stable-" + testPortalID
		f.ids[dir] = id
	}
	node := f.node
	if len(f.created) > 0 {
		node = &fakeNode{watcher: newFakeWatcher()}
	}
	node.status = f.status
	node.nodeID = id
	f.created = append(f.created, createdNode{dir: dir, hostname: hostname, nodeID: id})
	return node.clone()
}

type fakeNode struct {
	mu                        sync.Mutex
	status                    Status
	nodeID                    string
	watcher                   *fakeWatcher
	loginRequested            bool
	startEntered              chan struct{}
	releaseStart              chan struct{}
	startReturned             bool
	closedBeforeStartReturned bool
	listenNetwork             string
	listenAddress             string
	listener                  *fakeListener
	realListener              net.Listener
	listenerClosedBeforeNode  bool
	observeClose              func()
	handlersExitedBeforeNode  bool
}

func (n *fakeNode) clone() *fakeNode {
	if n.watcher == nil {
		n.watcher = newFakeWatcher()
	}
	return n
}

func (n *fakeNode) Start() error {
	if n.startEntered != nil {
		close(n.startEntered)
		<-n.releaseStart
	}
	n.mu.Lock()
	n.startReturned = true
	n.mu.Unlock()
	return nil
}

func (n *fakeNode) Status(context.Context) (Status, error) { return n.status, nil }
func (n *fakeNode) Watch(context.Context) (Watcher, error) { return n.watcher, nil }
func (n *fakeNode) StartLoginInteractive(context.Context) error {
	n.loginRequested = true
	return nil
}
func (n *fakeNode) ListenTLS(network, address string) (net.Listener, error) {
	n.listenNetwork = network
	n.listenAddress = address
	if n.realListener != nil {
		return n.realListener, nil
	}
	n.listener = newFakeListener()
	return n.listener, nil
}
func (n *fakeNode) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.observeClose != nil {
		n.observeClose()
	}
	n.closedBeforeStartReturned = !n.startReturned
	if n.listener != nil {
		n.listenerClosedBeforeNode = n.listener.closed
	} else if n.realListener != nil {
		connection, err := net.DialTimeout(n.realListener.Addr().Network(), n.realListener.Addr().String(), 50*time.Millisecond)
		if err == nil {
			_ = connection.Close()
		}
		n.listenerClosedBeforeNode = err != nil
	}
	return nil
}

type fakeListener struct {
	mu       sync.Mutex
	closed   bool
	done     chan struct{}
	closeErr error
}

func newFakeListener() *fakeListener { return &fakeListener{done: make(chan struct{})} }
func (l *fakeListener) Accept() (net.Conn, error) {
	<-l.done
	return nil, net.ErrClosed
}
func (l *fakeListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		l.closed = true
		close(l.done)
	}
	return l.closeErr
}
func (l *fakeListener) Addr() net.Addr { return fakeAddr("tailnet") }

type fakeAddr string

func (a fakeAddr) Network() string { return string(a) }
func (a fakeAddr) String() string  { return string(a) }

type fakeWatcher struct {
	ch chan Notification
}

func newFakeWatcher() *fakeWatcher { return &fakeWatcher{ch: make(chan Notification, 8)} }
func (w *fakeWatcher) Next() (Notification, error) {
	notification, ok := <-w.ch
	if !ok {
		return Notification{}, errors.New("closed")
	}
	return notification, nil
}
func (w *fakeWatcher) Close() error                   { close(w.ch); return nil }
func (w *fakeWatcher) send(notification Notification) { w.ch <- notification }
