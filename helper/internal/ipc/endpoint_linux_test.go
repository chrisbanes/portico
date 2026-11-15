package ipc

import (
	"errors"
	"testing"
)

func testUserRuntimeDir(t *testing.T) {
	for _, dir := range []string{"", "relative/run"} {
		t.Setenv("XDG_RUNTIME_DIR", dir)
		if _, err := UserEndpoint(Test); !errors.Is(err, ErrUnsafeEndpoint) {
			t.Errorf("XDG_RUNTIME_DIR=%q: UserEndpoint = %v, want unsafe", dir, err)
		}
	}
	t.Setenv("XDG_RUNTIME_DIR", shortTemp(t))
	if _, err := UserEndpoint(Test); err != nil {
		t.Errorf("a private XDG_RUNTIME_DIR: UserEndpoint = %v", err)
	}
}
