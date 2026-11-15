package management

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-json-experiment/json/jsontext"

	"github.com/chrisbanes/portico/helper/internal/discovery"
	"github.com/chrisbanes/portico/helper/internal/ipc"
	"github.com/chrisbanes/portico/helper/internal/service"
)

// callTimeout is the deadline for one call; tests lower it.
var callTimeout = 10 * time.Second

// closeGrace is how long a final error frame gets to reach the client
// before the connection closes; the transport has no flush.
const closeGrace = time.Second

// afterMutation, for tests only, runs once the engine has returned a receipt
// and before the reply is sent.
var afterMutation func()

// Serve answers management v1 on listener until listener.Close, which waits
// for every connection's in-flight engine work. limits must be the ones the
// listener was opened with: snapshots are paged to fit limits.MaxFrame.
func Serve(listener *ipc.Listener, limits ipc.Limits, engine *service.Engine) error {
	return listener.Serve(func(c *ipc.Conn) { serveConn(c, engine, limits.MaxFrame) })
}

// conn is one client connection with its own engine session.
type conn struct {
	ic       *ipc.Conn
	session  *service.Session
	maxFrame int
	wg       sync.WaitGroup

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
}

// queueSize bounds the snapshots, and separately the enrolment events,
// waiting to be encoded for one connection.
const queueSize = 128

func serveConn(ic *ipc.Conn, engine *service.Engine, maxFrame int) {
	c := &conn{
		ic: ic, maxFrame: maxFrame,
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
		return
	}
	if *hello.Version != Version {
		c.closeWith(&Error{Code: ipc.CodeIncompatibleVersion}, []int{Version})
		return
	}
	c.session = engine.Connect()
	snapshot := c.session.GetSnapshot()
	automatic := "unavailable"
	if snapshot.Service.AutomaticDiscovery {
		automatic = "available"
	}
	c.sendJSON(helloFrame{Type: "hello", Version: Version, Epoch: snapshot.Epoch, Capabilities: Capabilities{automatic}})
	c.session.ReceiveEnrolmentEvents(c.enqueueEvent)
	c.wg.Go(c.pump)
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
	c.session.Close()
	c.wg.Wait()
}

// closeWith sends a final connection-level error and closes once it has had
// closeGrace to reach the client. Nothing more is read.
func (c *conn) closeWith(e *Error, supportedVersions []int) {
	c.sendJSON(errorFrame{Type: "error", Error: e, SupportedVersions: supportedVersions})
	select {
	case <-c.ic.Done():
	case <-time.After(closeGrace):
	}
	c.ic.Close()
}

type method struct {
	mutating bool
	run      func(ctx context.Context, c *conn, req service.Request, params jsontext.Value) (any, error)
}

// subscribed is subscribe's result: the reply is {} and the stream follows.
type subscribed struct{}

func mutation[P any](call func(*service.Session, service.Request, P) (service.Receipt, error)) method {
	return method{mutating: true, run: func(_ context.Context, c *conn, req service.Request, raw jsontext.Value) (any, error) {
		var p P
		if ipc.Decode(raw, &p) != nil {
			return nil, invalid("params")
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
		if ipc.Decode(raw, &p) != nil {
			return nil, invalid("params")
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
	release, err := c.ic.Admit()
	if err != nil {
		c.replyError(id, err)
		return true
	}
	c.wg.Go(func() {
		defer release()
		c.run(id, m, req, in.Params)
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
func (c *conn) run(id string, m method, req service.Request, params jsontext.Value) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	var replied atomic.Bool
	timer := time.AfterFunc(callTimeout, func() {
		if !replied.CompareAndSwap(false, true) {
			return
		}
		if m.mutating {
			c.sendJSON(replyFrame{Type: "reply", CallID: id, Uncertain: true})
		} else {
			c.replyError(id, &service.Error{Code: service.CodeRuntimeUnavailable})
		}
	})
	result, err := m.run(ctx, c, req, params)
	timer.Stop()
	if replied.CompareAndSwap(false, true) {
		c.reply(id, result, err)
	} else if _, ok := result.(subscribed); ok {
		// The client was told subscribe failed, so it expects no stream.
		c.ic.Close()
	}
}

func (c *conn) reply(id string, result any, err error) {
	if err != nil {
		c.replyError(id, err)
		return
	}
	switch r := result.(type) {
	case service.Snapshot:
		frames, err := snapshotFrames(r, id, c.maxFrame)
		if err != nil {
			c.replyError(id, err)
			return
		}
		c.send(frames...)
	case subscribed:
		c.sendJSON(replyFrame{Type: "reply", CallID: id, Result: struct{}{}})
		close(c.ready)
	default:
		payload, err := json.Marshal(replyFrame{Type: "reply", CallID: id, Result: r})
		if err != nil || len(payload) > c.maxFrame {
			c.replyError(id, &service.Error{Code: service.CodeCapacityExceeded})
			return
		}
		c.send(payload)
	}
}

func (c *conn) replyError(id string, err error) {
	c.sendJSON(replyFrame{Type: "reply", CallID: id, Error: wireError(err)})
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
	for _, frame := range frames {
		if err := c.ic.Send(frame); err != nil {
			return err
		}
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
