package ipc

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-json-experiment/json"
)

// Limits bound what one service accepts. Tests shorten them.
type Limits struct {
	Clients      int           // connected clients
	CallsPerConn int           // in-flight calls on one connection
	Calls        int           // in-flight calls across connections
	OutputQueue  int           // frames queued for one client
	FrameTimeout time.Duration // first frame, and the rest of any started frame
	MaxFrame     int           // payload bytes
}

// DefaultLimits are the management v1 limits.
var DefaultLimits = Limits{Clients: 16, CallsPerConn: 8, Calls: 64, OutputQueue: 128, FrameTimeout: 5 * time.Second, MaxFrame: MaxFrame}

var errAccept = errors.New("ipc: accept failed")

// slowConsumerGrace is how long a slow client's last frame may take before
// the slowConsumer error is abandoned and the connection just closes.
const slowConsumerGrace = time.Second

// Listener serves one endpoint while its owner holds the lifetime lock.
type Listener struct {
	ln     *net.UnixListener
	limits Limits
	calls  atomic.Int32

	mu     sync.Mutex
	closed bool
	conns  map[*Conn]struct{}
	wg     sync.WaitGroup
}

// Listen binds the endpoint. lock is the service's lifetime lock file; Listen
// re-takes its exclusive flock without blocking, so it fails with
// ownershipBusy unless this process holds that lock. Only then may it remove a
// stale socket, and never a live one or anything that is not this user's
// socket.
func Listen(endpoint Endpoint, lock *os.File, limits Limits) (*Listener, error) {
	if os.Geteuid() == 0 {
		return nil, ErrRoot
	}
	if lock == nil {
		return nil, &Error{Code: CodeOwnershipBusy}
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, &Error{Code: CodeOwnershipBusy}
		}
		return nil, ErrUnsafeEndpoint
	}
	if err := os.Mkdir(endpoint.dir, 0o700); err != nil && !os.IsExist(err) {
		return nil, ErrUnsafeEndpoint
	}
	if err := endpoint.checkDir(); err != nil {
		return nil, err
	}
	exists, err := endpoint.socketInfo()
	if err != nil {
		return nil, err
	}
	if exists {
		probe, err := net.DialTimeout("unix", endpoint.socket, time.Second)
		if err == nil {
			probe.Close()
			return nil, &Error{Code: CodeOwnershipBusy}
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			return nil, ErrUnsafeEndpoint
		}
		if err := os.Remove(endpoint.socket); err != nil {
			return nil, ErrUnsafeEndpoint
		}
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: endpoint.socket, Net: "unix"})
	if err != nil {
		return nil, ErrUnsafeEndpoint
	}
	// The 0700 directory already keeps others out while the mode is set.
	if err := os.Chmod(endpoint.socket, 0o600); err != nil {
		ln.Close()
		return nil, ErrUnsafeEndpoint
	}
	return &Listener{ln: ln, limits: limits, conns: map[*Conn]struct{}{}}, nil
}

// Serve accepts connections until Close, running handle for each verified
// peer on its own goroutine. It returns nil after Close.
func (l *Listener) Serve(handle func(*Conn)) error {
	if !l.track() {
		return nil
	}
	defer l.wg.Done()
	for {
		c, err := l.ln.AcceptUnix()
		if err != nil {
			if l.isClosed() {
				return nil
			}
			return errAccept
		}
		if !l.track() {
			c.Close()
			return nil
		}
		go l.serveConn(c, handle)
	}
}

// track counts a goroutine Close must wait for, unless Close has begun.
func (l *Listener) track() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return false
	}
	l.wg.Add(1)
	return true
}

func (l *Listener) isClosed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

func (l *Listener) serveConn(c *net.UnixConn, handle func(*Conn)) {
	defer l.wg.Done()
	// An unauthenticated peer is closed before any byte is read or sent.
	if verifyPeer(c) != nil {
		c.Close()
		return
	}
	l.mu.Lock()
	if l.closed || len(l.conns) >= l.limits.Clients {
		l.mu.Unlock()
		_ = c.SetWriteDeadline(time.Now().Add(l.limits.FrameTimeout))
		_ = writeError(c, CodeCapacityExceeded)
		c.Close()
		return
	}
	conn := &Conn{
		c: c, l: l,
		out:      make(chan []byte, l.limits.OutputQueue),
		overflow: make(chan struct{}),
		done:     make(chan struct{}),
		written:  make(chan struct{}),
	}
	l.conns[conn] = struct{}{}
	l.mu.Unlock()
	go conn.write()
	handle(conn)
	conn.Close()
	<-conn.written
	l.mu.Lock()
	delete(l.conns, conn)
	l.mu.Unlock()
}

// Close stops accepting, closes every connection and returns once every
// goroutine the Listener started, including each handle call, has returned.
func (l *Listener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	conns := make([]*Conn, 0, len(l.conns))
	for conn := range l.conns {
		conns = append(conns, conn)
	}
	l.mu.Unlock()
	err := l.ln.Close()
	for _, conn := range conns {
		conn.Close()
	}
	l.wg.Wait()
	return err
}

// Conn is one verified client connection on the server side.
type Conn struct {
	c       *net.UnixConn
	l       *Listener
	out     chan []byte
	started bool // read by the handle goroutine only
	calls   atomic.Int32

	overflow     chan struct{}
	overflowOnce sync.Once
	done         chan struct{}
	closeOnce    sync.Once
	written      chan struct{}
}

// Read returns the next frame. The first frame, and the rest of any frame
// once its first byte arrives, must come within FrameTimeout; between frames
// there is no deadline, for subscriptions. Any failure closes the connection
// and returns ErrInvalidFrame or net.ErrClosed.
func (c *Conn) Read() ([]byte, error) {
	var header [4]byte
	start := 0
	if c.started {
		if err := c.c.SetReadDeadline(time.Time{}); err != nil {
			return nil, c.closeWith(err)
		}
		if _, err := io.ReadFull(c.c, header[:1]); err != nil {
			return nil, c.closeWith(err)
		}
		start = 1
	}
	if err := c.c.SetReadDeadline(time.Now().Add(c.l.limits.FrameTimeout)); err != nil {
		return nil, c.closeWith(err)
	}
	if _, err := io.ReadFull(c.c, header[start:]); err != nil {
		return nil, c.closeWith(err)
	}
	payload, err := readPayload(c.c, header, c.l.limits.MaxFrame)
	if err != nil {
		return nil, c.closeWith(err)
	}
	c.started = true
	return payload, nil
}

// closeWith closes the connection and reports err without its addresses,
// which name the endpoint.
func (c *Conn) closeWith(err error) error {
	c.Close()
	if errors.Is(err, ErrInvalidFrame) {
		return err
	}
	return net.ErrClosed
}

// Send queues one frame without blocking. A full queue disconnects the client
// with slowConsumer instead of waiting for it.
func (c *Conn) Send(payload []byte) error {
	if len(payload) == 0 || len(payload) > c.l.limits.MaxFrame {
		return ErrInvalidFrame
	}
	select {
	case <-c.done:
		return net.ErrClosed
	default:
	}
	select {
	case c.out <- payload:
		return nil
	default:
		c.overflowOnce.Do(func() {
			close(c.overflow)
			// A frame already being written gets a grace period, so the
			// slowConsumer error can follow it if the client makes room.
			_ = c.c.SetWriteDeadline(time.Now().Add(slowConsumerGrace))
		})
		return &Error{Code: CodeSlowConsumer, Retryable: true}
	}
}

func (c *Conn) write() {
	defer close(c.written)
	for {
		select {
		case <-c.overflow:
			c.writeSlowConsumer()
			return
		case <-c.done:
			return
		case payload := <-c.out:
			select {
			case <-c.overflow:
				c.writeSlowConsumer()
				return
			default:
			}
			if WriteFrame(c.c, payload) != nil {
				c.Close()
				return
			}
		}
	}
}

func (c *Conn) writeSlowConsumer() {
	_ = writeError(c.c, CodeSlowConsumer)
	c.Close()
}

// Admit reserves one in-flight call slot, refusing with capacityExceeded past
// the per-connection or global limit. The returned release is idempotent.
func (c *Conn) Admit() (release func(), err error) {
	if c.calls.Add(1) > int32(c.l.limits.CallsPerConn) {
		c.calls.Add(-1)
		return nil, &Error{Code: CodeCapacityExceeded, Retryable: true}
	}
	if c.l.calls.Add(1) > int32(c.l.limits.Calls) {
		c.l.calls.Add(-1)
		c.calls.Add(-1)
		return nil, &Error{Code: CodeCapacityExceeded, Retryable: true}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			c.calls.Add(-1)
			c.l.calls.Add(-1)
		})
	}, nil
}

// Close disconnects the client. It is safe to call more than once.
func (c *Conn) Close() {
	c.closeOnce.Do(func() {
		close(c.done)
		c.c.Close()
	})
}

// Done is closed once the connection is closed.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Dial connects to a running service after checking the endpoint is this
// user's, and returns the connection only once the server's kernel-reported
// UID is this user's; nothing is sent before that.
func Dial(ctx context.Context, endpoint Endpoint) (*net.UnixConn, error) {
	if os.Geteuid() == 0 {
		return nil, ErrRoot
	}
	if err := endpoint.checkDir(); err != nil {
		return nil, err
	}
	exists, err := endpoint.socketInfo()
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotRunning
	}
	var dialer net.Dialer
	c, err := dialer.DialContext(ctx, "unix", endpoint.socket)
	if err != nil {
		return nil, ErrNotRunning
	}
	conn := c.(*net.UnixConn)
	if err := verifyPeer(conn); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// peerUID is replaced in tests to simulate a failed credential lookup.
var peerUID = func(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return -1, err
	}
	uid, lookupErr := -1, error(nil)
	if err := raw.Control(func(fd uintptr) { uid, lookupErr = platformPeerUID(int(fd)) }); err != nil {
		return -1, err
	}
	return uid, lookupErr
}

// verifyPeer requires the peer's kernel-reported UID to be this process's
// own nonzero effective UID.
func verifyPeer(c *net.UnixConn) error {
	euid := os.Geteuid()
	uid, err := peerUID(c)
	if err != nil || euid == 0 || uid != euid {
		return ErrPeerRejected
	}
	return nil
}

type errorFrame struct {
	Type  string `json:"type"`
	Error Error  `json:"error"`
}

func writeError(w io.Writer, code Code) error {
	payload, err := json.Marshal(errorFrame{Type: "error", Error: Error{Code: code, Retryable: true}})
	if err != nil {
		return err
	}
	return WriteFrame(w, payload)
}
