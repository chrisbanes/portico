package portal

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
	ID           string
	Name         string
	Port         uint16
	DesiredState DesiredState
}

type DesiredState string

const (
	DesiredStateEnabled DesiredState = "enabled"
	DesiredStateStopped DesiredState = "stopped"
)

type ReconcileOutcome string

const (
	OutcomeConverged   ReconcileOutcome = "converged"
	OutcomeStartFailed ReconcileOutcome = "startFailed"
	OutcomeCloseFailed ReconcileOutcome = "closeFailed"
)

type ReconcileEntry struct {
	PortalID string           `json:"portalId"`
	Outcome  ReconcileOutcome `json:"outcome"`
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

func (c Config) validateDesired() error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.DesiredState != DesiredStateEnabled && c.DesiredState != DesiredStateStopped {
		return errors.New("invalid portal desired state")
	}
	return nil
}

type Status struct {
	BackendState   string
	StableNodeID   string
	DNSName        string
	CertDomains    []string
	Addresses      []string
	TailnetName    string
	MagicDNSSuffix string
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
	State          State    `json:"state"`
	StableNodeID   string   `json:"stableNodeId,omitempty"`
	AssignedName   string   `json:"assignedName,omitempty"`
	PortalURL      string   `json:"portalURL,omitempty"`
	Addresses      []string `json:"addresses"`
	TailnetName    string   `json:"tailnetName,omitempty"`
	MagicDNSSuffix string   `json:"magicDNSSuffix,omitempty"`
}

type Event struct {
	PortalID          string
	Status            *StatusEvent
	AuthenticationURL string
}

type Runtime struct {
	mu        sync.Mutex
	stateRoot string
	factory   NodeFactory
	portals   map[string]*portalRuntime
}

type portalRuntime struct {
	mu                    sync.Mutex
	config                *Config
	node                  Node
	watcher               Watcher
	cancel                context.CancelFunc
	runContext            context.Context
	watchDone             sync.WaitGroup
	emit                  func(Event)
	authenticationPending bool
	proxy                 *proxyServer
	isRunning             bool
}

func NewRuntime(stateRoot string, factory NodeFactory) *Runtime {
	return &Runtime{stateRoot: stateRoot, factory: factory, portals: make(map[string]*portalRuntime)}
}

func (r *Runtime) Reconcile(ctx context.Context, configs []Config, emit func(Event)) ([]ReconcileEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	desired := make(map[string]Config, len(configs))
	for _, config := range configs {
		if err := config.validateDesired(); err != nil {
			return nil, err
		}
		config = config.normalized()
		if _, exists := desired[config.ID]; exists {
			return nil, errors.New("duplicate portal configuration")
		}
		desired[config.ID] = config
	}

	portalIDs := make([]string, 0, len(desired)+len(r.portals))
	seen := make(map[string]struct{}, len(desired)+len(r.portals))
	for portalID := range desired {
		portalIDs = append(portalIDs, portalID)
		seen[portalID] = struct{}{}
	}
	for portalID := range r.portals {
		if _, exists := seen[portalID]; !exists {
			portalIDs = append(portalIDs, portalID)
		}
	}
	sort.Strings(portalIDs)

	entries := make([]ReconcileEntry, 0, len(portalIDs))
	for _, portalID := range portalIDs {
		config, included := desired[portalID]
		outcome := r.reconcilePortalLocked(ctx, portalID, config, included, emit)
		entries = append(entries, ReconcileEntry{PortalID: portalID, Outcome: outcome})
	}
	return entries, nil
}

func (r *Runtime) reconcilePortalLocked(
	ctx context.Context,
	portalID string,
	config Config,
	included bool,
	emit func(Event),
) ReconcileOutcome {
	portal := r.portals[portalID]
	if !included || config.DesiredState == DesiredStateStopped {
		if portal == nil {
			return OutcomeConverged
		}
		if err := portal.close(); err != nil {
			return OutcomeCloseFailed
		}
		delete(r.portals, portalID)
		return OutcomeConverged
	}

	if portal == nil {
		return r.startPortalLocked(ctx, config, emit)
	}
	if portal.config == nil || !portal.isRunning {
		if err := portal.close(); err != nil {
			return OutcomeStartFailed
		}
		delete(r.portals, portalID)
		return r.startPortalLocked(ctx, config, emit)
	}
	if portal.config.Name != config.Name {
		return OutcomeStartFailed
	}
	if portal.config.Port != config.Port {
		if err := portal.updatePort(config.Port); err != nil {
			return OutcomeStartFailed
		}
	}
	return OutcomeConverged
}

func (r *Runtime) startPortalLocked(ctx context.Context, config Config, emit func(Event)) ReconcileOutcome {
	node := r.factory(filepath.Join(r.stateRoot, config.ID), config.Name)
	portal := &portalRuntime{}
	r.portals[config.ID] = portal
	if err := portal.start(ctx, config, node, emit); err != nil {
		if closeErr := portal.close(); closeErr == nil {
			delete(r.portals, config.ID)
		}
		return OutcomeStartFailed
	}
	return OutcomeConverged
}

func (r *Runtime) Authenticate(ctx context.Context, portalID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	portalID, err := normalizePortalID(portalID)
	if err != nil {
		return errors.New("portal is not running")
	}
	portal := r.portals[portalID]
	if portal == nil {
		return errors.New("portal is not running")
	}
	return portal.authenticate(ctx)
}

func (r *Runtime) CleanupRejectedPortal(portalID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	portalID, err := normalizePortalID(portalID)
	if err != nil {
		return errors.New("invalid portal ID")
	}
	stateDirectory, err := validatedStateDirectory(r.stateRoot, portalID)
	if err != nil {
		return err
	}
	if portal := r.portals[portalID]; portal != nil {
		if err := portal.close(); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(stateDirectory); err != nil {
		return errors.New("remove portal state")
	}
	delete(r.portals, portalID)
	return nil
}

func (r *Runtime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var errs []error
	for portalID, portal := range r.portals {
		if err := portal.close(); err != nil {
			errs = append(errs, err)
		} else {
			delete(r.portals, portalID)
		}
	}
	return errors.Join(errs...)
}

func normalizePortalID(portalID string) (string, error) {
	portalID = strings.ToLower(portalID)
	if !uuidPattern.MatchString(portalID) {
		return "", errors.New("invalid portal ID")
	}
	return portalID, nil
}

func validatedStateDirectory(stateRoot, portalID string) (string, error) {
	portalID, err := normalizePortalID(portalID)
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(filepath.Clean(stateRoot))
	if err != nil {
		return "", errors.New("resolve state root")
	}
	target := filepath.Join(root, portalID)
	if filepath.Dir(target) != root || filepath.Base(target) != portalID {
		return "", errors.New("invalid portal state directory")
	}
	return target, nil
}

func (r *portalRuntime) start(ctx context.Context, config Config, node Node, emit func(Event)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.config = &config
	r.node = node
	r.emit = emit
	if err := node.Start(); err != nil {
		return errors.New("start portal")
	}
	watchContext, cancel := context.WithCancel(ctx)
	watcher, err := node.Watch(watchContext)
	if err != nil {
		cancel()
		return errors.New("watch portal status")
	}
	r.watcher = watcher
	r.cancel = cancel
	r.runContext = watchContext
	if err := r.emitStatusLocked(ctx); err != nil {
		return err
	}
	r.watchDone.Add(1)
	go r.watch(watchContext, watcher)
	r.isRunning = true
	return nil
}

func (r *portalRuntime) updatePort(port uint16) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.config == nil {
		return errors.New("portal is not running")
	}
	handler, err := newLoopbackProxy(int(port))
	if err != nil {
		return err
	}
	if r.proxy != nil {
		r.proxy.replaceHandler(handler)
	}
	r.config.Port = port
	return nil
}

func (r *portalRuntime) authenticate(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.node == nil || r.config == nil {
		return errors.New("portal is not running")
	}
	if err := r.node.StartLoginInteractive(ctx); err != nil {
		return errors.New("start interactive login")
	}
	r.authenticationPending = true
	return nil
}

func (r *portalRuntime) close() error {
	r.mu.Lock()
	err := r.closeLocked()
	r.mu.Unlock()
	r.watchDone.Wait()
	return err
}

func (r *portalRuntime) closeLocked() error {
	var proxyErr error
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	if r.watcher != nil {
		_ = r.watcher.Close()
		r.watcher = nil
	}
	if r.proxy != nil {
		proxyErr = r.proxy.close()
		if proxyErr == nil {
			r.proxy = nil
		}
	}
	var nodeErr error
	if r.node != nil {
		nodeErr = r.node.Close()
		if nodeErr == nil {
			r.node = nil
		}
	}
	err := errors.Join(proxyErr, nodeErr)
	r.isRunning = false
	if err == nil {
		r.config = nil
		r.runContext = nil
		r.emit = nil
		r.proxy = nil
	}
	r.authenticationPending = false
	return err
}

func (r *portalRuntime) watch(ctx context.Context, watcher Watcher) {
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

func (r *portalRuntime) emitStatusLocked(ctx context.Context) error {
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

func (r *portalRuntime) ensureProxyLocked() error {
	if r.proxy != nil {
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
	r.proxy = startProxyServer(r.runContext, listener, handler)
	return nil
}

func mapStatus(status Status) StatusEvent {
	mapped := StatusEvent{
		State:          mapBackendState(status.BackendState),
		StableNodeID:   status.StableNodeID,
		Addresses:      append([]string{}, status.Addresses...),
		TailnetName:    status.TailnetName,
		MagicDNSSuffix: status.MagicDNSSuffix,
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
