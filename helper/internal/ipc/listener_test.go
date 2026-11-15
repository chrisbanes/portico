package ipc

import (
	"bytes"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSameUserPeersVerifyEachOther(t *testing.T) {
	s := serve(t, testLimits(), func(c *Conn) {
		frame, err := c.Read()
		if err != nil {
			return
		}
		if err := c.Send(append([]byte("echo:"), frame...)); err != nil {
			t.Errorf("Send: %v", err)
		}
		<-c.Done()
	})
	c := s.dial(t)
	if err := WriteFrame(c, []byte(`{"type":"hello","version":1}`)); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFrame(c, MaxFrame)
	if err != nil || string(got) != `echo:{"type":"hello","version":1}` {
		t.Fatalf("reply = (%q, %v)", got, err)
	}
}

func TestFailedCredentialLookupClosesTheConnection(t *testing.T) {
	lookup := peerUID
	t.Cleanup(func() { peerUID = lookup })
	peerUID = func(*net.UnixConn) (int, error) { return -1, errors.New("lookup failed") }
	var handled atomic.Bool
	s := serve(t, testLimits(), func(*Conn) { handled.Store(true) })

	if _, err := Dial(t.Context(), s.endpoint); !errors.Is(err, ErrPeerRejected) {
		t.Errorf("Dial with a failed lookup = %v, want rejected", err)
	}
	raw, err := net.Dial("unix", s.endpoint.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if got := readClosed(t, raw); len(got) != 0 || handled.Load() {
		t.Fatalf("an unverified peer got %d bytes, handled %v", len(got), handled.Load())
	}
}

func TestStalledFramesCloseTheConnection(t *testing.T) {
	limits := testLimits()
	readErr := make(chan error, 4)
	s := serve(t, limits, func(c *Conn) {
		for {
			if _, err := c.Read(); err != nil {
				readErr <- err
				return
			}
		}
	})
	for name, send := range map[string]func(net.Conn) error{
		"no handshake": func(net.Conn) error { return nil },
		"partial handshake": func(c net.Conn) error {
			_, err := c.Write(header(10)[:2])
			return err
		},
		"partial later frame": func(c net.Conn) error {
			if err := WriteFrame(c, []byte("{}")); err != nil {
				return err
			}
			// Idle time between frames is fine: subscriptions have no deadline.
			time.Sleep(3 * limits.FrameTimeout)
			_, err := c.Write(append(header(10), "{"...))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := s.dial(t)
			started := time.Now()
			if err := send(c); err != nil {
				t.Fatal(err)
			}
			readClosed(t, c)
			if elapsed := time.Since(started); elapsed > 3*limits.FrameTimeout+2*time.Second {
				t.Errorf("closed after %v", elapsed)
			}
			if err := <-readErr; !errors.Is(err, net.ErrClosed) || strings.Contains(err.Error(), s.endpoint.dir) {
				t.Errorf("Read = %v, want a closed connection without the endpoint path", err)
			}
		})
	}
}

func TestOversizedFrameClosesTheConnection(t *testing.T) {
	s := serve(t, testLimits(), func(c *Conn) {
		if _, err := c.Read(); !errors.Is(err, ErrInvalidFrame) {
			t.Errorf("Read = %v, want invalid frame", err)
		}
	})
	c := s.dial(t)
	if _, err := c.Write(header(MaxFrame + 1)); err != nil {
		t.Fatal(err)
	}
	readClosed(t, c)
}

func TestSeventeenthClientIsRefused(t *testing.T) {
	limits := testLimits()
	var mu sync.Mutex
	conns := 0
	s := serve(t, limits, func(c *Conn) {
		mu.Lock()
		conns++
		mu.Unlock()
		<-c.Done()
	})
	for range limits.Clients {
		s.dial(t)
	}
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return conns == limits.Clients })
	c := s.dial(t)
	got, err := ReadFrame(c, MaxFrame)
	if err != nil || !bytes.Contains(got, []byte(`"capacityExceeded"`)) {
		t.Fatalf("17th client got (%s, %v), want capacityExceeded", got, err)
	}
	readClosed(t, c)
}

func TestInFlightCallLimits(t *testing.T) {
	limits := testLimits()
	conns := make(chan *Conn, 9)
	s := serve(t, limits, func(c *Conn) {
		conns <- c
		<-c.Done()
	})
	var all []*Conn
	for range 9 {
		s.dial(t)
		all = append(all, <-conns)
	}
	var releases []func()
	for i := range 8 {
		for range limits.CallsPerConn {
			release, err := all[i].Admit()
			if err != nil {
				t.Fatalf("call within the limits refused: %v", err)
			}
			releases = append(releases, release)
		}
		if i == 0 {
			if _, err := all[0].Admit(); !isCode(err, CodeCapacityExceeded) {
				t.Errorf("9th call on one connection = %v, want capacityExceeded", err)
			}
		}
	}
	if _, err := all[8].Admit(); !isCode(err, CodeCapacityExceeded) {
		t.Errorf("65th call = %v, want capacityExceeded", err)
	}
	releases[0]()
	releases[0]()
	if _, err := all[8].Admit(); err != nil {
		t.Errorf("call after a release = %v", err)
	}
	if _, err := all[8].Admit(); !isCode(err, CodeCapacityExceeded) {
		t.Errorf("a double release freed two global slots: %v", err)
	}
}

func TestSlowConsumerIsDisconnectedWithoutBlockingTheProducer(t *testing.T) {
	limits := testLimits()
	// Large frames fill the socket buffer, so the writer stalls on the first.
	frame := bytes.Repeat([]byte("x"), 256<<10)
	result := make(chan int, 1)
	s := serve(t, limits, func(c *Conn) {
		for i := 1; ; i++ {
			started := time.Now()
			err := c.Send(frame)
			if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
				t.Errorf("Send %d blocked for %v", i, elapsed)
			}
			if err != nil {
				if !isCode(err, CodeSlowConsumer) {
					t.Errorf("Send %d = %v", i, err)
				}
				result <- i
				<-c.Done()
				return
			}
		}
	})
	c := s.dial(t)
	overflowed := <-result
	// One frame may already be with the writer when the queue fills.
	if overflowed != limits.OutputQueue+1 && overflowed != limits.OutputQueue+2 {
		t.Errorf("overflowed at Send %d, want a backlog of %d", overflowed, limits.OutputQueue)
	}
	// The frame the writer had started, if any, completes; then the error.
	for i := 0; ; i++ {
		got, err := ReadFrame(c, MaxFrame)
		if err != nil || i > 1 {
			t.Fatalf("frame %d: (%.80s, %v), want slowConsumer after at most one data frame", i, got, err)
		}
		if bytes.Contains(got, []byte(`"slowConsumer"`)) {
			break
		}
		if len(got) != len(frame) {
			t.Fatalf("frame %d is %d bytes, want a whole data frame", i, len(got))
		}
	}
	readClosed(t, c)
}

func TestCloseWaitsForEveryConnection(t *testing.T) {
	handled := make(chan chan struct{}, 3)
	s := serve(t, testLimits(), func(c *Conn) {
		done := make(chan struct{})
		handled <- done
		defer close(done)
		_, _ = c.Read()
	})
	var dones []chan struct{}
	for range 3 {
		s.dial(t)
		dones = append(dones, <-handled)
	}
	if err := s.listener.Close(); err != nil {
		t.Fatal(err)
	}
	for i, done := range dones {
		select {
		case <-done:
		default:
			t.Errorf("connection %d's handler outlived Close", i)
		}
	}
	select {
	case <-s.served:
	default:
		t.Error("Serve outlived Close")
	}
	if _, err := Dial(t.Context(), s.endpoint); err == nil {
		t.Error("Dial succeeded after Close")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
