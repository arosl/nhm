//go:build !unix

// Taken from ntm (github.com/Dicklesworthstone/ntm)
// e2e/fakeagent/resize_signal_other.go at commit 9d5ed977; see LICENSE in
// this directory and PROVENANCE.md. Local changes: this header only.

package main

import (
	"os"
	"syscall"
)

// resizeSignal on platforms without SIGWINCH (Windows, Plan 9, ...) is a
// signal number that is never delivered: signal.Notify registration is
// harmless and the resize case simply never fires. The fixture is only run
// by the Unix-only tmux E2E harness; this stub exists so the package builds
// and vets on every GOOS.
var resizeSignal os.Signal = syscall.Signal(0x7fff)
