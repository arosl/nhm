package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/arosl/nhm/internal/core/uilaunch"
	"github.com/arosl/nhm/internal/core/zoom"
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

// The board's zoom brings the tab that pane.zoom names in front. A tab
// that stays behind is a *zoom.Unshown, which the board reports as a
// zoom; a failed zoom is not (ScarletOrchid's review of 289e9a4).
func TestGoBoardZoomBringsItsTabInFront(t *testing.T) {
	s, _ := cliInstance(t)
	boardHerdr(t, s)
	ie, err := openInstance(s.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ie.db.Close() }()
	s.Handle("workspace.focus", func(herdrtest.Request) (any, *herdrtest.Error) { return map[string]any{"type": "ok"}, nil })
	s.Handle("pane.zoom", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "pane_zoom", "zoom": map[string]any{"pane_id": "w1:p9", "zoomed": true,
			"layout": map[string]any{"workspace_id": "w1", "tab_id": "w1:t9", "zoomed": true}}}, nil
	})
	cfg := goBoardConfig(context.Background(), ie, ui.Context{SocketPath: s.SocketPath, WorkspaceID: "w1"}, nil, nil, nil)
	ctx := context.Background()
	if err := cfg.Zoom(ctx, "w1", "w1:p9"); err != nil {
		t.Fatal(err)
	}
	if tabs := requests(s, "tab.focus"); len(tabs) != 1 || !strings.Contains(string(tabs[0].Params), `"w1:t9"`) {
		t.Fatalf("tab.focus = %+v", tabs)
	}
	s.Handle("tab.focus", func(herdrtest.Request) (any, *herdrtest.Error) {
		return nil, &herdrtest.Error{Code: "tab_not_found", Message: "no such tab"}
	})
	var unshown *zoom.Unshown
	if err := cfg.Zoom(ctx, "w1", "w1:p9"); !errors.As(err, &unshown) || unshown.PaneID != "w1:p9" ||
		!strings.Contains(unshown.Reason, "no such tab") {
		t.Fatalf("zoom with its tab behind = %v", err)
	}
	s.Handle("pane.zoom", func(herdrtest.Request) (any, *herdrtest.Error) {
		return nil, &herdrtest.Error{Code: "pane_not_found", Message: "no such pane"}
	})
	if err := cfg.Zoom(ctx, "w1", "w1:p9"); err == nil || errors.As(err, &unshown) || !strings.Contains(err.Error(), "no such pane") {
		t.Fatalf("a failed zoom = %v", err)
	}
	// A pane alone in its tab: its tab comes in front, and the board gets
	// a *zoom.Alone (nhm-tt9).
	boardHerdr(t, s)
	s.Handle("pane.zoom", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "pane_zoom", "zoom": map[string]any{"pane_id": "w1:p9", "zoomed": false, "reason": "single_pane",
			"layout": map[string]any{"workspace_id": "w1", "tab_id": "w1:t9", "zoomed": false}}}, nil
	})
	var alone *zoom.Alone
	if err := cfg.Zoom(ctx, "w1", "w1:p9"); !errors.As(err, &alone) || alone.PaneID != "w1:p9" {
		t.Fatalf("a pane alone in its tab = %v", err)
	}
}

// prefix+d opens the board in a tab. An old [ui] block in nhm's config
// (aro-acf has one) is ignored.
func TestUIBoardOpensTheGoBoard(t *testing.T) {
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
	if code := runUI([]string{"board"}, &bytes.Buffer{}, &stderr); code != exitOK || placement() != "tab" {
		t.Fatalf("default: code %d, placement %q: %s", code, placement(), stderr.String())
	}
	opens := len(requests(s, "plugin.pane.open"))
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "nhm")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[ui]\nboard = \"go\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The board of the first call is starting: the second finds it and
	// opens none.
	if code := runUI([]string{"board"}, &bytes.Buffer{}, &stderr); code != exitOK || len(requests(s, "plugin.pane.open")) != opens {
		t.Fatalf("[ui]: code %d, %d opens: %s", code, len(requests(s, "plugin.pane.open")), stderr.String())
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
		if code := runGoBoard("", &stderr); code != exitRefused {
			t.Errorf("%s: runGoBoard = %d: %s", name, code, stderr.String())
		}
	}
	if n := len(requests(s, "session.snapshot")); n != 0 {
		t.Fatalf("a refused board asked herdr %d times", n)
	}
}

// --go on board and menu is accepted for old callers and does nothing.
func TestBoardAndMenuAcceptGo(t *testing.T) {
	s, _ := zoomServer(t)
	t.Setenv("HERDR_SOCKET_PATH", s.SocketPath)
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", "")
	t.Setenv(ui.ContextEnv, "")
	// board is ntm's dashboard verb, so an unknown session is ntm's
	// resolver error, exit 1 (nhm-2p6.3.8).
	if code, _, errOut := run("board", "--go", "nosuch"); code != exitError || !strings.Contains(errOut, `Error: session "nosuch" not found`) {
		t.Errorf("board --go nosuch = %d %q", code, errOut)
	}
	if code, _, errOut := run("menu", "--go", "stray"); code != exitRefused || !strings.Contains(errOut, "Usage: nhm palette") {
		t.Errorf("menu --go stray = %d %q", code, errOut)
	}
}
