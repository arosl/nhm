// Command guarded links bubbletea and termbg, and does nothing else
// (TestStartsWithoutProbe).
package main

import (
	_ "github.com/charmbracelet/bubbletea"

	_ "github.com/arosl/nhm/internal/ui/tui/termbg"
)

func main() {}
