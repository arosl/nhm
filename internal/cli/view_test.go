package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

// viewServer is waitInstance's server with workspace w1 ("swarm") of two
// tabs (w1:t1 holds w1:p1; w1:t2 holds w1:p2 and w1:p3, zoomed) and a
// pane of another workspace. herdr's zoom and focus calls are recorded.
func viewServer(t *testing.T) (*herdrtest.Server, func() []string) {
	t.Helper()
	s, _, _ := waitInstance(t, func(string) map[string]any { return nil })
	pane := func(id, tab, ws string) map[string]any {
		return map[string]any{"pane_id": id, "tab_id": tab, "workspace_id": ws, "terminal_id": "t-" + id,
			"agent_status": "unknown", "focused": false, "revision": 1}
	}
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "layouts": []any{}, "tabs": []any{}, "agents": []any{},
			"panes": []any{pane("w2:p1", "w2:t1", "w2"), pane("w1:p1", "w1:t1", "w1"), pane("w1:p2", "w1:t2", "w1"), pane("w1:p3", "w1:t2", "w1")},
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
		changed := p.PaneID == "w1:p2"
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

// nhm view, tile and v are ntm's view on herdr: the zoom off in each tab
// of the workspace, then its focus (the operator's ruling, nhm-k9d,
// 2026-10-23).
func TestViewTakesTheZoomOffAndFocuses(t *testing.T) {
	s, calls := viewServer(t)
	for _, verb := range []string{"view", "tile", "v"} {
		code, out, errOut := run(verb, "swarm", "--socket", s.SocketPath)
		if code != 0 || out != "✓ Zoom off in every tab of 'swarm' (tabs: 2, zoomed before: 1); focused it; open herdr to see it\n" {
			t.Fatalf("%s = %d %q %q", verb, code, out, errOut)
		}
		if got := strings.Join(calls(), ";"); got != "pane.zoom w1:p1 off;pane.zoom w1:p2 off;workspace.focus w1" {
			t.Fatalf("%s calls = %s", verb, got)
		}
	}
	code, out, _ := run("view", "w1", "--socket", s.SocketPath, "--json")
	res := envelopeOf(t, out)["result"].(map[string]any)
	if code != 0 || res["workspace_id"] != "w1" || fmt.Sprint(res["tabs"]) != "[w1:t1 w1:t2]" || fmt.Sprint(res["unzoomed"]) != "[w1:t2]" {
		t.Fatalf("view --json = %d %s", code, out)
	}
	calls()
	// Without a session: the calling pane's workspace.
	t.Setenv("HERDR_WORKSPACE_ID", "w1")
	if code, out, _ := run("view", "--socket", s.SocketPath); code != 0 || !strings.Contains(out, "'w1'") {
		t.Fatalf("view without a session = %d %q", code, out)
	}
	calls()
	t.Setenv("HERDR_WORKSPACE_ID", "")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{}, "name the session"},
		{[]string{"nosuch"}, `session "nosuch": no such workspace (nhm list shows the sessions)`},
		{[]string{"a", "b"}, "Usage: nhm view"},
	} {
		code, _, errOut := run(append(append([]string{"view"}, c.args...), "--socket", s.SocketPath)...)
		if code != exitRefused || !strings.Contains(errOut, c.want) {
			t.Errorf("view %v = %d %q, want %q", c.args, code, errOut, c.want)
		}
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("a refused view called herdr: %v", got)
	}
}
