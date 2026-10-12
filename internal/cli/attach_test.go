package cli

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

// zoomServer is waitInstance's server, with herdr's focus and zoom calls
// recorded in order. ACFS's Bash owns the instance: attach and zoom change
// no agent, so they run under either owner.
func zoomServer(t *testing.T) (*herdrtest.Server, func() []string) {
	t.Helper()
	s, _, _ := waitInstance(t, func(string) map[string]any { return nil })
	var mu sync.Mutex
	var calls []string
	record := func(name string) herdrtest.Handler {
		return func(r herdrtest.Request) (any, *herdrtest.Error) {
			var p struct {
				WorkspaceID string `json:"workspace_id"`
				PaneID      string `json:"pane_id"`
				Mode        string `json:"mode"`
			}
			_ = json.Unmarshal(r.Params, &p)
			mu.Lock()
			calls = append(calls, strings.TrimSpace(name+" "+p.WorkspaceID+p.PaneID+" "+p.Mode))
			mu.Unlock()
			if name == "pane.zoom" {
				return map[string]any{"type": "pane_zoom", "zoom": map[string]any{"pane_id": p.PaneID, "zoomed": true,
					"changed": true, "zoom_changed": true, "focus_changed": true}}, nil
			}
			return map[string]any{"type": "ok"}, nil
		}
	}
	s.Handle("workspace.focus", record("workspace.focus"))
	s.Handle("pane.zoom", record("pane.zoom"))
	return s, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := calls
		calls = nil
		return out
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

func TestZoomResolvesSlotNameKindAndPaneID(t *testing.T) {
	s, calls := zoomServer(t)
	code, out, errOut := runVerb(runZoom, "swarm", "BlueLake", "--socket", s.SocketPath, "--json")
	res := decodeEnvelope(t, out)["result"].(map[string]any)
	if code != 0 || res["pane_id"] != "w1:p2" || res["mail_name"] != "BlueLake" || res["zoomed"] != true {
		t.Fatalf("zoom by name = %d %s %s", code, out, errOut)
	}
	if got := calls(); strings.Join(got, ";") != "workspace.focus w1;pane.zoom w1:p2 on" {
		t.Fatalf("calls = %v", got)
	}
	slot, _ := res["slot"].(string)
	for _, c := range []struct{ arg, pane string }{{slot, "w1:p2"}, {"cc", "w1:p1"}, {"codex", "w1:p2"}, {"icyknoll", "w1:p1"}} {
		code, out, _ := runVerb(runZoom, "swarm", c.arg, "--socket", s.SocketPath, "--json")
		if res := decodeEnvelope(t, out)["result"].(map[string]any); code != 0 || res["pane_id"] != c.pane {
			t.Errorf("zoom %q = %d %s", c.arg, code, out)
		}
	}
	calls()
	// A herdr pane id must be a pane of the session's workspace.
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "layouts": []any{}, "tabs": []any{}, "agents": []any{},
			"workspaces": []any{map[string]any{"workspace_id": "w1", "number": 1, "label": "swarm", "focused": true,
				"pane_count": 1, "tab_count": 1, "active_tab_id": "w1:t1", "agent_status": "idle"}},
			"panes": []any{map[string]any{"pane_id": "w1:p7", "tab_id": "w1:t3", "workspace_id": "w1", "terminal_id": "t7",
				"agent_status": "unknown", "focused": false, "revision": 1}},
		}}, nil
	})
	if code, out, _ := runVerb(runZoom, "swarm", "w1:p7", "--socket", s.SocketPath); code != 0 || !strings.Contains(out, "zoomed w1:p7") {
		t.Fatalf("zoom by pane id = %d %s", code, out)
	}
	if got := calls(); strings.Join(got, ";") != "workspace.focus w1;pane.zoom w1:p7 on" {
		t.Fatalf("calls = %v", got)
	}
	for _, c := range []struct{ arg, want string }{
		{"1", "no tmux pane indexes"}, {"w2:p1", "not in workspace w1"}, {"NoSuchAgent", "no active binding"}, {"agy", "no agent matches"},
	} {
		code, out, _ := runVerb(runZoom, "swarm", c.arg, "--socket", s.SocketPath, "--json")
		if code != exitRefused || !strings.Contains(decodeEnvelope(t, out)["error"].(string), c.want) {
			t.Errorf("zoom %q = %d %s", c.arg, code, out)
		}
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("a refused zoom called herdr: %v", got)
	}
	if code, _, errOut := runVerb(runZoom, "swarm"); code != exitRefused || !strings.Contains(errOut, "Usage: nhm zoom") {
		t.Fatalf("zoom without a pane = %d %q", code, errOut)
	}
}

func TestRunDispatchesAttachAndZoom(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, verb := range []string{"attach", "zoom"} {
		code, _, errOut := run(verb, "--no-such-flag")
		if code == 0 || !strings.Contains(errOut, "Usage: nhm "+verb) {
			t.Errorf("%s --no-such-flag = %d %q", verb, code, errOut)
		}
	}
}
