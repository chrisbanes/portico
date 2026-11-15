package ipc

/*
#include <unistd.h>
*/
import "C"

import (
	"path/filepath"
	"unsafe"
)

// sunPathMax is sizeof(sockaddr_un.sun_path), including the terminating NUL.
const sunPathMax = 104

// userRuntimeDir is the user's Darwin temporary directory from confstr,
// never a client-supplied TMPDIR.
func userRuntimeDir() (string, error) {
	n := C.confstr(C._CS_DARWIN_USER_TEMP_DIR, nil, 0)
	if n == 0 {
		return "", ErrUnsafeEndpoint
	}
	buf := make([]byte, n)
	if C.confstr(C._CS_DARWIN_USER_TEMP_DIR, (*C.char)(unsafe.Pointer(&buf[0])), n) != n {
		return "", ErrUnsafeEndpoint
	}
	return filepath.Clean(string(buf[:n-1])), nil
}
