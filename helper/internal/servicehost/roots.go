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
	"syscall"

	"github.com/chrisbanes/portico/helper/internal/ipc"
)

var (
	// ErrUnguardedVariant refuses the macOS production and Dev roots, which
	// an older app may still write, until the MIG-1 legacy-writer guard can
	// exclude it.
	ErrUnguardedVariant = errors.New("servicehost: this variant waits for the legacy-writer guard")
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
}

// Resolve locates and validates the variant's roots, creating a missing
// state root 0700. Only the test variant takes paths, and it requires both
// as absolute paths; production and Dev use the platform's own lookups.
func Resolve(variant ipc.Variant, stateRoot, runtimeDir string) (Roots, error) {
	// Refused before anything touches the filesystem.
	if err := refuseUnguarded(runtime.GOOS, variant); err != nil {
		return Roots{}, err
	}
	var endpoint ipc.Endpoint
	var err error
	switch variant {
	case ipc.Test:
		if !filepath.IsAbs(stateRoot) || !filepath.IsAbs(runtimeDir) {
			return Roots{}, ErrInvalidRoots
		}
		endpoint, err = ipc.EndpointIn(runtimeDir, ipc.Test)
	case ipc.Production, ipc.Dev:
		if stateRoot != "" || runtimeDir != "" {
			return Roots{}, ErrInvalidRoots
		}
		var home *user.User
		if home, err = user.Current(); err != nil {
			return Roots{}, ErrInvalidRoots
		}
		if stateRoot, err = defaultStateRoot(runtime.GOOS, variant, home.HomeDir, os.Getenv("XDG_STATE_HOME")); err != nil {
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
	return Roots{StateRoot: stateRoot, Endpoint: endpoint}, nil
}

// refuseUnguarded fails closed for the macOS production and Dev variants.
// Owner: issue #104 slice T5, the MIG-1 guard, removes this refusal once its
// guard keeps older app versions from writing those roots.
func refuseUnguarded(goos string, variant ipc.Variant) error {
	if goos == "darwin" && (variant == ipc.Production || variant == ipc.Dev) {
		return ErrUnguardedVariant
	}
	return nil
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
// requires a real directory, not a symlink, that this user owns. A looser
// mode on such a directory is tightened to 0700.
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
	if info.Mode().Perm() != 0o700 && os.Chmod(root, 0o700) != nil {
		return "", ErrInvalidRoots
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", ErrInvalidRoots
	}
	return resolved, nil
}
