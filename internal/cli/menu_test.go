package cli

import (
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/ui"
)

// The menu's surfaces are the Go palette in its popup and the board's
// tab, in the captured pane's directory.
func TestMenuSurfaces(t *testing.T) {
	c := ui.Context{SocketPath: "/run/herdr.sock", WorkspaceID: "w1", Cwd: t.TempDir()}
	s := menuSurfaces(c)
	args := func(id string) string {
		cmd := s(id)
		if cmd == nil {
			return ""
		}
		if cmd.Dir != c.Cwd {
			t.Errorf("%s runs in %q", id, cmd.Dir)
		}
		return strings.Join(cmd.Args[1:], " ")
	}
	if args("palette") != "palette" || args("board") != "ui board" || s("palette.go") != nil || s("view.status") != nil {
		t.Fatalf("surfaces: %q %q", args("palette"), args("board"))
	}
}
