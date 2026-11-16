//go:build !porticofake

package main

import (
	"flag"

	"github.com/chrisbanes/portico/helper/internal/ipc"
	"github.com/chrisbanes/portico/helper/internal/service"
)

// runtimeFlags adds no flags: a production build can only run real nodes.
func runtimeFlags(*flag.FlagSet) func(ipc.Variant) (service.RuntimeFactory, error) {
	return func(ipc.Variant) (service.RuntimeFactory, error) { return service.RealRuntimeFactory, nil }
}

// hookFlags adds no flags: a production build has no store hook.
func hookFlags(*flag.FlagSet) func(ipc.Variant) (func(service.Phase) error, error) {
	return func(ipc.Variant) (func(service.Phase) error, error) { return nil, nil }
}
