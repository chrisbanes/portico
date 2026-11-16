package servicehost

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/chrisbanes/portico/helper/internal/ipc"
	"github.com/chrisbanes/portico/helper/internal/management"
	"github.com/chrisbanes/portico/helper/internal/service"
)

var (
	// closeTimeout bounds Engine.Close at shutdown; tests lower it.
	closeTimeout = 10 * time.Second
	// persistenceRetryAfter is how long a persistenceUnavailable failure is
	// served before the host exits, so the service manager's back-off loads
	// the state again; tests lower it.
	persistenceRetryAfter = 30 * time.Second
)

// Config is one service process.
type Config struct {
	Roots  Roots
	Engine service.EngineOptions
	Limits ipc.Limits
}

// StartupFailure is Run's result once a host that could not load its state
// stops: the state's reason code, served meanwhile as service.startupFailure.
type StartupFailure struct{ Reason service.Code }

func (f *StartupFailure) Error() string { return "servicehost: startup failure: " + string(f.Reason) }

// Run serves the roots until ctx ends. It takes the lifetime lock first and
// keeps it until everything it guards has stopped: when the state cannot
// load it serves that failure instead of an empty install, and at shutdown
// it releases the lock only after the engine has closed. If the engine
// cannot close in time, Run returns still holding the lock, and the process
// must exit, which ends the in-process nodes.
//
// ownershipBusy returns before the endpoint is touched. Every other non-nil
// result, including a stopped StartupFailure, is a failed run.
func Run(ctx context.Context, cfg Config) error {
	owner, err := service.Acquire(cfg.Roots.StateRoot)
	if err != nil {
		return err
	}
	store, err := owner.Open(service.Options{})
	if err != nil {
		return serveFailure(ctx, cfg, owner, err)
	}
	engine, err := service.New(store, cfg.Engine)
	if err != nil {
		return errors.Join(err, store.Close())
	}
	listener, err := ipc.Listen(cfg.Roots.Endpoint, owner.LockFile(), cfg.Limits)
	if err != nil {
		return errors.Join(err, shutdown(engine, store))
	}
	served := make(chan error, 1)
	go func() { served <- management.Serve(listener, engine) }()
	serveErr := stopServing(ctx, listener, served, nil)
	return errors.Join(serveErr, shutdown(engine, store))
}

// serveFailure serves the state's load error with the lock held and no
// engine. unsupportedState is served until ctx ends; persistenceUnavailable,
// which may clear by itself, only for persistenceRetryAfter.
func serveFailure(ctx context.Context, cfg Config, owner *service.Owner, openErr error) error {
	failure := &StartupFailure{Reason: service.CodeUnsupportedState}
	var retry <-chan time.Time
	if errors.Is(openErr, service.ErrPersistenceUnavailable) {
		failure.Reason = service.CodePersistenceUnavailable
		retry = time.After(persistenceRetryAfter)
	}
	listener, err := ipc.Listen(cfg.Roots.Endpoint, owner.LockFile(), cfg.Limits)
	if err != nil {
		return errors.Join(err, owner.Close())
	}
	log.Print(failure)
	served := make(chan error, 1)
	go func() { served <- management.ServeStartupFailure(listener, failure.Reason) }()
	serveErr := stopServing(ctx, listener, served, retry)
	return errors.Join(failure, serveErr, owner.Close())
}

// stopServing waits for ctx, retry or the server to end, then stops
// accepting and closes every session before returning the server's result.
func stopServing(ctx context.Context, listener *ipc.Listener, served <-chan error, retry <-chan time.Time) error {
	select {
	case <-ctx.Done():
	case <-retry:
	case err := <-served:
		listener.Close()
		return err
	}
	listener.Close()
	return <-served
}

// shutdown closes the engine within closeTimeout, then the store, whose
// Close releases the lifetime lock last. A failed close keeps the lock.
func shutdown(engine *service.Engine, store *service.Store) error {
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	if err := engine.Close(ctx); err != nil {
		return err
	}
	return store.Close()
}
