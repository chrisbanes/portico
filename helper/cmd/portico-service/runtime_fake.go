//go:build porticofake

package main

import (
	"errors"
	"flag"
	"path/filepath"

	"github.com/chrisbanes/portico/helper/internal/fakeruntime"
	"github.com/chrisbanes/portico/helper/internal/ipc"
	"github.com/chrisbanes/portico/helper/internal/service"
)

// runtimeFlags adds --fake-runtime, which only test builds have.
func runtimeFlags(flags *flag.FlagSet) func(ipc.Variant) (service.RuntimeFactory, error) {
	dir := flags.String("fake-runtime", "", "test variant only: log runtime calls in this absolute directory instead of starting nodes")
	return func(variant ipc.Variant) (service.RuntimeFactory, error) {
		if *dir == "" {
			return service.RealRuntimeFactory, nil
		}
		if variant != ipc.Test || !filepath.IsAbs(*dir) {
			return nil, errors.New("--fake-runtime needs --variant test and an absolute directory")
		}
		return fakeruntime.Factory(*dir), nil
	}
}
