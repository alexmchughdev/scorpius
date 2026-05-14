// Command scorpius is the chaos engineering agent.
package main

import (
	"os"

	"github.com/alexmchughdev/scorpius/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:]))
}
