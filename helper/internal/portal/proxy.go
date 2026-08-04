package portal

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const proxyShutdownTimeout = 500 * time.Millisecond

func newLoopbackProxy(port int) (http.Handler, error) {
	if port < 1 || port > 65535 {
		return nil, errors.New("invalid Local App port")
	}
	target := loopbackDestination(port)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(target)
			request.SetXForwarded()
			request.Out.Header.Set("X-Forwarded-Proto", "https")
		},
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(writer, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
	}
	return proxy, nil
}

func newDestinationProxy(destination Destination) (http.Handler, error) {
	if err := destination.Validate(); err != nil {
		return nil, err
	}
	if destination.Kind == DestinationLocalApp {
		return newLoopbackProxy(int(destination.Port))
	}
	target := &url.URL{Scheme: destination.Scheme, Host: net.JoinHostPort(destination.Host, strconv.Itoa(int(destination.Port)))}
	transport := &http.Transport{DialContext: safeRemoteDialer(net.DefaultResolver.LookupNetIP)}
	return newRemoteProxy(target, transport), nil
}

func newRemoteProxy(target *url.URL, transport http.RoundTripper) http.Handler {
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(target)
		},
		ErrorLog: log.New(io.Discard, "", 0),
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(writer, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
	}
	return proxy
}

func safeRemoteDialer(resolve func(context.Context, string, string) ([]netip.Addr, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if literal, err := netip.ParseAddr(host); err == nil {
			literal = literal.Unmap()
			if literal.IsLoopback() || literal.IsUnspecified() {
				return nil, errors.New("loopback Remote App destination")
			}
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(literal.String(), port))
		}
		addresses, err := resolve(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		for _, resolved := range addresses {
			resolved = resolved.Unmap()
			if resolved.IsLoopback() || resolved.IsUnspecified() {
				continue
			}
			if connection, err := (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(resolved.String(), port)); err == nil {
				return connection, nil
			}
		}
		return nil, errors.New("Remote App destination is unavailable")
	}
}

func loopbackDestination(port int) *url.URL {
	return &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(port)}
}

type proxyServer struct {
	cancel   context.CancelFunc
	server   *http.Server
	listener net.Listener
	handler  *trackedHandler
	done     chan error
	closeMu  sync.Mutex
	stopOnce sync.Once
	doneOnce sync.Once
	serveErr error
}

func startProxyServer(ctx context.Context, listener net.Listener, handler http.Handler) *proxyServer {
	serveContext, cancel := context.WithCancel(ctx)
	tracked := &trackedHandler{handler: handler, isAccepting: true}
	server := &http.Server{
		Handler: tracked,
		BaseContext: func(net.Listener) context.Context {
			return serveContext
		},
		ErrorLog: log.New(io.Discard, "", 0),
	}
	proxy := &proxyServer{
		cancel:   cancel,
		server:   server,
		listener: listener,
		handler:  tracked,
		done:     make(chan error, 1),
	}
	go func() {
		proxy.done <- server.Serve(listener)
	}()
	return proxy
}

func (p *proxyServer) close() error {
	p.closeMu.Lock()
	defer p.closeMu.Unlock()
	p.stopOnce.Do(func() {
		p.cancel()
		p.handler.stopAccepting()
	})
	shutdownContext, cancel := context.WithTimeout(context.Background(), proxyShutdownTimeout)
	_ = p.server.Shutdown(shutdownContext)
	cancel()
	closeErr := p.server.Close()
	listenerErr := p.listener.Close()
	p.handler.wait()
	p.doneOnce.Do(func() { p.serveErr = <-p.done })
	serveErr := p.serveErr
	p.serveErr = nil
	if errors.Is(closeErr, http.ErrServerClosed) || errors.Is(closeErr, net.ErrClosed) {
		closeErr = nil
	}
	if errors.Is(listenerErr, net.ErrClosed) {
		listenerErr = nil
	}
	if errors.Is(serveErr, http.ErrServerClosed) || errors.Is(serveErr, net.ErrClosed) {
		serveErr = nil
	}
	return errors.Join(closeErr, listenerErr, serveErr)
}

func (p *proxyServer) replaceHandler(handler http.Handler) {
	p.handler.replace(handler)
}

type trackedHandler struct {
	mu          sync.Mutex
	handler     http.Handler
	isAccepting bool
	active      sync.WaitGroup
}

func (h *trackedHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	h.mu.Lock()
	if !h.isAccepting {
		h.mu.Unlock()
		http.Error(writer, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	handler := h.handler
	h.active.Add(1)
	h.mu.Unlock()
	defer h.active.Done()
	handler.ServeHTTP(writer, request)
}

func (h *trackedHandler) replace(handler http.Handler) {
	h.mu.Lock()
	h.handler = handler
	h.mu.Unlock()
}

func (h *trackedHandler) stopAccepting() {
	h.mu.Lock()
	h.isAccepting = false
	h.mu.Unlock()
}

func (h *trackedHandler) wait() {
	h.active.Wait()
}
