package portal

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

type State string

const (
	StateAuthenticating   State = "authenticating"
	StateAwaitingApproval State = "awaitingApproval"
	StateConnecting       State = "connecting"
	StateOnline           State = "online"
	StateStopped          State = "stopped"
	StateError            State = "error"
)

type Config struct {
	ID   string
	Name string
	Port uint16
}

var (
	uuidPattern     = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	dnsLabelPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
)

func (c Config) Validate() error {
	if !uuidPattern.MatchString(c.ID) || !dnsLabelPattern.MatchString(c.Name) || c.Port == 0 {
		return errors.New("invalid portal configuration")
	}
	return nil
}

func (c Config) normalized() Config {
	c.ID = strings.ToLower(c.ID)
	return c
}

type Status struct {
	BackendState string
	StableNodeID string
	DNSName      string
	CertDomains  []string
	Addresses    []string
}

type Notification struct {
	AuthURL string
}

type Watcher interface {
	Next() (Notification, error)
	Close() error
}

type Node interface {
	Start() error
	Status(context.Context) (Status, error)
	Watch(context.Context) (Watcher, error)
	StartLoginInteractive(context.Context) error
	ListenTLS(network, address string) (net.Listener, error)
	Close() error
}

type NodeFactory func(dir, hostname string) Node

type StatusEvent struct {
	State        State    `json:"state"`
	StableNodeID string   `json:"stableNodeId,omitempty"`
	AssignedName string   `json:"assignedName,omitempty"`
	PortalURL    string   `json:"portalURL,omitempty"`
	Addresses    []string `json:"addresses"`
}

type Event struct {
	PortalID          string
	Status            *StatusEvent
	AuthenticationURL string
}

type Runtime struct {
	mu                    sync.Mutex
	stateRoot             string
	factory               NodeFactory
	config                *Config
	node                  Node
	watcher               Watcher
	cancel                context.CancelFunc
	watchDone             sync.WaitGroup
	serveDone             sync.WaitGroup
	emit                  func(Event)
	authenticationPending bool
	httpServer            *http.Server
	listener              net.Listener
}

func NewRuntime(stateRoot string, factory NodeFactory) *Runtime {
	return &Runtime{stateRoot: stateRoot, factory: factory}
}

func (r *Runtime) Start(ctx context.Context, config Config, emit func(Event)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := config.Validate(); err != nil {
		return err
	}
	config = config.normalized()
	if r.node != nil {
		return errors.New("a portal is already running")
	}
	node := r.factory(filepath.Join(r.stateRoot, config.ID), config.Name)
	if err := node.Start(); err != nil {
		_ = node.Close()
		return errors.New("start portal")
	}
	watchContext, cancel := context.WithCancel(ctx)
	watcher, err := node.Watch(watchContext)
	if err != nil {
		cancel()
		_ = node.Close()
		return errors.New("watch portal status")
	}
	r.config = &config
	r.node = node
	r.watcher = watcher
	r.cancel = cancel
	r.emit = emit
	if err := r.emitStatusLocked(ctx); err != nil {
		r.closeLocked()
		return err
	}
	r.watchDone.Add(1)
	go r.watch(watchContext, watcher)
	return nil
}

func (r *Runtime) Authenticate(ctx context.Context, portalID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.node == nil || r.config == nil || !strings.EqualFold(r.config.ID, portalID) {
		return errors.New("portal is not running")
	}
	if err := r.node.StartLoginInteractive(ctx); err != nil {
		return errors.New("start interactive login")
	}
	r.authenticationPending = true
	return nil
}

func (r *Runtime) Close() error {
	r.mu.Lock()
	err := r.closeLocked()
	r.mu.Unlock()
	r.watchDone.Wait()
	r.serveDone.Wait()
	return err
}

func (r *Runtime) closeLocked() error {
	if r.cancel != nil {
		r.cancel()
	}
	if r.watcher != nil {
		_ = r.watcher.Close()
	}
	if r.httpServer != nil {
		_ = r.httpServer.Close()
	}
	if r.listener != nil {
		_ = r.listener.Close()
	}
	var err error
	if r.node != nil {
		err = r.node.Close()
	}
	r.config = nil
	r.node = nil
	r.watcher = nil
	r.cancel = nil
	r.emit = nil
	r.authenticationPending = false
	r.httpServer = nil
	r.listener = nil
	return err
}

func (r *Runtime) watch(ctx context.Context, watcher Watcher) {
	defer r.watchDone.Done()
	for {
		notification, err := watcher.Next()
		if err != nil {
			return
		}
		r.mu.Lock()
		if notification.AuthURL != "" && r.authenticationPending && r.config != nil && r.emit != nil {
			r.authenticationPending = false
			r.emit(Event{PortalID: r.config.ID, AuthenticationURL: notification.AuthURL})
		}
		_ = r.emitStatusLocked(ctx)
		r.mu.Unlock()
	}
}

func (r *Runtime) emitStatusLocked(ctx context.Context) error {
	if r.node == nil || r.config == nil || r.emit == nil {
		return nil
	}
	status, err := r.node.Status(ctx)
	if err != nil {
		r.emit(Event{PortalID: r.config.ID, Status: &StatusEvent{State: StateError, Addresses: []string{}}})
		return nil
	}
	mapped := mapStatus(status)
	r.emit(Event{PortalID: r.config.ID, Status: &mapped})
	if mapped.State == StateOnline && mapped.PortalURL != "" {
		if err := r.ensureProxyLocked(); err != nil {
			r.emit(Event{PortalID: r.config.ID, Status: &StatusEvent{State: StateError, Addresses: mapped.Addresses}})
			return err
		}
	}
	return nil
}

func (r *Runtime) ensureProxyLocked() error {
	if r.httpServer != nil {
		return nil
	}
	handler, err := newLoopbackProxy(int(r.config.Port))
	if err != nil {
		return err
	}
	listener, err := r.node.ListenTLS("tcp", ":443")
	if err != nil {
		return errors.New("listen for portal HTTPS")
	}
	server := &http.Server{Handler: handler}
	r.listener = listener
	r.httpServer = server
	r.serveDone.Add(1)
	go func() {
		defer r.serveDone.Done()
		_ = server.Serve(listener)
	}()
	return nil
}

func mapStatus(status Status) StatusEvent {
	mapped := StatusEvent{
		State:        mapBackendState(status.BackendState),
		StableNodeID: status.StableNodeID,
		Addresses:    append([]string{}, status.Addresses...),
	}
	dnsName := strings.TrimSuffix(status.DNSName, ".")
	if dnsName != "" {
		mapped.AssignedName = strings.SplitN(dnsName, ".", 2)[0]
		for _, domain := range status.CertDomains {
			if strings.EqualFold(strings.TrimSuffix(domain, "."), dnsName) {
				mapped.PortalURL = "https://" + dnsName + "/"
				break
			}
		}
	}
	return mapped
}

func mapBackendState(backend string) State {
	switch backend {
	case "NeedsLogin":
		return StateAuthenticating
	case "NeedsMachineAuth":
		return StateAwaitingApproval
	case "Starting", "NoState":
		return StateConnecting
	case "Running":
		return StateOnline
	case "Stopped":
		return StateStopped
	default:
		return StateError
	}
}
