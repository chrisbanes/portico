package portal

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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
	_ = runtime.Close()
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
	t.Cleanup(func() { _ = runtime.Close() })
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
	t.Cleanup(func() { _ = runtime.Close() })

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
	t.Cleanup(func() { _ = runtime.Close() })

	if err := runtime.CleanupRejectedPortal(testPortalID); err != nil {
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
	if err := runtime.CleanupRejectedPortal(testPortalID); err != nil {
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
	t.Cleanup(func() { _ = runtime.Close() })

	if err := runtime.RemovePortal(strings.ToUpper(testPortalID)); err != nil {
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
	if err := runtime.RemovePortal(testPortalID); err != nil {
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

	if err := runtime.RemovePortal(testPortalID); err != nil {
		t.Fatalf("RemovePortal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, testPortalID)); !os.IsNotExist(err) {
		t.Fatalf("close-time Portal state still exists: %v", err)
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
	t.Cleanup(func() { _ = runtime.Close() })
	if err := os.Symlink(outside, filepath.Join(root, testPortalID)); err != nil {
		t.Fatal(err)
	}

	if err := runtime.RemovePortal(testPortalID); err == nil {
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
	t.Cleanup(func() { _ = runtime.Close() })

	if err := runtime.RemovePortal(testPortalID); err == nil {
		t.Fatal("RemovePortal = nil, want close failure")
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "keep" {
		t.Fatalf("identity state = %q, %v", got, err)
	}
	if err := runtime.Authenticate(context.Background(), testPortalID); err != nil {
		t.Fatalf("runtime ownership was lost: %v", err)
	}
	if err := runtime.RemovePortal(testPortalID); err != nil {
		t.Fatalf("RemovePortal retry: %v", err)
	}
	if _, err := os.Stat(stateDirectory); !os.IsNotExist(err) {
		t.Fatalf("state directory remains after confirmed close: %v", err)
	}
}

func TestRemovePortalTreatsMissingStateRootAsAbsentState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	runtime := NewRuntime(root, func(_, _ string) Node { return &fakeNode{watcher: newFakeWatcher()} })

	if err := runtime.RemovePortal(testPortalID); err != nil {
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

	if err := runtime.RemovePortal(testPortalID); err != nil {
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
		if err := runtime.CleanupRejectedPortal(portalID); err == nil {
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
	go func() { cleanupDone <- runtime.CleanupRejectedPortal(secondPortalID) }()

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
	_ = runtime.Close()
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
		startDone <- reconcileOne(runtime, Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787)}, func(Event) {})
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
	if err := reconcileOne(runtime, Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787)}, func(Event) {}); err != nil {
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
	if err := reconcileOne(runtime, Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(8787)}, func(Event) {}); err != nil {
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
	if err := reconcileOne(runtime, Config{ID: testPortalID, Name: "hermes", Destination: localAppDestination(uint16(port))}, func(Event) {}); err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime, "http://" + listener.Addr().String(), factory
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
	startErr                  error
	closeResults              []error
	closeCalls                int
	loginRequested            bool
	startEntered              chan struct{}
	releaseStart              chan struct{}
	startReturned             bool
	closedBeforeStartReturned bool
	closed                    bool
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
	return n.startErr
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
	n.closeCalls++
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
