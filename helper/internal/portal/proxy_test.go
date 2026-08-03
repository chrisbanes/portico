package portal

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestLoopbackProxyPreservesPublicPathAndQuery(t *testing.T) {
	localApp := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Local-App-Host", request.Host)
		_, _ = io.WriteString(writer, request.URL.RequestURI())
	}))
	defer localApp.Close()
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(localApp.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
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
