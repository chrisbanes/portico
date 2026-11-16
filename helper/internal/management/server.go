package management

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-json-experiment/json/jsontext"

	"github.com/chrisbanes/portico/helper/internal/discovery"
	"github.com/chrisbanes/portico/helper/internal/installation"
	"github.com/chrisbanes/portico/helper/internal/ipc"
	"github.com/chrisbanes/portico/helper/internal/service"
)

// callTimeout is the deadline for one call; tests lower it.
var callTimeout = 10 * time.Second

// afterMutation, for tests only, runs once the engine has returned a receipt
// and before the reply is sent.
var afterMutation func()

// afterDeadline, for tests only, runs in the deadline timer once it has
// claimed the reply.
var afterDeadline func()

// afterSubscribe, for tests only, runs once the initial snapshot is queued
// and before the subscribe reply is sent.
var afterSubscribe func()

// Serve answers management v1 on listener until listener.Close, which waits
// for every connection's in-flight engine work.
func Serve(listener *ipc.Listener, engine *service.Engine) error {
	return listener.Serve(func(c *ipc.Conn) { serveConn(c, engine, nil) })
}

// ServeStartupFailure answers management v1 while the host holds the
// lifetime lock but its state could not load, so there is no engine. It
// answers hello and getSnapshot, whose service.startupFailure is reason
// (unsupportedState or persistenceUnavailable); every other valid call gets
// reason as its error.
func ServeStartupFailure(listener *ipc.Listener, reason service.Code) error {
	failure := &service.Snapshot{
		Version: Version,
		Epoch:   newUUID(),
		Service: service.ServiceState{Runtime: "unavailable", StartupFailure: reason},
		// Nothing loaded, so the enums take their neutral values.
		Logging:            installation.LoggingUndecided,
		LaunchAtLoginOffer: installation.OfferNotOffered,
		Alerts:             []installation.Alert{},
		Portals:            []service.PortalSnapshot{},
		Availability:       map[service.Operation]service.Availability{},
	}
	return listener.Serve(func(c *ipc.Conn) { serveConn(c, nil, failure) })
}

// conn is one client connection with its own engine session.
type conn struct {
	ic      *ipc.Conn
	session *service.Session
	// failure, set only without an engine, is the startup-failure snapshot.
	failure *service.Snapshot
	wg      sync.WaitGroup

	// sendMu keeps every frame set contiguous. Send never blocks, so it is
	// never held across I/O, and never while calling into the engine.
	sendMu sync.Mutex

	subscribed atomic.Bool
	// ready is closed once the subscribe reply is queued, so the initial
	// snapshot follows it.
	ready     chan struct{}
	snapshots chan service.Snapshot
	events    chan service.EnrolmentEvent
	// full is closed when either queue overflows.
	full     chan struct{}
	fullOnce sync.Once

	// held parks enrolment events by requestID while a resumeEnrolment call
	// for that request is unanswered, so its reply goes first.
	heldMu sync.Mutex
	held   map[string]*heldEvents
}

type heldEvents struct {
	calls  int
	events []service.EnrolmentEvent
}

// queueSize bounds the snapshots, and separately the enrolment events,
// waiting to be encoded for one connection.
const queueSize = 128

func serveConn(ic *ipc.Conn, engine *service.Engine, failure *service.Snapshot) {
	c := &conn{
		ic:        ic,
		failure:   failure,
		held:      map[string]*heldEvents{},
		ready:     make(chan struct{}),
		snapshots: make(chan service.Snapshot, queueSize),
		events:    make(chan service.EnrolmentEvent, queueSize),
		full:      make(chan struct{}),
	}
	frame, err := ic.Read()
	if err != nil {
		return
	}
	var hello inbound
	if ipc.Decode(frame, &hello) != nil || hello.Type != "hello" || hello.Version == nil ||
		hello.CallID != nil || hello.Method != nil || hello.RequestID != nil || hello.IssuedAt != nil ||
		hello.ExpectedRevision != nil || hello.Params != nil {
		c.closeWith(invalid(""), nil)
		<-ic.Done()
		return
	}
	if *hello.Version != Version {
		// Nothing more is read, so a command behind the hello never runs.
		c.closeWith(&Error{Code: ipc.CodeIncompatibleVersion}, []int{Version})
		<-ic.Done()
		return
	}
	snapshot := failure
	if engine != nil {
		c.session = engine.Connect()
		s := c.session.GetSnapshot()
		snapshot = &s
	}
	automatic := "unavailable"
	if snapshot.Service.AutomaticDiscovery {
		automatic = "available"
	}
	c.sendJSON(helloFrame{Type: "hello", Version: Version, Epoch: snapshot.Epoch, Capabilities: Capabilities{automatic}})
	if c.session != nil {
		c.session.ReceiveEnrolmentEvents(c.enqueueEvent)
		c.wg.Go(c.pump)
	}
	for {
		frame, err := ic.Read()
		if err != nil {
			break
		}
		if !c.handle(frame) {
			c.closeWith(invalid(""), nil)
			break
		}
	}
	// Closing the session first ends this connection's enrolment lease at
	// once; admitted engine work still finishes before the handler returns.
	if c.session != nil {
		c.session.Close()
	}
	c.wg.Wait()
	<-ic.Done()
}

// closeWith ends the connection with a connection-level error, after every
// frame already queued.
func (c *conn) closeWith(e *Error, supportedVersions []int) {
	payload, err := json.Marshal(errorFrame{Type: "error", Error: e, SupportedVersions: supportedVersions})
	if err != nil {
		c.ic.Close()
		return
	}
	c.ic.CloseAfter(payload)
}

type method struct {
	mutating bool
	run      func(ctx context.Context, c *conn, req service.Request, params jsontext.Value) (any, error)
}

// subscribed is subscribe's result: the reply is {} and the stream follows.
type subscribed struct{}

// decodeParams decodes params strictly. A portalID or alertID must be a
// canonical UUID, so the engine never echoes an arbitrary client string in
// error.portalID.
func decodeParams(raw jsontext.Value, p any) error {
	if ipc.Decode(raw, p) != nil {
		return invalid("params")
	}
	var ids struct {
		PortalID *string `json:"portalID"`
		AlertID  *string `json:"alertID"`
	}
	// Case-insensitive v1 is safe here: the strict Decode above already
	// refused any member not spelled exactly as p declares it.
	_ = json.Unmarshal(raw, &ids)
	switch {
	case ids.PortalID != nil && !uuidPattern.MatchString(*ids.PortalID):
		return invalid("portalID")
	case ids.AlertID != nil && !uuidPattern.MatchString(*ids.AlertID):
		return invalid("alertID")
	}
	return nil
}

func mutation[P any](call func(*service.Session, service.Request, P) (service.Receipt, error)) method {
	return method{mutating: true, run: func(_ context.Context, c *conn, req service.Request, raw jsontext.Value) (any, error) {
		var p P
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		receipt, err := call(c.session, req, p)
		if err != nil {
			return nil, err
		}
		if afterMutation != nil {
			afterMutation()
		}
		return wireReceipt(receipt), nil
	}}
}

func read[P any](call func(ctx context.Context, c *conn, p P) (any, error)) method {
	return method{run: func(ctx context.Context, c *conn, _ service.Request, raw jsontext.Value) (any, error) {
		var p P
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		return call(ctx, c, p)
	}}
}

type empty struct{}

var methods = map[string]method{
	"getSnapshot": read(func(_ context.Context, c *conn, _ empty) (any, error) {
		return c.session.GetSnapshot(), nil
	}),
	"subscribe": read(func(_ context.Context, c *conn, _ empty) (any, error) {
		if !c.subscribed.CompareAndSwap(false, true) {
			return nil, &service.Error{Code: service.CodeNotAvailable}
		}
		c.session.Subscribe(c.enqueueSnapshot)
		if afterSubscribe != nil {
			afterSubscribe()
		}
		return subscribed{}, nil
	}),
	"getRequestResult": read(func(_ context.Context, c *conn, p RequestResultParams) (any, error) {
		if !uuidPattern.MatchString(p.RequestID) {
			return nil, invalid("requestID")
		}
		issuedAt, ok := parseIssuedAt(p.IssuedAt)
		if !ok {
			return nil, invalid("issuedAt")
		}
		result, err := c.session.GetRequestResult(p.RequestID, issuedAt)
		if err != nil {
			return nil, err
		}
		if result.Receipt == nil {
			return RequestResult{NotAccepted: true}, nil
		}
		receipt := wireReceipt(*result.Receipt)
		return RequestResult{Receipt: &receipt}, nil
	}),
	"discoverLocalApps": read(func(ctx context.Context, c *conn, _ empty) (any, error) {
		candidates, err := c.session.DiscoverLocalApps(ctx)
		if err != nil {
			return nil, err
		}
		if candidates == nil {
			candidates = []discovery.Candidate{}
		}
		return DiscoverResult{Candidates: candidates}, nil
	}),
	"getDiagnostics": read(func(_ context.Context, c *conn, p DiagnosticsParams) (any, error) {
		report, err := c.session.GetDiagnostics(p.PortalID)
		if err != nil {
			return nil, err
		}
		return DiagnosticsResult{Report: report}, nil
	}),
	"createPortal":          mutation((*service.Session).CreatePortal),
	"setDesiredState":       mutation((*service.Session).SetDesiredState),
	"setDestination":        mutation((*service.Session).SetDestination),
	"setLaunchAtLoginOffer": mutation((*service.Session).SetLaunchAtLoginOffer),
	"setOperationalLogging": mutation((*service.Session).SetOperationalLogging),
	"removePortal":          mutation((*service.Session).RemovePortal),
	"retryRemoval":          mutation((*service.Session).RetryRemoval),
	"dismissAlert":          mutation((*service.Session).DismissAlert),
	"resetTailnet":          mutation((*service.Session).ResetTailnet),
	"retryRuntime": mutation(func(s *service.Session, req service.Request, _ service.RetryRuntimeParams) (service.Receipt, error) {
		return s.RetryRuntime(req)
	}),
	"resumeEnrolment": mutation((*service.Session).ResumeEnrolment),
}

// handle validates one call frame and starts it. It reports false for a
// frame that is not a well-formed call, which closes the connection.
func (c *conn) handle(frame []byte) bool {
	var in inbound
	if ipc.Decode(frame, &in) != nil || in.Type != "call" || in.Version != nil || in.CallID == nil || !uuidPattern.MatchString(*in.CallID) {
		return false
	}
	id := *in.CallID
	m, ok := methods[deref(in.Method)]
	if !ok {
		c.replyError(id, invalid("method"))
		return true
	}
	req, err := request(m, in)
	if err != nil {
		c.replyError(id, err)
		return true
	}
	if c.failure != nil {
		switch {
		case deref(in.Method) != "getSnapshot":
			c.replyError(id, &service.Error{Code: c.failure.Service.StartupFailure})
		case decodeParams(in.Params, &empty{}) != nil:
			c.replyError(id, invalid("params"))
		default:
			c.send(c.replyFrames(id, *c.failure, nil)...)
		}
		return true
	}
	release, err := c.ic.Admit()
	if err != nil {
		c.replyError(id, err)
		return true
	}
	// The engine may emit this request's enrolment event before run replies.
	held := ""
	if deref(in.Method) == "resumeEnrolment" {
		held = req.ID
		c.hold(held)
	}
	c.wg.Go(func() {
		defer release()
		c.run(id, held, m, req, in.Params)
	})
	return true
}

// request validates the envelope: mutations carry the durable request
// identity, and nothing else may.
func request(m method, in inbound) (service.Request, error) {
	if in.Params == nil {
		return service.Request{}, invalid("params")
	}
	if !m.mutating {
		switch {
		case in.RequestID != nil:
			return service.Request{}, invalid("requestID")
		case in.IssuedAt != nil:
			return service.Request{}, invalid("issuedAt")
		case in.ExpectedRevision != nil:
			return service.Request{}, invalid("expectedRevision")
		}
		return service.Request{}, nil
	}
	if in.RequestID == nil || !uuidPattern.MatchString(*in.RequestID) {
		return service.Request{}, invalid("requestID")
	}
	issuedAt, ok := parseIssuedAt(deref(in.IssuedAt))
	if !ok {
		return service.Request{}, invalid("issuedAt")
	}
	if in.ExpectedRevision == nil {
		return service.Request{}, invalid("expectedRevision")
	}
	return service.Request{ID: *in.RequestID, IssuedAt: issuedAt, ExpectedRevision: *in.ExpectedRevision}, nil
}

// run executes one admitted call under the call deadline. A mutation past
// the deadline is answered as uncertain and its engine work carries on: it
// is service-owned once admitted.
// held is the requestID whose enrolment events wait for this reply, or "".
func (c *conn) run(id, held string, m method, req service.Request, params jsontext.Value) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	var replied atomic.Bool
	fired := make(chan struct{})
	timer := time.AfterFunc(callTimeout, func() {
		defer close(fired)
		if !replied.CompareAndSwap(false, true) {
			return
		}
		if afterDeadline != nil {
			afterDeadline()
		}
		if m.mutating {
			payload, _ := json.Marshal(replyFrame{Type: "reply", CallID: id, Uncertain: true})
			c.sendReply(held, payload)
		} else {
			c.replyError(id, &service.Error{Code: service.CodeRuntimeUnavailable})
		}
	})
	result, err := m.run(ctx, c, req, params)
	if !timer.Stop() {
		<-fired
	}
	if replied.CompareAndSwap(false, true) {
		c.reply(id, held, result, err)
	} else if _, ok := result.(subscribed); ok {
		// The client was told subscribe failed, so it expects no stream. The
		// error frame follows the queued runtimeUnavailable reply.
		c.closeWith(&Error{Code: ipc.Code(service.CodeRuntimeUnavailable), Retryable: true}, nil)
	}
}

func (c *conn) reply(id, held string, result any, err error) {
	c.sendReply(held, c.replyFrames(id, result, err)...)
	if _, ok := result.(subscribed); ok && err == nil {
		close(c.ready)
	}
}

func (c *conn) replyFrames(id string, result any, err error) [][]byte {
	if err != nil {
		return [][]byte{errorReply(id, err)}
	}
	switch r := result.(type) {
	case service.Snapshot:
		frames, err := snapshotFrames(r, id, c.ic.MaxFrame())
		if err != nil {
			return [][]byte{errorReply(id, err)}
		}
		return frames
	case subscribed:
		result = struct{}{}
	}
	payload, err := json.Marshal(replyFrame{Type: "reply", CallID: id, Result: result})
	if err != nil || len(payload) > c.ic.MaxFrame() {
		return [][]byte{errorReply(id, &service.Error{Code: service.CodeCapacityExceeded})}
	}
	return [][]byte{payload}
}

func errorReply(id string, err error) []byte {
	payload, _ := json.Marshal(replyFrame{Type: "reply", CallID: id, Error: wireError(err)})
	return payload
}

func (c *conn) replyError(id string, err error) { c.send(errorReply(id, err)) }

func (c *conn) hold(requestID string) {
	c.heldMu.Lock()
	defer c.heldMu.Unlock()
	h := c.held[requestID]
	if h == nil {
		h = &heldEvents{}
		c.held[requestID] = h
	}
	h.calls++
}

// park keeps back an event whose resumeEnrolment reply is still to come.
func (c *conn) park(e service.EnrolmentEvent) bool {
	c.heldMu.Lock()
	defer c.heldMu.Unlock()
	h := c.held[e.RequestID]
	if h != nil {
		h.events = append(h.events, e)
	}
	return h != nil
}

func (c *conn) unhold(requestID string) []service.EnrolmentEvent {
	c.heldMu.Lock()
	defer c.heldMu.Unlock()
	h := c.held[requestID]
	events := h.events
	h.events = nil
	if h.calls--; h.calls == 0 {
		delete(c.held, requestID)
	}
	return events
}

// sendReply queues a reply and then, in the same contiguous run, the
// enrolment events parked behind it.
func (c *conn) sendReply(held string, frames ...[]byte) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	err := c.sendLocked(frames...)
	if held == "" {
		return err
	}
	for _, e := range c.unhold(held) {
		if err == nil {
			payload, _ := json.Marshal(enrolmentEventFrame(e))
			err = c.sendLocked(payload)
		}
	}
	return err
}

func (c *conn) sendJSON(frame any) error {
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return c.send(payload)
}

// send queues frames contiguously. A failure means the connection is going
// away: the transport disconnects a client whose queue overflows.
func (c *conn) send(frames ...[]byte) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return c.sendLocked(frames...)
}

func (c *conn) sendLocked(frames ...[]byte) error {
	for _, frame := range frames {
		if err := c.ic.Send(frame); err != nil {
			return err
		}
	}
	return nil
}

// failed handles a pump send failure. slowConsumer and a closed or ending
// connection are already closing in the transport; closing here would cut
// their final frame. Anything else must not leave the stream silently dead.
func (c *conn) failed(err error) {
	var e *Error
	if errors.Is(err, net.ErrClosed) || errors.As(err, &e) && e.Code == ipc.CodeSlowConsumer {
		return
	}
	c.ic.Close()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
