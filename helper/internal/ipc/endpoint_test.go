package ipc

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestEndpointIsPrivateToThisUser(t *testing.T) {
	s := serve(t, testLimits(), func(*Conn) {})
	for path, want := range map[string]fs.FileMode{s.endpoint.dir: fs.ModeDir | 0o700, s.endpoint.socket: fs.ModeSocket | 0o600} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode() != want || !ownedBy(info, os.Geteuid()) {
			t.Errorf("mode %v (owned %v), want %v owned by this user", info.Mode(), ownedBy(info, os.Geteuid()), want)
		}
	}
	if len(s.endpoint.socket) >= sunPathMax {
		t.Errorf("socket path is %d bytes, over the %d-byte sun_path", len(s.endpoint.socket), sunPathMax)
	}
}

func TestUserEndpointUsesThePlatformLookup(t *testing.T) {
	if _, err := UserEndpoint(Variant("../portico")); !errors.Is(err, ErrUnsafeEndpoint) {
		t.Errorf("unknown variant = %v", err)
	}
	testUserRuntimeDir(t)
}

func TestEndpointNeverPrintsItsPaths(t *testing.T) {
	endpoint := testEndpoint(t)
	nested := struct{ endpoint Endpoint }{endpoint}
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		if got := fmt.Sprintf(format, endpoint); strings.Contains(got, endpoint.dir) {
			t.Errorf("%s printed %q", format, got)
		}
	}
	// Nested, fmt cannot call its methods, but the pointer hides the paths
	// from the verbs a struct is printed with (a bad verb such as %s still
	// shows them).
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if got := fmt.Sprintf(format, nested); strings.Contains(got, endpoint.dir) {
			t.Errorf("nested %s printed %q", format, got)
		}
	}
}

func TestDialBeforeAnyServiceRanIsNotRunning(t *testing.T) {
	if _, err := Dial(t.Context(), testEndpoint(t)); !errors.Is(err, ErrNotRunning) {
		t.Errorf("Dial = %v, want not running", err)
	}
}

func TestEndpointRejectsUnsafeBases(t *testing.T) {
	shared := filepath.Join(shortTemp(t), "s")
	if err := os.Mkdir(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(shared, "b")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	groupWritable := filepath.Join(shortTemp(t), "g")
	if err := os.Mkdir(groupWritable, 0o700); err != nil || os.Chmod(groupWritable, 0o770) != nil {
		t.Fatal(err)
	}
	long := filepath.Join(shortTemp(t), strings.Repeat("x", sunPathMax))
	if err := os.Mkdir(long, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{
		"relative":       "relative/dir",
		"another owner":  "/",
		"group-writable": groupWritable,
		"too long":       long,
	} {
		if _, err := EndpointIn(dir, Test); !errors.Is(err, ErrUnsafeEndpoint) {
			t.Errorf("%s: EndpointIn = %v, want unsafe", name, err)
		}
	}
	if err := os.Chmod(shared, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := EndpointIn(base, Test); !errors.Is(err, ErrUnsafeEndpoint) {
		t.Errorf("shared-writable ancestor: EndpointIn = %v, want unsafe", err)
	}
	// A sticky shared ancestor, like /tmp, cannot have our directory swapped.
	if err := os.Chmod(shared, 0o777|fs.ModeSticky); err != nil {
		t.Fatal(err)
	}
	if _, err := EndpointIn(base, Test); err != nil {
		t.Errorf("sticky ancestor: EndpointIn = %v", err)
	}
}

func TestListenRejectsUnsafeEndpointEntries(t *testing.T) {
	t.Run("symlinked directory", func(t *testing.T) {
		endpoint := testEndpoint(t)
		target := filepath.Join(filepath.Dir(endpoint.dir), "target")
		if err := os.Mkdir(target, 0o700); err != nil || os.Symlink(target, endpoint.dir) != nil {
			t.Fatal(err)
		}
		if _, err := Listen(endpoint, lockFile(t), testLimits()); !errors.Is(err, ErrUnsafeEndpoint) {
			t.Errorf("Listen = %v, want unsafe", err)
		}
		if _, err := Dial(t.Context(), endpoint); !errors.Is(err, ErrUnsafeEndpoint) {
			t.Errorf("Dial = %v, want unsafe", err)
		}
	})
	t.Run("open directory", func(t *testing.T) {
		endpoint := testEndpoint(t)
		if err := os.Mkdir(endpoint.dir, 0o700); err != nil || os.Chmod(endpoint.dir, 0o755) != nil {
			t.Fatal(err)
		}
		if _, err := Listen(endpoint, lockFile(t), testLimits()); !errors.Is(err, ErrUnsafeEndpoint) {
			t.Errorf("Listen = %v, want unsafe", err)
		}
	})
	for name, plant := range map[string]func(path string) error{
		// The link leads to a dead socket, which Listen would otherwise remove.
		"symlinked socket": func(path string) error {
			dead := filepath.Join(filepath.Dir(path), "dead.sock")
			ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: dead, Net: "unix"})
			if err != nil {
				return err
			}
			ln.SetUnlinkOnClose(false)
			ln.Close()
			return os.Symlink(dead, path)
		},
		"regular file": func(path string) error { return os.WriteFile(path, []byte("keep"), 0o600) },
	} {
		t.Run(name, func(t *testing.T) {
			endpoint := testEndpoint(t)
			if err := os.Mkdir(endpoint.dir, 0o700); err != nil || plant(endpoint.socket) != nil {
				t.Fatal(err)
			}
			if _, err := Listen(endpoint, lockFile(t), testLimits()); !errors.Is(err, ErrUnsafeEndpoint) {
				t.Errorf("Listen = %v, want unsafe", err)
			}
			if _, err := os.Lstat(endpoint.socket); err != nil {
				t.Errorf("Listen unlinked what was at the socket path: %v", err)
			}
		})
	}
}

func TestListenNeverUnlinksALiveSocket(t *testing.T) {
	s := serve(t, testLimits(), func(c *Conn) { <-c.Done() })
	if _, err := Listen(s.endpoint, lockFile(t), testLimits()); !isCode(err, CodeOwnershipBusy) {
		t.Fatalf("second Listen = %v, want ownershipBusy", err)
	}
	s.dial(t)
}

func TestOnlyTheLockHolderRemovesADeadSocket(t *testing.T) {
	endpoint := testEndpoint(t)
	if err := os.Mkdir(endpoint.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	dead, err := net.ListenUnix("unix", &net.UnixAddr{Name: endpoint.socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	dead.SetUnlinkOnClose(false)
	dead.Close()

	held := lockFile(t)
	other, err := os.OpenFile(held.Name(), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(endpoint, other, testLimits()); !isCode(err, CodeOwnershipBusy) {
		t.Fatalf("Listen without the lock = %v, want ownershipBusy", err)
	}
	if _, err := os.Lstat(endpoint.socket); err != nil {
		t.Fatalf("a non-holder removed the dead socket: %v", err)
	}
	listener, err := Listen(endpoint, held, testLimits())
	if err != nil {
		t.Fatalf("Listen as the lock holder = %v", err)
	}
	listener.Close()
}

func isCode(err error, code Code) bool {
	var ipcErr *Error
	return errors.As(err, &ipcErr) && ipcErr.Code == code
}
