package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/arosl/nhm/internal/core/uilaunch"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/runner"
)

// boardHerdr serves workspace w1 and the panes the plugin opens, each
// with this process as its shell.
func boardHerdr(t *testing.T, s *herdrtest.Server) {
	t.Helper()
	var mu sync.Mutex
	panes := []any{}
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		mu.Lock()
		defer mu.Unlock()
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "layouts": []any{}, "tabs": []any{}, "agents": []any{},
			"workspaces": []any{map[string]any{"workspace_id": "w1", "number": 1, "label": "swarm", "focused": true,
				"pane_count": 1, "tab_count": 1, "active_tab_id": "w1:t1", "agent_status": "idle"}},
			"panes": append([]any{}, panes...),
		}}, nil
	})
	s.Handle("plugin.pane.open", func(herdrtest.Request) (any, *herdrtest.Error) {
		p := map[string]any{"pane_id": "w1:p9", "tab_id": "w1:t9", "workspace_id": "w1", "terminal_id": "t-9",
			"agent_status": "unknown", "focused": true, "revision": 1}
		mu.Lock()
		panes = append(panes, p)
		mu.Unlock()
		return map[string]any{"type": "plugin_pane_opened", "plugin_pane": map[string]any{"plugin_id": "nhm", "entrypoint": "board", "pane": p}}, nil
	})
	s.Handle("pane.process_info", func(herdrtest.Request) (any, *herdrtest.Error) {
		pid := os.Getpid()
		return map[string]any{"type": "pane_process_info", "process_info": map[string]any{"pane_id": "w1:p9",
			"shell_pid": pid, "foreground_process_group_id": pid}}, nil
	})
	s.Handle("plugin.pane.focus", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "plugin_pane_focused"}, nil
	})
	s.Handle("tab.focus", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			TabID string `json:"tab_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		return map[string]any{"type": "tab_info", "tab": map[string]any{"tab_id": p.TabID, "workspace_id": "w1", "number": 9,
			"label": "9", "focused": true, "pane_count": 1, "agent_status": "unknown"}}, nil
	})
}

func requests(s *herdrtest.Server, method string) []herdrtest.Request {
	var out []herdrtest.Request
	for _, r := range s.Requests() {
		if r.Method == method {
			out = append(out, r)
		}
	}
	return out
}

// prefix+d opens one board tab with a token and the captured context;
// once the board registers, prefix+d focuses it and opens nothing.
func TestGoBoardOpensThenFocuses(t *testing.T) {
	s, inst := cliInstance(t)
	boardHerdr(t, s)
	t.Setenv("HERDR_SOCKET_PATH", s.SocketPath)
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", `{"workspace_id":"w1","workspace_label":"swarm","focused_pane_id":"w1:p1"}`)
	var stderr bytes.Buffer
	if code := openGoBoard(&stderr); code != exitOK {
		t.Fatalf("openGoBoard = %d: %s", code, stderr.String())
	}
	opens := requests(s, "plugin.pane.open")
	if len(opens) != 1 {
		t.Fatalf("%d opens", len(opens))
	}
	var p struct {
		Placement   string            `json:"placement"`
		WorkspaceID string            `json:"workspace_id"`
		Entrypoint  string            `json:"entrypoint"`
		Env         map[string]string `json:"env"`
	}
	if err := json.Unmarshal(opens[0].Params, &p); err != nil {
		t.Fatal(err)
	}
	var c ui.Context
	if err := json.Unmarshal([]byte(p.Env[ui.ContextEnv]), &c); err != nil {
		t.Fatal(err)
	}
	token := p.Env[uilaunch.TokenEnv]
	if p.Placement != "tab" || p.WorkspaceID != "w1" || p.Entrypoint != "board" || token == "" ||
		c.InstanceID != inst.ID || c.WorkspaceID != "w1" {
		t.Fatalf("open params = %+v, context %+v", p, c)
	}
	// prefix+d before the board registers: it opens no second tab, and its
	// line in herdr's plugin log says a board is starting.
	stderr.Reset()
	if code := openGoBoard(&stderr); code != exitOK || !strings.Contains(stderr.String(), "a board is starting in this workspace") {
		t.Fatalf("openGoBoard while starting = %d: %q", code, stderr.String())
	}
	if n := len(requests(s, "plugin.pane.open")); n != 1 {
		t.Fatalf("%d opens while the board starts", n)
	}
	// The board starts in the tab and registers.
	env := uilaunch.Env{InstanceDir: inst.Dir, InstanceID: inst.ID}
	if _, err := uilaunch.Register(context.Background(), env, herdr.New(s.SocketPath), "w1", token, uilaunch.Self{PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := openGoBoard(&stderr); code != exitOK {
		t.Fatalf("second openGoBoard = %d: %s", code, stderr.String())
	}
	if n := len(requests(s, "plugin.pane.open")); n != 1 {
		t.Fatalf("%d opens after the second prefix+d", n)
	}
	// Both focus the board's pane: the starting one, then the live one.
	focus := requests(s, "plugin.pane.focus")
	if len(focus) != 2 || !strings.Contains(string(focus[0].Params), `"w1:p9"`) || !strings.Contains(string(focus[1].Params), `"w1:p9"`) {
		t.Fatalf("focus = %+v", focus)
	}
	// Each of the three brings the board's tab in front of the attached
	// clients, which do not follow the plugin pane's focus.
	tabs := requests(s, "tab.focus")
	if len(tabs) != 3 {
		t.Fatalf("tab.focus = %+v", tabs)
	}
	for _, r := range tabs {
		if !strings.Contains(string(r.Params), `"w1:t9"`) {
			t.Fatalf("tab.focus = %+v", tabs)
		}
	}
}

// The real board's config hands its guard the runner's Ended for this
// instance: a request held for a job is released once the job has a
// result (QuietSpring's re-check of 1ce695a, LOW 3).
func TestGoBoardConfigReleasesEndedJobs(t *testing.T) {
	s, _ := cliInstance(t)
	boardHerdr(t, s)
	ie, err := openInstance(s.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ie.db.Close() }()
	cfg := goBoardConfig(context.Background(), ie, ui.Context{SocketPath: s.SocketPath, WorkspaceID: "w1"}, nil, nil, nil)
	if cfg.OperationEnded == nil || cfg.NewOperationID == nil {
		t.Fatal("the board's guard has no ids or no release")
	}
	id, err := cfg.NewOperationID()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OperationEnded(id) {
		t.Fatalf("%s ended before its job ran", id)
	}
	dir := runner.JobDir(ie.inst.Dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runner.ResultPath(dir, id), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !cfg.OperationEnded(id) {
		t.Fatalf("%s did not end with its result written", id)
	}
}

// The board's zoom brings the zoomed pane's tab in front; a pane that
// herdr no longer lists is an error.
func TestFrontTabFocusesThePanesTab(t *testing.T) {
	s, _ := cliInstance(t)
	boardHerdr(t, s)
	h := herdr.New(s.SocketPath)
	ctx := context.Background()
	if err := frontTab(ctx, h, "w1:p9"); err == nil || !strings.Contains(err.Error(), "w1:p9 is gone") {
		t.Fatalf("frontTab before the pane = %v", err)
	}
	if _, err := h.PluginPaneOpen(ctx, herdr.PluginPaneOpenParams{PluginID: "nhm", Entrypoint: "board", Placement: "tab"}); err != nil {
		t.Fatal(err)
	}
	if err := frontTab(ctx, h, "w1:p9"); err != nil {
		t.Fatal(err)
	}
	if tabs := requests(s, "tab.focus"); len(tabs) != 1 || !strings.Contains(string(tabs[0].Params), `"w1:t9"`) {
		t.Fatalf("tab.focus = %+v", tabs)
	}
}

// prefix+d follows [ui] board: "go" opens the Go board in a tab; the
// default opens today's board popup.
func TestUIBoardFollowsTheSwitch(t *testing.T) {
	s, _ := cliInstance(t)
	boardHerdr(t, s)
	t.Setenv("HERDR_SOCKET_PATH", s.SocketPath)
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", `{"workspace_id":"w1","workspace_label":"swarm","focused_pane_id":"w1:p1"}`)
	placement := func() string {
		opens := requests(s, "plugin.pane.open")
		var p struct {
			Placement string `json:"placement"`
		}
		if err := json.Unmarshal(opens[len(opens)-1].Params, &p); err != nil {
			t.Fatal(err)
		}
		return p.Placement
	}
	var stderr bytes.Buffer
	if code := runUI([]string{"board"}, &bytes.Buffer{}, &stderr); code != exitOK || placement() != "popup" {
		t.Fatalf("default: code %d, placement %q: %s", code, placement(), stderr.String())
	}
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "nhm")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[ui]\nboard = \"go\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runUI([]string{"board"}, &bytes.Buffer{}, &stderr); code != exitOK || placement() != "tab" {
		t.Fatalf("go: code %d, placement %q: %s", code, placement(), stderr.String())
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[ui]\nboard = \"gogo\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := runUI([]string{"board"}, &bytes.Buffer{}, &stderr); code != exitError || !strings.Contains(stderr.String(), "ui.board") {
		t.Fatalf("a bad value: code %d: %s", code, stderr.String())
	}
}

// The board refuses a context of another instance, and one without a
// workspace: it never falls back to the focused workspace.
func TestGoBoardValidatesItsContext(t *testing.T) {
	s, _ := cliInstance(t)
	for name, c := range map[string]ui.Context{
		"other instance": {Version: ui.ContextVersion, InstanceID: "inst-0000000000ff", SocketPath: s.SocketPath, WorkspaceID: "w1"},
		"no workspace":   {Version: ui.ContextVersion, SocketPath: s.SocketPath},
	} {
		data, _ := json.Marshal(c)
		t.Setenv(ui.ContextEnv, string(data))
		var stderr bytes.Buffer
		if code := runGoBoard(&stderr); code != exitRefused {
			t.Errorf("%s: runGoBoard = %d: %s", name, code, stderr.String())
		}
	}
	if n := len(requests(s, "session.snapshot")); n != 0 {
		t.Fatalf("a refused board asked herdr %d times", n)
	}
}
