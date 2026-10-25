package cli

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

// switchServer is paneServer with pane.focus recorded; fail makes it fail.
func switchServer(t *testing.T, fail bool) (*herdrtest.Server, func() []string) {
	t.Helper()
	s, _, _ := paneServer(t, copyText)
	var mu sync.Mutex
	var focused []string
	s.Handle("pane.focus", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		mu.Lock()
		focused = append(focused, p.PaneID)
		mu.Unlock()
		if fail {
			return nil, &herdrtest.Error{Code: "pane_not_found", Message: "no pane"}
		}
		return map[string]any{"type": "pane_info", "pane": map[string]any{"pane_id": p.PaneID, "tab_id": "w1:t1",
			"workspace_id": "w1", "terminal_id": "t", "agent_status": "idle", "focused": true, "revision": 2}}, nil
	})
	return s, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := focused
		focused = nil
		return out
	}
}

// switch focuses the pane of the agent named, through herdr's
// pane.focus, and prints ntm's line; --session keeps one workspace, by
// label or id.
func TestSwitchFocusesTheAgentsPane(t *testing.T) {
	s, focused := switchServer(t, false)
	code, out, errOut := run("switch", "BlueLake", "--socket", s.SocketPath)
	if code != 0 || errOut != "" || out != "Switched to pane w1:p2 (BlueLake, pane 2) in session swarm\n" {
		t.Fatalf("switch = %d %q %q", code, out, errOut)
	}
	if got := focused(); len(got) != 1 || got[0] != "w1:p2" {
		t.Errorf("focused = %v", got)
	}
	for _, sess := range []string{"swarm", "w1"} {
		if code, out, _ := run("switch", "IcyKnoll", "--session", sess, "--socket", s.SocketPath); code != 0 ||
			out != "Switched to pane w1:p1 (IcyKnoll, pane 1) in session swarm\n" {
			t.Errorf("--session %s = %d %q", sess, code, out)
		}
	}
	if got := focused(); len(got) != 2 {
		t.Errorf("focused = %v", got)
	}
}

// A name no session holds, a name in another case, or a session that
// does not hold it: ntm's error, exit 1, and no focus.
func TestSwitchNotFound(t *testing.T) {
	s, focused := switchServer(t, false)
	for _, args := range [][]string{{"Nobody"}, {"bluelake"}, {"BlueLake", "--session", "other"}} {
		code, out, errOut := run(append(append([]string{"switch"}, args...), "--socket", s.SocketPath)...)
		want := "Error: no agent named \"" + args[0] + "\" found in any session\n"
		if code != 1 || out != "" || errOut != want {
			t.Errorf("switch %v = %d %q %q", args, code, out, errOut)
		}
	}
	if got := focused(); len(got) != 0 {
		t.Errorf("focused = %v", got)
	}
	code, _, errOut := run("switch", "  ", "--socket", s.SocketPath)
	if code != 1 || errOut != "Error: agent name cannot be empty\n" {
		t.Errorf("switch '  ' = %d %q", code, errOut)
	}
	code, _, errOut = run("switch", "--socket", s.SocketPath)
	if code != 1 || errOut != "Error: accepts 1 arg(s), received 0\n" {
		t.Errorf("switch = %d %q", code, errOut)
	}
	s2, _ := switchServer(t, true)
	code, _, errOut = run("switch", "BlueLake", "--socket", s2.SocketPath)
	if code != 1 || errOut != "Error: herdr pane.focus w1:p2: herdr pane.focus: pane_not_found: no pane\n" {
		t.Errorf("a failed focus = %d %q", code, errOut)
	}
}

// --json prints ntm's object, with action "selected".
func TestSwitchJSON(t *testing.T) {
	s, _ := switchServer(t, false)
	code, out, _ := run("switch", "IcyKnoll", "--json", "--socket", s.SocketPath)
	var got map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil {
		t.Fatalf("switch --json = %d %q", code, out)
	}
	if b, _ := json.Marshal(got); string(b) != `{"action":"selected","agent_name":"IcyKnoll","pane_id":"w1:p1","pane_index":1,"pane_title":"IcyKnoll opus","session":"swarm"}` {
		t.Errorf("JSON = %s", b)
	}
}
