package main

import (
	"os"

	"github.com/chrisbanes/portico/helper/internal/protocol"
)

func main() {
	os.Exit(protocol.Serve(os.Stdin, os.Stdout, os.Stderr))
}
