package portal

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const testPortalID = "9f55ca93-d7b3-4eab-a871-310ea576005a"
const secondPortalID = "5ea74329-3144-4ba2-925f-138d14d61fcc"

func localAppDestination(port uint16) Destination {
	return Destination{Kind: DestinationLocalApp, Port: port}
}

func remoteAppDestination(t *testing.T, remote *httptest.Server) Destination {
	t.Helper()
	remoteURL, err := url.Parse(remote.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, portText, err := net.SplitHostPort(remoteURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	return Destination{Kind: DestinationRemoteApp, Scheme: remoteURL.Scheme, Host: "app.example.com", Port: uint16(port)}
}

func trustedRemoteProxy(t *testing.T, destination Destination, remote *httptest.Server) (http.Handler, error) {
	t.Helper()
	remoteURL, err := url.Parse(remote.URL)
	if err != nil {
		return nil, err
	}
	transport := remote.Client().Transport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, remoteURL.Host)
	}
	target := &url.URL{
		Scheme: destination.Scheme,
		Host:   net.JoinHostPort(destination.Host, strconv.Itoa(int(destination.Port))),
	}
	return newRemoteProxy(target, transport), nil
}

func TestDestinationRejectsIPv4MappedLoopback(t *testing.T) {
	destination := Destination{
		Kind: DestinationRemoteApp, Scheme: "https", Host: "::ffff:127.0.0.1", Port: 443,
	}
	if err := destination.Validate(); err == nil {
		t.Fatal("Destination.Validate() succeeded for IPv4-mapped loopback")
	}
}

func TestRuntimeReconcileContinuesAfterStartFailureAndRetainsOwnershipUntilCleanup(t *testing.T) {
	root := t.TempDir()
	created := make(map[string][]*fakeNode)
	runtime := NewRuntime(root, func(dir, _ string) Node {
		portalID := filepath.Base(dir)
		node := &fakeNode{watcher: newFakeWatcher(), status: Status{BackendState: "Starting"}}
		if portalID == testPortalID && len(created[portalID]) == 0 {
			node.startErr = errors.New("start failed")
			node.closeResults = []error{errors.New("cleanup unconfirmed"), nil}
		}
		created[portalID] = append(created[portalID], node)
		return node
	})
	desired := []Config{
		{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled},
		{ID: secondPortalID, Name: "atlas", Destination: localAppDestination(8788), DesiredState: DesiredStateEnabled},
	}

	entries, err := runtime.Reconcile(context.Background(), desired, func(Event) {})

	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	want := []ReconcileEntry{
		{PortalID: secondPortalID, Outcome: OutcomeConverged},
		{PortalID: testPortalID, Outcome: OutcomeStartFailed},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("entries = %+v, want %+v", entries, want)
	}
	if len(created[testPortalID]) != 1 || len(created[secondPortalID]) != 1 {
		t.Fatalf("created = %+v, want one owned node per Portal", created)
	}

	entries, err = runtime.Reconcile(context.Background(), desired, func(Event) {})
	if err != nil {
		t.Fatalf("retry Reconcile: %v", err)
	}
	if entries[1].Outcome != OutcomeConverged || len(created[testPortalID]) != 2 {
		t.Fatalf("retry = (%+v, %d nodes), want cleanup then one replacement", entries, len(created[testPortalID]))
	}

	conflicting := append([]Config(nil), desired...)
	conflicting[1].Name = "renamed"
	entries, err = runtime.Reconcile(context.Background(), conflicting, func(Event) {})
	if err != nil {
		t.Fatalf("conflicting Reconcile: %v", err)
	}
	if entries[0].Outcome != OutcomeStartFailed || len(created[secondPortalID]) != 1 {
		t.Fatalf("conflicting immutable name = (%+v, %d nodes), want retained identity", entries, len(created[secondPortalID]))
	}
	_ = runtime.Close(context.Background())
}

func TestRuntimePortEditPreservesIdentityAndDrainsAcceptedHTTPAndWebSocketTraffic(t *testing.T) {
	oldHTTPEntered := make(chan struct{})
	releaseOldHTTP := make(chan struct{})
	var releaseOldHTTPOnce sync.Once
	releaseOld := func() { releaseOldHTTPOnce.Do(func() { close(releaseOldHTTP) }) }
	defer releaseOld()
	t.Cleanup(releaseOld)
	oldLocalApp := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.EqualFold(request.Header.Get("Upgrade"), "websocket") {
			echoWebSocket(t, writer, request, "old:")
			return
		}
		_, _ = io.WriteString(writer, "old-start\n")
		writer.(http.Flusher).Flush()
		close(oldHTTPEntered)
		<-releaseOldHTTP
		_, _ = io.WriteString(writer, "old-end\n")
	}))
	t.Cleanup(oldLocalApp.Close)
	newLocalApp := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.EqualFold(request.Header.Get("Upgrade"), "websocket") {
			echoWebSocket(t, writer, request, "new:")
			return
		}
		_, _ = io.WriteString(writer, "new-http\n")
	}))
	t.Cleanup(newLocalApp.Close)
	oldPort, _ := localAppPort(t, oldLocalApp.URL)
	newPort, _ := localAppPort(t, newLocalApp.URL)

	tailnetListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	factory := newFakeFactory()
	factory.status = Status{
		BackendState: "Running",
		DNSName:      "hermes.example.ts.net.",
		CertDomains:  []string{"hermes.example.ts.net"},
	}
	factory.node.realListener = tailnetListener
	runtime := NewRuntime(t.TempDir(), factory.New)
	desired := []Config{{
		ID: testPortalID, Name: "hermes", Destination: localAppDestination(uint16(oldPort)), DesiredState: DesiredStateEnabled,
	}}
	if entries, reconcileErr := runtime.Reconcile(context.Background(), desired, func(Event) {}); reconcileErr != nil || entries[0].Outcome != OutcomeConverged {
		t.Fatalf("initial Reconcile = (%+v, %v), want converged", entries, reconcileErr)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	proxyURL := "http://" + tailnetListener.Addr().String()

	oldHTTPResponse := make(chan *http.Response, 1)
	go func() {
		response, requestErr := http.Get(proxyURL)
		if requestErr != nil {
			oldHTTPResponse <- nil
			return
		}
		oldHTTPResponse <- response
	}()
	waitForSignal(t, oldHTTPEntered, "old Local App HTTP request")
	response := <-oldHTTPResponse
	if response == nil {
		t.Fatal("old HTTP request failed")
	}
	defer response.Body.Close()
	first := make([]byte, len("old-start\n"))
	if _, err := io.ReadFull(response.Body, first); err != nil || string(first) != "old-start\n" {
		t.Fatalf("old HTTP prefix = (%q, %v)", first, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	oldWebSocket, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(proxyURL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer oldWebSocket.CloseNow()

	desired[0].Destination = localAppDestination(uint16(newPort))
	entries, err := runtime.Reconcile(context.Background(), desired, func(Event) {})
	if err != nil || entries[0].Outcome != OutcomeConverged {
		t.Fatalf("port-edit Reconcile = (%+v, %v), want converged", entries, err)
	}
	if len(factory.created) != 1 || factory.node.realListener != tailnetListener {
		t.Fatalf("factory created %d nodes, want unchanged node and listener", len(factory.created))
	}

	newHTTPResponse, err := http.Get(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	newHTTPBody, _ := io.ReadAll(newHTTPResponse.Body)
	_ = newHTTPResponse.Body.Close()
	if string(newHTTPBody) != "new-http\n" {
		t.Fatalf("new HTTP response = %q, want new Local App", newHTTPBody)
	}
	assertWebSocketEcho(t, ctx, oldWebSocket, "old:existing")
	newWebSocket, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(proxyURL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer newWebSocket.CloseNow()
	assertWebSocketEcho(t, ctx, newWebSocket, "new:fresh")

	releaseOld()
	remainder, err := io.ReadAll(response.Body)
	if err != nil || string(remainder) != "old-end\n" {
		t.Fatalf("old HTTP remainder = (%q, %v), want drained old Local App", remainder, err)
	}
}

func TestRuntimeDestinationReplacementFailureRetainsServingPortalAndCanRetry(t *testing.T) {
	oldLocalApp := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "old")
	}))
	t.Cleanup(oldLocalApp.Close)
	newLocalApp := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "new")
	}))
	t.Cleanup(newLocalApp.Close)
	oldPort, _ := localAppPort(t, oldLocalApp.URL)
	newPort, _ := localAppPort(t, newLocalApp.URL)
	tailnetListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	factory := newFakeFactory()
	factory.status = Status{
		BackendState: "Running",
		DNSName:      "hermes.example.ts.net.",
		CertDomains:  []string{"hermes.example.ts.net"},
	}
	factory.node.realListener = tailnetListener
	runtime := NewRuntime(t.TempDir(), factory.New)
	shouldFail := true
	runtime.proxyForDestination = func(destination Destination) (http.Handler, error) {
		if destination.Port == uint16(newPort) && shouldFail {
			return nil, errors.New("replacement failed")
		}
		return newDestinationProxy(destination)
	}
	desired := []Config{{
		ID: testPortalID, Name: "hermes", Destination: localAppDestination(uint16(oldPort)), DesiredState: DesiredStateEnabled,
	}}
	if entries, reconcileErr := runtime.Reconcile(context.Background(), desired, func(Event) {}); reconcileErr != nil || entries[0].Outcome != OutcomeConverged {
		t.Fatalf("initial Reconcile = (%+v, %v), want converged", entries, reconcileErr)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	proxyURL := "http://" + tailnetListener.Addr().String()

	desired[0].Destination = localAppDestination(uint16(newPort))
	entries, err := runtime.Reconcile(context.Background(), desired, func(Event) {})
	if err != nil || entries[0].Outcome != OutcomeStartFailed {
		t.Fatalf("failed replacement = (%+v, %v), want startFailed", entries, err)
	}
	response, err := http.Get(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(body) != "old" {
		t.Fatalf("old destination response = %q, want old", body)
	}
	if len(factory.created) != 1 || factory.node.realListener != tailnetListener {
		t.Fatal("failed replacement changed Portal node or listener")
	}

	shouldFail = false
	entries, err = runtime.Reconcile(context.Background(), desired, func(Event) {})
	if err != nil || entries[0].Outcome != OutcomeConverged {
		t.Fatalf("replacement retry = (%+v, %v), want converged", entries, err)
	}
	response, err = http.Get(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(body) != "new" {
		t.Fatalf("replacement retry response = %q, want new", body)
	}
}

func TestRuntimeDestinationReplacementReroutesNewRequestsToRemoteTLSOrigin(t *testing.T) {
	oldRemote := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "old TLS")
	}))
	t.Cleanup(oldRemote.Close)
	newRemote := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "new TLS")
	}))
	t.Cleanup(newRemote.Close)
	oldDestination := remoteAppDestination(t, oldRemote)
	newDestination := remoteAppDestination(t, newRemote)
	tailnetListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	factory := newFakeFactory()
	factory.status = Status{
		BackendState: "Running",
		DNSName:      "hermes.example.ts.net.",
		CertDomains:  []string{"hermes.example.ts.net"},
	}
	factory.node.realListener = tailnetListener
	runtime := NewRuntime(t.TempDir(), factory.New)
	runtime.proxyForDestination = func(destination Destination) (http.Handler, error) {
		switch destination.Port {
		case oldDestination.Port:
			return trustedRemoteProxy(t, destination, oldRemote)
		case newDestination.Port:
			return trustedRemoteProxy(t, destination, newRemote)
		default:
			return nil, errors.New("unexpected Remote App destination")
		}
	}
	desired := []Config{{
		ID: testPortalID, Name: "hermes", Destination: oldDestination, DesiredState: DesiredStateEnabled,
	}}
	if entries, reconcileErr := runtime.Reconcile(context.Background(), desired, func(Event) {}); reconcileErr != nil || entries[0].Outcome != OutcomeConverged {
		t.Fatalf("initial Reconcile = (%+v, %v), want converged", entries, reconcileErr)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	proxyURL := "http://" + tailnetListener.Addr().String()

	desired[0].Destination = newDestination
	entries, err := runtime.Reconcile(context.Background(), desired, func(Event) {})
	if err != nil || entries[0].Outcome != OutcomeConverged {
		t.Fatalf("TLS replacement = (%+v, %v), want converged", entries, err)
	}
	response, err := http.Get(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(body) != "new TLS" {
		t.Fatalf("TLS replacement response = %q, want new TLS", body)
	}
	if len(factory.created) != 1 || factory.node.realListener != tailnetListener {
		t.Fatal("TLS replacement changed Portal node or listener")
	}
}

func TestRuntimeReconcileStopsAndOmitsPortalsWithoutDeletingIdentity(t *testing.T) {
	root := t.TempDir()
	nodes := make(map[string]*fakeNode)
	runtime := NewRuntime(root, func(dir, _ string) Node {
		portalID := filepath.Base(dir)
		node := &fakeNode{watcher: newFakeWatcher(), status: Status{BackendState: "Starting"}}
		nodes[portalID] = node
		return node
	})
	desired := []Config{
		{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled},
		{ID: secondPortalID, Name: "atlas", Destination: localAppDestination(8788), DesiredState: DesiredStateEnabled},
	}
	if _, err := runtime.Reconcile(context.Background(), desired, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	for _, portalID := range []string{testPortalID, secondPortalID} {
		if err := os.MkdirAll(filepath.Join(root, portalID), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := runtime.Reconcile(context.Background(), []Config{{
		ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateStopped,
	}}, func(Event) {})

	if err != nil {
		t.Fatal(err)
	}
	want := []ReconcileEntry{
		{PortalID: secondPortalID, Outcome: OutcomeConverged},
		{PortalID: testPortalID, Outcome: OutcomeConverged},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("entries = %+v, want stopped and omitted convergence", entries)
	}
	for portalID, node := range nodes {
		if !node.closed {
			t.Fatalf("Portal %s was not closed", portalID)
		}
		if _, err := os.Stat(filepath.Join(root, portalID)); err != nil {
			t.Fatalf("Portal %s identity was deleted: %v", portalID, err)
		}
	}

	entries, err = runtime.Reconcile(context.Background(), []Config{{
		ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateStopped,
	}}, func(Event) {})
	if err != nil || len(entries) != 1 || entries[0].PortalID != testPortalID || entries[0].Outcome != OutcomeConverged {
		t.Fatalf("already-converged retry = (%+v, %v)", entries, err)
	}
}

func TestRuntimeReconcileRetainsOwnershipAfterCloseFailure(t *testing.T) {
	var created []*fakeNode
	runtime := NewRuntime(t.TempDir(), func(_, _ string) Node {
		node := &fakeNode{
			watcher:      newFakeWatcher(),
			status:       Status{BackendState: "Starting"},
			closeResults: []error{errors.New("close unconfirmed"), nil},
		}
		created = append(created, node)
		return node
	})
	enabled := Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled}
	if _, err := runtime.Reconcile(context.Background(), []Config{enabled}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	stopped := enabled
	stopped.DesiredState = DesiredStateStopped

	entries, err := runtime.Reconcile(context.Background(), []Config{stopped}, func(Event) {})
	if err != nil || entries[0].Outcome != OutcomeCloseFailed || len(created) != 1 {
		t.Fatalf("failed close = (%+v, %v, %d nodes), want retained ownership", entries, err, len(created))
	}
	entries, err = runtime.Reconcile(context.Background(), []Config{stopped}, func(Event) {})
	if err != nil || entries[0].Outcome != OutcomeConverged || len(created) != 1 || created[0].closeCalls != 2 {
		t.Fatalf("close retry = (%+v, %v, %d nodes, %d closes)", entries, err, len(created), created[0].closeCalls)
	}
}

func TestRuntimeReconcileRejectsWholeInvalidSnapshotBeforeMutation(t *testing.T) {
	created := 0
	runtime := NewRuntime(t.TempDir(), func(_, _ string) Node {
		created++
		return &fakeNode{watcher: newFakeWatcher()}
	})
	configs := []Config{
		{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled},
		{ID: secondPortalID, Name: "Atlas", Destination: localAppDestination(8788), DesiredState: DesiredStateEnabled},
	}

	if entries, err := runtime.Reconcile(context.Background(), configs, func(Event) {}); err == nil || entries != nil {
		t.Fatalf("Reconcile = (%+v, %v), want full rejection", entries, err)
	}
	if created != 0 {
		t.Fatalf("created %d nodes before validating the full snapshot", created)
	}
}

func TestRemoteAppDestinationRejectsLoopbackAndNoncanonicalHosts(t *testing.T) {
	for _, destination := range []Destination{
		{Kind: DestinationRemoteApp, Scheme: "https", Host: "localhost", Port: 443},
		{Kind: DestinationRemoteApp, Scheme: "https", Host: "127.0.0.1", Port: 443},
		{Kind: DestinationRemoteApp, Scheme: "https", Host: "[::1]", Port: 443},
		{Kind: DestinationRemoteApp, Scheme: "https", Host: "Example.COM", Port: 443},
	} {
		if err := destination.Validate(); err == nil {
			t.Fatalf("Validate(%+v) = nil, want rejection", destination)
		}
	}
	if err := (Destination{Kind: DestinationRemoteApp, Scheme: "https", Host: "app.example.com", Port: 443}).Validate(); err != nil {
		t.Fatalf("valid Remote App rejected: %v", err)
	}
}

func echoWebSocket(t *testing.T, writer http.ResponseWriter, request *http.Request, prefix string) {
	t.Helper()
	connection, err := websocket.Accept(writer, request, nil)
	if err != nil {
		return
	}
	defer connection.CloseNow()
	messageType, message, err := connection.Read(request.Context())
	if err != nil {
		return
	}
	_ = connection.Write(request.Context(), messageType, append([]byte(prefix), message...))
}

func assertWebSocketEcho(t *testing.T, ctx context.Context, connection *websocket.Conn, expected string) {
	t.Helper()
	message := strings.TrimPrefix(expected, "old:")
	message = strings.TrimPrefix(message, "new:")
	if err := connection.Write(ctx, websocket.MessageText, []byte(message)); err != nil {
		t.Fatal(err)
	}
	_, response, err := connection.Read(ctx)
	if err != nil || string(response) != expected {
		t.Fatalf("WebSocket response = (%q, %v), want %q", response, err, expected)
	}
}

func TestRuntimeKeepsTwoIndependentPortalsOnline(t *testing.T) {
	root := t.TempDir()
	events := make(map[string]StatusEvent)
	factory := func(dir, hostname string) Node {
		id := filepath.Base(dir)
		suffix := "1"
		address := "100.64.0.1"
		if id == secondPortalID {
			suffix = "2"
			address = "100.64.0.2"
		}
		dnsName := hostname + "-" + suffix + ".example.ts.net."
		return &fakeNode{
			watcher: newFakeWatcher(),
			status: Status{
				BackendState: "Running",
				StableNodeID: "node-" + suffix,
				DNSName:      dnsName,
				CertDomains:  []string{strings.TrimSuffix(dnsName, ".")},
				Addresses:    []string{address},
			},
		}
	}
	runtime := NewRuntime(root, factory)
	emit := func(event Event) {
		if event.Status != nil {
			events[event.PortalID] = *event.Status
		}
	}

	configs := []Config{
		{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled},
		{ID: secondPortalID, Name: "atlas", Destination: localAppDestination(8788), DesiredState: DesiredStateEnabled},
	}
	if _, err := runtime.Reconcile(context.Background(), configs, emit); err != nil {
		t.Fatalf("Reconcile Portals: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })

	first, firstOK := events[testPortalID]
	second, secondOK := events[secondPortalID]
	if !firstOK || !secondOK || first.State != StateOnline || second.State != StateOnline {
		t.Fatalf("events = %+v, want both Portals online", events)
	}
	if first.StableNodeID == second.StableNodeID || first.AssignedName == second.AssignedName || first.PortalURL == second.PortalURL || first.Addresses[0] == second.Addresses[0] {
		t.Fatalf("statuses = (%+v, %+v), want independent identities and addresses", first, second)
	}
}

func TestCleanupRejectedPortalClosesAndDeletesOnlyAddressedPortal(t *testing.T) {
	root := t.TempDir()
	nodes := make(map[string]*fakeNode)
	runtime := NewRuntime(root, func(dir, _ string) Node {
		node := &fakeNode{watcher: newFakeWatcher(), status: Status{BackendState: "Starting"}}
		nodes[filepath.Base(dir)] = node
		return node
	})
	configs := []Config{
		{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled},
		{ID: secondPortalID, Name: "atlas", Destination: localAppDestination(8788), DesiredState: DesiredStateEnabled},
	}
	if _, err := runtime.Reconcile(context.Background(), configs, func(Event) {}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, config := range configs {
		if err := os.MkdirAll(filepath.Join(root, config.ID), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })

	if err := runtime.CleanupRejectedPortal(context.Background(), testPortalID); err != nil {
		t.Fatalf("CleanupRejectedPortal: %v", err)
	}
	if !nodes[testPortalID].closed {
		t.Fatal("rejected Portal node was not closed")
	}
	if _, err := os.Stat(filepath.Join(root, testPortalID)); !os.IsNotExist(err) {
		t.Fatalf("rejected Portal state still exists: %v", err)
	}
	if nodes[secondPortalID].closed {
		t.Fatal("unrelated Portal node was closed")
	}
	if _, err := os.Stat(filepath.Join(root, secondPortalID)); err != nil {
		t.Fatalf("unrelated Portal state was affected: %v", err)
	}
	if err := runtime.Authenticate(context.Background(), secondPortalID); err != nil {
		t.Fatalf("unrelated Portal is no longer online: %v", err)
	}
	if err := runtime.CleanupRejectedPortal(context.Background(), testPortalID); err != nil {
		t.Fatalf("repeated cleanup should be idempotent: %v", err)
	}
}

func TestRemovePortalClosesAndDeletesOnlyAddressedPortal(t *testing.T) {
	root := t.TempDir()
	nodes := make(map[string]*fakeNode)
	runtime := NewRuntime(root, func(dir, _ string) Node {
		node := &fakeNode{watcher: newFakeWatcher(), status: Status{BackendState: "Starting"}}
		nodes[filepath.Base(dir)] = node
		return node
	})
	configs := []Config{
		{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled},
		{ID: secondPortalID, Name: "atlas", Destination: localAppDestination(8788), DesiredState: DesiredStateEnabled},
	}
	if _, err := runtime.Reconcile(context.Background(), configs, func(Event) {}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, config := range configs {
		if err := os.MkdirAll(filepath.Join(root, config.ID), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })

	if err := runtime.RemovePortal(context.Background(), strings.ToUpper(testPortalID)); err != nil {
		t.Fatalf("RemovePortal: %v", err)
	}
	if !nodes[testPortalID].closed {
		t.Fatal("removed Portal node was not closed")
	}
	if _, err := os.Stat(filepath.Join(root, testPortalID)); !os.IsNotExist(err) {
		t.Fatalf("removed Portal state still exists: %v", err)
	}
	if nodes[secondPortalID].closed {
		t.Fatal("unrelated Portal node was closed")
	}
	if _, err := os.Stat(filepath.Join(root, secondPortalID)); err != nil {
		t.Fatalf("unrelated Portal state was affected: %v", err)
	}
	if err := runtime.RemovePortal(context.Background(), testPortalID); err != nil {
		t.Fatalf("repeated removal should be idempotent: %v", err)
	}
}

func TestRemovePortalDeletesStateCreatedWhileRuntimeCloses(t *testing.T) {
	root := t.TempDir()
	node := &fakeNode{watcher: newFakeWatcher(), status: Status{BackendState: "Starting"}}
	node.observeClose = func() {
		if err := os.MkdirAll(filepath.Join(root, testPortalID), 0o700); err != nil {
			t.Errorf("create close-time state: %v", err)
		}
	}
	runtime := NewRuntime(root, func(_, _ string) Node { return node })
	if _, err := runtime.Reconcile(context.Background(), []Config{{
		ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled,
	}}, func(Event) {}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if err := runtime.RemovePortal(context.Background(), testPortalID); err != nil {
		t.Fatalf("RemovePortal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, testPortalID)); !os.IsNotExist(err) {
		t.Fatalf("close-time Portal state still exists: %v", err)
	}
}

func TestCleanupDoesNotDeleteStateOfConcurrentSamePortalReplacement(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, testPortalID), 0o700); err != nil {
		t.Fatal(err)
	}
	first := &fakeNode{
		watcher:      newFakeWatcher(),
		status:       Status{BackendState: "Starting"},
		closeEntered: make(chan struct{}),
		releaseClose: make(chan struct{}),
	}
	replacementState := filepath.Join(root, testPortalID, "replacement")
	replacement := &fakeNode{
		watcher: newFakeWatcher(),
		status:  Status{BackendState: "Starting"},
		startHook: func() {
			if err := os.MkdirAll(filepath.Dir(replacementState), 0o700); err != nil {
				t.Errorf("create replacement state directory: %v", err)
				return
			}
			if err := os.WriteFile(replacementState, []byte("new"), 0o600); err != nil {
				t.Errorf("create replacement state: %v", err)
			}
		},
	}
	created := 0
	runtime := NewRuntime(root, func(_, _ string) Node {
		created++
		if created == 1 {
			return first
		}
		return replacement
	})
	config := Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled}
	if err := reconcileOne(runtime, config, func(Event) {}); err != nil {
		t.Fatalf("initial Reconcile: %v", err)
	}

	cleanupDone := make(chan error, 1)
	go func() { cleanupDone <- runtime.RemovePortal(context.Background(), testPortalID) }()
	waitForSignal(t, first.closeEntered, "initial Portal close")
	reconciled := make(chan error, 1)
	go func() { reconciled <- reconcileOne(runtime, config, func(Event) {}) }()
	close(first.releaseClose)
	if err := <-reconciled; err != nil {
		t.Fatalf("replacement Reconcile: %v", err)
	}
	if err := <-cleanupDone; err != nil {
		t.Fatalf("RemovePortal: %v", err)
	}
	if _, err := os.Stat(replacementState); err != nil {
		t.Fatalf("replacement Portal state was deleted: %v", err)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestRuntimeCloseRetainsPortalGateUntilRegistryRemoval(t *testing.T) {
	node := &fakeNode{
		watcher:      newFakeWatcher(),
		status:       Status{BackendState: "Starting"},
		closeEntered: make(chan struct{}),
		releaseClose: make(chan struct{}),
	}
	runtime := NewRuntime(t.TempDir(), func(_, _ string) Node { return node })
	config := Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled}
	if err := reconcileOne(runtime, config, func(Event) {}); err != nil {
		t.Fatalf("initial Reconcile: %v", err)
	}
	portal := runtime.portal(testPortalID)
	if portal == nil {
		t.Fatal("initial Portal was not retained")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- runtime.Close(context.Background()) }()
	waitForSignal(t, node.closeEntered, "Runtime.Close Portal close")
	runtime.mu.Lock()
	close(node.releaseClose)
	acquireContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := portal.acquire(acquireContext); !errors.Is(err, context.DeadlineExceeded) {
		runtime.mu.Unlock()
		t.Fatalf("Portal gate acquire = %v, want it to remain held until registry removal", err)
	}
	runtime.mu.Unlock()
	if err := <-closeDone; err != nil {
		t.Fatalf("Runtime.Close: %v", err)
	}
	if runtime.portal(testPortalID) != nil {
		t.Fatal("Runtime.Close retained a Portal after its confirmed close")
	}
}

func TestRemovePortalDeadlineRetainsGateUntilNodeCloseCompletes(t *testing.T) {
	node := &fakeNode{
		watcher:      newFakeWatcher(),
		status:       Status{BackendState: "Starting"},
		closeEntered: make(chan struct{}),
		releaseClose: make(chan struct{}),
	}
	runtime := NewRuntime(t.TempDir(), func(_, _ string) Node { return node })
	config := Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled}
	if err := reconcileOne(runtime, config, func(Event) {}); err != nil {
		t.Fatalf("initial Reconcile: %v", err)
	}
	portal := runtime.portal(testPortalID)
	if portal == nil {
		t.Fatal("initial Portal was not retained")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	removeDone := make(chan error, 1)
	go func() { removeDone <- runtime.RemovePortal(ctx, testPortalID) }()
	waitForSignal(t, node.closeEntered, "RemovePortal node close")
	if err := <-removeDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RemovePortal = %v, want deadline", err)
	}
	if runtime.portal(testPortalID) != portal {
		t.Fatal("RemovePortal deadline released UUID ownership")
	}
	acquireCtx, acquireCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer acquireCancel()
	if err := portal.acquire(acquireCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Portal gate acquire = %v, want node close to retain the gate", err)
	}

	close(node.releaseClose)
	acquireCtx, acquireCancel = context.WithTimeout(context.Background(), time.Second)
	defer acquireCancel()
	if err := portal.acquire(acquireCtx); err != nil {
		t.Fatalf("Portal gate was not released after node close: %v", err)
	}
	portal.release()
	node.mu.Lock()
	node.closeEntered, node.releaseClose = nil, nil
	node.mu.Unlock()
	if err := runtime.RemovePortal(context.Background(), testPortalID); err != nil {
		t.Fatalf("RemovePortal retry: %v", err)
	}
}

func TestRuntimeEmitsStartupEventAfterReleasingPortalGate(t *testing.T) {
	runtime := NewRuntime(t.TempDir(), func(_, _ string) Node {
		return &fakeNode{watcher: newFakeWatcher(), status: Status{BackendState: "Starting"}}
	})
	config := Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled}
	if _, err := runtime.Reconcile(context.Background(), []Config{config}, func(Event) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if err := runtime.Authenticate(ctx, testPortalID); err != nil {
			t.Errorf("startup event held the Portal gate: %v", err)
		}
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestPortalSuppressesStartupOnlineAfterFailureDelivered(t *testing.T) {
	events := make(chan Event, 2)
	portal := &portalRuntime{
		gate:   make(chan struct{}, 1),
		phase:  portalRunning,
		config: &Config{ID: testPortalID},
		emit:   func(event Event) { events <- event },
	}
	portal.gate <- struct{}{}
	portal.fail()
	portal.emitStartupEvents([]Event{{PortalID: testPortalID, Status: &StatusEvent{State: StateOnline}}})
	event := <-events
	if event.Status == nil || event.Status.State != StateError {
		t.Fatalf("first event = %+v, want StateError", event)
	}
	select {
	case stale := <-events:
		t.Fatalf("stale startup event after failure = %+v", stale)
	default:
	}
}

func TestRemovePortalRejectsUntrustedTargetsWithoutClosingRuntime(t *testing.T) {
	outside := t.TempDir()
	protected := filepath.Join(outside, "protected")
	if err := os.WriteFile(protected, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	node := &fakeNode{watcher: newFakeWatcher(), status: Status{BackendState: "Starting"}}
	runtime := NewRuntime(root, func(_, _ string) Node { return node })
	if err := reconcileOne(runtime, Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787)}, func(Event) {}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	if err := os.Symlink(outside, filepath.Join(root, testPortalID)); err != nil {
		t.Fatal(err)
	}

	if err := runtime.RemovePortal(context.Background(), testPortalID); err == nil {
		t.Fatal("RemovePortal = nil, want symlink rejection")
	}
	if node.closeCalls != 0 {
		t.Fatalf("close calls = %d, want zero before target validation", node.closeCalls)
	}
	if got, err := os.ReadFile(protected); err != nil || string(got) != "keep" {
		t.Fatalf("protected state = %q, %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(root, testPortalID)); err != nil {
		t.Fatalf("rejected symlink was changed: %v", err)
	}
	if err := runtime.Authenticate(context.Background(), testPortalID); err != nil {
		t.Fatalf("runtime ownership was lost: %v", err)
	}
}

func TestRemovePortalPreservesStateAndOwnershipWhenCloseFails(t *testing.T) {
	root := t.TempDir()
	node := &fakeNode{
		watcher:      newFakeWatcher(),
		status:       Status{BackendState: "Starting"},
		closeResults: []error{errors.New("close failed"), nil},
	}
	runtime := NewRuntime(root, func(_, _ string) Node { return node })
	if err := reconcileOne(runtime, Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787)}, func(Event) {}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	stateDirectory := filepath.Join(root, testPortalID)
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(stateDirectory, "identity")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })

	if err := runtime.RemovePortal(context.Background(), testPortalID); err == nil {
		t.Fatal("RemovePortal = nil, want close failure")
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "keep" {
		t.Fatalf("identity state = %q, %v", got, err)
	}
	if err := runtime.Authenticate(context.Background(), testPortalID); err != nil {
		t.Fatalf("runtime ownership was lost: %v", err)
	}
	if err := runtime.RemovePortal(context.Background(), testPortalID); err != nil {
		t.Fatalf("RemovePortal retry: %v", err)
	}
	if _, err := os.Stat(stateDirectory); !os.IsNotExist(err) {
		t.Fatalf("state directory remains after confirmed close: %v", err)
	}
}

func TestRemovePortalTreatsMissingStateRootAsAbsentState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	runtime := NewRuntime(root, func(_, _ string) Node { return &fakeNode{watcher: newFakeWatcher()} })

	if err := runtime.RemovePortal(context.Background(), testPortalID); err != nil {
		t.Fatalf("RemovePortal: %v", err)
	}
}

func TestRemovePortalCanonicalizesSymlinkedTrustedRoot(t *testing.T) {
	parent := t.TempDir()
	canonicalRoot := filepath.Join(parent, "canonical")
	if err := os.Mkdir(canonicalRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	stateDirectory := filepath.Join(canonicalRoot, testPortalID)
	if err := os.Mkdir(stateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "root-link")
	if err := os.Symlink(canonicalRoot, root); err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(root, func(_, _ string) Node { return &fakeNode{watcher: newFakeWatcher()} })

	if err := runtime.RemovePortal(context.Background(), testPortalID); err != nil {
		t.Fatalf("RemovePortal: %v", err)
	}
	if _, err := os.Stat(stateDirectory); !os.IsNotExist(err) {
		t.Fatalf("state directory remains: %v", err)
	}
	if info, err := os.Lstat(root); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("trusted root spelling changed: %v, %v", info, err)
	}
}

func TestCleanupRejectedPortalRejectsUntrustedDeletionTargets(t *testing.T) {
	root := t.TempDir()
	protected := filepath.Join(root, secondPortalID)
	if err := os.MkdirAll(protected, 0o700); err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(root, func(_, _ string) Node { return &fakeNode{watcher: newFakeWatcher()} })

	for _, portalID := range []string{"../" + secondPortalID, "not-a-uuid", testPortalID + "/child"} {
		if err := runtime.CleanupRejectedPortal(context.Background(), portalID); err == nil {
			t.Fatalf("CleanupRejectedPortal(%q) = nil, want error", portalID)
		}
	}
	if _, err := os.Stat(protected); err != nil {
		t.Fatalf("protected state directory was affected: %v", err)
	}
}

func TestCleanupWaitsForStartBeforeDeletingAnyPortalState(t *testing.T) {
	root := t.TempDir()
	blocked := &fakeNode{
		watcher:      newFakeWatcher(),
		startEntered: make(chan struct{}),
		releaseStart: make(chan struct{}),
	}
	runtime := NewRuntime(root, func(_, _ string) Node { return blocked })
	protected := filepath.Join(root, secondPortalID)
	if err := os.MkdirAll(protected, 0o700); err != nil {
		t.Fatal(err)
	}
	startDone := make(chan error, 1)
	go func() {
		startDone <- reconcileOne(runtime, Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787)}, func(Event) {})
	}()
	<-blocked.startEntered
	cleanupDone := make(chan error, 1)
	go func() { cleanupDone <- runtime.CleanupRejectedPortal(context.Background(), secondPortalID) }()

	select {
	case <-cleanupDone:
		t.Fatal("cleanup completed while another Portal start was in progress")
	default:
	}
	if _, err := os.Stat(protected); err != nil {
		t.Fatalf("state was deleted during another Portal start: %v", err)
	}
	close(blocked.releaseStart)
	if err := <-startDone; err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := <-cleanupDone; err != nil {
		t.Fatalf("CleanupRejectedPortal: %v", err)
	}
	_ = runtime.Close(context.Background())
}

func TestRuntimeUsesUUIDStateDirectoryAndStableIdentity(t *testing.T) {
	factory := newFakeFactory()
	root := t.TempDir()
	config := Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787)}

	for range 2 {
		runtime := NewRuntime(root, factory.New)
		if err := reconcileOne(runtime, config, func(Event) {}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := runtime.Close(context.Background()); err != nil {
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
		{ID: "../escape", Name: "hermes", Destination: localAppDestination(8787)},
		{ID: testPortalID, Name: "Hermes", Destination: localAppDestination(8787)},
		{ID: testPortalID, Name: "hermes", Destination: localAppDestination(0)},
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
			if err := reconcileOne(runtime, Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787)}, func(event Event) {
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

func TestMapStatusKeepsOpaqueTailnetNameSeparateFromDisplaySuffix(t *testing.T) {
	mapped := mapStatus(Status{
		BackendState:   "Running",
		TailnetName:    "opaque-identity-do-not-display",
		MagicDNSSuffix: "safe.example.ts.net",
	})

	if mapped.TailnetName != "opaque-identity-do-not-display" || mapped.MagicDNSSuffix != "safe.example.ts.net" {
		t.Fatalf("mapped status = %+v, want exact identity and separate suffix", mapped)
	}
}

func TestAuthenticateEmitsTransientURLFromFreshNotification(t *testing.T) {
	factory := newFakeFactory()
	events := make(chan Event, 8)
	runtime := NewRuntime(t.TempDir(), factory.New)
	if err := reconcileOne(runtime, Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787)}, func(event Event) {
		events <- event
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer runtime.Close(context.Background())
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

func TestAuthenticationCanBringANeedsLoginPortalOnlineAndStartListener(t *testing.T) {
	node := &fakeNode{watcher: newFakeWatcher(), status: Status{BackendState: "NeedsLogin"}}
	runtime := NewRuntime(t.TempDir(), func(_, _ string) Node { return node })
	events := make(chan Event, 4)
	config := Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled}
	if _, err := runtime.Reconcile(context.Background(), []Config{config}, func(event Event) { events <- event }); err != nil {
		t.Fatalf("initial Reconcile: %v", err)
	}
	initial := <-events
	if initial.Status == nil || initial.Status.State != StateAuthenticating {
		t.Fatalf("initial event = %+v, want NeedsLogin authentication state", initial)
	}
	if err := runtime.Authenticate(context.Background(), testPortalID); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	node.status = Status{BackendState: "Running", DNSName: "hermes.example.ts.net.", CertDomains: []string{"hermes.example.ts.net"}}
	node.watcher.send(Notification{AuthURL: "https://login.tailscale.com/a/transient"})
	var authentication, online bool
	for range 2 {
		event := <-events
		authentication = authentication || event.AuthenticationURL != ""
		online = online || event.Status != nil && event.Status.State == StateOnline
	}
	if !authentication || !online {
		t.Fatalf("events did not report authentication then online state: authentication=%v online=%v", authentication, online)
	}
	if node.listenNetwork != "tcp" || node.listenAddress != ":443" {
		t.Fatalf("Listen = (%q, %q), want raw tailnet TCP :443 after Running", node.listenNetwork, node.listenAddress)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestStartupCancellationReachesTSNetReadiness(t *testing.T) {
	node := &fakeNode{
		watcher:    newFakeWatcher(),
		status:     Status{BackendState: "Running", DNSName: "hermes.example.ts.net.", CertDomains: []string{"hermes.example.ts.net"}},
		upEntered:  make(chan struct{}),
		upCanceled: make(chan struct{}),
	}
	runtime := NewRuntime(t.TempDir(), func(_, _ string) Node { return node })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan []ReconcileEntry, 1)
	go func() {
		entries, _ := runtime.Reconcile(ctx, []Config{{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled}}, func(Event) {})
		done <- entries
	}()
	waitForSignal(t, node.upEntered, "tsnet readiness")
	cancel()
	waitForSignal(t, node.upCanceled, "tsnet readiness cancellation")
	entries := <-done
	if len(entries) != 1 || entries[0].Outcome != OutcomeStartFailed {
		t.Fatalf("Reconcile entries = %+v, want cancelled startup failure", entries)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestWatcherFailureEmitsOneErrorAndPreventsFalseConvergence(t *testing.T) {
	failingWatcher := &controlledErrorWatcher{release: make(chan struct{})}
	first := &fakeNode{watcherOverride: failingWatcher, status: Status{BackendState: "Starting"}}
	second := &fakeNode{watcher: newFakeWatcher(), status: Status{BackendState: "Starting"}}
	created := 0
	runtime := NewRuntime(t.TempDir(), func(_, _ string) Node {
		created++
		if created == 1 {
			return first
		}
		return second
	})
	config := Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled}
	events := make(chan Event, 4)
	if _, err := runtime.Reconcile(context.Background(), []Config{config}, func(event Event) { events <- event }); err != nil {
		t.Fatalf("initial Reconcile: %v", err)
	}
	<-events
	close(failingWatcher.release)
	errorEvent := <-events
	if errorEvent.Status == nil || errorEvent.Status.State != StateError {
		t.Fatalf("watcher event = %+v, want one sanitized error", errorEvent)
	}
	entries, err := runtime.Reconcile(context.Background(), []Config{config}, func(Event) {})
	if err != nil || len(entries) != 1 || entries[0].Outcome != OutcomeConverged || created != 2 {
		t.Fatalf("reconcile after watcher failure = (%+v, %v, nodes=%d), want confirmed replacement", entries, err, created)
	}
	select {
	case extra := <-events:
		t.Fatalf("extra watcher failure event = %+v", extra)
	default:
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestWatcherFailureDuringStartupReturnsStartFailedWithoutOnline(t *testing.T) {
	watcher := &controlledErrorWatcher{release: make(chan struct{})}
	node := &fakeNode{
		watcherOverride: watcher,
		status:          Status{BackendState: "Starting"},
		statusEntered:   make(chan struct{}),
		releaseStatus:   make(chan struct{}),
	}
	runtime := NewRuntime(t.TempDir(), func(_, _ string) Node { return node })
	config := Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled}
	events := make(chan Event, 2)
	result := make(chan []ReconcileEntry, 1)
	go func() {
		entries, _ := runtime.Reconcile(context.Background(), []Config{config}, func(event Event) { events <- event })
		result <- entries
	}()
	waitForSignal(t, node.statusEntered, "startup status read")
	close(watcher.release)
	waitForPortalPhase(t, runtime.portal(testPortalID), portalFailed)
	close(node.releaseStatus)
	entries := <-result
	if len(entries) != 1 || entries[0].Outcome != OutcomeStartFailed {
		t.Fatalf("Reconcile entries = %+v, want start failure", entries)
	}
	event := <-events
	if event.Status == nil || event.Status.State != StateError {
		t.Fatalf("watcher failure event = %+v, want one sanitized error", event)
	}
	select {
	case stale := <-events:
		t.Fatalf("unexpected startup event after watcher failure = %+v", stale)
	default:
	}
}

func TestProxyServeFailureEmitsOneErrorAndRecoversOnlyAfterConfirmedClose(t *testing.T) {
	failingListener := newControlledFailureListener()
	first := &fakeNode{
		watcher:          newFakeWatcher(),
		status:           Status{BackendState: "Running", DNSName: "hermes.example.ts.net.", CertDomains: []string{"hermes.example.ts.net"}},
		listenerOverride: failingListener,
		closeResults:     []error{errors.New("node close failed")},
	}
	second := &fakeNode{watcher: newFakeWatcher(), status: Status{BackendState: "Starting"}}
	created := 0
	runtime := NewRuntime(t.TempDir(), func(_, _ string) Node {
		created++
		if created == 1 {
			return first
		}
		return second
	})
	config := Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled}
	events := make(chan Event, 4)
	if _, err := runtime.Reconcile(context.Background(), []Config{config}, func(event Event) { events <- event }); err != nil {
		t.Fatalf("initial Reconcile: %v", err)
	}
	<-events
	close(failingListener.release)
	errorEvent := <-events
	if errorEvent.Status == nil || errorEvent.Status.State != StateError {
		t.Fatalf("proxy Serve failure event = %+v, want one sanitized error", errorEvent)
	}
	entries, err := runtime.Reconcile(context.Background(), []Config{config}, func(Event) {})
	if err != nil || len(entries) != 1 || entries[0].Outcome != OutcomeStartFailed || created != 1 {
		t.Fatalf("reconcile before confirmed close = (%+v, %v, nodes=%d), want retained failure", entries, err, created)
	}
	entries, err = runtime.Reconcile(context.Background(), []Config{config}, func(Event) {})
	if err != nil || len(entries) != 1 || entries[0].Outcome != OutcomeConverged || created != 2 {
		t.Fatalf("reconcile after confirmed close = (%+v, %v, nodes=%d), want replacement", entries, err, created)
	}
	select {
	case extra := <-events:
		t.Fatalf("extra proxy Serve failure event = %+v", extra)
	default:
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestProxyFailureDuringWatcherStatusEmitsOneErrorWithoutStaleOnline(t *testing.T) {
	for _, test := range []struct {
		name                     string
		ignoreStatusCancellation bool
	}{
		{name: "context error"},
		{name: "stale success", ignoreStatusCancellation: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			listener := newControlledFailureListener()
			node := &fakeNode{
				watcher:                  newFakeWatcher(),
				status:                   Status{BackendState: "Running", DNSName: "hermes.example.ts.net.", CertDomains: []string{"hermes.example.ts.net"}},
				listenerOverride:         listener,
				blockStatusCall:          2,
				statusEntered:            make(chan struct{}),
				statusReturned:           make(chan struct{}),
				releaseStatus:            make(chan struct{}),
				ignoreStatusCancellation: test.ignoreStatusCancellation,
			}
			runtime := NewRuntime(t.TempDir(), func(_, _ string) Node { return node })
			config := Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled}
			events := make(chan Event, 3)
			if _, err := runtime.Reconcile(context.Background(), []Config{config}, func(event Event) { events <- event }); err != nil {
				t.Fatalf("initial Reconcile: %v", err)
			}
			<-events
			node.watcher.send(Notification{})
			waitForSignal(t, node.statusEntered, "watcher status read")
			close(listener.release)
			waitForPortalPhase(t, runtime.portal(testPortalID), portalFailed)
			close(node.releaseStatus)
			waitForSignal(t, node.statusReturned, "watcher status completion")
			event := <-events
			if event.Status == nil || event.Status.State != StateError {
				t.Fatalf("proxy failure event = %+v, want one sanitized error", event)
			}
			select {
			case stale := <-events:
				t.Fatalf("stale watcher status after proxy failure = %+v", stale)
			default:
			}
			_ = runtime.Close(context.Background())
		})
	}
}

func TestRuntimeCloseDoesNotEmitErrorForCanceledWatcherStatus(t *testing.T) {
	node := &fakeNode{
		watcher:         newFakeWatcher(),
		status:          Status{BackendState: "Starting"},
		blockStatusCall: 2,
		statusEntered:   make(chan struct{}),
		statusReturned:  make(chan struct{}),
		releaseStatus:   make(chan struct{}),
	}
	runtime := NewRuntime(t.TempDir(), func(_, _ string) Node { return node })
	config := Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled}
	events := make(chan Event, 2)
	if _, err := runtime.Reconcile(context.Background(), []Config{config}, func(event Event) { events <- event }); err != nil {
		t.Fatalf("initial Reconcile: %v", err)
	}
	<-events
	node.watcher.send(Notification{})
	waitForSignal(t, node.statusEntered, "watcher status read")
	closeDone := make(chan error, 1)
	go func() { closeDone <- runtime.Close(context.Background()) }()
	waitForSignal(t, node.statusReturned, "canceled watcher status read")
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case event := <-events:
		t.Fatalf("close emitted watcher event = %+v", event)
	default:
	}
}

func TestRuntimeCloseSuppressesQueuedAuthenticationAfterWatcherCancellation(t *testing.T) {
	node := &fakeNode{
		watcher:         newFakeWatcher(),
		status:          Status{BackendState: "Starting"},
		blockStatusCall: 2,
		statusEntered:   make(chan struct{}),
		statusReturned:  make(chan struct{}),
		releaseStatus:   make(chan struct{}),
	}
	runtime := NewRuntime(t.TempDir(), func(_, _ string) Node { return node })
	config := Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled}
	events := make(chan Event, 3)
	if _, err := runtime.Reconcile(context.Background(), []Config{config}, func(event Event) { events <- event }); err != nil {
		t.Fatalf("initial Reconcile: %v", err)
	}
	<-events
	if err := runtime.Authenticate(context.Background(), testPortalID); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	node.watcher.send(Notification{AuthURL: "https://login.tailscale.com/a/transient"})
	waitForSignal(t, node.statusEntered, "watcher status read")
	closeDone := make(chan error, 1)
	go func() { closeDone <- runtime.Close(context.Background()) }()
	waitForSignal(t, node.statusReturned, "canceled watcher status read")
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case event := <-events:
		t.Fatalf("close emitted queued authentication = %+v", event)
	default:
	}
}

func TestStatusReadFailurePreventsFalseConvergence(t *testing.T) {
	first := &fakeNode{watcher: newFakeWatcher(), status: Status{BackendState: "Starting"}}
	second := &fakeNode{watcher: newFakeWatcher(), status: Status{BackendState: "Starting"}}
	created := 0
	runtime := NewRuntime(t.TempDir(), func(_, _ string) Node {
		created++
		if created == 1 {
			return first
		}
		return second
	})
	config := Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled}
	events := make(chan Event, 3)
	if _, err := runtime.Reconcile(context.Background(), []Config{config}, func(event Event) { events <- event }); err != nil {
		t.Fatal(err)
	}
	<-events
	first.statusErr = errors.New("status failed")
	first.watcher.send(Notification{})
	event := <-events
	if event.Status == nil || event.Status.State != StateError {
		t.Fatalf("status event = %+v, want sanitized error", event)
	}
	entries, err := runtime.Reconcile(context.Background(), []Config{config}, func(Event) {})
	if err != nil || len(entries) != 1 || entries[0].Outcome != OutcomeConverged || created != 2 {
		t.Fatalf("reconcile after status failure = (%+v, %v, nodes=%d), want confirmed replacement", entries, err, created)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStartAndCloseAreSerialized(t *testing.T) {
	factory := newFakeFactory()
	factory.node.startEntered = make(chan struct{})
	factory.node.releaseStart = make(chan struct{})
	runtime := NewRuntime(t.TempDir(), factory.New)
	startDone := make(chan error, 1)
	go func() {
		startDone <- reconcileOne(runtime, Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787)}, func(Event) {})
	}()
	<-factory.node.startEntered
	closeDone := make(chan error, 1)
	go func() { closeDone <- runtime.Close(context.Background()) }()

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

func TestPortalWatcherUsesRuntimeCancellationContext(t *testing.T) {
	node := &fakeNode{watcher: newFakeWatcher(), status: Status{BackendState: "Starting"}}
	runtime := NewRuntime(t.TempDir(), func(_, _ string) Node { return node })
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := runtime.Reconcile(ctx, []Config{{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled}}, func(Event) {}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if node.watchContext == nil {
		t.Fatal("Watch was not called")
	}
	cancel()
	select {
	case <-node.watchContext.Done():
	case <-time.After(time.Second):
		t.Fatal("watch context did not inherit runtime cancellation")
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestPortalFailureDoesNotWaitForLifecycleGate(t *testing.T) {
	portal := &portalRuntime{gate: make(chan struct{}, 1), phase: portalRunning}
	portal.gate <- struct{}{}
	if err := portal.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	failed := make(chan struct{})
	go func() {
		portal.fail()
		close(failed)
	}()
	waitForSignal(t, failed, "background failure while Portal gate is held")
	phase, _, _ := portal.snapshot()
	if phase != portalFailed {
		t.Fatalf("Portal phase = %q, want failed", phase)
	}
	portal.release()
}

func TestRuntimeCloseStartsIndependentPortalsUnderOneDeadline(t *testing.T) {
	first := &fakeNode{watcher: newFakeWatcher(), status: Status{BackendState: "Starting"}, closeEntered: make(chan struct{}), releaseClose: make(chan struct{})}
	second := &fakeNode{watcher: newFakeWatcher(), status: Status{BackendState: "Starting"}, closeEntered: make(chan struct{}), releaseClose: make(chan struct{})}
	created := 0
	runtime := NewRuntime(t.TempDir(), func(_, _ string) Node {
		created++
		if created == 1 {
			return first
		}
		return second
	})
	configs := []Config{{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled}, {ID: secondPortalID, Name: "atlas", Destination: localAppDestination(8788), DesiredState: DesiredStateEnabled}}
	if _, err := runtime.Reconcile(context.Background(), configs, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runtime.Close(ctx) }()
	waitForSignal(t, first.closeEntered, "first Portal close")
	waitForSignal(t, second.closeEntered, "second Portal close")
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close = %v, want shared deadline", err)
	}
	close(first.releaseClose)
	close(second.releaseClose)
}

func TestLateNodeCloseRetainsPortalOwnershipUntilItCompletes(t *testing.T) {
	node := &fakeNode{watcher: newFakeWatcher(), status: Status{BackendState: "Starting"}, closeEntered: make(chan struct{}), releaseClose: make(chan struct{})}
	runtime := NewRuntime(t.TempDir(), func(_, _ string) Node { return node })
	config := Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787), DesiredState: DesiredStateEnabled}
	if _, err := runtime.Reconcile(context.Background(), []Config{config}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := runtime.Close(ctx)
	cancel()
	waitForSignal(t, node.closeEntered, "late node close")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close = %v, want deadline", err)
	}
	if runtime.portal(testPortalID) == nil {
		t.Fatal("late node close released UUID ownership")
	}
	close(node.releaseClose)
}

func TestOnlineRuntimeListensTLSAndClosesListenerBeforeNode(t *testing.T) {
	factory := newFakeFactory()
	factory.status = Status{
		BackendState: "Running",
		DNSName:      "hermes.example.ts.net.",
		CertDomains:  []string{"hermes.example.ts.net"},
	}
	runtime := NewRuntime(t.TempDir(), factory.New)
	if err := reconcileOne(runtime, Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787)}, func(Event) {}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if factory.node.listenNetwork != "tcp" || factory.node.listenAddress != ":443" {
		t.Fatalf("Listen = (%q, %q), want (tcp, :443)", factory.node.listenNetwork, factory.node.listenAddress)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !factory.node.listener.closed || !factory.node.listenerClosedBeforeNode {
		t.Fatal("TLS listener was not closed before the tsnet node")
	}
}

func TestOnlineRuntimeTLSHandshakeUsesNodeCertificateCallback(t *testing.T) {
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
	certificate := testTLSCertificate(t)
	certificateRequested := make(chan struct{}, 1)
	factory.node.realListener = listener
	factory.node.tlsConfig = &tls.Config{GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		certificateRequested <- struct{}{}
		return &certificate, nil
	}}
	runtime := NewRuntime(t.TempDir(), factory.New)
	if err := reconcileOne(runtime, Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787)}, func(Event) {}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	connection, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         "hermes.example.ts.net",
	})
	if err != nil {
		t.Fatalf("TLS handshake: %v", err)
	}
	_ = connection.Close()
	waitForSignal(t, certificateRequested, "node certificate callback")
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
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
	if err := reconcileOne(runtime, Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787)}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	factory.node.listener.closeErr = errors.New("close failed")

	if err := runtime.Close(context.Background()); err == nil {
		t.Fatal("Runtime.Close error = nil, want listener shutdown failure")
	}
	if !factory.node.listenerClosedBeforeNode {
		t.Fatal("tsnet node did not close after the listener failure")
	}
}

func TestRuntimeCloseCancelsActiveRequest(t *testing.T) {
	requestEntered := make(chan struct{})
	cancellationDelivered := make(chan struct{})
	transport := &cancellationTrackingTransport{
		transport: http.DefaultTransport,
		canceled:  cancellationDelivered,
	}
	var cancellationDeliveredBeforeNode bool
	runtime, proxyURL, factory := newOnlineRuntimeWithLocalAppWithProxy(t,
		http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			close(requestEntered)
			<-request.Context().Done()
		}),
		func(destination Destination) (http.Handler, error) {
			return newLoopbackProxyWithTransport(int(destination.Port), transport)
		},
	)
	factory.node.observeClose = func() {
		select {
		case <-cancellationDelivered:
			cancellationDeliveredBeforeNode = true
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
	go func() { closeDone <- runtime.Close(context.Background()) }()
	if err := waitForRuntimeClose(t, closeDone); err != nil {
		t.Fatal(err)
	}
	_ = waitForError(t, requestDone, "client request")
	if !cancellationDeliveredBeforeNode {
		t.Fatal("tsnet node closed before cancellation reached the active Local App proxy request")
	}
}

func TestPortalCloseRetainsProxyUntilDeadlineDrainingCanBeRetried(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	handlerEntered := make(chan struct{})
	releaseHandler := make(chan struct{})
	proxy := startProxyServer(context.Background(), listener, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(handlerEntered)
		<-releaseHandler
	}), nil)
	node := &fakeNode{watcher: newFakeWatcher()}
	portal := &portalRuntime{gate: make(chan struct{}, 1), phase: portalRunning, proxy: proxy, node: node}
	portal.gate <- struct{}{}
	requestDone := make(chan struct{})
	go func() {
		response, _ := http.Get("http://" + listener.Addr().String())
		if response != nil {
			_ = response.Body.Close()
		}
		close(requestDone)
	}()
	waitForSignal(t, handlerEntered, "blocking proxy request")
	closeContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = portal.close(closeContext)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close = %v, want shared deadline", err)
	}
	if portal.proxy != proxy {
		t.Fatal("Portal dropped its proxy after a close deadline")
	}
	if node.closeCalls != 0 {
		t.Fatal("Portal closed the node before proxy draining completed")
	}
	close(releaseHandler)
	if err := portal.close(context.Background()); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
	<-requestDone
	if node.closeCalls != 1 {
		t.Fatalf("node close calls = %d, want one after proxy drain", node.closeCalls)
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
	go func() { closeDone <- runtime.Close(context.Background()) }()
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
	if response, err = http.Get(proxyURL); err == nil {
		_ = response.Body.Close()
		t.Fatal("Remote App proxy accepted a new request after Runtime.Close")
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
	go func() { closeDone <- runtime.Close(context.Background()) }()
	waitForSignal(t, handlerExited, "Local App WebSocket handler exit")
	if _, _, err := connection.Read(ctx); err == nil {
		t.Fatal("client WebSocket remained open after Runtime.Close")
	}
	if err := waitForRuntimeClose(t, closeDone); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeCloseCancelsActiveRemoteAppRequestBeforeNodeClose(t *testing.T) {
	requestEntered := make(chan struct{})
	cancellationDelivered := make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(requestEntered)
		<-request.Context().Done()
	}))
	t.Cleanup(remote.Close)
	remoteURL, err := url.Parse(remote.URL)
	if err != nil {
		t.Fatal(err)
	}
	baseTransport := http.DefaultTransport.(*http.Transport).Clone()
	baseTransport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, remoteURL.Host)
	}
	transport := &cancellationTrackingTransport{
		transport: baseTransport,
		canceled:  cancellationDelivered,
	}
	var cancellationDeliveredBeforeNode bool
	runtime, proxyURL, factory, _ := newOnlineRuntimeWithRemoteAppWithProxy(t, remote, func(Event) {}, func(destination Destination) (http.Handler, error) {
		target := &url.URL{
			Scheme: destination.Scheme,
			Host:   net.JoinHostPort(destination.Host, strconv.Itoa(int(destination.Port))),
		}
		return newRemoteProxy(target, transport), nil
	})
	factory.node.observeClose = func() {
		select {
		case <-cancellationDelivered:
			cancellationDeliveredBeforeNode = true
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
	waitForSignal(t, requestEntered, "active Remote App request")
	closeDone := make(chan error, 1)
	go func() { closeDone <- runtime.Close(context.Background()) }()
	if err := waitForRuntimeClose(t, closeDone); err != nil {
		t.Fatal(err)
	}
	_ = waitForError(t, requestDone, "Remote App client request")
	if !cancellationDeliveredBeforeNode {
		t.Fatal("tsnet node closed before cancellation reached the active Remote App proxy request")
	}
}

func TestRuntimeCloseClosesIdleRemoteAppTransport(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "ok")
	}))
	t.Cleanup(remote.Close)
	runtime, proxyURL, factory, transport := newOnlineRuntimeWithRemoteApp(t, remote, func(Event) {})
	response, err := http.Get(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()

	if err := runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !transport.closed.Load() {
		t.Fatal("Remote App idle transport connections were not closed")
	}
	if !factory.node.listenerClosedBeforeNode {
		t.Fatal("TLS listener was not closed before the tsnet node")
	}
}

func TestRemoteAppDestinationFailureDoesNotPublishPortalHealth(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		connection, _, err := writer.(http.Hijacker).Hijack()
		if err == nil {
			_ = connection.Close()
		}
	}))
	t.Cleanup(remote.Close)
	events := make([]Event, 0, 1)
	_, proxyURL, _, _ := newOnlineRuntimeWithRemoteApp(t, remote, func(event Event) {
		events = append(events, event)
	})
	if len(events) != 1 || events[0].Status == nil || events[0].Status.State != StateOnline {
		t.Fatalf("initial events = %+v, want only Online status", events)
	}

	response, err := http.Get(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want generic bad gateway", response.StatusCode)
	}
	if len(events) != 1 {
		t.Fatalf("events after destination failure = %+v, want no health event", events)
	}
}

func TestRuntimeCloseClosesTrustedHTTPSRemoteAppWebSocket(t *testing.T) {
	handlerEntered := make(chan struct{})
	handlerExited := make(chan struct{})
	remote := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := websocket.Accept(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.CloseNow()
		close(handlerEntered)
		_, _, _ = connection.Read(request.Context())
		close(handlerExited)
	}))
	t.Cleanup(remote.Close)
	runtime, proxyURL, _, _ := newOnlineRuntimeWithRemoteApp(t, remote, func(Event) {})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(proxyURL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	waitForSignal(t, handlerEntered, "trusted HTTPS Remote App WebSocket handler")
	closeDone := make(chan error, 1)
	go func() { closeDone <- runtime.Close(context.Background()) }()
	waitForSignal(t, handlerExited, "trusted HTTPS Remote App WebSocket handler exit")
	if _, _, err := connection.Read(ctx); err == nil {
		t.Fatal("client WebSocket remained open after Runtime.Close")
	}
	if err := waitForRuntimeClose(t, closeDone); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteAppPortalsKeepTrafficAndFailuresIsolated(t *testing.T) {
	firstRemote := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/fail" {
			connection, _, err := writer.(http.Hijacker).Hijack()
			if err == nil {
				_ = connection.Close()
			}
			return
		}
		_, _ = io.WriteString(writer, "first")
	}))
	t.Cleanup(firstRemote.Close)
	secondRemote := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "second")
	}))
	t.Cleanup(secondRemote.Close)
	firstListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	secondListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = firstListener.Close()
		t.Fatal(err)
	}
	firstDestination := remoteAppDestination(t, firstRemote)
	secondDestination := remoteAppDestination(t, secondRemote)
	listeners := map[string]net.Listener{testPortalID: firstListener, secondPortalID: secondListener}
	remotes := map[uint16]*httptest.Server{
		firstDestination.Port:  firstRemote,
		secondDestination.Port: secondRemote,
	}
	runtime := NewRuntime(t.TempDir(), func(dir, _ string) Node {
		portalID := filepath.Base(dir)
		return &fakeNode{
			watcher:      newFakeWatcher(),
			realListener: listeners[portalID],
			status: Status{
				BackendState: "Running",
				DNSName:      "hermes.example.ts.net.",
				CertDomains:  []string{"hermes.example.ts.net"},
			},
		}
	})
	runtime.proxyForDestination = func(destination Destination) (http.Handler, error) {
		remote := remotes[destination.Port]
		remoteURL, err := url.Parse(remote.URL)
		if err != nil {
			return nil, err
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, remoteURL.Host)
		}
		target := &url.URL{
			Scheme: destination.Scheme,
			Host:   net.JoinHostPort(destination.Host, strconv.Itoa(int(destination.Port))),
		}
		return newRemoteProxy(target, transport), nil
	}
	events := make([]Event, 0, 2)
	entries, err := runtime.Reconcile(context.Background(), []Config{
		{
			ID: testPortalID, Name: "hermes", Destination: firstDestination, DesiredState: DesiredStateEnabled,
		},
		{
			ID: secondPortalID, Name: "atlas", Destination: secondDestination, DesiredState: DesiredStateEnabled,
		},
	}, func(event Event) {
		events = append(events, event)
	})
	if err != nil || len(entries) != 2 ||
		entries[0].Outcome != OutcomeConverged || entries[1].Outcome != OutcomeConverged {
		t.Fatalf("Reconcile = (%+v, %v), want both Remote Apps online", entries, err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })

	assertRemoteAppResponse(t, "http://"+firstListener.Addr().String(), "/", http.StatusOK, "first")
	assertRemoteAppResponse(t, "http://"+secondListener.Addr().String(), "/", http.StatusOK, "second")
	assertRemoteAppResponse(t, "http://"+firstListener.Addr().String(), "/fail", http.StatusBadGateway, "Bad Gateway\n")
	assertRemoteAppResponse(t, "http://"+secondListener.Addr().String(), "/", http.StatusOK, "second")
	if len(events) != 2 {
		t.Fatalf("events after first Remote App failure = %+v, want only initial Online states", events)
	}
}

func assertRemoteAppResponse(t *testing.T, baseURL, path string, wantStatus int, wantBody string) {
	t.Helper()
	response, err := http.Get(baseURL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != wantStatus || string(body) != wantBody {
		t.Fatalf("response = (%d, %q, %v), want (%d, %q)", response.StatusCode, body, err, wantStatus, wantBody)
	}
}

func newOnlineRuntimeWithLocalApp(t *testing.T, handler http.Handler) (*Runtime, string, *fakeFactory) {
	return newOnlineRuntimeWithLocalAppWithProxy(t, handler, nil)
}

func newOnlineRuntimeWithLocalAppWithProxy(
	t *testing.T,
	handler http.Handler,
	proxyForDestination func(Destination) (http.Handler, error),
) (*Runtime, string, *fakeFactory) {
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
	if proxyForDestination != nil {
		runtime.proxyForDestination = proxyForDestination
	}
	if err := reconcileOne(runtime, Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(uint16(port))}, func(Event) {}); err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	return runtime, "http://" + listener.Addr().String(), factory
}

func newOnlineRuntimeWithRemoteApp(
	t *testing.T,
	remote *httptest.Server,
	emit func(Event),
) (*Runtime, string, *fakeFactory, *closeTrackingTransport) {
	return newOnlineRuntimeWithRemoteAppWithProxy(t, remote, emit, nil)
}

func newOnlineRuntimeWithRemoteAppWithProxy(
	t *testing.T,
	remote *httptest.Server,
	emit func(Event),
	proxyForDestination func(Destination) (http.Handler, error),
) (*Runtime, string, *fakeFactory, *closeTrackingTransport) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	remoteURL, err := url.Parse(remote.URL)
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if remoteURL.Scheme == "https" {
		transport = remote.Client().Transport.(*http.Transport).Clone()
	}
	trackedTransport := &closeTrackingTransport{Transport: transport}
	trackedTransport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, remoteURL.Host)
	}
	factory := newFakeFactory()
	factory.status = Status{
		BackendState: "Running",
		DNSName:      "hermes.example.ts.net.",
		CertDomains:  []string{"hermes.example.ts.net"},
	}
	factory.node.realListener = listener
	runtime := NewRuntime(t.TempDir(), factory.New)
	runtime.proxyForDestination = func(destination Destination) (http.Handler, error) {
		target := &url.URL{
			Scheme: destination.Scheme,
			Host:   net.JoinHostPort(destination.Host, strconv.Itoa(int(destination.Port))),
		}
		return newRemoteProxy(target, trackedTransport), nil
	}
	if proxyForDestination != nil {
		runtime.proxyForDestination = proxyForDestination
	}
	if err := reconcileOne(runtime, Config{
		ID:          testPortalID,
		Name:        "hermes",
		Destination: remoteAppDestination(t, remote),
	}, emit); err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	return runtime, "http://" + listener.Addr().String(), factory, trackedTransport
}

func reconcileOne(runtime *Runtime, config Config, emit func(Event)) error {
	config.DesiredState = DesiredStateEnabled
	entries, err := runtime.Reconcile(context.Background(), []Config{config}, emit)
	if err != nil {
		return err
	}
	if len(entries) != 1 || entries[0].Outcome != OutcomeConverged {
		return errors.New("portal did not converge")
	}
	return nil
}

func waitForSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForPortalPhase(t *testing.T, portal *portalRuntime, want portalPhase) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		phase, _, _ := portal.snapshot()
		if phase == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("Portal phase = %q, want %q", phase, want)
		default:
		}
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
	statusErr                 error
	statusCalls               int
	blockStatusCall           int
	statusEntered             chan struct{}
	statusReturned            chan struct{}
	releaseStatus             chan struct{}
	ignoreStatusCancellation  bool
	nodeID                    string
	watcher                   *fakeWatcher
	watcherOverride           Watcher
	watchContext              context.Context
	startErr                  error
	closeResults              []error
	closeCalls                int
	loginRequested            bool
	startEntered              chan struct{}
	releaseStart              chan struct{}
	startHook                 func()
	upEntered                 chan struct{}
	upCanceled                chan struct{}
	startReturned             bool
	closedBeforeStartReturned bool
	closed                    bool
	listenNetwork             string
	listenAddress             string
	listener                  *fakeListener
	realListener              net.Listener
	listenerOverride          net.Listener
	tlsConfig                 *tls.Config
	listenerClosedBeforeNode  bool
	observeClose              func()
	closeEntered              chan struct{}
	releaseClose              chan struct{}
}

func (n *fakeNode) clone() *fakeNode {
	if n.watcher == nil {
		n.watcher = newFakeWatcher()
	}
	return n
}

func (n *fakeNode) Start() error {
	if n.startHook != nil {
		n.startHook()
	}
	if n.startEntered != nil {
		close(n.startEntered)
		<-n.releaseStart
	}
	n.mu.Lock()
	n.startReturned = true
	n.mu.Unlock()
	return n.startErr
}

func (n *fakeNode) Status(ctx context.Context) (Status, error) {
	n.mu.Lock()
	n.statusCalls++
	block := n.blockStatusCall == 0 || n.statusCalls == n.blockStatusCall
	n.mu.Unlock()
	if n.statusEntered != nil && block {
		close(n.statusEntered)
		if n.statusReturned != nil {
			defer close(n.statusReturned)
		}
		if n.ignoreStatusCancellation {
			<-n.releaseStatus
			return n.status, n.statusErr
		}
		select {
		case <-n.releaseStatus:
		case <-ctx.Done():
			return Status{}, ctx.Err()
		}
	}
	return n.status, n.statusErr
}
func (n *fakeNode) Watch(ctx context.Context) (Watcher, error) {
	n.watchContext = ctx
	if n.watcherOverride != nil {
		return n.watcherOverride, nil
	}
	return n.watcher, nil
}
func (n *fakeNode) StartLoginInteractive(context.Context) error {
	n.loginRequested = true
	return nil
}
func (n *fakeNode) Up(ctx context.Context) (Status, error) {
	if n.upEntered != nil {
		close(n.upEntered)
		<-ctx.Done()
		close(n.upCanceled)
		return Status{}, ctx.Err()
	}
	return n.status, nil
}
func (n *fakeNode) Listen(network, address string) (net.Listener, error) {
	n.listenNetwork = network
	n.listenAddress = address
	if n.listenerOverride != nil {
		return n.listenerOverride, nil
	}
	if n.realListener != nil {
		return n.realListener, nil
	}
	n.listener = newFakeListener()
	return n.listener, nil
}
func (n *fakeNode) TLSConfig() *tls.Config { return n.tlsConfig }
func (n *fakeNode) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.closeCalls++
	if n.closeEntered != nil {
		close(n.closeEntered)
		<-n.releaseClose
	}
	if n.observeClose != nil {
		n.observeClose()
	}
	n.closedBeforeStartReturned = !n.startReturned
	n.closed = true
	if n.listener != nil {
		n.listenerClosedBeforeNode = n.listener.closed
	} else if n.realListener != nil {
		connection, err := net.DialTimeout(n.realListener.Addr().Network(), n.realListener.Addr().String(), 50*time.Millisecond)
		if err == nil {
			_ = connection.Close()
		}
		n.listenerClosedBeforeNode = err != nil
	}
	if n.closeCalls <= len(n.closeResults) {
		return n.closeResults[n.closeCalls-1]
	}
	return nil
}

func testTLSCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Minute),
		DNSNames:     []string{"hermes.example.ts.net"},
	}, &x509.Certificate{}, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: privateKey}
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

type controlledFailureListener struct {
	release chan struct{}
	closed  chan struct{}
	close   sync.Once
}

func newControlledFailureListener() *controlledFailureListener {
	return &controlledFailureListener{release: make(chan struct{}), closed: make(chan struct{})}
}

func (l *controlledFailureListener) Accept() (net.Conn, error) {
	select {
	case <-l.release:
		return nil, errors.New("tailnet listener failed")
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *controlledFailureListener) Close() error {
	l.close.Do(func() { close(l.closed) })
	return nil
}

func (*controlledFailureListener) Addr() net.Addr { return fakeAddr("tailnet") }

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

type controlledErrorWatcher struct{ release chan struct{} }

func (w *controlledErrorWatcher) Next() (Notification, error) {
	<-w.release
	return Notification{}, errors.New("watch failed")
}

func (*controlledErrorWatcher) Close() error { return nil }

type cancellationTrackingTransport struct {
	transport  http.RoundTripper
	canceled   chan struct{}
	cancelOnce sync.Once
}

func (t *cancellationTrackingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.transport.RoundTrip(request)
	select {
	case <-request.Context().Done():
		t.cancelOnce.Do(func() { close(t.canceled) })
	default:
	}
	return response, err
}

func (t *cancellationTrackingTransport) CloseIdleConnections() {
	if transport, ok := t.transport.(interface{ CloseIdleConnections() }); ok {
		transport.CloseIdleConnections()
	}
}
