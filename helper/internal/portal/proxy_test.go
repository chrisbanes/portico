package portal

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestLoopbackProxyPreservesPublicPathAndQuery(t *testing.T) {
	localApp := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Local-App-Host", request.Host)
		_, _ = io.WriteString(writer, request.URL.RequestURI())
	}))
	defer localApp.Close()
	port, portText := localAppPort(t, localApp.URL)
	proxy, err := newLoopbackProxy(port)
	if err != nil {
		t.Fatalf("newLoopbackProxy: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "https://portal.example.ts.net/path/to/app?hello=world", nil)
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Body.String() != "/path/to/app?hello=world" {
		t.Fatalf("response = (%d, %q), want proxied public path and query", response.Code, response.Body.String())
	}
	wantAuthority := "127.0.0.1:" + portText
	if response.Header().Get("X-Local-App-Host") != wantAuthority {
		t.Fatalf("Local App Host = %q, want %q", response.Header().Get("X-Local-App-Host"), wantAuthority)
	}
}

func TestRemoteAppHTTPProxyUsesConfiguredAuthority(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Remote-Host", request.Host)
		_, _ = io.WriteString(writer, request.URL.RequestURI())
	}))
	defer remote.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(remote.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	target := &url.URL{Scheme: "http", Host: "app.example.com:" + port}
	transport := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(remote.URL, "http://"))
	}}
	proxy := newRemoteProxy(target, transport)
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://portal.example.ts.net/path?ok=yes", nil))
	if response.Code != http.StatusOK || response.Body.String() != "/path?ok=yes" {
		t.Fatalf("response = (%d, %q), want proxied Remote App response", response.Code, response.Body.String())
	}
	if got := response.Header().Get("X-Remote-Host"); got != "app.example.com:"+port {
		t.Fatalf("Remote App authority = %q, want %q", got, "app.example.com:"+port)
	}
}

func TestRemoteAppHTTPSProxyUsesTrustedTestTransport(t *testing.T) {
	remote := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, "trusted TLS")
	}))
	defer remote.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(remote.URL, "https://"))
	if err != nil {
		t.Fatal(err)
	}
	transport := remote.Client().Transport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(remote.URL, "https://"))
	}
	proxy := newRemoteProxy(&url.URL{Scheme: "https", Host: "example.com:" + port}, transport)
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://portal.example.ts.net/", nil))
	if response.Code != http.StatusOK || response.Body.String() != "trusted TLS" {
		t.Fatalf("response = (%d, %q), want trusted HTTPS Remote App", response.Code, response.Body.String())
	}
}

func TestRemoteAppDialerNeverDialsLoopbackResolution(t *testing.T) {
	dial := safeRemoteDialer(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	})
	if connection, err := dial(context.Background(), "tcp", "app.example.com:443"); err == nil || connection != nil {
		t.Fatalf("loopback resolution dial = (%v, %v), want rejection", connection, err)
	}
}

func TestRemoteAppDialerNeverDialsIPv4MappedLoopbackResolution(t *testing.T) {
	dial := safeRemoteDialer(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("::ffff:127.0.0.1")}, nil
	})
	if connection, err := dial(context.Background(), "tcp", "app.example.com:443"); err == nil || connection != nil {
		t.Fatalf("IPv4-mapped loopback resolution dial = (%v, %v), want rejection", connection, err)
	}
}

func TestLoopbackProxyReplacesForwardingHeadersAndAuthority(t *testing.T) {
	received := make(chan *http.Request, 1)
	localApp := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		received <- request.Clone(request.Context())
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer localApp.Close()
	port, portText := localAppPort(t, localApp.URL)
	proxy, err := newLoopbackProxy(port)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "https://portal.example.ts.net/app", nil)
	request.RemoteAddr = "100.64.0.7:54321"
	request.Header.Set("Forwarded", "for=attacker;host=attacker.example;proto=http")
	request.Header.Set("X-Forwarded-For", "192.0.2.1")
	request.Header.Set("X-Forwarded-Host", "attacker.example")
	request.Header.Set("X-Forwarded-Proto", "http")
	proxy.ServeHTTP(httptest.NewRecorder(), request)

	got := <-received
	if got.Host != "127.0.0.1:"+portText {
		t.Fatalf("Local App Host = %q, want loopback authority", got.Host)
	}
	if forwarded := got.Header.Get("Forwarded"); forwarded != "" {
		t.Fatalf("Forwarded = %q, want removed", forwarded)
	}
	if forwardedFor := got.Header.Values("X-Forwarded-For"); len(forwardedFor) != 1 || forwardedFor[0] != "100.64.0.7" {
		t.Fatalf("X-Forwarded-For = %q, want actual client address", forwardedFor)
	}
	if forwardedHost := got.Header.Values("X-Forwarded-Host"); len(forwardedHost) != 1 || forwardedHost[0] != "portal.example.ts.net" {
		t.Fatalf("X-Forwarded-Host = %q, want public host", forwardedHost)
	}
	if forwardedProto := got.Header.Values("X-Forwarded-Proto"); len(forwardedProto) != 1 || forwardedProto[0] != "https" {
		t.Fatalf("X-Forwarded-Proto = %q, want canonical HTTPS scheme", forwardedProto)
	}
}

func TestLoopbackProxyDoesNotLogTrafficSecrets(t *testing.T) {
	const (
		requestSecret  = "request-body-do-not-log"
		responseSecret = "response-body-do-not-log"
		cookieSecret   = "cookie-do-not-log"
		authSecret     = "authorization-do-not-log"
	)
	localApp := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		writer.Header().Set("Set-Cookie", "session="+cookieSecret)
		_, _ = io.WriteString(writer, responseSecret)
	}))
	defer localApp.Close()
	port, _ := localAppPort(t, localApp.URL)

	var logs bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()
	log.SetOutput(&logs)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	})

	proxy, err := newLoopbackProxy(port)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://portal.example.ts.net/", strings.NewReader(requestSecret))
	request.Header.Set("Cookie", "session="+cookieSecret)
	request.Header.Set("Authorization", "Bearer "+authSecret)
	request.Header.Set("Proxy-Authorization", "Bearer "+authSecret)
	proxy.ServeHTTP(httptest.NewRecorder(), request)

	unavailable, err := newLoopbackProxy(1)
	if err != nil {
		t.Fatal(err)
	}
	failedRequest := httptest.NewRequest(http.MethodPost, "https://portal.example.ts.net/", strings.NewReader(requestSecret))
	failedRequest.Header.Set("Cookie", "session="+cookieSecret)
	failedRequest.Header.Set("Authorization", "Bearer "+authSecret)
	unavailable.ServeHTTP(httptest.NewRecorder(), failedRequest)

	for _, secret := range []string{requestSecret, responseSecret, cookieSecret, authSecret} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("traffic secret appeared in logs")
		}
	}
}

func TestLoopbackProxyStreamsImmediately(t *testing.T) {
	testLoopbackProxyImmediateWrite(t, "application/octet-stream", "first chunk\n", true)
}

func TestLoopbackProxyFlushesSSEImmediately(t *testing.T) {
	testLoopbackProxyImmediateWrite(t, "text/event-stream", "data: first\n\n", false)
}

func testLoopbackProxyImmediateWrite(t *testing.T, contentType, firstWrite string, fixedLength bool) {
	t.Helper()
	release := make(chan struct{})
	localApp := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", contentType)
		if fixedLength {
			writer.Header().Set("Content-Length", strconv.Itoa(len(firstWrite)+len("finished\n")))
		}
		_, _ = io.WriteString(writer, firstWrite)
		writer.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(writer, "finished\n")
	}))
	defer localApp.Close()
	proxy := newTestProxyServer(t, localApp.URL)
	defer proxy.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(proxy.URL)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	defer response.Body.Close()
	delivered := make([]byte, len(firstWrite))
	_, err = io.ReadFull(response.Body, delivered)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	if string(delivered) != firstWrite {
		close(release)
		t.Fatalf("first delivery = %q, want response before Local App completion", delivered)
	}
	close(release)
}

func TestLoopbackProxyProxiesWebSocketBidirectionally(t *testing.T) {
	localApp := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := websocket.Accept(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.CloseNow()
		messageType, message, err := connection.Read(request.Context())
		if err != nil {
			return
		}
		_ = connection.Write(request.Context(), messageType, append([]byte("app-to-client:"), message...))
	}))
	defer localApp.Close()
	proxy := newTestProxyServer(t, localApp.URL)
	defer proxy.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(proxy.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	if err := connection.Write(ctx, websocket.MessageText, []byte("client-to-app")); err != nil {
		t.Fatal(err)
	}
	messageType, message, err := connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.MessageText || string(message) != "app-to-client:client-to-app" {
		t.Fatalf("WebSocket response = (%v, %q), want distinct bidirectional messages", messageType, message)
	}
}

func TestLoopbackProxyPropagatesCancellation(t *testing.T) {
	requestEntered := make(chan struct{})
	requestCanceled := make(chan struct{})
	localApp := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(requestEntered)
		<-request.Context().Done()
		close(requestCanceled)
	}))
	defer localApp.Close()
	proxy := newTestProxyServer(t, localApp.URL)
	defer proxy.Close()

	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, proxy.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan error, 1)
	go func() {
		response, err := http.DefaultClient.Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		requestDone <- err
	}()
	select {
	case <-requestEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("Local App request did not start")
	}
	cancel()
	select {
	case <-requestCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("client cancellation did not reach Local App")
	}
	select {
	case err := <-requestDone:
		if err == nil {
			t.Fatal("client request completed without cancellation error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client request did not finish after cancellation")
	}
}

func newTestProxyServer(t *testing.T, localAppURL string) *httptest.Server {
	t.Helper()
	port, _ := localAppPort(t, localAppURL)
	proxy, err := newLoopbackProxy(port)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(proxy)
}

func localAppPort(t *testing.T, localAppURL string) (int, string) {
	t.Helper()
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(localAppURL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return port, portText
}

func TestLoopbackProxyReturnsBadGatewayWhenLocalAppUnavailable(t *testing.T) {
	proxy, err := newLoopbackProxy(1)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://portal.example.ts.net/", nil))
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadGateway)
	}
}

func TestLoopbackProxyAcceptsOnlyPortBoundaries(t *testing.T) {
	for _, port := range []int{1, 65535} {
		proxy, err := newLoopbackProxy(port)
		if err != nil || proxy == nil {
			t.Fatalf("newLoopbackProxy(%d) = (%v, %v), want proxy", port, proxy, err)
		}
	}
	for _, port := range []int{-1, 0, 65536} {
		if proxy, err := newLoopbackProxy(port); err == nil || proxy != nil {
			t.Fatalf("newLoopbackProxy(%d) = (%v, %v), want rejection", port, proxy, err)
		}
	}
}

func TestLoopbackDestinationCannotContainHostSchemePathOrURL(t *testing.T) {
	for _, port := range []int{80, 8787} {
		if got := loopbackDestination(port).String(); got != "http://127.0.0.1:"+strconv.Itoa(port) {
			t.Fatalf("destination = %q, want internally constructed loopback URL", got)
		}
	}
}

func TestProxyCloseCanRetryAfterUnconfirmedListenerClose(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	flaky := &flakyCloseListener{Listener: listener}
	flaky.failuresRemaining.Store(2)
	proxy := startProxyServer(context.Background(), flaky, http.NotFoundHandler())

	if err := proxy.close(); err == nil {
		t.Fatal("first close succeeded, want unconfirmed close failure")
	}
	done := make(chan error, 1)
	go func() { done <- proxy.close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("retry close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retry close blocked after Serve result was already collected")
	}
}

type flakyCloseListener struct {
	net.Listener
	failuresRemaining atomic.Int32
}

func (l *flakyCloseListener) Close() error {
	if l.failuresRemaining.Add(-1) >= 0 {
		return errors.New("injected listener close failure")
	}
	return l.Listener.Close()
}
