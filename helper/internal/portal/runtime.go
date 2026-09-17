package portal

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
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
	Destination  Destination
	DesiredState DesiredState
}

type Destination struct {
	Kind   string `json:"kind"`
	Scheme string `json:"scheme,omitempty"`
	Host   string `json:"host,omitempty"`
	Port   uint16 `json:"port"`
}

func (d *Destination) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	var kind string
	if raw, ok := fields["kind"]; !ok || json.Unmarshal(raw, &kind) != nil {
		return errors.New("invalid destination")
	}
	if kind == DestinationLocalApp {
		if len(fields) != 2 || fields["port"] == nil {
			return errors.New("invalid Local App destination")
		}
		var port uint16
		if err := json.Unmarshal(fields["port"], &port); err != nil {
			return err
		}
		*d = Destination{Kind: kind, Port: port}
	} else if kind == DestinationRemoteApp {
		if len(fields) != 4 || fields["scheme"] == nil || fields["host"] == nil || fields["port"] == nil {
			return errors.New("invalid Remote App destination")
		}
		var scheme, host string
		var port uint16
		if err := json.Unmarshal(fields["scheme"], &scheme); err != nil {
			return err
		}
		if err := json.Unmarshal(fields["host"], &host); err != nil {
			return err
		}
		if err := json.Unmarshal(fields["port"], &port); err != nil {
			return err
		}
		*d = Destination{Kind: kind, Scheme: scheme, Host: host, Port: port}
	} else {
		return errors.New("invalid destination kind")
	}
	return d.Validate()
}

const (
	DestinationLocalApp  = "localApp"
	DestinationRemoteApp = "remoteApp"
)

func (d Destination) Validate() error {
	if d.Port == 0 {
		return errors.New("invalid destination")
	}
	switch d.Kind {
	case DestinationLocalApp:
		if d.Scheme != "" || d.Host != "" {
			return errors.New("invalid Local App destination")
		}
	case DestinationRemoteApp:
		if d.Scheme != "http" && d.Scheme != "https" {
			return errors.New("invalid Remote App scheme")
		}
		if !isCanonicalRemoteHost(d.Host) || isLoopbackHost(d.Host) {
			return errors.New("invalid Remote App host")
		}
	default:
		return errors.New("invalid destination kind")
	}
	return nil
}

func isCanonicalRemoteHost(host string) bool {
	if host == "" || strings.TrimSpace(host) != host || strings.ContainsAny(host, "[]%") {
		return false
	}
	if address, err := netip.ParseAddr(host); err == nil {
		return address.String() == host
	}
	if len(host) > 253 || host != strings.ToLower(host) || strings.HasSuffix(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if !dnsLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	if address, err := netip.ParseAddr(host); err == nil {
		address = address.Unmap()
		return address.IsLoopback() || address.IsUnspecified()
	}
	return false
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
	if !uuidPattern.MatchString(c.ID) || !dnsLabelPattern.MatchString(c.Name) || c.Destination.Validate() != nil {
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
	Up(context.Context) (Status, error)
	Status(context.Context) (Status, error)
	Watch(context.Context) (Watcher, error)
	StartLoginInteractive(context.Context) error
	Listen(network, address string) (net.Listener, error)
	TLSConfig() *tls.Config
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
	mu                  sync.Mutex
	closing             bool
	stateRoot           string
	factory             NodeFactory
	proxyForDestination func(Destination) (http.Handler, error)
	portals             map[string]*portalRuntime
}

type portalRuntime struct {
	mu                    sync.Mutex
	gate                  chan struct{}
	eventMu               sync.Mutex
	phase                 portalPhase
	config                *Config
	node                  Node
	watcher               Watcher
	cancel                context.CancelFunc
	runContext            context.Context
	watchDone             sync.WaitGroup
	emit                  func(Event)
	proxyForDestination   func(Destination) (http.Handler, error)
	authenticationPending bool
	proxy                 *proxyServer
}

type portalPhase string

const (
	portalStarting portalPhase = "starting"
	portalRunning  portalPhase = "running"
	portalFailed   portalPhase = "failed"
	portalClosing  portalPhase = "closing"
)

var errRuntimeClosing = errors.New("runtime is closing")

func NewRuntime(stateRoot string, factory NodeFactory) *Runtime {
	return &Runtime{
		stateRoot:           stateRoot,
		factory:             factory,
		proxyForDestination: newDestinationProxy,
		portals:             make(map[string]*portalRuntime),
	}
}

func (r *Runtime) Reconcile(ctx context.Context, configs []Config, emit func(Event)) ([]ReconcileEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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

	r.mu.Lock()
	if r.closing {
		r.mu.Unlock()
		return nil, errRuntimeClosing
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
	r.mu.Unlock()
	sort.Strings(portalIDs)

	entries := make([]ReconcileEntry, 0, len(portalIDs))
	for _, portalID := range portalIDs {
		config, included := desired[portalID]
		outcome := r.reconcilePortal(ctx, portalID, config, included, emit)
		entries = append(entries, ReconcileEntry{PortalID: portalID, Outcome: outcome})
	}
	return entries, nil
}

func (r *Runtime) reconcilePortal(
	ctx context.Context,
	portalID string,
	config Config,
	included bool,
	emit func(Event),
) ReconcileOutcome {
	if !included || config.DesiredState == DesiredStateStopped {
		portal := r.portal(portalID)
		if portal == nil {
			return OutcomeConverged
		}
		if err := r.closeAndRemove(ctx, portalID, portal); err != nil {
			return OutcomeCloseFailed
		}
		return OutcomeConverged
	}

	for {
		if ctx.Err() != nil {
			return OutcomeStartFailed
		}
		portal, created := r.reserve(ctx, portalID, false)
		if portal == nil {
			return OutcomeStartFailed
		}
		if created {
			if ctx.Err() != nil {
				r.remove(portalID, portal)
				portal.release()
				return OutcomeStartFailed
			}
			node := r.factory(filepath.Join(r.stateRoot, config.ID), config.Name)
			events, err := portal.start(ctx, config, node, emit)
			if err != nil {
				_ = r.closeAndRemoveHeld(ctx, portalID, portal)
				for _, event := range events {
					emit(event)
				}
				return OutcomeStartFailed
			}
			portal.release()
			portal.emitStartupEvents(events)
			return OutcomeConverged
		}
		if err := portal.acquire(ctx); err != nil {
			return OutcomeStartFailed
		}
		phase, previous, _ := portal.snapshot()
		if phase == portalRunning && previous.Name == config.Name {
			if previous.Destination != config.Destination {
				err := portal.updateDestinationLocked(config.Destination)
				portal.release()
				if err != nil {
					return OutcomeStartFailed
				}
				return OutcomeConverged
			}
			portal.release()
			return OutcomeConverged
		}
		if phase == portalRunning {
			portal.release()
			return OutcomeStartFailed
		}
		if err := r.closeAndRemoveHeld(ctx, portalID, portal); err != nil {
			return OutcomeStartFailed
		}
	}
}

func (r *Runtime) portal(portalID string) *portalRuntime {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.portals[portalID]
}

func (r *Runtime) reserve(ctx context.Context, portalID string, allowDuringClose bool) (*portalRuntime, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !allowDuringClose && ctx.Err() != nil {
		return nil, false
	}
	if r.closing && !allowDuringClose {
		return nil, false
	}
	if portal := r.portals[portalID]; portal != nil {
		return portal, false
	}
	portal := &portalRuntime{gate: make(chan struct{}, 1), phase: portalStarting, proxyForDestination: r.proxyForDestination}
	r.portals[portalID] = portal
	return portal, true
}

func (r *Runtime) remove(portalID string, portal *portalRuntime) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.portals[portalID] == portal {
		delete(r.portals, portalID)
	}
}

func (r *Runtime) closeAndRemove(ctx context.Context, portalID string, portal *portalRuntime) error {
	portal.requestStop()
	if err := portal.acquire(ctx); err != nil {
		return err
	}
	return r.closeAndRemoveHeld(ctx, portalID, portal)
}

// closeAndRemoveHeld closes an already-gated Portal and removes its registry
// entry before making that Portal available to another lifecycle operation.
func (r *Runtime) closeAndRemoveHeld(ctx context.Context, portalID string, portal *portalRuntime) error {
	portal.requestStop()
	err, _ := portal.closeHeld(ctx, true, func() { r.remove(portalID, portal) })
	return err
}

func (r *Runtime) current(portalID string, portal *portalRuntime) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.portals[portalID] == portal
}

func (r *Runtime) Authenticate(ctx context.Context, portalID string) error {
	portalID, err := normalizePortalID(portalID)
	if err != nil {
		return errors.New("portal is not running")
	}
	portal := r.portal(portalID)
	if portal == nil {
		return errors.New("portal is not running")
	}
	return portal.authenticate(ctx)
}

func (r *Runtime) CleanupRejectedPortal(ctx context.Context, portalID string) error {
	return r.cleanupPortal(ctx, portalID)
}

func (r *Runtime) RemovePortal(ctx context.Context, portalID string) error {
	return r.cleanupPortal(ctx, portalID)
}

func (r *Runtime) cleanupPortal(ctx context.Context, portalID string) error {
	portalID, err := normalizePortalID(portalID)
	if err != nil {
		return errors.New("invalid portal ID")
	}
	stateRoot, targetExists, err := openPortalStateRoot(r.stateRoot, portalID)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if stateRoot != nil {
		defer stateRoot.Close()
	}
	var portal *portalRuntime
	for {
		var created bool
		portal, created = r.reserve(ctx, portalID, true)
		if portal == nil {
			return ctx.Err()
		}
		if !created {
			if err := portal.acquire(ctx); err != nil {
				return err
			}
		}
		if r.current(portalID, portal) {
			break
		}
		portal.release()
	}
	releaseGate := true
	defer func() {
		if releaseGate {
			portal.release()
		}
	}()
	portal.requestStop()
	if err, retainedGate := portal.closeHeld(ctx, false, nil); err != nil {
		if retainedGate {
			releaseGate = false
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if stateRoot == nil {
		stateRoot, targetExists, err = openPortalStateRoot(r.stateRoot, portalID)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if stateRoot != nil {
			defer stateRoot.Close()
		}
	} else {
		targetExists, err = portalStateTargetExists(stateRoot, portalID)
		if err != nil {
			return err
		}
	}
	if targetExists {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := stateRoot.RemoveAll(portalID); err != nil {
			return errors.New("remove portal state")
		}
	}
	r.remove(portalID, portal)
	return nil
}

func openPortalStateRoot(stateRoot, portalID string) (*os.Root, bool, error) {
	root, err := filepath.Abs(filepath.Clean(stateRoot))
	if err != nil {
		return nil, false, errors.New("resolve state root")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, err
		}
		return nil, false, errors.New("resolve state root")
	}
	openedRoot, err := os.OpenRoot(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, err
		}
		return nil, false, errors.New("open state root")
	}
	targetExists, err := portalStateTargetExists(openedRoot, portalID)
	if err != nil {
		_ = openedRoot.Close()
		return nil, false, err
	}
	return openedRoot, targetExists, nil
}

func portalStateTargetExists(root *os.Root, portalID string) (bool, error) {
	info, err := root.Lstat(portalID)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, errors.New("inspect portal state")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, errors.New("invalid portal state directory")
	}
	return true, nil
}

func (r *Runtime) Close(ctx context.Context) error {
	r.mu.Lock()
	r.closing = true
	portals := make(map[string]*portalRuntime, len(r.portals))
	for portalID, portal := range r.portals {
		portals[portalID] = portal
	}
	r.mu.Unlock()
	var group sync.WaitGroup
	var errsMu sync.Mutex
	var errs []error
	for portalID, portal := range portals {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := r.closeAndRemove(ctx, portalID, portal); err != nil {
				errsMu.Lock()
				errs = append(errs, err)
				errsMu.Unlock()
				return
			}
		}()
	}
	group.Wait()
	return errors.Join(errs...)
}

func normalizePortalID(portalID string) (string, error) {
	portalID = strings.ToLower(portalID)
	if !uuidPattern.MatchString(portalID) {
		return "", errors.New("invalid portal ID")
	}
	return portalID, nil
}

func (r *portalRuntime) acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.gate:
		return nil
	}
}

func (r *portalRuntime) release() { r.gate <- struct{}{} }

func (r *portalRuntime) snapshot() (portalPhase, Config, Destination) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.config == nil {
		return r.phase, Config{}, Destination{}
	}
	return r.phase, *r.config, r.config.Destination
}

func (r *portalRuntime) start(ctx context.Context, config Config, node Node, emit func(Event)) ([]Event, error) {
	r.mu.Lock()
	r.config = &config
	r.node = node
	r.emit = emit
	r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return r.startupFailure(err)
	}
	if err := node.Start(); err != nil {
		return r.startupFailure(errors.New("start portal"))
	}
	if err := ctx.Err(); err != nil {
		return r.startupFailure(err)
	}
	watchContext, cancel := context.WithCancel(ctx)
	watcher, err := node.Watch(watchContext)
	if err != nil {
		cancel()
		return r.startupFailure(errors.New("watch portal status"))
	}
	r.mu.Lock()
	r.watcher = watcher
	r.cancel = cancel
	r.runContext = watchContext
	r.mu.Unlock()
	r.watchDone.Add(1)
	go r.watch(watchContext, watcher)
	status, err := node.Status(ctx)
	if err != nil {
		return r.startupFailure(errors.New("read portal status"))
	}
	mapped := mapStatus(status)
	if mapped.State == StateOnline && mapped.PortalURL != "" {
		if err := r.ensureProxyLocked(watchContext); err != nil {
			return r.startupFailure(err)
		}
	}
	r.mu.Lock()
	if r.phase == portalFailed {
		r.mu.Unlock()
		return nil, errors.New("portal failed during startup")
	}
	r.phase = portalRunning
	r.mu.Unlock()
	return []Event{{PortalID: config.ID, Status: &mapped}}, nil
}

func (r *portalRuntime) startupFailure(err error) ([]Event, error) {
	event, recorded := r.recordFailure()
	if !recorded {
		return nil, err
	}
	return []Event{event}, err
}

func (r *portalRuntime) updateDestinationLocked(destination Destination) error {
	if r.config == nil {
		return errors.New("portal is not running")
	}
	handler, err := r.proxyForDestination(destination)
	if err != nil {
		return err
	}
	if r.proxy != nil {
		r.proxy.replaceHandler(handler)
	}
	r.config.Destination = destination
	return nil
}

func (r *portalRuntime) authenticate(ctx context.Context) error {
	if err := r.acquire(ctx); err != nil {
		return errors.New("portal is not running")
	}
	defer r.release()
	if r.node == nil || r.config == nil {
		return errors.New("portal is not running")
	}
	if err := r.node.StartLoginInteractive(ctx); err != nil {
		return errors.New("start interactive login")
	}
	r.authenticationPending = true
	return nil
}

func (r *portalRuntime) close(ctx context.Context) error {
	r.requestStop()
	if err := r.acquire(ctx); err != nil {
		return err
	}
	err, _ := r.closeHeld(ctx, true, nil)
	return err
}

// closeHeld closes a Portal while its operation gate is already held. Callers
// that retain the gate for a larger operation must pass false and release it
// only after their own durable work has completed.
func (r *portalRuntime) closeHeld(ctx context.Context, releaseWhenDone bool, onClose func()) (error, bool) {
	releaseGate := releaseWhenDone
	defer func() {
		if releaseGate {
			r.release()
		}
	}()
	r.mu.Lock()
	r.phase = portalClosing
	watcher, proxy, node := r.watcher, r.proxy, r.node
	r.mu.Unlock()
	if watcher != nil {
		_ = watcher.Close()
		r.mu.Lock()
		if r.watcher == watcher {
			r.watcher = nil
		}
		r.mu.Unlock()
	}
	var errs []error
	if proxy != nil {
		if err := proxy.close(ctx); err != nil {
			if ctx.Err() != nil {
				r.markFailed()
				return err, false
			}
			errs = append(errs, err)
		} else {
			r.mu.Lock()
			if r.proxy == proxy {
				r.proxy = nil
			}
			r.mu.Unlock()
		}
	}
	if node != nil {
		nodeDone := make(chan error, 1)
		go func() { nodeDone <- node.Close() }()
		select {
		case err := <-nodeDone:
			if err != nil {
				errs = append(errs, err)
			}
		case <-ctx.Done():
			releaseGate = false
			go func() {
				<-nodeDone
				r.markFailed()
				r.release()
			}()
			return ctx.Err(), true
		}
	}
	done := make(chan struct{})
	go func() { r.watchDone.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		r.markFailed()
		return ctx.Err(), false
	}
	if err := errors.Join(errs...); err != nil {
		r.markFailed()
		return err, false
	}
	r.mu.Lock()
	r.config, r.node, r.watcher, r.runContext, r.emit = nil, nil, nil, nil, nil
	r.authenticationPending = false
	r.phase = portalFailed
	r.mu.Unlock()
	if onClose != nil {
		onClose()
	}
	return nil, false
}

func (r *portalRuntime) requestStop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
}

func (r *portalRuntime) failLocked() {
	r.phase = portalFailed
	if r.cancel != nil {
		r.cancel()
	}
}

func (r *portalRuntime) markFailed() {
	r.mu.Lock()
	r.failLocked()
	r.mu.Unlock()
}

func (r *portalRuntime) errorEventLocked() Event {
	if r.config == nil {
		return Event{}
	}
	return Event{PortalID: r.config.ID, Status: &StatusEvent{State: StateError, Addresses: []string{}}}
}

func (r *portalRuntime) watch(ctx context.Context, watcher Watcher) {
	defer r.watchDone.Done()
	for {
		notification, err := watcher.Next()
		if err != nil {
			if ctx.Err() == nil {
				r.fail()
			}
			return
		}
		var events []Event
		if r.acquire(ctx) != nil {
			return
		}
		r.mu.Lock()
		if notification.AuthURL != "" && r.authenticationPending && r.config != nil && r.emit != nil {
			r.authenticationPending = false
			events = append(events, Event{PortalID: r.config.ID, AuthenticationURL: notification.AuthURL})
		}
		r.mu.Unlock()
		events = append(events, r.statusEvents(ctx)...)
		r.release()
		for _, event := range events {
			r.emitWatchEvent(ctx, event)
		}
	}
}

func (r *portalRuntime) statusEvents(ctx context.Context) []Event {
	if ctx.Err() != nil {
		return nil
	}
	r.mu.Lock()
	node, config := r.node, r.config
	r.mu.Unlock()
	if node == nil || config == nil {
		return nil
	}
	status, err := node.Status(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		event, recorded := r.recordFailure()
		if !recorded {
			return nil
		}
		return []Event{event}
	}
	if !r.isCurrentRun(ctx) {
		return nil
	}
	mapped := mapStatus(status)
	if mapped.State == StateOnline && mapped.PortalURL != "" {
		if err := r.ensureProxyLocked(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			event, recorded := r.recordFailure()
			if !recorded {
				return nil
			}
			return []Event{event}
		}
	}
	if !r.isCurrentRun(ctx) {
		return nil
	}
	return []Event{{PortalID: config.ID, Status: &mapped}}
}

func (r *portalRuntime) ensureProxyLocked(ctx context.Context) error {
	if r.proxy != nil {
		return nil
	}
	handler, err := r.proxyForDestination(r.config.Destination)
	if err != nil {
		return err
	}
	if _, err := r.node.Up(ctx); err != nil {
		return errors.New("wait for portal readiness")
	}
	if !r.isCurrentRun(ctx) {
		return errors.New("portal stopped before HTTPS readiness")
	}
	listener, err := r.node.Listen("tcp", ":443")
	if err != nil {
		return errors.New("listen for portal HTTPS")
	}
	if !r.isCurrentRun(ctx) {
		_ = listener.Close()
		return errors.New("portal stopped before HTTPS serving")
	}
	if tlsConfig := r.node.TLSConfig(); tlsConfig != nil {
		listener = tls.NewListener(listener, tlsConfig)
	}
	r.proxy = startProxyServer(ctx, listener, handler, func(err error) {
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			r.fail()
		}
	})
	return nil
}

func (r *portalRuntime) fail() {
	event, recorded := r.recordFailure()
	if recorded {
		r.emitEvent(event)
	}
}

func (r *portalRuntime) recordFailure() (Event, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.phase != portalStarting && r.phase != portalRunning {
		return Event{}, false
	}
	r.failLocked()
	return r.errorEventLocked(), true
}

func (r *portalRuntime) isCurrentRun(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.phase == portalStarting || r.phase == portalRunning
}

func (r *portalRuntime) emitEvent(event Event) {
	r.eventMu.Lock()
	defer r.eventMu.Unlock()
	r.emitEventLocked(event)
}

func (r *portalRuntime) emitWatchEvent(ctx context.Context, event Event) {
	r.eventMu.Lock()
	defer r.eventMu.Unlock()
	if (event.Status == nil || event.Status.State != StateError) && !r.isCurrentRun(ctx) {
		return
	}
	r.emitEventLocked(event)
}

func (r *portalRuntime) emitStartupEvents(events []Event) {
	r.eventMu.Lock()
	defer r.eventMu.Unlock()
	r.mu.Lock()
	running := r.phase == portalRunning
	r.mu.Unlock()
	if !running {
		return
	}
	for _, event := range events {
		r.emitEventLocked(event)
	}
}

func (r *portalRuntime) emitEventLocked(event Event) {
	r.mu.Lock()
	emit := r.emit
	deliver := event.Status == nil || event.Status.State == StateError || r.phase == portalRunning
	r.mu.Unlock()
	if deliver && emit != nil && event.PortalID != "" {
		emit(event)
	}
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
