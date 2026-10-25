package cli

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/store"
)

// dashboardWorld is an enrolled server with one workspace, swarm: a
// shell (w1:p3) and IcyKnoll's Claude (opus, slot 0.1, tagged frontend)
// and BlueLake's Codex (gpt, 0.2, focused) in tab w1:t1, with their sizes
// in herdr's layout and their foreground processes. The board is
// replaced: it records the workspace it was asked for. CI, TERM and
// NO_COLOR are cleared, so the TUI path is the default.
func dashboardWorld(t *testing.T) (*herdrtest.Server, *[]string) {
	t.Helper()
	t.Setenv("CI", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	t.Setenv("NHM_TUI_DEBUG", "")
	s, inst := cliInstance(t)
	pane := func(id, agent, label, title string) map[string]any {
		p := map[string]any{"pane_id": id, "tab_id": "w1:t1", "workspace_id": "w1", "terminal_id": "t-" + id,
			"label": label, "title": title, "agent_status": "idle", "focused": id == "w1:p2", "revision": 1}
		if agent != "" {
			p["agent"] = agent
		}
		return p
	}
	rect := func(id string, h int) map[string]any {
		return map[string]any{"pane_id": id, "focused": id == "w1:p2", "rect": map[string]any{"x": 0, "y": 0, "width": 80, "height": h}}
	}
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "tabs": []any{}, "agents": []any{},
			"panes": []any{pane("w1:p1", "claude", "IcyKnoll opus[frontend]", ""), pane("w1:p2", "codex", "BlueLake gpt", ""), pane("w1:p3", "", "", "zsh")},
			"layouts": []any{map[string]any{"workspace_id": "w1", "tab_id": "w1:t1", "zoomed": false, "focused_pane_id": "w1:p2",
				"area": map[string]any{"x": 0, "y": 0, "width": 80, "height": 48}, "splits": []any{},
				"panes": []any{rect("w1:p1", 24), rect("w1:p2", 12), rect("w1:p3", 12)}}},
			"workspaces": []any{map[string]any{"workspace_id": "w1", "number": 1, "label": "swarm", "focused": true,
				"pane_count": 3, "tab_count": 1, "active_tab_id": "w1:t1", "agent_status": "idle"}},
		}}, nil
	})
	s.Handle("pane.process_info", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		name := map[string]string{"w1:p1": "claude", "w1:p2": "codex", "w1:p3": "zsh"}[p.PaneID]
		return map[string]any{"type": "pane_process_info", "process_info": map[string]any{"pane_id": p.PaneID,
			"foreground_processes": []any{map[string]any{"pid": 7, "name": name}}}}, nil
	})
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, b := range []struct{ name, pane, kind, model string }{{"IcyKnoll", "w1:p1", "claude", "opus"}, {"BlueLake", "w1:p2", "codex", "gpt"}} {
		if _, err := bindings.NewStore(db).Create(context.Background(), bindings.New{MailName: b.name, MailProjectKey: "/p",
			ProjectID: "proj", Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1",
				PaneID: b.pane, HerdrAlias: strings.ToLower(b.name), Kind: b.kind, Model: b.model}}); err != nil {
			t.Fatal(err)
		}
	}
	var boards []string
	old := dashboardGoBoard
	dashboardGoBoard = func(ws string, _ io.Writer) int {
		boards = append(boards, ws)
		return exitOK
	}
	t.Cleanup(func() { dashboardGoBoard = old })
	return s, &boards
}

const dashboardPlain = "Session: swarm\nPanes: 3\n" +
	"Pane Types: Claude=1 Codex=1 Gemini=0 Grok=0 Cursor=0 Windsurf=0 Aider=0 Opencode=0 Omp=0 Ollama=0 User=1 Other=0\n" +
	"------------------------------------------------------------\n" +
	"[user] zsh - idle (80x12)\n" +
	"[claude] IcyKnoll opus[frontend] (opus) [frontend] - idle (80x24)\n" +
	"[codex] BlueLake gpt (gpt) - active (80x12)\n"

// --no-tui is ntm's plain form, and CI, TERM=dumb or NO_COLOR picks it
// by itself.
func TestDashboardNoTUIPrintsPlain(t *testing.T) {
	s, boards := dashboardWorld(t)
	if code, out, errOut := run("dashboard", "swarm", "--no-tui", "--socket", s.SocketPath); code != 0 || out != dashboardPlain {
		t.Fatalf("--no-tui = %d %q %q", code, out, errOut)
	}
	for _, env := range [][2]string{{"CI", "true"}, {"TERM", "dumb"}, {"NO_COLOR", "1"}} {
		t.Run(env[0], func(t *testing.T) {
			t.Setenv(env[0], env[1])
			if code, out, _ := run("dash", "swarm", "--socket", s.SocketPath); code != 0 || out != dashboardPlain {
				t.Errorf("%s=%s: %d %q", env[0], env[1], code, out)
			}
		})
	}
	if len(*boards) != 0 {
		t.Fatalf("the plain form opened the board: %v", *boards)
	}
}

// --json is ntm's bare DashboardOutput, and implies --no-tui.
func TestDashboardJSONIsDashboardOutput(t *testing.T) {
	s, boards := dashboardWorld(t)
	code, out, errOut := run("d", "swarm", "--json", "--debug", "--socket", s.SocketPath)
	var got dashboardOutput
	if code != 0 || errOut != "" || json.Unmarshal([]byte(out), &got) != nil {
		t.Fatalf("--json = %d %q %q", code, out, errOut)
	}
	want := `{
  "session": "swarm",
  "pane_count": 3,
  "agent_counts": {
    "claude": 1,
    "codex": 1,
    "user": 1
  },
  "panes": [
    {
      "id": "w1:p3",
      "index": 0,
      "type": "user",
      "command": "zsh",
      "width": 80,
      "height": 12,
      "active": false
    },
    {
      "id": "w1:p1",
      "index": 1,
      "type": "claude",
      "variant": "opus",
      "tags": [
        "frontend"
      ],
      "command": "claude",
      "width": 80,
      "height": 24,
      "active": false
    },
    {
      "id": "w1:p2",
      "index": 2,
      "type": "codex",
      "variant": "gpt",
      "command": "codex",
      "width": 80,
      "height": 12,
      "active": true
    }
  ]
}
`
	if out != want {
		t.Errorf("--json =\n%s\nwant\n%s", out, want)
	}
	if len(*boards) != 0 {
		t.Fatalf("--json opened the board: %v", *boards)
	}
}

// The TUI is nhm's board: a named session resolves as ntm's does, and the
// board takes its workspace; without one, the board's own. ntm's --popup
// changes nothing; --debug, NHM_TUI_DEBUG=1 and --attention-cursor wait
// for their beads. board is the same verb.
func TestDashboardRunsTheBoard(t *testing.T) {
	s, boards := dashboardWorld(t)
	for _, args := range [][]string{{"dashboard", "swarm"}, {"board", "sw"}, {"dashboard", "--popup", "swarm"}, {"board", "--go", "swarm"}, {"dash"}} {
		*boards = nil
		if code, out, errOut := run(append(args, "--socket", s.SocketPath)...); code != 0 || out != "" || errOut != "" {
			t.Errorf("%v = %d %q %q", args, code, out, errOut)
		}
		want := "w1"
		if len(args) == 1 {
			want = ""
		}
		if len(*boards) != 1 || (*boards)[0] != want {
			t.Errorf("%v: board for %v, want %q", args, *boards, want)
		}
	}
	*boards = nil
	for _, c := range []struct {
		args []string
		env  string
		want string
	}{
		{[]string{"--debug"}, "", "Error: --debug is not in this nhm yet (deferred: nhm-2p6.3.13)\n"},
		{nil, "1", "Error: --debug is not in this nhm yet (deferred: nhm-2p6.3.13)\n"},
		{[]string{"--attention-cursor", "5"}, "", "Error: --attention-cursor is not in this nhm yet (deferred: nhm-2p6.22)\n"},
		{[]string{"--inferred"}, "", "Error: unknown flag: --inferred\n"},
	} {
		t.Setenv("NHM_TUI_DEBUG", c.env)
		if code, _, errOut := run(append([]string{"dashboard", "swarm", "--socket", s.SocketPath}, c.args...)...); code != 1 || errOut != c.want {
			t.Errorf("%v (NHM_TUI_DEBUG=%q) = %d %q", c.args, c.env, code, errOut)
		}
	}
	if len(*boards) != 0 {
		t.Errorf("a refused dashboard opened the board: %v", *boards)
	}
	// A board that fails exits 1.
	dashboardGoBoard = func(string, io.Writer) int { return exitRefused }
	t.Setenv("NHM_TUI_DEBUG", "")
	if code, _, _ := run("dashboard", "swarm", "--socket", s.SocketPath); code != 1 {
		t.Errorf("failed board = %d", code)
	}
}
