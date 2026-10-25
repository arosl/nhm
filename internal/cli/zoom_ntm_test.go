package cli

import (
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/ui/tui/theme"
)

// zoomWorld is listWorld's enrolled server (swarm: IcyKnoll's Claude in
// slot 0.1, BlueLake's Codex in 0.2 and a shell, all in tab w1:t1), with
// herdr's zoom, focus and process calls recorded in order.
func zoomWorld(t *testing.T) (*herdrtest.Server, func() string) {
	t.Helper()
	s := listWorld(t, true)
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
					"layout": map[string]any{"workspace_id": "w1", "tab_id": "w1:t1", "zoomed": true}}}, nil
			case "tab.focus":
				return map[string]any{"type": "tab_info", "tab": map[string]any{"tab_id": p.TabID, "workspace_id": "w1", "number": 1,
					"label": "1", "focused": true, "pane_count": 3, "agent_status": "idle"}}, nil
			case "pane.process_info":
				name := map[string]string{"w1:p1": "claude", "w1:p2": "codex", "w1:p3": "zsh"}[p.PaneID]
				return map[string]any{"type": "pane_process_info", "process_info": map[string]any{"pane_id": p.PaneID,
					"foreground_processes": []any{map[string]any{"pid": 7, "name": name}}}}, nil
			}
			return map[string]any{"type": "ok"}, nil
		}
	}
	for _, m := range []string{"workspace.focus", "pane.zoom", "tab.focus", "pane.process_info"} {
		s.Handle(m, record(m))
	}
	return s, func() string {
		mu.Lock()
		defer mu.Unlock()
		out := strings.Join(calls, ";")
		calls = nil
		return out
	}
}

// zoomed is ntm's success line, with ntm's colour escapes.
func zoomed(what string) string {
	t := theme.Current()
	return colorize(t.Success) + "✓" + colorize(t.Text) + " Zoomed " + what + "\n"
}

// ntm's zoom: the pane's zoom, the line, then the attach (on herdr the
// workspace's focus, and the pane's tab in front).
func TestZoomNTMZoomsThenAttaches(t *testing.T) {
	s, calls := zoomWorld(t)
	code, out, errOut := run("zoom", "swarm", "1", "--socket", s.SocketPath)
	if code != 0 || out != zoomed("pane 1 in 'swarm'") || errOut != "" {
		t.Fatalf("zoom = %d %q %q", code, out, errOut)
	}
	if got := calls(); got != "pane.zoom w1:p1 on;workspace.focus w1;tab.focus w1:t1" {
		t.Fatalf("calls = %s", got)
	}
	// z is zoom, and a unique prefix names the session, as in ntm.
	if code, out, _ := run("z", "swa", "2", "--socket", s.SocketPath); code != 0 || out != zoomed("pane 2 in 'swarm'") {
		t.Fatalf("z = %d %q", code, out)
	}
}

// Under --json ntm zooms and attaches nothing: SuccessResponse.
func TestZoomNTMJSONIsSuccessResponse(t *testing.T) {
	s, calls := zoomWorld(t)
	code, out, errOut := run("zoom", "swarm", "cod", "--socket", s.SocketPath, "--json")
	if code != 0 || errOut != "" || out != "{\n  \"success\": true,\n  \"message\": \"zoomed pane 2 in 'swarm'\"\n}\n" {
		t.Fatalf("zoom --json = %d %q %q", code, out, errOut)
	}
	if got := calls(); got != "pane.zoom w1:p2 on" {
		t.Fatalf("calls = %s", got)
	}
}

// The pane is ntm's index, a slot, a herdr pane id, an agent type (its
// first pane) or an agent's name.
func TestZoomNTMPaneForms(t *testing.T) {
	s, calls := zoomWorld(t)
	for _, c := range []struct{ arg, pane, ref string }{
		{"1", "w1:p1", "1"}, {"0.2", "w1:p2", "2"}, {"w1:p3", "w1:p3", "w1:p3"}, {"cc", "w1:p1", "1"},
		{"claude", "w1:p1", "1"}, {"codex", "w1:p2", "2"}, {"user", "w1:p3", "w1:p3"}, {"bluelake", "w1:p2", "2"},
	} {
		code, out, errOut := run("zoom", "swarm", c.arg, "--socket", s.SocketPath, "--json")
		if code != 0 || !strings.Contains(out, `"zoomed pane `+c.ref+` in 'swarm'"`) {
			t.Errorf("zoom %q = %d %q %q", c.arg, code, out, errOut)
		}
		if got := calls(); got != "pane.zoom "+c.pane+" on" {
			t.Errorf("zoom %q: calls = %s", c.arg, got)
		}
	}
}

// ntm's errors: after the session is known, ntm's JSON failure envelope
// under --json; before, the root's; text is "Error: <msg>"; exit 1.
func TestZoomNTMErrors(t *testing.T) {
	s, calls := zoomWorld(t)
	for _, c := range []struct{ arg, msg string }{
		{"3", "pane 3 not found in session 'swarm'"},
		{"2.1", "pane 2.1 not found in session 'swarm'"},
		{"w2:p1", "pane w2:p1 not found in session 'swarm'"},
		{"agy", `no panes match selector "agy"`},
		{"%3", "pane %3: herdr has no tmux pane ids; give a pane index, the slot N.M (nhm status shows it) or a herdr pane id"},
	} {
		code, out, errOut := run("zoom", "swarm", c.arg, "--socket", s.SocketPath, "--json")
		var env ntmErrorResponse
		if code != 1 || errOut != "" || json.Unmarshal([]byte(out), &env) != nil || env.Error != c.msg || strings.Count(out, "\n") != 1 {
			t.Errorf("zoom %q --json = %d %q %q", c.arg, code, out, errOut)
		}
		if code, out, errOut := run("zoom", "swarm", c.arg, "--socket", s.SocketPath); code != 1 || out != "" || errOut != "Error: "+c.msg+"\n" {
			t.Errorf("zoom %q = %d %q %q", c.arg, code, out, errOut)
		}
	}
	if got := calls(); got != "" {
		t.Fatalf("a refused zoom called herdr: %s", got)
	}
	code, _, errOut := run("zoom", "nosuch", "1", "--socket", s.SocketPath)
	if code != 1 || !strings.HasPrefix(errOut, `Error: session "nosuch" not found`) {
		t.Errorf("unknown session = %d %q", code, errOut)
	}
	code, out, _ := run("zoom", "nosuch", "1", "--socket", s.SocketPath, "--json")
	if code != 1 || !strings.HasPrefix(out, `{"success":false,`) || !strings.Contains(out, `"error_code":`) {
		t.Errorf("unknown session --json = %d %q", code, out)
	}
	if code, _, errOut := run("zoom", "swarm", "1", "extra", "--socket", s.SocketPath); code != 1 ||
		errOut != "Error: accepts at most 2 arg(s), received 3\n" {
		t.Errorf("three args = %d %q", code, errOut)
	}
}

// Without a pane: --json needs it, a non-terminal refuses, and a
// terminal gets ntm's numbered list.
func TestZoomNTMWithoutAPane(t *testing.T) {
	s, calls := zoomWorld(t)
	code, out, _ := run("zoom", "swarm", "--socket", s.SocketPath, "--json")
	if code != 1 || out != `{"success":false,"error":"pane index is required for JSON output"}`+"\n" {
		t.Errorf("--json = %d %q", code, out)
	}
	code, out, errOut := run("zoom", "swarm", "--socket", s.SocketPath)
	if code != 1 || out != "" || errOut != "Error: non-interactive environment: pane index is required for zoom\n" {
		t.Errorf("non-interactive = %d %q %q", code, out, errOut)
	}
	if got := calls(); got != "" {
		t.Fatalf("calls = %s", got)
	}

	oldTTY, oldIn := zoomInteractive, zoomStdin
	t.Cleanup(func() { zoomInteractive, zoomStdin = oldTTY, oldIn })
	zoomInteractive = func(io.Writer) bool { return true }
	// The panes in ntm's order: the shell (ntm's user pane, index 0) first.
	zoomStdin = strings.NewReader("3\n")
	code, out, errOut = run("zoom", "swarm", "--socket", s.SocketPath)
	if code != 0 || errOut != "" || !strings.HasPrefix(out, "\n\x1b[1mSelect pane to zoom:\x1b[0m\n\n  \x1b[38;5;245m1\x1b[0m ") ||
		!strings.Contains(out, "\x1b[0m (zsh)\n  \x1b[38;5;245m2\x1b[0m ") ||
		!strings.Contains(out, "\x1b[0mIcyKnoll opus[frontend] (claude)\n  \x1b[38;5;245m3\x1b[0m ") ||
		!strings.Contains(out, "\x1b[0mBlueLake gpt (codex)\n\nEnter number (or q to cancel): ") ||
		!strings.HasSuffix(out, zoomed("pane 2 in 'swarm'")) {
		t.Errorf("picker = %d %q %q", code, out, errOut)
	}
	if got := calls(); !strings.HasSuffix(got, "pane.zoom w1:p2 on;workspace.focus w1;tab.focus w1:t1") {
		t.Errorf("picker calls = %s", got)
	}
	for _, in := range []string{"q\n", "\n", ""} {
		zoomStdin = strings.NewReader(in)
		if code, out, _ := run("zoom", "swarm", "--socket", s.SocketPath); code != 0 || strings.Contains(out, "Zoomed") {
			t.Errorf("cancel %q = %d %q", in, code, out)
		}
	}
	if got := calls(); strings.Contains(got, "pane.zoom") {
		t.Errorf("a cancelled pick zoomed: %s", got)
	}
	zoomStdin = strings.NewReader("9\n")
	if code, _, errOut := run("zoom", "swarm", "--socket", s.SocketPath); code != 1 || errOut != "Error: invalid selection\n" {
		t.Errorf("invalid = %d %q", code, errOut)
	}
}

// A failed zoom fails before the line; a failed attach after it. A tab
// that stays behind is a warning; a lone pane is a zoom with nothing to
// change, as with a lone tmux pane.
func TestZoomNTMHerdrOutcomes(t *testing.T) {
	s, calls := zoomWorld(t)
	fail := func(method string) {
		s.Handle(method, func(herdrtest.Request) (any, *herdrtest.Error) {
			return nil, &herdrtest.Error{Code: "pane_not_found", Message: method + " failed"}
		})
	}
	fail("pane.zoom")
	code, out, errOut := run("zoom", "swarm", "1", "--socket", s.SocketPath)
	if code != 1 || out != "" || !strings.HasPrefix(errOut, "Error: failed to zoom pane: ") || !strings.Contains(errOut, "pane.zoom failed") {
		t.Errorf("failed zoom = %d %q %q", code, out, errOut)
	}
	code, out, _ = run("zoom", "swarm", "1", "--socket", s.SocketPath, "--json")
	if code != 1 || !strings.HasPrefix(out, `{"success":false,"error":"failed to zoom pane: `) {
		t.Errorf("failed zoom --json = %d %q", code, out)
	}
	if got := calls(); strings.Contains(got, "focus") {
		t.Errorf("a failed zoom focused: %s", got)
	}

	s, calls = zoomWorld(t)
	fail("workspace.focus")
	code, out, errOut = run("zoom", "swarm", "1", "--socket", s.SocketPath)
	if code != 1 || out != zoomed("pane 1 in 'swarm'") || !strings.Contains(errOut, "workspace.focus failed") {
		t.Errorf("failed attach = %d %q %q", code, out, errOut)
	}
	calls()

	s, _ = zoomWorld(t)
	fail("tab.focus")
	code, out, errOut = run("zoom", "swarm", "1", "--socket", s.SocketPath)
	if code != 0 || out != zoomed("pane 1 in 'swarm'") ||
		!strings.HasPrefix(errOut, "warning: zoomed w1:p1, but herdr did not bring its tab in front: ") {
		t.Errorf("tab behind = %d %q %q", code, out, errOut)
	}

	s, calls = zoomWorld(t)
	s.Handle("pane.zoom", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "pane_zoom", "zoom": map[string]any{"pane_id": "w1:p1", "zoomed": false, "changed": false,
			"zoom_changed": false, "focus_changed": false, "reason": "single_pane",
			"layout": map[string]any{"workspace_id": "w1", "tab_id": "w1:t1", "zoomed": false}}}, nil
	})
	code, out, errOut = run("zoom", "swarm", "1", "--socket", s.SocketPath)
	if code != 0 || out != zoomed("pane 1 in 'swarm'") || errOut != "" {
		t.Errorf("lone pane = %d %q %q", code, out, errOut)
	}
	if got := calls(); got != "workspace.focus w1;tab.focus w1:t1" {
		t.Errorf("lone pane calls = %s", got)
	}
}
