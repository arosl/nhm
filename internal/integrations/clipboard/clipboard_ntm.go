// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/clipboard/clipboard.go at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: copy only (Paste and the
// backends' paste are left out, nothing in nhm pastes); herdr takes the
// tmux buffer's place: inside a herdr pane ($HERDR_PANE_ID) the last
// backend writes OSC 52 to the pane's terminal, which herdr passes to its
// attached clients and their terminals put on the clipboard; New takes
// the opener of that terminal.

// Package clipboard puts text on the user's clipboard, as ntm's copy and
// extract --copy do.
package clipboard

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// clipTimeout is the maximum duration for any clipboard operation.
// Prevents indefinite hangs when clipboard tools stall (e.g., dead X11 display,
// unresponsive Wayland compositor, slow PowerShell on WSL).
const clipTimeout = 5 * time.Second

// HerdrMax is the most text the herdr backend sends: herdr 0.9.5 passed a
// 100,000-byte write whole and dropped a 1,000,000-byte one (seen in the
// sandbox, nhm-2p6.8.1).
const HerdrMax = 100000

// clipCmdRun creates and runs a command that reads from stdin, with timeout.
func clipCmdRun(text string, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), clipTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

// Clipboard exposes a unified copy interface across platforms.
// Use New() to pick the best available backend at runtime.
type Clipboard interface {
	Copy(text string) error
	Available() bool
	Backend() string
}

// backend is an internal abstraction implemented per platform/tool.
type backend interface {
	copy(text string) error
	available() bool
	name() string
}

type detector struct {
	goos     string
	getenv   func(string) string
	lookPath func(string) error
	readFile func(string) ([]byte, error)
	// herdrTerminal opens the calling herdr pane's terminal for writing.
	herdrTerminal func() (io.WriteCloser, error)
}

// defaultDetector uses the real environment and filesystem.
func defaultDetector(herdrTerminal func() (io.WriteCloser, error)) detector {
	return detector{
		goos:   runtime.GOOS,
		getenv: os.Getenv,
		lookPath: func(bin string) error {
			_, err := exec.LookPath(bin)
			return err
		},
		readFile:      os.ReadFile,
		herdrTerminal: herdrTerminal,
	}
}

type clipboardImpl struct {
	b backend
}

func (c *clipboardImpl) Copy(text string) error { return c.b.copy(text) }
func (c *clipboardImpl) Available() bool        { return c.b.available() }
func (c *clipboardImpl) Backend() string        { return c.b.name() }

// New constructs a Clipboard using the current platform and available
// tools. herdrTerminal opens the calling herdr pane's terminal, for the
// herdr backend.
func New(herdrTerminal func() (io.WriteCloser, error)) (Clipboard, error) {
	return newWithDetector(defaultDetector(herdrTerminal))
}

// newWithDetector is test-only entrypoint that accepts a stub detector.
func newWithDetector(det detector) (Clipboard, error) {
	b, err := chooseBackend(det)
	if err != nil {
		return nil, err
	}
	return &clipboardImpl{b: b}, nil
}

func chooseBackend(det detector) (backend, error) {
	switch det.goos {
	case "darwin":
		if det.lookPath("pbcopy") == nil && det.lookPath("pbpaste") == nil {
			return &pbcopyBackend{}, nil
		}
		return nil, fmt.Errorf("pbcopy/pbpaste not found; install Xcode command line tools")

	case "linux":
		if isWSL(det) {
			if det.lookPath("clip.exe") == nil {
				return &wslBackend{}, nil
			}
			// Fall through to other Linux options if clip.exe unavailable.
		}

		if isWayland(det) {
			if det.lookPath("wl-copy") == nil {
				return &wlBackend{}, nil
			}
		}

		// X11 tools - require DISPLAY
		if det.getenv("DISPLAY") != "" {
			if det.lookPath("xclip") == nil {
				return &xclipBackend{}, nil
			}
			if det.lookPath("xsel") == nil {
				return &xselBackend{}, nil
			}
		}

		// As a last resort on Linux, try Wayland if present even without env hint.
		if det.lookPath("wl-copy") == nil {
			return &wlBackend{}, nil
		}

		if det.lookPath("clip.exe") == nil {
			return &wslBackend{}, nil
		}

		// herdr where ntm has the tmux buffer: a pane's OSC 52 write.
		if det.getenv("HERDR_PANE_ID") != "" && det.herdrTerminal != nil {
			return &herdrBackend{open: det.herdrTerminal}, nil
		}

		return nil, errors.New("no clipboard utility found (install wl-copy, xclip, or xsel)")

	default:
		return nil, fmt.Errorf("clipboard not supported on %s", det.goos)
	}
}

func isWSL(det detector) bool {
	if det.getenv("WSL_DISTRO_NAME") != "" || det.getenv("WSL_INTEROP") != "" {
		return true
	}
	data, err := det.readFile("/proc/version")
	if err == nil && bytes.Contains(bytes.ToLower(data), []byte("microsoft")) {
		return true
	}
	return false
}

func isWayland(det detector) bool {
	if strings.ToLower(det.getenv("XDG_SESSION_TYPE")) == "wayland" {
		return true
	}
	return det.getenv("WAYLAND_DISPLAY") != ""
}

// ==== Backends ====

type pbcopyBackend struct{}

func (pbcopyBackend) copy(text string) error {
	return clipCmdRun(text, "pbcopy")
}

func (pbcopyBackend) available() bool { return true }
func (pbcopyBackend) name() string    { return "pbcopy" }

type wlBackend struct{}

func (b wlBackend) copy(text string) error {
	return clipCmdRun(text, "wl-copy")
}

func (b wlBackend) available() bool { return true }
func (b wlBackend) name() string    { return "wl-copy" }

type xclipBackend struct{}

func (xclipBackend) copy(text string) error {
	return clipCmdRun(text, "xclip", "-selection", "clipboard")
}

func (xclipBackend) available() bool { return true }
func (xclipBackend) name() string    { return "xclip" }

type xselBackend struct{}

func (xselBackend) copy(text string) error {
	return clipCmdRun(text, "xsel", "--clipboard", "--input")
}

func (xselBackend) available() bool { return true }
func (xselBackend) name() string    { return "xsel" }

type wslBackend struct{}

func (b wslBackend) copy(text string) error {
	return clipCmdRun(text, "clip.exe")
}

func (b wslBackend) available() bool { return true }
func (b wslBackend) name() string    { return "wsl-clipboard" }

// herdrBackend writes OSC 52 to the calling herdr pane's terminal. herdr
// passes the write to its attached clients, and their terminals put the
// text on the user's clipboard, over SSH too. With no client attached,
// herdr drops it.
type herdrBackend struct {
	open func() (io.WriteCloser, error)
}

func (b herdrBackend) copy(text string) error {
	if len(text) > HerdrMax {
		return fmt.Errorf("herdr passes at most %d bytes to the clipboard, and the text has %d; give --output FILE", HerdrMax, len(text))
	}
	w, err := b.open()
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte(text))+"\a")
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	return err
}

func (herdrBackend) available() bool { return true }
func (herdrBackend) name() string    { return "herdr-osc52" }
