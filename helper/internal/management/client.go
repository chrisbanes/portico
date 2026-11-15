package management

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/chrisbanes/portico/helper/internal/ipc"
	"github.com/chrisbanes/portico/helper/internal/service"
)

// ErrUncertain means a mutation's outcome is unknown: its deadline passed or
// the connection dropped. Recover it with getRequestResult and the same
// request ID; never allocate a new one.
var ErrUncertain = errors.New("management: mutation outcome uncertain")

// Mutation is the durable request identity a mutating call carries.
type Mutation struct {
	RequestID        string
	IssuedAt         time.Time
	ExpectedRevision uint64
}

// Client is a minimal typed management v1 client.
type Client struct {
	conn         *net.UnixConn
	Epoch        string
	Capabilities Capabilities
	// record, for tests, sees every frame the client reads.
	record func([]byte)

	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[string]chan clientFrame
	err     *Error

	snapshots chan service.Snapshot
	enrolment chan service.EnrolmentEvent
	done      chan struct{}
	// closing is closed by Close, so readLoop never blocks delivering to a
	// channel nobody reads.
	closing   chan struct{}
	closeOnce sync.Once
}

// clientFrame is every member a server frame may carry. Unknown members are
// tolerated, as the contract asks of clients.
type clientFrame struct {
	Type              string                   `json:"type"`
	CallID            string                   `json:"callID"`
	Result            json.RawMessage          `json:"result"`
	Error             *Error                   `json:"error"`
	Uncertain         bool                     `json:"uncertain"`
	Version           int                      `json:"version"`
	Epoch             string                   `json:"epoch"`
	Capabilities      Capabilities             `json:"capabilities"`
	SupportedVersions []int                    `json:"supportedVersions"`
	Snapshot          *service.Snapshot        `json:"snapshot"`
	Sequence          uint64                   `json:"sequence"`
	Revision          uint64                   `json:"revision"`
	PageCount         int                      `json:"pageCount"`
	PageIndex         int                      `json:"pageIndex"`
	Portals           []service.PortalSnapshot `json:"portals"`
	Event             string                   `json:"event"`
	RequestID         string                   `json:"requestID"`
	PortalID          string                   `json:"portalID"`
	Nonce             string                   `json:"nonce"`
	URL               string                   `json:"url"`
}

// Dial connects and completes the hello exchange. A version mismatch
// returns the server's incompatibleVersion *Error.
func Dial(ctx context.Context, endpoint ipc.Endpoint) (*Client, error) {
	return dial(ctx, endpoint, nil)
}

func dial(ctx context.Context, endpoint ipc.Endpoint, record func([]byte)) (*Client, error) {
	conn, err := ipc.Dial(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	return newClient(ctx, conn, record)
}

func newClient(ctx context.Context, conn *net.UnixConn, record func([]byte)) (*Client, error) {
	c := &Client{
		conn: conn, record: record,
		pending:   map[string]chan clientFrame{},
		snapshots: make(chan service.Snapshot, 1024),
		enrolment: make(chan service.EnrolmentEvent, 16),
		done:      make(chan struct{}),
		closing:   make(chan struct{}),
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	hello, err := c.handshake()
	_ = conn.SetDeadline(time.Time{})
	if err != nil {
		conn.Close()
		return nil, err
	}
	c.Epoch, c.Capabilities = hello.Epoch, hello.Capabilities
	go c.readLoop()
	return c, nil
}

func (c *Client) handshake() (clientFrame, error) {
	payload, _ := json.Marshal(map[string]any{"type": "hello", "version": Version})
	if err := ipc.WriteFrame(c.conn, payload); err != nil {
		return clientFrame{}, err
	}
	frame, err := c.readFrame()
	switch {
	case err != nil:
		return clientFrame{}, err
	case frame.Type == "error" && frame.Error != nil:
		return clientFrame{}, frame.Error
	case frame.Type != "hello" || frame.Version != Version:
		return clientFrame{}, &Error{Code: ipc.CodeIncompatibleVersion}
	}
	return frame, nil
}

func (c *Client) readFrame() (clientFrame, error) {
	payload, err := ipc.ReadFrame(c.conn, ipc.MaxFrame)
	if err != nil {
		return clientFrame{}, err
	}
	if c.record != nil {
		c.record(payload)
	}
	var frame clientFrame
	if err := json.Unmarshal(payload, &frame); err != nil {
		return clientFrame{}, err
	}
	return frame, nil
}

func (c *Client) readLoop() {
	defer func() {
		c.conn.Close()
		close(c.snapshots)
		close(c.enrolment)
		close(c.done)
	}()
	sets := assembler{}
	for {
		frame, err := c.readFrame()
		if err != nil {
			return
		}
		switch frame.Type {
		case "reply":
			c.deliver(frame)
		case "snapshot":
			if frame.Snapshot != nil && !c.put(*frame.Snapshot) {
				return
			}
		case "snapshotBegin", "snapshotPage", "snapshotEnd":
			s, ok := sets.feed(frame)
			switch {
			case !ok:
			case frame.CallID != "":
				result, _ := json.Marshal(s)
				c.deliver(clientFrame{Type: "reply", CallID: frame.CallID, Result: result})
			case !c.put(s):
				return
			}
		case "enrolment":
			select {
			case c.enrolment <- service.EnrolmentEvent{Kind: service.EnrolmentEventKind(frame.Event), RequestID: frame.RequestID, PortalID: frame.PortalID, Nonce: frame.Nonce, URL: frame.URL}:
			case <-c.closing:
				return
			}
		case "error":
			c.mu.Lock()
			c.err = frame.Error
			c.mu.Unlock()
			return
		}
	}
}

// put delivers a subscription snapshot, or reports false once Close is called.
func (c *Client) put(s service.Snapshot) bool {
	select {
	case c.snapshots <- s:
		return true
	case <-c.closing:
		return false
	}
}

func (c *Client) deliver(frame clientFrame) {
	c.mu.Lock()
	ch := c.pending[frame.CallID]
	delete(c.pending, frame.CallID)
	c.mu.Unlock()
	if ch != nil {
		ch <- frame
	}
}

// Call makes one call and decodes its result into result, which may be nil.
// mutation is required for mutating methods and must be nil otherwise.
// Errors are a reply's *Error, ErrUncertain (joined with ctx.Err() when ctx
// ends a written mutation), ctx.Err(), or a transport error.
func (c *Client) Call(ctx context.Context, method string, mutation *Mutation, params, result any) error {
	if params == nil {
		params = struct{}{}
	}
	id := newUUID()
	frame := callFrame{Type: "call", CallID: id, Method: method, Params: params}
	if mutation != nil {
		issuedAt := mutation.IssuedAt.UTC().Format(time.RFC3339Nano)
		frame.RequestID, frame.IssuedAt, frame.ExpectedRevision = &mutation.RequestID, &issuedAt, &mutation.ExpectedRevision
	}
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	reply := make(chan clientFrame, 1)
	c.mu.Lock()
	c.pending[id] = reply
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	c.writeMu.Lock()
	err = ipc.WriteFrame(c.conn, payload)
	c.writeMu.Unlock()
	if err != nil {
		return c.lost(mutation)
	}
	select {
	case r := <-reply:
		return decodeReply(r, result)
	case <-ctx.Done():
		// The frame was written, so a mutation may already be accepted.
		if mutation != nil {
			return errors.Join(ErrUncertain, ctx.Err())
		}
		return ctx.Err()
	case <-c.done:
		select {
		case r := <-reply:
			return decodeReply(r, result)
		default:
			return c.lost(mutation)
		}
	}
}

// lost is the error for a call whose connection dropped before its reply.
func (c *Client) lost(mutation *Mutation) error {
	if mutation != nil {
		return ErrUncertain
	}
	return net.ErrClosed
}

func decodeReply(r clientFrame, result any) error {
	switch {
	case r.Uncertain:
		return ErrUncertain
	case r.Error != nil:
		return r.Error
	case result != nil:
		return json.Unmarshal(r.Result, result)
	}
	return nil
}

// Snapshots delivers each complete subscription snapshot in order. The
// caller must keep reading, as the server disconnects a slow client. It is
// closed when the connection ends.
func (c *Client) Snapshots() <-chan service.Snapshot { return c.snapshots }

// Enrolment delivers this connection's private enrolment events.
func (c *Client) Enrolment() <-chan service.EnrolmentEvent { return c.enrolment }

// Done is closed once the connection has ended.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err is the connection-level error the server closed with, if any.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		return nil
	}
	return c.err
}

func (c *Client) Close() error {
	c.closeOnce.Do(func() { close(c.closing) })
	err := c.conn.Close()
	<-c.done
	return err
}

// assembler rebuilds paged snapshot sets, keyed by callID ("" for the
// subscription). A set is presented only when complete: every page in order,
// as many as announced, with matching sequence. Anything else drops it.
type assembler map[string]*pagedSet

type pagedSet struct {
	begin    clientFrame
	snapshot service.Snapshot
	pages    int
}

func (a assembler) feed(f clientFrame) (service.Snapshot, bool) {
	switch f.Type {
	case "snapshotBegin":
		a[f.CallID] = &pagedSet{begin: f}
	case "snapshotPage":
		set := a[f.CallID]
		if set == nil {
			return service.Snapshot{}, false
		}
		if f.PageIndex != set.pages || f.PageIndex >= set.begin.PageCount || (f.PageIndex == 0) != (f.Snapshot != nil) {
			delete(a, f.CallID)
			return service.Snapshot{}, false
		}
		if f.Snapshot != nil {
			set.snapshot = *f.Snapshot
			set.snapshot.Portals = nil
		}
		set.snapshot.Portals = append(set.snapshot.Portals, f.Portals...)
		set.pages++
	case "snapshotEnd":
		set := a[f.CallID]
		delete(a, f.CallID)
		if set == nil || set.pages == 0 || set.pages != set.begin.PageCount || f.Sequence != set.begin.Sequence ||
			set.snapshot.Sequence != set.begin.Sequence || set.snapshot.Epoch != set.begin.Epoch || set.snapshot.Revision != set.begin.Revision {
			return service.Snapshot{}, false
		}
		if set.snapshot.Portals == nil {
			set.snapshot.Portals = []service.PortalSnapshot{}
		}
		return set.snapshot, true
	}
	return service.Snapshot{}, false
}
