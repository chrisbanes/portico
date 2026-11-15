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
