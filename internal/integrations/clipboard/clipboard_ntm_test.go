// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/clipboard/clipboard_test.go and clipboard_extra_test.go at
// commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md. Local
// changes: the backend choice tests as one table, with herdr where ntm
// has the tmux buffer; the paste and command-timeout tests are left out;
// plus nhm's tests of the herdr backend.

package clipboard

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

type nopWriteCloser struct{ *bytes.Buffer }

func (nopWriteCloser) Close() error { return nil }

func newStubDetector(goos string, env map[string]string, bins map[string]bool, version string) detector {
	return detector{
		goos:   goos,
		getenv: func(key string) string { return env[key] },
		lookPath: func(bin string) error {
			if bins[bin] {
				return nil
			}
			return fmt.Errorf("not found")
		},
		readFile: func(path string) ([]byte, error) {
			if path == "/proc/version" && version != "" {
				return []byte(version), nil
			}
			return nil, os.ErrNotExist
		},
		herdrTerminal: func() (io.WriteCloser, error) { return nopWriteCloser{&bytes.Buffer{}}, nil },
	}
}

func TestChooseBackend(t *testing.T) {
	for _, c := range []struct {
		name, goos, version string
		env                 map[string]string
		bins                map[string]bool
		want                string // "" for an error
	}{
		{"darwin pbcopy", "darwin", "", nil, map[string]bool{"pbcopy": true, "pbpaste": true}, "pbcopy"},
		{"darwin without pbcopy", "darwin", "", nil, nil, ""},
		{"wayland", "linux", "", map[string]string{"XDG_SESSION_TYPE": "wayland"}, map[string]bool{"wl-copy": true, "wl-paste": true}, "wl-copy"},
		{"wayland display", "linux", "", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, map[string]bool{"wl-copy": true}, "wl-copy"},
		{"xclip", "linux", "", map[string]string{"DISPLAY": ":0"}, map[string]bool{"xclip": true}, "xclip"},
		{"xsel fallback", "linux", "", map[string]string{"DISPLAY": ":0"}, map[string]bool{"xsel": true}, "xsel"},
		{"xclip without DISPLAY", "linux", "", nil, map[string]bool{"xclip": true}, ""},
		{"wsl", "linux", "", map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, map[string]bool{"clip.exe": true}, "wsl-clipboard"},
		{"wsl proc version", "linux", "Linux version 5.15 microsoft-standard-WSL2", nil, map[string]bool{"clip.exe": true}, "wsl-clipboard"},
		{"wsl without clip.exe falls through", "linux", "", map[string]string{"WSL_DISTRO_NAME": "Ubuntu", "DISPLAY": ":0"}, map[string]bool{"xclip": true}, "xclip"},
		{"wl-copy last resort", "linux", "", nil, map[string]bool{"wl-copy": true}, "wl-copy"},
		{"clip.exe fallback", "linux", "", nil, map[string]bool{"clip.exe": true}, "wsl-clipboard"},
		{"herdr where ntm has tmux", "linux", "", map[string]string{"HERDR_PANE_ID": "wQ:pY"}, nil, "herdr-osc52"},
		{"a tool before herdr", "linux", "", map[string]string{"HERDR_PANE_ID": "wQ:pY", "DISPLAY": ":0"}, map[string]bool{"xclip": true}, "xclip"},
		{"no tools", "linux", "", nil, nil, ""},
		{"unsupported os", "plan9", "", nil, nil, ""},
	} {
		b, err := chooseBackend(newStubDetector(c.goos, c.env, c.bins, c.version))
		switch {
		case c.want == "" && err == nil:
			t.Errorf("%s: backend %s, want an error", c.name, b.name())
		case c.want != "" && (err != nil || b.name() != c.want):
			t.Errorf("%s: %v, %v; want %s", c.name, b, err, c.want)
		}
	}
	_, err := chooseBackend(newStubDetector("linux", nil, nil, ""))
	if err == nil || err.Error() != "no clipboard utility found (install wl-copy, xclip, or xsel)" {
		t.Errorf("no tools = %v", err)
	}
	_, err = chooseBackend(newStubDetector("plan9", nil, nil, ""))
	if err == nil || err.Error() != "clipboard not supported on plan9" {
		t.Errorf("plan9 = %v", err)
	}
}

func TestIsWSLAndWayland(t *testing.T) {
	if !isWSL(newStubDetector("linux", map[string]string{"WSL_INTEROP": "/run/x"}, nil, "")) ||
		isWSL(newStubDetector("linux", nil, nil, "Linux version 6.8 generic")) {
		t.Error("isWSL")
	}
	if !isWayland(newStubDetector("linux", map[string]string{"XDG_SESSION_TYPE": "Wayland"}, nil, "")) ||
		isWayland(newStubDetector("linux", map[string]string{"XDG_SESSION_TYPE": "x11"}, nil, "")) {
		t.Error("isWayland")
	}
}

func TestNewWithDetector(t *testing.T) {
	c, err := newWithDetector(newStubDetector("linux", map[string]string{"DISPLAY": ":0"}, map[string]bool{"xclip": true}, ""))
	if err != nil || !c.Available() || c.Backend() != "xclip" {
		t.Fatalf("newWithDetector = %v, %v", c, err)
	}
	if _, err := newWithDetector(newStubDetector("linux", nil, nil, "")); err == nil {
		t.Fatal("no tools: want an error")
	}
}

// The herdr backend writes one OSC 52 sequence, BEL-ended, to the pane's
// terminal; over herdr's limit it refuses and names --output.
func TestHerdrBackendWritesOSC52(t *testing.T) {
	var buf bytes.Buffer
	b := herdrBackend{open: func() (io.WriteCloser, error) { return nopWriteCloser{&buf}, nil }}
	if err := b.copy("héllo\n"); err != nil {
		t.Fatal(err)
	}
	if want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("héllo\n")) + "\a"; buf.String() != want {
		t.Errorf("wrote %q, want %q", buf.String(), want)
	}
	buf.Reset()
	err := b.copy(strings.Repeat("x", HerdrMax+1))
	if err == nil || !strings.Contains(err.Error(), "give --output FILE") || buf.Len() != 0 {
		t.Errorf("over the limit = %v, wrote %d bytes", err, buf.Len())
	}
	if err := b.copy(strings.Repeat("x", HerdrMax)); err != nil {
		t.Errorf("at the limit = %v", err)
	}
	failing := herdrBackend{open: func() (io.WriteCloser, error) { return nil, errors.New("no terminal") }}
	if err := failing.copy("x"); err == nil || err.Error() != "no terminal" {
		t.Errorf("no terminal = %v", err)
	}
}
