package cli

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/ui/tui/theme"
)

// viewServer is waitInstance's server with workspace w1 ("swarm") of two
// tabs (w1:t1 holds w1:p1; w1:t2 holds w1:p2 and w1:p3, zoomed, with
// w1:p3 focused) and a zoomed tab of another workspace. herdr's zoom and
// focus calls are recorded.
func viewServer(t *testing.T) (*herdrtest.Server, func() []string) {
	t.Helper()
	s, _, _ := waitInstance(t, func(string) map[string]any { return nil })
	pane := func(id, tab, ws string) map[string]any {
		return map[string]any{"pane_id": id, "tab_id": tab, "workspace_id": ws, "terminal_id": "t-" + id,
			"agent_status": "unknown", "focused": false, "revision": 1}
	}
	layout := func(ws, tab, focused string, zoomed bool) map[string]any {
		return map[string]any{"workspace_id": ws, "tab_id": tab, "zoomed": zoomed, "focused_pane_id": focused,
			"area": map[string]any{"x": 0, "y": 0, "width": 80, "height": 24}, "panes": []any{}, "splits": []any{}}
	}
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "tabs": []any{}, "agents": []any{},
			"layouts": []any{layout("w2", "w2:t1", "w2:p1", true), layout("w1", "w1:t1", "w1:p1", false), layout("w1", "w1:t2", "w1:p3", true)},
			"panes":   []any{pane("w2:p1", "w2:t1", "w2"), pane("w1:p1", "w1:t1", "w1"), pane("w1:p2", "w1:t2", "w1"), pane("w1:p3", "w1:t2", "w1")},
			"workspaces": []any{map[string]any{"workspace_id": "w1", "number": 1, "label": "swarm", "focused": false,
				"pane_count": 3, "tab_count": 2, "active_tab_id": "w1:t1", "agent_status": "idle"}},
		}}, nil
	})
	var mu sync.Mutex
	var calls []string
	s.Handle("pane.zoom", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
			Mode   string `json:"mode"`
		}
		_ = json.Unmarshal(r.Params, &p)
		mu.Lock()
		calls = append(calls, "pane.zoom "+p.PaneID+" "+p.Mode)
		mu.Unlock()
		changed := p.PaneID == "w1:p3"
		reason := "already_unzoomed"
		if changed {
			reason = ""
		}
		return map[string]any{"type": "pane_zoom", "zoom": map[string]any{"pane_id": p.PaneID, "zoomed": false,
			"changed": changed, "zoom_changed": changed, "focus_changed": false, "reason": reason,
			"layout": map[string]any{"workspace_id": "w1", "tab_id": "w1:t1", "zoomed": false}}}, nil
	})
	s.Handle("workspace.focus", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			WorkspaceID string `json:"workspace_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		mu.Lock()
		calls = append(calls, "workspace.focus "+p.WorkspaceID)
		mu.Unlock()
		return map[string]any{"type": "ok"}, nil
	})
	return s, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := calls
		calls = nil
		return out
	}
}

// tiled is ntm's view line, with ntm's colour escapes.
func tiled(session string) string {
	t := theme.Current()
	return colorize(t.Success) + "✓" + colorize(t.Text) + " Tiled layout applied to '" + session + "'\n"
}

// nhm view, tile and v are ntm's view on herdr: the zoom off in each
// zoomed tab of the workspace, through the tab's focused pane so that its
// focus stays (no layout: the operator's ruling, nhm-k9d, 2026-10-23),
// ntm's line, then the workspace's focus, as ntm attaches.
func TestViewNTMTakesTheZoomOffThenAttaches(t *testing.T) {
	s, calls := viewServer(t)
	for _, verb := range []string{"view", "tile", "v"} {
		code, out, errOut := run(verb, "swarm", "--socket", s.SocketPath)
		if code != 0 || out != tiled("swarm") || errOut != "" {
			t.Fatalf("%s = %d %q %q", verb, code, out, errOut)
		}
		if got := strings.Join(calls(), ";"); got != "pane.zoom w1:p3 off;workspace.focus w1" {
			t.Fatalf("%s calls = %s", verb, got)
		}
	}
	// A unique prefix names the session, and the only one is taken
	// without a name, as in ntm.
	for _, args := range [][]string{{"sw"}, {}} {
		if code, out, _ := run(append(append([]string{"view"}, args...), "--socket", s.SocketPath)...); code != 0 || out != tiled("swarm") {
			t.Errorf("view %v = %d %q", args, code, out)
		}
	}
	calls()
}

// Under --json ntm tiles and attaches nothing: SuccessResponse.
func TestViewNTMJSONIsSuccessResponse(t *testing.T) {
	s, calls := viewServer(t)
	code, out, errOut := run("view", "swarm", "--socket", s.SocketPath, "--json")
	if code != 0 || errOut != "" || out != "{\n  \"success\": true,\n  \"message\": \"tiled layout applied to 'swarm'\"\n}\n" {
		t.Fatalf("view --json = %d %q %q", code, out, errOut)
	}
	if got := strings.Join(calls(), ";"); got != "pane.zoom w1:p3 off" {
		t.Fatalf("calls = %s", got)
	}
}

// ntm's errors, exit 1: the resolver's (the root's, before the session is
// known), a failed tiling (ntm's JSON envelope under --json), and a failed
// attach after the line.
func TestViewNTMErrors(t *testing.T) {
	s, calls := viewServer(t)
	code, out, errOut := run("view", "nosuch", "--socket", s.SocketPath)
	if code != 1 || out != "" || !strings.HasPrefix(errOut, `Error: session "nosuch" not found`) {
		t.Errorf("unknown session = %d %q %q", code, out, errOut)
	}
	if code, _, errOut := run("view", "a", "b", "--socket", s.SocketPath); code != 1 || errOut != "Error: accepts at most 1 arg(s), received 2\n" {
		t.Errorf("two args = %d %q", code, errOut)
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("a refused view called herdr: %v", got)
	}
	fail := func(method string) {
		s.Handle(method, func(herdrtest.Request) (any, *herdrtest.Error) {
			return nil, &herdrtest.Error{Code: "pane_not_found", Message: method + " failed"}
		})
	}
	fail("workspace.focus")
	code, out, errOut = run("view", "swarm", "--socket", s.SocketPath)
	if code != 1 || out != tiled("swarm") || !strings.Contains(errOut, "workspace.focus failed") {
		t.Errorf("failed attach = %d %q %q", code, out, errOut)
	}
	fail("pane.zoom")
	code, out, errOut = run("view", "swarm", "--socket", s.SocketPath)
	if code != 1 || out != "" || !strings.HasPrefix(errOut, "Error: failed to apply layout: tab w1:t2: ") {
		t.Errorf("failed tiling = %d %q %q", code, out, errOut)
	}
	code, out, _ = run("view", "swarm", "--socket", s.SocketPath, "--json")
	if code != 1 || !strings.HasPrefix(out, `{"success":false,"error":"failed to apply layout: tab w1:t2: `) || strings.Count(out, "\n") != 1 {
		t.Errorf("failed tiling --json = %d %q", code, out)
	}
}
