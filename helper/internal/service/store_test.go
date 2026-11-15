package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/chrisbanes/portico/helper/internal/installation"
)

func setLogging(logging installation.Logging) func(*State) error {
	return func(s *State) error {
		s.Installation.Logging = logging
		s.Revision++
		return nil
	}
}

func stateBytes(t *testing.T, dir string, s *Store) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, s.generation, stateFileName))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestStoreCommitPersists(t *testing.T) {
	dir := t.TempDir()
	s := mustOpen(t, dir)
	if err := s.Commit(setLogging(installation.LoggingDisabled)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, s.generation, stateFileName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state document mode %v, %v", info, err)
	}
	s.Close()
	again := mustOpen(t, dir)
	defer again.Close()
	if got := again.State(); got.Revision != 1 || got.Installation.Logging != installation.LoggingDisabled {
		t.Fatalf("reopened state = %+v", got)
	}
}

func TestInterruptedCommitKeepsPriorDocument(t *testing.T) {
	for _, phase := range []Phase{PhaseCommitWrite, PhaseCommitRename} {
		t.Run(string(phase), func(t *testing.T) {
			dir := t.TempDir()
			mustOpen(t, dir).Close()
			s, err := Open(dir, failAt(phase))
			if err != nil {
				t.Fatal(err)
			}
			prior := stateBytes(t, dir, s)
			if err := s.Commit(setLogging(installation.LoggingEnabled)); !errors.Is(err, ErrPersistenceUnavailable) {
				t.Fatalf("Commit() error = %v", err)
			}
			if s.State().Revision != 0 || string(stateBytes(t, dir, s)) != string(prior) {
				t.Fatal("a failed commit changed the authoritative document")
			}
			// Writes stay stopped until the store is reopened.
			s.opts = Options{}
			if err := s.Commit(setLogging(installation.LoggingEnabled)); !errors.Is(err, ErrPersistenceUnavailable) {
				t.Fatalf("Commit() after failure error = %v", err)
			}
			s.Close()
			again := mustOpen(t, dir)
			defer again.Close()
			if again.State().Revision != 0 {
				t.Fatalf("reopened state = %+v", again.State())
			}
			if err := again.Commit(setLogging(installation.LoggingEnabled)); err != nil {
				t.Fatal(err)
			}
		})
	}

	t.Run("uncertain after rename", func(t *testing.T) {
		dir := t.TempDir()
		mustOpen(t, dir).Close()
		s, err := Open(dir, failAt(PhaseCommitFsync))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.Commit(setLogging(installation.LoggingEnabled)); !errors.Is(err, ErrPersistenceUnavailable) {
			t.Fatalf("Commit() error = %v", err)
		}
		if s.State().Revision != 0 {
			t.Fatal("an uncertain commit was applied in memory")
		}
		if err := s.Commit(setLogging(installation.LoggingDisabled)); !errors.Is(err, ErrPersistenceUnavailable) {
			t.Fatalf("Commit() after an uncertain outcome error = %v", err)
		}
	})
}

func TestOversizeCommitRejectedBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	s := mustOpen(t, dir)
	defer s.Close()
	prior := stateBytes(t, dir, s)
	defer func(limit int) { maxStateSize = limit }(maxStateSize)
	maxStateSize = len(prior) + 8

	grow := func(st *State) error {
		st.Installation.Binding = &installation.TailnetBinding{Name: "a-long-opaque-tailnet-name", MagicDNSSuffix: "example.ts.net"}
		return nil
	}
	if err := s.Commit(grow); !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("Commit() error = %v, want capacityExceeded", err)
	}
	if string(stateBytes(t, dir, s)) != string(prior) {
		t.Fatal("an oversize commit wrote the document")
	}
	assertGone(t, dir, filepath.Join(s.generation, stateFileName+".tmp"))
	// Rejection is not a write failure; the store keeps accepting commits.
	if err := s.Commit(func(st *State) error { st.Revision++; return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidCommitRejectedBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	s := mustOpen(t, dir)
	defer s.Close()
	prior := stateBytes(t, dir, s)
	if err := s.Commit(setLogging("maybe")); err == nil {
		t.Fatal("Commit() accepted an invalid installation")
	}
	if string(stateBytes(t, dir, s)) != string(prior) || s.State().Installation.Logging != installation.LoggingUndecided {
		t.Fatal("an invalid commit changed state")
	}
}

func TestCommitDoesNotRetainCallerReferences(t *testing.T) {
	dir := t.TempDir()
	s := mustOpen(t, dir)
	defer s.Close()
	binding := &installation.TailnetBinding{Name: "opaque-tailnet-id", MagicDNSSuffix: "one.ts.net"}
	if err := s.Commit(func(st *State) error {
		st.Installation.Binding = binding
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	binding.Name = ""
	if got := s.State().Installation.Binding; got == nil || got.Name != "opaque-tailnet-id" {
		t.Fatalf("State() follows a caller-held pointer: %+v", got)
	}
}
