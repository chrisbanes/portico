package ipc

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// shortTemp is a temporary base directory short enough for sun_path, unlike
// t.TempDir, whose path includes the test name.
func shortTemp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ipc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func lockFile(t *testing.T) *os.File {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(t.TempDir(), "service-owner.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	return file
}

func testEndpoint(t *testing.T) Endpoint {
	t.Helper()
	endpoint, err := EndpointIn(shortTemp(t), Test)
	if err != nil {
		t.Fatal(err)
	}
	return endpoint
}

type server struct {
	endpoint Endpoint
	listener *Listener
	served   chan struct{}
}

// serve listens on a fresh endpoint and serves handle until the test ends.
func serve(t *testing.T, limits Limits, handle func(*Conn)) *server {
	t.Helper()
	endpoint := testEndpoint(t)
	listener, err := Listen(endpoint, lockFile(t), limits)
	if err != nil {
		t.Fatal(err)
	}
	s := &server{endpoint: endpoint, listener: listener, served: make(chan struct{})}
	go func() {
		defer close(s.served)
		if err := listener.Serve(handle); err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()
	t.Cleanup(func() { listener.Close() })
	return s
}

func (s *server) dial(t *testing.T) *net.UnixConn {
	t.Helper()
	c, err := Dial(t.Context(), s.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func testLimits() Limits {
	limits := DefaultLimits
	limits.FrameTimeout = 200 * time.Millisecond
	return limits
}

// readClosed reads until the server closes c, returning what it sent.
func readClosed(t *testing.T, c net.Conn) []byte {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var got []byte
	buf := make([]byte, 4096)
	for {
		n, err := c.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				t.Fatal("the server did not close the connection")
			}
			return got
		}
	}
}

func waitClosed(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}
