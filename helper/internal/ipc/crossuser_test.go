package ipc

import (
	"bufio"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// TestCrossUserPeersAreRejected runs a probe as nobody and as root through
// passwordless sudo, so it runs only with PORTICO_IPC_CROSS_USER=1, as in CI.
func TestCrossUserPeersAreRejected(t *testing.T) {
	if os.Getenv("PORTICO_IPC_CROSS_USER") != "1" {
		t.Skip("set PORTICO_IPC_CROSS_USER=1 to run the probe as other users through sudo -n")
	}
	// A world-traversable base, so other users can reach the sockets.
	base, err := os.MkdirTemp("/tmp", "ipcx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(base, "peerprobe")
	if out, err := exec.Command("go", "build", "-o", probe, "./internal/peerprobe").CombinedOutput(); err != nil {
		t.Fatalf("build probe: %v\n%s", err, out)
	}
	if err := os.Chmod(probe, 0o755); err != nil {
		t.Fatal(err)
	}
	others := map[string][]string{"other user": {"sudo", "-n", "-u", "nobody"}, "root": {"sudo", "-n"}}

	t.Run("server rejects", func(t *testing.T) {
		endpoint, err := EndpointIn(base, Test)
		if err != nil {
			t.Fatal(err)
		}
		var handled atomic.Bool
		listener, err := Listen(endpoint, lockFile(t), testLimits())
		if err != nil {
			t.Fatal(err)
		}
		go listener.Serve(func(*Conn) { handled.Store(true) })
		t.Cleanup(func() { listener.Close() })
		// Test only: open the endpoint so other users reach the credential check.
		if err := os.Chmod(endpoint.dir, 0o755); err != nil || os.Chmod(endpoint.socket, 0o777) != nil {
			t.Fatal(err)
		}
		for name, prefix := range others {
			cmd := exec.Command(prefix[0], append(prefix[1:], probe, "client", endpoint.socket)...)
			cmd.Stderr = os.Stderr
			out, err := cmd.Output()
			if err != nil || strings.TrimSpace(string(out)) != "bytes=0 closed=true" {
				t.Errorf("%s client: %v, %q; want closed with no bytes", name, err, out)
			}
		}
		if handled.Load() {
			t.Error("another user's connection reached the handler")
		}
	})

	t.Run("client rejects", func(t *testing.T) {
		for name, prefix := range others {
			dir := filepath.Join(base, strings.ReplaceAll(name, " ", "-"))
			if err := os.Mkdir(dir, 0o700); err != nil || os.Chmod(dir, 0o777) != nil {
				t.Fatal(err)
			}
			socket := filepath.Join(dir, "management.sock")
			cmd := exec.Command(prefix[0], append(prefix[1:], probe, "server", socket)...)
			cmd.Stderr = os.Stderr
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			// A probe left waiting in Accept would outlive a failed test; a
			// root probe is reached through its sudo parent.
			t.Cleanup(func() {
				if c, err := net.Dial("unix", socket); err == nil {
					c.Close()
				}
				_ = cmd.Wait()
			})
			lines := bufio.NewScanner(stdout)
			if !lines.Scan() || lines.Text() != "ready" {
				t.Fatalf("%s server did not start: %q", name, lines.Text())
			}
			// With the directory private again, Dial refuses the socket on
			// its owner before connecting.
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			endpoint := Endpoint{&endpointPaths{dir: dir, socket: socket}}
			if _, err := Dial(t.Context(), endpoint); !errors.Is(err, ErrUnsafeEndpoint) {
				t.Errorf("%s: Dial = %v, want unsafe", name, err)
			}
			// The kernel-credential check refuses it even once connected.
			c, err := net.Dial("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			if err := verifyPeer(c.(*net.UnixConn)); !errors.Is(err, ErrPeerRejected) {
				t.Errorf("%s: verifyPeer = %v, want rejected", name, err)
			}
			c.Close()
			if !lines.Scan() || lines.Text() != "bytes=0 closed=true" {
				t.Errorf("%s server saw %q, want no bytes", name, lines.Text())
			}
		}
	})
}
