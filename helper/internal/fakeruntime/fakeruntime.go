// Package fakeruntime is a service.Runtime that starts no nodes and appends
// every call to a JSON-lines log, so lifecycle tests can run the real host
// and IPC without a tailnet. Only porticofake builds of portico-service
// select it.
package fakeruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/chrisbanes/portico/helper/internal/installation"
	"github.com/chrisbanes/portico/helper/internal/portal"
	"github.com/chrisbanes/portico/helper/internal/service"
)

// Entry is one logged call.
type Entry struct {
	PID      int      `json:"pid"`
	Call     string   `json:"call"`
	Enabled  []string `json:"enabled,omitempty"`
	Stopped  []string `json:"stopped,omitempty"`
	PortalID string   `json:"portalID,omitempty"`
	Failed   bool     `json:"failed,omitempty"`
}

// LogName and FailRemovalName are files in the fake's directory: the call
// log, and a marker that makes RemovePortal fail while it exists.
const (
	LogName         = "runtime.log"
	FailRemovalName = "fail-removal"
)

var errRemoval = errors.New("fakeruntime: removal failed")

// Factory builds fake runtimes that log to dir.
func Factory(dir string) service.RuntimeFactory {
	return func(string, installation.Logging) (service.Runtime, error) {
		r := &Runtime{dir: dir}
		return r, r.log(Entry{Call: "start"})
	}
}

type Runtime struct {
	dir string
	mu  sync.Mutex
}

func (r *Runtime) log(e Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e.PID = os.Getpid()
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(r.dir, LogName), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return errors.Join(err, f.Close())
}

// Reconcile reports every configured Portal converged: Enabled ones running,
// Stopped ones down.
func (r *Runtime) Reconcile(_ context.Context, configs []portal.Config, _ func(portal.Event)) ([]portal.ReconcileEntry, error) {
	e := Entry{Call: "reconcile", Enabled: []string{}, Stopped: []string{}}
	var entries []portal.ReconcileEntry
	for _, c := range configs {
		if c.DesiredState == portal.DesiredStateEnabled {
			e.Enabled = append(e.Enabled, c.ID)
		} else {
			e.Stopped = append(e.Stopped, c.ID)
		}
		entries = append(entries, portal.ReconcileEntry{PortalID: c.ID, Outcome: portal.OutcomeConverged})
	}
	return entries, r.log(e)
}

func (r *Runtime) AuthenticateAttempt(_ context.Context, portalID, _ string) error {
	return r.log(Entry{Call: "authenticate", PortalID: portalID})
}

func (r *Runtime) CleanupRejectedPortal(_ context.Context, portalID string) error {
	return r.log(Entry{Call: "cleanupRejected", PortalID: portalID})
}

func (r *Runtime) RemovePortal(_ context.Context, portalID string) error {
	_, err := os.Lstat(filepath.Join(r.dir, FailRemovalName))
	failed := err == nil
	if err := r.log(Entry{Call: "remove", PortalID: portalID, Failed: failed}); err != nil {
		return err
	}
	if failed {
		return errRemoval
	}
	return nil
}

func (r *Runtime) Close(context.Context) error { return r.log(Entry{Call: "close"}) }
