// portico-service is this user's Portal-management service, started by
// launchd or systemd --user. See docs/service-lifecycle.md.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/chrisbanes/portico/helper/internal/ipc"
	"github.com/chrisbanes/portico/helper/internal/service"
	"github.com/chrisbanes/portico/helper/internal/servicehost"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("portico-service: ")
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("portico-service", flag.ContinueOnError)
	variantName := flags.String("variant", "production", "production, dev or test")
	stateRoot := flags.String("state-root", "", "test variant only: absolute state root")
	runtimeDir := flags.String("runtime-dir", "", "test variant only: absolute directory for the management endpoint")
	legacyGuard := flags.Bool("legacy-guard", false, "test variant on macOS only: require the legacy-writer guard, as production and dev do")
	selectRuntime := runtimeFlags(flags)
	selectHook := hookFlags(flags)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		log.Print("unexpected arguments")
		return 2
	}
	variant, err := servicehost.ParseVariant(*variantName)
	if err != nil {
		log.Print(servicehost.Reason(err))
		return 2
	}
	factory, err := selectRuntime(variant)
	if err != nil {
		log.Print(err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	hook, err := selectHook(ctx, variant)
	if err != nil {
		log.Print(err)
		return 2
	}
	roots, err := servicehost.Resolve(variant, *stateRoot, *runtimeDir, *legacyGuard)
	if err != nil {
		log.Print(servicehost.Reason(err))
		return 1
	}
	err = servicehost.Run(ctx, servicehost.Config{
		Roots: roots,
		Engine: service.EngineOptions{
			RuntimeFactory:            factory,
			Discoverer:                discoverer(),
			LaunchAtLoginOfferCapable: runtime.GOOS == "darwin" && variant == ipc.Production,
		},
		Limits: ipc.DefaultLimits,
		Hook:   hook,
	})
	if err != nil {
		log.Print(servicehost.Reason(err))
		return 1
	}
	return 0
}
