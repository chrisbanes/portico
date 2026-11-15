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

// closeTimeout bounds Engine.Close at shutdown; tests lower it.
var closeTimeout = 10 * time.Second

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
	store, openErr := owner.Open(service.Options{})
	var engine *service.Engine
	if openErr == nil {
		if engine, err = service.New(store, cfg.Engine); err != nil {
			return errors.Join(err, store.Close())
		}
	}
	listener, err := ipc.Listen(cfg.Roots.Endpoint, owner.LockFile(), cfg.Limits)
	if err != nil {
		if engine == nil {
			return errors.Join(err, owner.Close())
		}
		return errors.Join(err, shutdown(engine, store))
	}

	served := make(chan error, 1)
	var failure *StartupFailure
	if engine != nil {
		go func() { served <- management.Serve(listener, engine) }()
	} else {
		failure = &StartupFailure{Reason: service.CodeUnsupportedState}
		if errors.Is(openErr, service.ErrPersistenceUnavailable) {
			failure.Reason = service.CodePersistenceUnavailable
		}
		log.Print(failure)
		go func() { served <- management.ServeStartupFailure(listener, failure.Reason) }()
	}
	// Stop accepting and close every session before the engine.
	var serveErr error
	select {
	case <-ctx.Done():
		listener.Close()
		serveErr = <-served
	case serveErr = <-served:
		listener.Close()
	}
	if engine == nil {
		return errors.Join(failure, serveErr, owner.Close())
	}
	return errors.Join(serveErr, shutdown(engine, store))
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
