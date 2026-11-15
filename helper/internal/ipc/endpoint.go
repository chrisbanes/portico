package ipc

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// Variant keeps production, Dev and test services in separate directories.
type Variant string

const (
	Production Variant = "portico"
	Dev        Variant = "portico-dev"
	Test       Variant = "portico-test"
)

// Endpoint is a validated management socket location. Its paths are never
// exposed, so they cannot leak into frames or diagnostics; behind a pointer,
// fmt prints only an address even when an Endpoint is a nested field printed
// with %v.
type Endpoint struct {
	*endpointPaths
}

type endpointPaths struct {
	dir    string
	socket string
}

func (Endpoint) String() string   { return "management endpoint" }
func (Endpoint) GoString() string { return "ipc.Endpoint{}" }

// UserEndpoint locates the variant's endpoint in this user's OS runtime
// directory; it never trusts a path from a client or the environment beyond
// the platform's own lookup.
func UserEndpoint(variant Variant) (Endpoint, error) {
	base, err := userRuntimeDir()
	if err != nil {
		return Endpoint{}, err
	}
	return EndpointIn(base, variant)
}

// EndpointIn places the variant's endpoint in an explicit absolute base
// directory, for the test service variant and tests.
func EndpointIn(base string, variant Variant) (Endpoint, error) {
	switch variant {
	case Production, Dev, Test:
	default:
		return Endpoint{}, ErrUnsafeEndpoint
	}
	if !filepath.IsAbs(base) {
		return Endpoint{}, ErrUnsafeEndpoint
	}
	resolved, err := filepath.EvalSymlinks(base)
	if err != nil {
		return Endpoint{}, ErrUnsafeEndpoint
	}
	if err := checkBase(resolved); err != nil {
		return Endpoint{}, err
	}
	dir := filepath.Join(resolved, string(variant))
	socket := filepath.Join(dir, "management.sock")
	if len(socket) >= sunPathMax {
		return Endpoint{}, ErrUnsafeEndpoint
	}
	return Endpoint{&endpointPaths{dir: dir, socket: socket}}, nil
}

// checkBase requires a base this user owns that nobody else can write, under
// ancestors owned by root or this user that are not shared-writable unless
// sticky, so no other user can swap the endpoint directory.
func checkBase(base string) error {
	info, err := os.Lstat(base)
	if err != nil || !info.IsDir() || !ownedBy(info, os.Geteuid()) || info.Mode().Perm()&0o022 != 0 {
		return ErrUnsafeEndpoint
	}
	for dir := filepath.Dir(base); ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || !(ownedBy(info, 0) || ownedBy(info, os.Geteuid())) ||
			info.Mode().Perm()&0o022 != 0 && info.Mode()&fs.ModeSticky == 0 {
			return ErrUnsafeEndpoint
		}
		if dir == filepath.Dir(dir) {
			return nil
		}
	}
}

// checkDir verifies the opened endpoint directory, not just its path: a
// directory this user owns, mode 0700, and not a symlink. A missing one means
// no service has run.
func (e Endpoint) checkDir() error {
	file, err := os.OpenFile(e.dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if os.IsNotExist(err) {
		return ErrNotRunning
	}
	if err != nil {
		return ErrUnsafeEndpoint
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.IsDir() || !ownedBy(info, os.Geteuid()) || info.Mode().Perm() != 0o700 {
		return ErrUnsafeEndpoint
	}
	return nil
}

// socketInfo reports whether a socket this user owns is at the endpoint.
func (e Endpoint) socketInfo() (exists bool, err error) {
	info, err := os.Lstat(e.socket)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil || info.Mode().Type() != fs.ModeSocket || !ownedBy(info, os.Geteuid()) {
		return true, ErrUnsafeEndpoint
	}
	return true, nil
}

func ownedBy(info fs.FileInfo, uid int) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == uid
}
