package servicehost

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/chrisbanes/portico/helper/internal/ipc"
	"github.com/chrisbanes/portico/helper/internal/service"
)

func TestReasonNamesNoPath(t *testing.T) {
	secret := "/Users/someone/Library/Application Support/Portico"
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&StartupFailure{Reason: service.CodeUnsupportedState}, "servicehost: startup failure: unsupportedState"},
		{service.ErrOwnershipBusy, "ownershipBusy"},
		{&ipc.Error{Code: ipc.CodeOwnershipBusy}, "ownershipBusy"},
		{fmt.Errorf("%w: open %s: denied", service.ErrPersistenceUnavailable, secret), "persistenceUnavailable"},
		{fmt.Errorf("%w: unsafe %s", service.ErrUnsupportedState, secret), "unsupportedState"},
		{context.DeadlineExceeded, "the runtime did not close in time"},
		{ErrUnguardedVariant, ErrUnguardedVariant.Error()},
		{errors.Join(ipc.ErrUnsafeEndpoint, errors.New(secret)), ipc.ErrUnsafeEndpoint.Error()},
		{errors.New(secret), "service failed"},
	} {
		if got := Reason(tc.err); got != tc.want {
			t.Errorf("Reason(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}
