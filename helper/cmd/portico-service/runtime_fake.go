//go:build porticofake

package main

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"time"

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

// hookFlags adds --pause-after-guard, for the legacy-exclusion VM proof: once
// the guard is installed and flagged, the import writes DIR/paused and waits
// for DIR/resume before staging.
func hookFlags(flags *flag.FlagSet) func(ipc.Variant) (func(service.Phase) error, error) {
	dir := flags.String("pause-after-guard", "", "test variant only: pause the import after the legacy guard until this absolute directory holds resume")
	return func(variant ipc.Variant) (func(service.Phase) error, error) {
		if *dir == "" {
			return nil, nil
		}
		if variant != ipc.Test || !filepath.IsAbs(*dir) {
			return nil, errors.New("--pause-after-guard needs --variant test and an absolute directory")
		}
		return func(p service.Phase) error {
			if p != service.PhaseCopy {
				return nil
			}
			if err := os.WriteFile(filepath.Join(*dir, "paused"), nil, 0o600); err != nil {
				return err
			}
			for {
				if _, err := os.Stat(filepath.Join(*dir, "resume")); err == nil {
					return nil
				}
				time.Sleep(100 * time.Millisecond)
			}
		}, nil
	}
}
