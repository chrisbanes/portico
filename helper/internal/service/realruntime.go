package service

import (
	"errors"
	"os"

	"tailscale.com/envknob"

	"github.com/chrisbanes/portico/helper/internal/installation"
	"github.com/chrisbanes/portico/helper/internal/portal"
)

const noLogsKnob = "TS_NO_LOGS_NO_SUPPORT"

// newPortalRuntime is replaced in tests to observe the environment it sees.
var newPortalRuntime = func(stateRoot string) Runtime {
	return portal.NewRuntime(stateRoot, portal.NewTSNetNode)
}

// RealRuntimeFactory builds the production tsnet runtime. Nothing calls it by
// default; #104's service host wires it in.
//
// Tailscale v1.102.0 reads TS_NO_LOGS_NO_SUPPORT each time a node builds its
// log transport and hostinfo, so the knob is set before the runtime exists and
// every node it starts sees the current choice. The knob is process-wide; a
// control-sent DisableLogTail also stays process-wide, which is only ever
// more private.
func RealRuntimeFactory(stateRoot string, logging installation.Logging) (Runtime, error) {
	switch logging {
	case installation.LoggingDisabled:
		envknob.Setenv(noLogsKnob, "true")
	case installation.LoggingEnabled:
		envknob.Setenv(noLogsKnob, "")
		os.Unsetenv(noLogsKnob)
	default:
		return nil, errors.New("a logging choice is required before the runtime starts")
	}
	return newPortalRuntime(stateRoot), nil
}
