//go:build unix

// Taken from ntm (github.com/Dicklesworthstone/ntm)
// e2e/fakeagent/resize_signal_unix.go at commit 9d5ed977; see LICENSE in
// this directory and PROVENANCE.md. Local changes: this header only.

package main

import (
	"os"
	"syscall"
)

// resizeSignal is the terminal-resize signal the render loop listens for.
// SIGWINCH only exists on Unix; see resize_signal_other.go for the stub used
// on platforms without it (the fixture itself is only exercised by the
// Unix-only tmux E2E harness, but the package must keep building everywhere
// so `GOOS=windows go vet ./...` stays clean).
var resizeSignal os.Signal = syscall.SIGWINCH
