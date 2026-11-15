package management

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chrisbanes/portico/helper/internal/ipc"
	"github.com/chrisbanes/portico/helper/internal/service"
)

// serveFailure runs ServeStartupFailure on a fresh endpoint, as the host does
// when the held root's state cannot load.
func serveFailure(t *testing.T, reason service.Code) ipc.Endpoint {
	t.Helper()
	endpoint, err := ipc.EndpointIn(shortTemp(t), ipc.Test)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(t.TempDir(), "service-owner.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	listener, err := ipc.Listen(endpoint, lock, ipc.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- ServeStartupFailure(listener, reason) }()
	t.Cleanup(func() {
		listener.Close()
		if err := <-served; err != nil {
			t.Errorf("ServeStartupFailure: %v", err)
		}
	})
	return endpoint
}

func TestStartupFailureServesItsSnapshotAndRefusesTheRest(t *testing.T) {
	for _, reason := range []service.Code{service.CodeUnsupportedState, service.CodePersistenceUnavailable} {
		t.Run(string(reason), func(t *testing.T) {
			c, err := Dial(ctx(t), serveFailure(t, reason))
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if c.Epoch == "" || c.Capabilities.AutomaticDiscovery != "unavailable" {
				t.Fatalf("hello = %q %+v", c.Epoch, c.Capabilities)
			}

			s := snapshot(t, c)
			if s.Service.StartupFailure != reason || s.Service.Runtime != "unavailable" || s.Service.PersistenceHealthy ||
				s.Epoch != c.Epoch || len(s.Portals) != 0 || s.Revision != 0 {
				t.Fatalf("snapshot = %+v", s)
			}

			// No mutation can create an empty install over the failed state.
			_, err = mutate(t, c, "createPortal", service.CreatePortalParams{Name: "web", Destination: localApp(8080), PublicAccess: "private"})
			wantCode(t, err, reason, "")
			if e := err.(*Error); e.Retryable != (reason == service.CodePersistenceUnavailable) {
				t.Fatalf("retryable = %v for %s", e.Retryable, reason)
			}
			err = c.Call(ctx(t), "setOperationalLogging", newMutation(t, c), service.SetOperationalLoggingParams{Logging: "enabled"}, nil)
			wantCode(t, err, reason, "")
			for _, call := range []struct {
				method string
				params any
			}{
				{"getRequestResult", RequestResultParams{RequestID: newUUID(), IssuedAt: time.Now().UTC().Format(time.RFC3339)}},
				{"subscribe", nil},
				{"discoverLocalApps", nil},
				{"getDiagnostics", nil},
			} {
				wantCode(t, c.Call(ctx(t), call.method, nil, call.params, nil), reason, "")
			}

			// The envelope is still validated first.
			wantCode(t, c.Call(ctx(t), "getSnapshot", nil, map[string]int{"extra": 1}, nil), service.CodeInvalidRequest, "params")
			wantCode(t, c.Call(ctx(t), "noSuchMethod", nil, nil, nil), service.CodeInvalidRequest, "method")
			wantCode(t, c.Call(ctx(t), "getSnapshot", newMutation(t, c), nil, nil), service.CodeInvalidRequest, "requestID")
			wantCode(t, c.Call(ctx(t), "createPortal", nil, nil, nil), service.CodeInvalidRequest, "requestID")
		})
	}
}

func TestStartupFailureRejectsAMismatchedVersion(t *testing.T) {
	conn, err := ipc.Dial(t.Context(), serveFailure(t, service.CodeUnsupportedState))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	writeJSON(t, conn, map[string]any{"type": "hello", "version": 2})
	if f := readJSON(t, conn); errorCode(f) != "incompatibleVersion" {
		t.Fatalf("frame = %v", f)
	}
}

// On the wire the field is the raw code, and a loaded service omits it.
func TestStartupFailureFieldOnTheWire(t *testing.T) {
	key := []byte(`"startupFailure"`)
	failed := &recorder{}
	c, err := dial(ctx(t), serveFailure(t, service.CodeUnsupportedState), failed.record)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	snapshot(t, c)
	if !bytes.Contains(bytes.Join(failed.all(), nil), []byte(`"startupFailure":"unsupportedState"`)) {
		t.Fatalf("no raw startupFailure in the failure snapshot's frames: %s", failed.all())
	}

	loaded := &recorder{}
	snapshot(t, newHarness(t).dialRecording(loaded.record))
	frames := loaded.all()
	if len(frames) < 2 || bytes.Contains(bytes.Join(frames, nil), key) {
		t.Fatalf("a loaded snapshot's frames carry startupFailure: %s", frames)
	}
}
