//go:build !darwin

package service

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

// No legacy reader exists off macOS, so the guard is refused before
// anything is imported or written.
func TestLegacyGuardIsRefusedOffDarwin(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "installation-v5.json"), fixture(t, "v5-public-local-app.json"), 0o600)
	before := originals(t, dir)
	if _, err := Open(dir, Options{LegacyGuard: true}); !errors.Is(err, ErrUnsupportedState) {
		t.Fatalf("Open() error = %v, want unsupportedState", err)
	}
	if after := originals(t, dir); !reflect.DeepEqual(after, before) {
		t.Fatalf("originals changed: %v -> %v", before, after)
	}
	assertGone(t, dir, serviceDir)
}
