package servicehost

import (
	"context"
	"errors"

	"github.com/chrisbanes/portico/helper/internal/ipc"
	"github.com/chrisbanes/portico/helper/internal/service"
)

// Reason is a path-free code for a host error, for logs: the errors
// themselves may wrap filesystem errors that name the state root.
func Reason(err error) string {
	var failure *StartupFailure
	var serviceErr *service.Error
	var ipcErr *ipc.Error
	switch {
	case err == nil:
		return ""
	case errors.As(err, &failure):
		return failure.Error()
	case errors.Is(err, service.ErrMigrationFailed):
		return "migrationFailed"
	case errors.Is(err, service.ErrMigrationGuardMissing):
		return "migrationGuardMissing"
	case errors.Is(err, service.ErrOwnershipBusy):
		return "ownershipBusy"
	case errors.Is(err, service.ErrUnsupportedState):
		return "unsupportedState"
	case errors.Is(err, service.ErrPersistenceUnavailable):
		return "persistenceUnavailable"
	case errors.As(err, &serviceErr):
		return string(serviceErr.Code)
	case errors.As(err, &ipcErr):
		return string(ipcErr.Code)
	case errors.Is(err, context.DeadlineExceeded):
		return "the runtime did not close in time"
	}
	for _, known := range []error{ErrUnguardedRoot, ErrInvalidRoots, ipc.ErrUnsafeEndpoint, ipc.ErrRoot} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return "service failed"
}
