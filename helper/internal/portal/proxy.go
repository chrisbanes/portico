package portal

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	proxyReadHeaderTimeout      = 10 * time.Second
	proxyIdleTimeout            = 60 * time.Second
	remoteSetupTimeout          = 30 * time.Second
	remoteTLSHandshakeTimeout   = 10 * time.Second
	remoteResponseHeaderTimeout = 30 * time.Second
	remoteIdleConnTimeout       = 90 * time.Second
	remoteExpectContinueTimeout = time.Second
)

func newLoopbackProxy(port int) (http.Handler, error) {
	return newLoopbackProxyWithTransport(port, nil)
}

func newLoopbackProxyWithTransport(port int, transport http.RoundTripper) (http.Handler, error) {
	if port < 1 || port > 65535 {
		return nil, errors.New("invalid Local App port")
	}
	target := loopbackDestination(port)
	proxy := &httputil.ReverseProxy{
		Transport: transport,
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
	dialer := &net.Dialer{}
	transport := newRemoteAppTransport(remoteTransportDependencies{
		resolve: net.DefaultResolver.LookupNetIP,
		dial:    dialer.DialContext,
	})
	return newRemoteProxy(target, transport), nil
}

func newRemoteProxy(target *url.URL, transport http.RoundTripper) http.Handler {
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(target)
			request.SetXForwarded()
			request.Out.Header.Set("X-Forwarded-Proto", "https")
			request.Out = request.Out.WithContext(context.WithValue(
				request.Out.Context(),
				remoteDialRequestContextKey{},
				request.In.Context(),
			))
		},
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(writer, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
	}
	return &remoteProxy{proxy: proxy, transport: transport}
}

type remoteProxy struct {
	proxy     *httputil.ReverseProxy
	transport http.RoundTripper
}

func (p *remoteProxy) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	p.proxy.ServeHTTP(writer, request)
}

func (p *remoteProxy) CloseIdleConnections() {
	if transport, ok := p.transport.(interface{ CloseIdleConnections() }); ok {
		transport.CloseIdleConnections()
	}
}

type remoteDialRequestContextKey struct{}

type remoteTransportDependencies struct {
	resolve               func(context.Context, string, string) ([]netip.Addr, error)
	dial                  func(context.Context, string, string) (net.Conn, error)
	setupTimeout          time.Duration
	tlsHandshakeTimeout   time.Duration
	responseHeaderTimeout time.Duration
}

type remoteAppTransport struct {
	ordinary *http.Transport
	upgrade  *http.Transport
}

func newRemoteAppTransport(dependencies remoteTransportDependencies) *remoteAppTransport {
	dependencies = normalizedRemoteTransportDependencies(dependencies)
	return &remoteAppTransport{
		ordinary: newRemoteHTTPTransport(dependencies, false),
		upgrade:  newRemoteHTTPTransport(dependencies, true),
	}
}

func newRemoteHTTPTransport(dependencies remoteTransportDependencies, onlyHTTP1 bool) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = safeRemoteDialerWithDependencies(dependencies)
	transport.DialTLSContext = safeRemoteTLSDialer(transport, dependencies)
	transport.TLSHandshakeTimeout = remoteTLSHandshakeTimeout
	transport.ResponseHeaderTimeout = dependencies.responseHeaderTimeout
	transport.IdleConnTimeout = remoteIdleConnTimeout
	transport.ExpectContinueTimeout = remoteExpectContinueTimeout
	if onlyHTTP1 {
		protocols := new(http.Protocols)
		protocols.SetHTTP1(true)
		transport.Protocols = protocols
	}
	return transport
}

func (t *remoteAppTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if isUpgradeRequest(request) {
		return t.upgrade.RoundTrip(request)
	}
	return t.ordinary.RoundTrip(request)
}

func (t *remoteAppTransport) CloseIdleConnections() {
	t.ordinary.CloseIdleConnections()
	t.upgrade.CloseIdleConnections()
}

func safeRemoteDialer(resolve func(context.Context, string, string) ([]netip.Addr, error)) func(context.Context, string, string) (net.Conn, error) {
	return safeRemoteDialerWithDependencies(remoteTransportDependencies{resolve: resolve})
}

func safeRemoteDialerWithDependencies(dependencies remoteTransportDependencies) func(context.Context, string, string) (net.Conn, error) {
	dependencies = normalizedRemoteTransportDependencies(dependencies)
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		setupContext, stopSetup, setupError := newRemoteSetupContext(ctx, dependencies.setupTimeout)
		defer stopSetup()
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if literal, err := netip.ParseAddr(host); err == nil {
			literal = literal.Unmap()
			if literal.IsLoopback() || literal.IsUnspecified() {
				return nil, errors.New("loopback Remote App destination")
			}
			connection, err := dependencies.dial(setupContext, network, net.JoinHostPort(literal.String(), port))
			if contextErr := setupError(); contextErr != nil {
				closeConnection(connection)
				return nil, contextErr
			}
			return connection, err
		}
		addresses, err := dependencies.resolve(setupContext, "ip", host)
		if err != nil {
			if contextErr := setupError(); contextErr != nil {
				return nil, contextErr
			}
			return nil, err
		}
		for _, resolved := range addresses {
			if contextErr := setupError(); contextErr != nil {
				return nil, contextErr
			}
			resolved = resolved.Unmap()
			if resolved.IsLoopback() || resolved.IsUnspecified() {
				continue
			}
			connection, err := dependencies.dial(setupContext, network, net.JoinHostPort(resolved.String(), port))
			if contextErr := setupError(); contextErr != nil {
				closeConnection(connection)
				return nil, contextErr
			}
			if err == nil {
				return connection, nil
			}
		}
		return nil, errors.New("Remote App destination is unavailable")
	}
}

func normalizedRemoteTransportDependencies(dependencies remoteTransportDependencies) remoteTransportDependencies {
	if dependencies.resolve == nil {
		dependencies.resolve = net.DefaultResolver.LookupNetIP
	}
	if dependencies.dial == nil {
		dependencies.dial = (&net.Dialer{}).DialContext
	}
	if dependencies.setupTimeout == 0 {
		dependencies.setupTimeout = remoteSetupTimeout
	}
	if dependencies.tlsHandshakeTimeout == 0 {
		dependencies.tlsHandshakeTimeout = remoteTLSHandshakeTimeout
	}
	if dependencies.responseHeaderTimeout == 0 {
		dependencies.responseHeaderTimeout = remoteResponseHeaderTimeout
	}
	return dependencies
}

func newRemoteSetupContext(transportContext context.Context, timeout time.Duration) (context.Context, func(), func() error) {
	setupContext, cancel := context.WithTimeout(transportContext, timeout)
	requestContext, _ := transportContext.Value(remoteDialRequestContextKey{}).(context.Context)
	stopRequestCancellation := func() bool { return true }
	if requestContext != nil {
		stopRequestCancellation = context.AfterFunc(requestContext, cancel)
	}
	stop := func() {
		stopRequestCancellation()
		cancel()
	}
	setupError := func() error {
		if requestContext != nil && requestContext.Err() != nil {
			return requestContext.Err()
		}
		if transportContext.Err() != nil {
			return transportContext.Err()
		}
		return setupContext.Err()
	}
	return setupContext, stop, setupError
}

func safeRemoteTLSDialer(transport *http.Transport, dependencies remoteTransportDependencies) func(context.Context, string, string) (net.Conn, error) {
	dial := safeRemoteDialerWithDependencies(dependencies)
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		rawConnection, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		if rawConnection == nil {
			return nil, errors.New("Remote App TLS dial returned no connection")
		}
		handedOff := false
		defer func() {
			if !handedOff {
				_ = rawConnection.Close()
			}
		}()

		config, err := remoteTLSConfig(transport, address)
		if err != nil {
			return nil, err
		}
		handshakeContext, stopHandshake, handshakeError := newRemoteSetupContext(ctx, dependencies.tlsHandshakeTimeout)
		defer stopHandshake()
		closeOnCancellation := context.AfterFunc(handshakeContext, func() {
			_ = rawConnection.Close()
		})
		defer closeOnCancellation()

		tlsConnection := tls.Client(rawConnection, config)
		if err := tlsConnection.HandshakeContext(handshakeContext); err != nil {
			if contextErr := handshakeError(); contextErr != nil {
				return nil, contextErr
			}
			return nil, err
		}
		if contextErr := handshakeError(); contextErr != nil {
			return nil, contextErr
		}
		if !closeOnCancellation() {
			if contextErr := handshakeError(); contextErr != nil {
				return nil, contextErr
			}
			return nil, context.Canceled
		}
		stopHandshake()
		handedOff = true
		return tlsConnection, nil
	}
}

func remoteTLSConfig(transport *http.Transport, address string) (*tls.Config, error) {
	config := transport.TLSClientConfig
	if config == nil {
		config = &tls.Config{}
	} else {
		config = config.Clone()
	}
	if config.ServerName == "" {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		config.ServerName = host
	}
	return config, nil
}

func closeConnection(connection net.Conn) {
	if connection != nil {
		_ = connection.Close()
	}
}

func isUpgradeRequest(request *http.Request) bool {
	return headerHasToken(request.Header.Values("Connection"), "upgrade") && strings.TrimSpace(request.Header.Get("Upgrade")) != ""
}

func headerHasToken(values []string, token string) bool {
	for _, value := range values {
		for _, candidate := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(candidate), token) {
				return true
			}
		}
	}
	return false
}

func loopbackDestination(port int) *url.URL {
	return &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(port)}
}

type proxyServer struct {
	cancel    context.CancelFunc
	server    *http.Server
	listener  net.Listener
	handler   *trackedHandler
	done      chan error
	closeGate chan struct{}
	stopOnce  sync.Once
	serveDone bool
	serveErr  error
	onExit    func(error)
}

func startProxyServer(ctx context.Context, listener net.Listener, handler http.Handler, onExit func(error)) *proxyServer {
	return startProxyServerWithTimeouts(ctx, listener, handler, onExit, proxyServerTimeouts{
		readHeader: proxyReadHeaderTimeout,
		idle:       proxyIdleTimeout,
	})
}

type proxyServerTimeouts struct {
	readHeader time.Duration
	idle       time.Duration
}

func startProxyServerWithTimeouts(
	ctx context.Context,
	listener net.Listener,
	handler http.Handler,
	onExit func(error),
	timeouts proxyServerTimeouts,
) *proxyServer {
	serveContext, cancel := context.WithCancel(ctx)
	tracked := newTrackedHandler(handler)
	server := &http.Server{
		Handler:           tracked,
		ReadHeaderTimeout: timeouts.readHeader,
		IdleTimeout:       timeouts.idle,
		BaseContext: func(net.Listener) context.Context {
			return serveContext
		},
		ErrorLog: log.New(io.Discard, "", 0),
	}
	proxy := &proxyServer{
		cancel:    cancel,
		server:    server,
		listener:  listener,
		handler:   tracked,
		done:      make(chan error, 1),
		closeGate: make(chan struct{}, 1),
		onExit:    onExit,
	}
	proxy.closeGate <- struct{}{}
	go func() {
		err := server.Serve(listener)
		proxy.done <- err
		if proxy.onExit != nil {
			proxy.onExit(err)
		}
	}()
	return proxy
}

func (p *proxyServer) close(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.closeGate:
	}
	releaseGate := true
	defer func() {
		if releaseGate {
			p.closeGate <- struct{}{}
		}
	}()
	p.stopOnce.Do(func() {
		p.cancel()
		p.handler.stopAccepting()
	})
	listenerDone := make(chan error, 1)
	go func() {
		listenerDone <- p.listener.Close()
	}()
	var listenerErr error
	select {
	case listenerErr = <-listenerDone:
	case <-ctx.Done():
		releaseGate = false
		go func() {
			<-listenerDone
			p.closeGate <- struct{}{}
		}()
		return ctx.Err()
	}
	if errors.Is(listenerErr, net.ErrClosed) {
		listenerErr = nil
	}
	if listenerErr != nil {
		return listenerErr
	}
	shutdownDone := make(chan struct{})
	go func() {
		_ = p.server.Shutdown(ctx)
		close(shutdownDone)
	}()
	select {
	case <-shutdownDone:
	case <-ctx.Done():
		releaseGate = false
		go func() {
			<-shutdownDone
			p.closeGate <- struct{}{}
		}()
		return ctx.Err()
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- p.server.Close() }()
	var closeErr error
	select {
	case closeErr = <-serverDone:
	case <-ctx.Done():
		releaseGate = false
		go func() {
			<-serverDone
			p.closeGate <- struct{}{}
		}()
		return ctx.Err()
	}
	waitDone := make(chan struct{})
	go func() { p.handler.wait(); close(waitDone) }()
	select {
	case <-waitDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	p.handler.closeIdleConnections()
	if !p.serveDone {
		select {
		case p.serveErr = <-p.done:
			p.serveDone = true
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	serveErr := p.serveErr
	p.serveErr = nil
	if errors.Is(closeErr, http.ErrServerClosed) || errors.Is(closeErr, net.ErrClosed) {
		closeErr = nil
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
	current     *handlerGeneration
	generations []*handlerGeneration
	isAccepting bool
}

type handlerGeneration struct {
	handler    http.Handler
	active     sync.WaitGroup
	retireOnce sync.Once
}

func newTrackedHandler(handler http.Handler) *trackedHandler {
	generation := &handlerGeneration{handler: handler}
	return &trackedHandler{
		current:     generation,
		generations: []*handlerGeneration{generation},
		isAccepting: true,
	}
}

func (h *trackedHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	h.mu.Lock()
	if !h.isAccepting {
		h.mu.Unlock()
		http.Error(writer, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	generation := h.current
	generation.active.Add(1)
	h.mu.Unlock()
	defer generation.active.Done()
	generation.handler.ServeHTTP(writer, request)
}

func (h *trackedHandler) replace(handler http.Handler) {
	h.mu.Lock()
	previous := h.current
	replacement := &handlerGeneration{handler: handler}
	h.current = replacement
	h.generations = append(h.generations, replacement)
	h.mu.Unlock()
	previous.retire()
}

func (h *trackedHandler) stopAccepting() {
	h.mu.Lock()
	h.isAccepting = false
	generations := append([]*handlerGeneration(nil), h.generations...)
	h.mu.Unlock()
	for _, generation := range generations {
		generation.retire()
	}
}

func (h *trackedHandler) wait() {
	h.mu.Lock()
	generations := append([]*handlerGeneration(nil), h.generations...)
	h.mu.Unlock()
	for _, generation := range generations {
		generation.active.Wait()
	}
}

func (h *trackedHandler) closeIdleConnections() {
	h.mu.Lock()
	generations := append([]*handlerGeneration(nil), h.generations...)
	h.mu.Unlock()
	for _, generation := range generations {
		generation.closeIdleConnections()
	}
}

func (g *handlerGeneration) retire() {
	g.retireOnce.Do(func() {
		go func() {
			g.active.Wait()
			if closer, ok := g.handler.(interface{ CloseIdleConnections() }); ok {
				closer.CloseIdleConnections()
			}
		}()
	})
}

func (g *handlerGeneration) closeIdleConnections() {
	if closer, ok := g.handler.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
