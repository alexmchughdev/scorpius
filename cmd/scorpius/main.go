// Command scorpius is the chaos engineering agent.
package main

import (
	"os"

	"github.com/alexmchughdev/scorpius/internal/cli"
)

// version is overwritten at build time via -ldflags="-X main.version=...".
// It defaults to "dev" so an untagged local build is recognisable.
var version = "dev"

func main() {
	os.Exit(cli.Execute(os.Args[1:], cli.WithVersion(version)))
}
