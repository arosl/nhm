// Command nhm is the herdr wrapper of the ACFS flywheel: what ntm is to
// tmux, on herdr.
package main

import (
	"os"

	"github.com/arosl/nhm/internal/cli"
	"github.com/arosl/nhm/internal/ui"
)

func main() {
	ui.FixPluginPath()
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
