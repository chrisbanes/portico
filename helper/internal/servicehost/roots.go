// Package servicehost runs one portico-service process: it resolves the
// variant's state root and management endpoint, takes the lifetime lock,
// loads the state and serves management v1 until it is told to stop.
package servicehost

import (
	"errors"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/chrisbanes/portico/helper/internal/ipc"
)

var (
	// ErrUnguardedRoot refuses the macOS production and Dev roots to a test
	// variant without the MIG-1 legacy-writer guard: an older app may still
	// write them, and only the guard excludes it.
	ErrUnguardedRoot = errors.New("servicehost: the production and Dev roots need the legacy-writer guard")
	// ErrInvalidRoots means the variant, state root or runtime directory is
	// missing, misplaced or unsafe. It never names the path.
	ErrInvalidRoots = errors.New("servicehost: invalid state root or runtime directory")
)

// ParseVariant maps the --variant flag to the endpoint variant.
func ParseVariant(name string) (ipc.Variant, error) {
	switch name {
	case "production":
		return ipc.Production, nil
	case "dev":
		return ipc.Dev, nil
	case "test":
		return ipc.Test, nil
	}
	return "", ErrInvalidRoots
}

// Roots are a variant's validated state root and management endpoint.
type Roots struct {
	StateRoot string
	Endpoint  ipc.Endpoint
	// LegacyGuard requires the MIG-1 guard on the state root.
	LegacyGuard bool
}

// Seams for tests: the effective UID and the user's home directory.
var (
	geteuid  = os.Geteuid
	userHome = func() (string, error) {
		u, err := user.Current()
		if err != nil {
			return "", err
		}
		return u.HomeDir, nil
	}
)

// Resolve locates and validates the variant's roots, creating a missing
// state root 0700. Only the test variant takes paths, and it requires both
// as absolute paths; production and Dev use the platform's own lookups.
// The macOS production and Dev variants always have the legacy-writer
// guard, which only macOS has readers for; legacyGuard asks for it on a
// test variant.
func Resolve(variant ipc.Variant, stateRoot, runtimeDir string, legacyGuard bool) (Roots, error) {
	// Refused before anything touches the filesystem.
	if geteuid() == 0 {
		return Roots{}, ipc.ErrRoot
	}
	if legacyGuard && (variant != ipc.Test || runtime.GOOS != "darwin") {
		return Roots{}, ErrInvalidRoots
	}
	legacyGuard = legacyGuard || (runtime.GOOS == "darwin" && (variant == ipc.Production || variant == ipc.Dev))
	var endpoint ipc.Endpoint
	var err error
	switch variant {
	case ipc.Test:
		if !filepath.IsAbs(stateRoot) || !filepath.IsAbs(runtimeDir) {
			return Roots{}, ErrInvalidRoots
		}
		if runtime.GOOS == "darwin" && !legacyGuard {
			home, err := userHome()
			if err != nil {
				return Roots{}, ErrInvalidRoots
			}
			if err := refuseGuardedRoot(stateRoot, home); err != nil {
				return Roots{}, err
			}
		}
		endpoint, err = ipc.EndpointIn(runtimeDir, ipc.Test)
	case ipc.Production, ipc.Dev:
		if stateRoot != "" || runtimeDir != "" {
			return Roots{}, ErrInvalidRoots
		}
		var home string
		if home, err = userHome(); err != nil {
			return Roots{}, ErrInvalidRoots
		}
		if stateRoot, err = defaultStateRoot(runtime.GOOS, variant, home, os.Getenv("XDG_STATE_HOME")); err != nil {
			return Roots{}, err
		}
		endpoint, err = ipc.UserEndpoint(variant)
	default:
		return Roots{}, ErrInvalidRoots
	}
	if err != nil {
		return Roots{}, ErrInvalidRoots
	}
	if stateRoot, err = prepareRoot(stateRoot); err != nil {
		return Roots{}, err
	}
	return Roots{StateRoot: stateRoot, Endpoint: endpoint, LegacyGuard: legacyGuard}, nil
}

// refuseGuardedRoot keeps an unguarded macOS test variant out of the
// production and Dev roots: a state root equal to, inside or containing
// either is refused, by resolved path and by file identity. APFS is usually
// case-insensitive, so the comparison ignores case.
func refuseGuardedRoot(stateRoot, home string) error {
	root := strings.ToLower(resolveExisting(stateRoot))
	for _, variant := range []ipc.Variant{ipc.Production, ipc.Dev} {
		guarded, err := defaultStateRoot("darwin", variant, home, "")
		if err != nil {
			return ErrInvalidRoots
		}
		if sameTree(guarded, stateRoot) || sameTree(stateRoot, guarded) {
			return ErrUnguardedRoot
		}
		guarded = strings.ToLower(resolveExisting(guarded))
		if within(guarded, root) || within(root, guarded) {
			return ErrUnguardedRoot
		}
	}
	return nil
}

// sameTree reports whether path is dir or inside it by file identity, which
// also sees through spellings symlink resolution misses: firmlinks such as
// /System/Volumes/Data/Users, and Unicode normalisation. Where dir does not
// exist yet, its deepest existing ancestor stands in, and the names below it
// are compared without case.
func sameTree(dir, path string) bool {
	dirBase, dirRest := existingAncestor(dir)
	if dirBase == nil {
		return false
	}
	for p, rest := filepath.Clean(path), ""; ; {
		if info, err := os.Stat(p); err == nil && os.SameFile(info, dirBase) {
			return rest == dirRest || dirRest == "" || strings.HasPrefix(rest, dirRest+"/")
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		rest = strings.ToLower(filepath.Join(filepath.Base(p), rest))
		p = parent
	}
}

// existingAncestor is path's deepest existing ancestor-or-self and the
// lower-cased names below it.
func existingAncestor(path string) (os.FileInfo, string) {
	rest := ""
	for p := filepath.Clean(path); ; {
		if info, err := os.Stat(p); err == nil {
			return info, rest
		}
		parent := filepath.Dir(p)
		if parent == p {
			return nil, ""
		}
		rest = strings.ToLower(filepath.Join(filepath.Base(p), rest))
		p = parent
	}
}

// resolveExisting resolves the symlinks in path's longest existing prefix
// and appends the rest, so a root that does not exist yet still resolves.
func resolveExisting(path string) string {
	path = filepath.Clean(path)
	rest := ""
	for {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return filepath.Join(path, rest)
		}
		rest = filepath.Join(filepath.Base(path), rest)
		path = parent
	}
}

// within reports whether path is dir or inside it; both are clean and absolute.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// defaultStateRoot is the variant's state root: the app's own Application
// Support directory on macOS, and $XDG_STATE_HOME (default ~/.local/state)
// elsewhere, in a directory named for the variant.
func defaultStateRoot(goos string, variant ipc.Variant, home, xdgStateHome string) (string, error) {
	if !filepath.IsAbs(home) {
		return "", ErrInvalidRoots
	}
	if goos == "darwin" {
		name := "Portico"
		if variant == ipc.Dev {
			name = "Portico Dev"
		}
		return filepath.Join(home, "Library", "Application Support", name), nil
	}
	base := filepath.Join(home, ".local", "state")
	if xdgStateHome != "" {
		// The XDG spec calls a relative value invalid; refuse rather than guess.
		if !filepath.IsAbs(xdgStateHome) {
			return "", ErrInvalidRoots
		}
		base = xdgStateHome
	}
	return filepath.Join(base, string(variant)), nil
}

// prepareRoot creates the state root 0700 if missing, as the app does, and
// requires a real directory, not a symlink, that this user owns and nobody
// else can write. Others could have planted entries in a writable root, so
// it is refused; one that is only readable or searchable by others is
// tightened to 0700, as the app does.
func prepareRoot(root string) (string, error) {
	root = filepath.Clean(root)
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		return "", ErrInvalidRoots
	}
	if err := os.Mkdir(root, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", ErrInvalidRoots
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return "", ErrInvalidRoots
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Geteuid() {
		return "", ErrInvalidRoots
	}
	if info.Mode().Perm()&0o022 != 0 {
		return "", ErrInvalidRoots
	}
	if info.Mode().Perm() != 0o700 && os.Chmod(root, 0o700) != nil {
		return "", ErrInvalidRoots
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", ErrInvalidRoots
	}
	return resolved, nil
}
