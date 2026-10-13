package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/ui"
)

// The menu's surfaces follow [ui]: today's palette and board by default,
// with "palette (new)" beside them; the Go ones when [ui] says so.
func TestMenuSurfaces(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	c := ui.Context{SocketPath: "/run/herdr.sock", WorkspaceID: "w1", Cwd: t.TempDir()}
	args := func(surface func(string) []string, id string) string { return strings.Join(surface(id), " ") }
	read := func() func(string) []string {
		t.Helper()
		s, err := menuSurfaces(c)
		if err != nil {
			t.Fatal(err)
		}
		return func(id string) []string {
			cmd := s(id)
			if cmd == nil {
				return nil
			}
			if cmd.Dir != c.Cwd {
				t.Errorf("%s runs in %q", id, cmd.Dir)
			}
			return cmd.Args[1:]
		}
	}
	s := read()
	if args(s, "palette") != "palette" || args(s, "palette.go") != "palette --go" || args(s, "board") != "board" || s("view.status") != nil {
		t.Fatalf("defaults: %q %q %q", args(s, "palette"), args(s, "palette.go"), args(s, "board"))
	}
	path := filepath.Join(cfg, "nhm", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[ui]\npalette = \"go\"\nboard = \"go\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s = read()
	if args(s, "palette") != "palette --go" || s("palette.go") != nil || args(s, "board") != "ui board" {
		t.Fatalf("go: %q %v %q", args(s, "palette"), s("palette.go"), args(s, "board"))
	}
	if err := os.WriteFile(path, []byte("[ui]\nboard = \"Go\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := menuSurfaces(c); err == nil {
		t.Fatal("a typo in [ui] was read")
	}
}
