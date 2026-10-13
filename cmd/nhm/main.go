// Command nhm is the herdr wrapper of the ACFS flywheel: what ntm is to
// tmux, on herdr.
package main

import (
	"os"

	"github.com/arosl/nhm/internal/cli"
	"github.com/arosl/nhm/internal/ui"
	// Pins Lip Gloss's background before bubbletea's init asks the
	// terminal for it: see package termbg.
	_ "github.com/arosl/nhm/internal/ui/tui/termbg"
)

func main() {
	ui.FixPluginPath()
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
