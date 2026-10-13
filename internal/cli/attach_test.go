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

func TestZoomResolvesSlotNameKindAndPaneID(t *testing.T) {
	s, calls := zoomServer(t)
	code, out, errOut := runVerb(runZoom, "swarm", "BlueLake", "--socket", s.SocketPath, "--json")
	res := decodeEnvelope(t, out)["result"].(map[string]any)
	if code != 0 || res["pane_id"] != "w1:p2" || res["mail_name"] != "BlueLake" || res["zoomed"] != true {
		t.Fatalf("zoom by name = %d %s %s", code, out, errOut)
	}
	// The zoomed pane's tab comes in front of the attached clients, which
	// do not follow pane.zoom to another tab (nhm-e46).
	if got := calls(); strings.Join(got, ";") != "workspace.focus w1;pane.zoom w1:p2 on;tab.focus w1:t2" {
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
	s.Handle("session.snapshot", snapshotWithPanes(map[string]string{"w1:p7": "w1:t3"}))
	if code, out, _ := runVerb(runZoom, "swarm", "w1:p7", "--socket", s.SocketPath); code != 0 || !strings.Contains(out, "zoomed w1:p7") {
		t.Fatalf("zoom by pane id = %d %s", code, out)
	}
	if got := calls(); strings.Join(got, ";") != "workspace.focus w1;pane.zoom w1:p7 on;tab.focus w1:t3" {
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

// The pane is zoomed but herdr does not bring its tab in front: nhm zoom
// succeeds with a warning, never fails a zoom that worked.
func TestZoomWarnsWhenItsTabStaysBehind(t *testing.T) {
	s, calls := zoomServer(t)
	s.Handle("tab.focus", func(herdrtest.Request) (any, *herdrtest.Error) {
		return nil, &herdrtest.Error{Code: "tab_not_found", Message: "no such tab"}
	})
	code, out, errOut := runVerb(runZoom, "swarm", "BlueLake", "--socket", s.SocketPath, "--json")
	env := decodeEnvelope(t, out)
	res, _ := env["result"].(map[string]any)
	warnings, _ := env["warnings"].([]any)
	if code != exitOK || res["zoomed"] != true || len(warnings) != 1 ||
		!strings.Contains(warnings[0].(string), "zoomed w1:p2, but herdr did not bring its tab in front") {
		t.Fatalf("zoom = %d %s %s", code, out, errOut)
	}
	if got := calls(); strings.Join(got, ";") != "workspace.focus w1;pane.zoom w1:p2 on" {
		t.Fatalf("calls = %v", got)
	}
	code, out, errOut = runVerb(runZoom, "swarm", "BlueLake", "--socket", s.SocketPath)
	if code != exitOK || !strings.Contains(out, "zoomed BlueLake") || !strings.Contains(errOut, "warning: zoomed w1:p2, but herdr did not") {
		t.Fatalf("zoom = %d %q %q", code, out, errOut)
	}
}

// A failed workspace focus or zoom fails nhm zoom: exit 1, no "zoomed",
// no warning, and no tab focus (ScarletOrchid's review of 289e9a4).
func TestFailedZoomFails(t *testing.T) {
	for _, method := range []string{"workspace.focus", "pane.zoom"} {
		s, calls := zoomServer(t)
		s.Handle(method, func(herdrtest.Request) (any, *herdrtest.Error) {
			return nil, &herdrtest.Error{Code: "pane_not_found", Message: method + " failed"}
		})
		code, out, errOut := runVerb(runZoom, "swarm", "BlueLake", "--socket", s.SocketPath, "--json")
		env := decodeEnvelope(t, out)
		msg, _ := env["error"].(string)
		warnings, _ := env["warnings"].([]any)
		if code != exitError || env["result"] != nil || len(warnings) != 0 || !strings.Contains(msg, method+" failed") {
			t.Errorf("%s fails: zoom --json = %d %s %q", method, code, out, errOut)
		}
		code, out, errOut = runVerb(runZoom, "swarm", "BlueLake", "--socket", s.SocketPath)
		if code != exitError || strings.Contains(out, "zoomed") || strings.Contains(errOut, "warning") || !strings.Contains(errOut, method+" failed") {
			t.Errorf("%s fails: zoom = %d %q %q", method, code, out, errOut)
		}
		if got := strings.Join(calls(), ";"); strings.Contains(got, "tab.focus") {
			t.Errorf("%s fails: calls = %s", method, got)
		}
	}
}

// The tab to bring in front is the one pane.zoom's answer names: a pane
// that herdr's snapshot no longer lists after the zoom is no warning.
func TestZoomTakesTheTabFromTheZoom(t *testing.T) {
	s, calls := zoomServer(t)
	s.Handle("session.snapshot", snapshotWithPanes(map[string]string{"w1:p1": "w1:t1"}))
	code, out, errOut := runVerb(runZoom, "swarm", "BlueLake", "--socket", s.SocketPath, "--json")
	env := decodeEnvelope(t, out)
	if warnings, _ := env["warnings"].([]any); code != exitOK || len(warnings) != 0 || env["error"] != nil {
		t.Fatalf("zoom = %d %s %q", code, out, errOut)
	}
	if got := strings.Join(calls(), ";"); got != "workspace.focus w1;pane.zoom w1:p2 on;tab.focus w1:t2" {
		t.Fatalf("calls = %s", got)
	}
}

// A pane alone in its tab is not zoomed (herdr: single_pane): nhm zoom
// brings its tab in front and says so, without "zoomed" or a warning
// (nhm-tt9).
func TestZoomSaysWhenThePaneIsAlone(t *testing.T) {
	s, calls := zoomServer(t)
	s.Handle("pane.zoom", func(r herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "pane_zoom", "zoom": map[string]any{"pane_id": "w1:p2", "zoomed": false, "changed": false,
			"zoom_changed": false, "focus_changed": false, "reason": "single_pane",
			"layout": map[string]any{"workspace_id": "w1", "tab_id": "w1:t2", "zoomed": false}}}, nil
	})
	code, out, errOut := runVerb(runZoom, "swarm", "BlueLake", "--socket", s.SocketPath)
	if code != exitOK || !strings.Contains(out, "BlueLake (") || !strings.Contains(out, "is alone in its tab in swarm: nothing to zoom") ||
		strings.Contains(out, "zoomed") || errOut != "" {
		t.Fatalf("zoom = %d %q %q", code, out, errOut)
	}
	if got := strings.Join(calls(), ";"); !strings.HasSuffix(got, "tab.focus w1:t2") {
		t.Fatalf("calls = %s", got)
	}
	code, out, _ = runVerb(runZoom, "swarm", "BlueLake", "--socket", s.SocketPath, "--json")
	env := decodeEnvelope(t, out)
	res, _ := env["result"].(map[string]any)
	if warnings, _ := env["warnings"].([]any); code != exitOK || res["zoomed"] == true || len(warnings) != 0 || env["error"] != nil {
		t.Fatalf("zoom --json = %d %s", code, out)
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
