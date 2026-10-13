// Package termbg pins Lip Gloss's background, so that no nhm verb asks
// the terminal for it at start.
//
// bubbletea v1.3.10 asks Lip Gloss in its init whether the background is
// dark (tea_init.go). Lip Gloss then writes an OSC 11 query and a cursor
// position request to the terminal, and waits up to five seconds for an
// answer. The nhm binary holds the UI, so once it links bubbletea every
// verb would wait in a terminal that answers neither: script(1), an
// agent's pty. Measured: 5,040 ms against 24 ms. ntm removes that call in
// its vendored bubbletea; nhm keeps the upstream module and pins the
// answer first.
//
// This init runs before bubbletea's. Since Go 1.21, the packages that are
// ready are initialized in import path order. This package imports only
// Lip Gloss, which bubbletea imports too, and github.com/arosl/... sorts
// before github.com/charmbracelet/bubbletea. Keep it so: an import here
// that bubbletea does not have can make this init run later.
//
// The value is ntm's default, dark. A UI sets the real one before its
// program starts, without terminal I/O: theme.ApplyLipGlossDefaults.
//
// cmd/nhm imports this package, so the binary that holds nhm's UI links
// it (TestBinariesLinkTermbg). nhm-hook links neither this package nor
// bubbletea or Lip Gloss (TestHookLinksNoTUI): package ui, which the hook
// imports, must not import internal/ui/tui.
package termbg

import "github.com/charmbracelet/lipgloss"

func init() { lipgloss.SetHasDarkBackground(true) }
