package cli

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

// zoomTabs is each pane's tab, as pane.zoom's layout names it.
var zoomTabs = map[string]string{"w1:p1": "w1:t1", "w1:p2": "w1:t2", "w1:p7": "w1:t3"}

// zoomServer is waitInstance's server, with the agents' panes in their
// tabs, and herdr's focus and zoom calls recorded in order. ACFS's Bash
// owns the instance: attach and zoom change no agent, so they run under
// either owner.
func zoomServer(t *testing.T) (*herdrtest.Server, func() []string) {
	t.Helper()
	s, _, _ := waitInstance(t, func(string) map[string]any { return nil })
	s.Handle("session.snapshot", snapshotWithPanes(map[string]string{"w1:p1": "w1:t1", "w1:p2": "w1:t2"}))
	var mu sync.Mutex
	var calls []string
	record := func(name string) herdrtest.Handler {
		return func(r herdrtest.Request) (any, *herdrtest.Error) {
			var p struct {
				WorkspaceID string `json:"workspace_id"`
				PaneID      string `json:"pane_id"`
				TabID       string `json:"tab_id"`
				Mode        string `json:"mode"`
			}
			_ = json.Unmarshal(r.Params, &p)
			mu.Lock()
			calls = append(calls, strings.TrimSpace(name+" "+p.WorkspaceID+p.PaneID+p.TabID+" "+p.Mode))
			mu.Unlock()
			switch name {
			case "pane.zoom":
				return map[string]any{"type": "pane_zoom", "zoom": map[string]any{"pane_id": p.PaneID, "zoomed": true,
					"changed": true, "zoom_changed": true, "focus_changed": true,
					"layout": map[string]any{"workspace_id": "w1", "tab_id": zoomTabs[p.PaneID], "zoomed": true}}}, nil
			case "tab.focus":
				return map[string]any{"type": "tab_info", "tab": map[string]any{"tab_id": p.TabID, "workspace_id": "w1", "number": 1,
					"label": "1", "focused": true, "pane_count": 1, "agent_status": "idle"}}, nil
			}
			return map[string]any{"type": "ok"}, nil
		}
	}
	s.Handle("workspace.focus", record("workspace.focus"))
	s.Handle("pane.zoom", record("pane.zoom"))
	s.Handle("tab.focus", record("tab.focus"))
	return s, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := calls
		calls = nil
		return out
	}
}

// snapshotWithPanes is waitInstance's snapshot (workspace w1, "swarm")
// with these panes, each in its tab (pane id -> tab id).
func snapshotWithPanes(tabs map[string]string) herdrtest.Handler {
	return func(herdrtest.Request) (any, *herdrtest.Error) {
		panes := []any{}
		for pane, tab := range tabs {
			panes = append(panes, map[string]any{"pane_id": pane, "tab_id": tab, "workspace_id": "w1", "terminal_id": "t-" + pane,
				"agent_status": "unknown", "focused": false, "revision": 1})
		}
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "layouts": []any{}, "tabs": []any{}, "agents": []any{}, "panes": panes,
			"workspaces": []any{map[string]any{"workspace_id": "w1", "number": 1, "label": "swarm", "focused": true,
				"pane_count": len(panes), "tab_count": 2, "active_tab_id": "w1:t1", "agent_status": "idle"}},
		}}, nil
	}
}

func TestAttachFocusesTheSessionsWorkspace(t *testing.T) {
	s, calls := zoomServer(t)
	code, out, errOut := runVerb(runAttach, "swarm", "--socket", s.SocketPath)
	if code != 0 || !strings.Contains(out, "focused swarm (w1)") {
		t.Fatalf("attach = %d %q %q", code, out, errOut)
	}
	if got := calls(); strings.Join(got, ";") != "workspace.focus w1" {
		t.Fatalf("calls = %v", got)
	}
	code, out, _ = runVerb(runAttach, "w1", "--socket", s.SocketPath, "--json")
	if res := decodeEnvelope(t, out)["result"].(map[string]any); code != 0 || res["workspace_id"] != "w1" {
		t.Fatalf("attach --json = %d %s", code, out)
	}
	code, out, _ = runVerb(runAttach, "nosuch", "--socket", s.SocketPath, "--json")
	if code != exitRefused || !strings.Contains(decodeEnvelope(t, out)["error"].(string), "nhm list") {
		t.Fatalf("unknown session = %d %s", code, out)
	}
	if got := calls(); len(got) != 1 {
		t.Fatalf("an unknown session focused something: %v", got)
	}
	if code, _, errOut := runVerb(runAttach, "a", "b"); code != exitRefused || !strings.Contains(errOut, "Usage: nhm attach") {
		t.Fatalf("two sessions = %d %q", code, errOut)
	}
}

func TestRunDispatchesAttach(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if code, _, errOut := run("attach", "--no-such-flag"); code == 0 || !strings.Contains(errOut, "Usage: nhm attach") {
		t.Errorf("attach --no-such-flag = %d %q", code, errOut)
	}
}
