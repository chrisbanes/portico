package ipc

import (
	"os"
	"path/filepath"
)

// sunPathMax is sizeof(sockaddr_un.sun_path), including the terminating NUL.
const sunPathMax = 108

// userRuntimeDir is $XDG_RUNTIME_DIR. There is no /tmp fallback: an unset or
// relative value is refused, and EndpointIn refuses an unsafe one.
func userRuntimeDir() (string, error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" || !filepath.IsAbs(dir) {
		return "", ErrUnsafeEndpoint
	}
	return dir, nil
}
