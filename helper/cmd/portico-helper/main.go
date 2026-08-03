package main

import (
	"os"
	"path/filepath"

	"github.com/chrisbanes/portico/helper/internal/portal"
	"github.com/chrisbanes/portico/helper/internal/protocol"
)

func main() {
	if len(os.Args) != 3 || os.Args[1] != "--state-root" || !filepath.IsAbs(os.Args[2]) {
		_, _ = os.Stderr.WriteString("portico-helper: invalid launch configuration\n")
		os.Exit(2)
	}
	stateRoot := filepath.Clean(os.Args[2])
	if err := os.MkdirAll(stateRoot, 0o700); err != nil || os.Chmod(stateRoot, 0o700) != nil {
		_, _ = os.Stderr.WriteString("portico-helper: unavailable state directory\n")
		os.Exit(2)
	}
	runtime := portal.NewRuntime(stateRoot, portal.NewTSNetNode)
	os.Exit(protocol.ServeWithRuntime(os.Stdin, os.Stdout, os.Stderr, runtime))
}
