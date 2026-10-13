package ui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/herdr"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func pluginContext(t *testing.T, workspaceCwd, paneCwd string) string {
	t.Helper()
	data, err := json.Marshal(map[string]string{"workspace_id": "w1", "workspace_label": "swarm", "workspace_cwd": workspaceCwd,
		"tab_id": "w1:t2", "focused_pane_id": "w1:p3", "focused_pane_cwd": paneCwd, "focused_pane_agent": "claude",
		"invocation_source": "keybinding"})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCaptureFromAPluginAction(t *testing.T) {
	ws := t.TempDir()
	sub := filepath.Join(ws, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := Capture(env(map[string]string{"HERDR_SOCKET_PATH": "/run/h.sock", "HERDR_PLUGIN_CONTEXT_JSON": pluginContext(t, ws, sub),
		"HERDR_PANE_ID": "w9:p9"}))
	if err != nil {
		t.Fatal(err)
	}
	// The plugin context wins over the action process's own pane.
	if c.Version != ContextVersion || c.PaneID != "w1:p3" || c.WorkspaceID != "w1" || c.Cwd != sub || c.Agent != "claude" || c.Source != "keybinding" {
		t.Fatalf("context = %+v", c)
	}
	if c.Header() != "workspace swarm · pane w1:p3" {
		t.Fatalf("header = %q", c.Header())
	}
	if _, pick, err := c.Selection(); !pick || err != nil {
		t.Fatalf("a captured context has a selection: %v %v", pick, err)
	}
	if _, err := Capture(env(map[string]string{"HERDR_PLUGIN_CONTEXT_JSON": "{"})); err == nil {
		t.Fatal("broken context accepted")
	}
}

// TestCaptureKeepsOnlyARealDirectory: a pane directory that is gone falls
// back to the workspace's; with neither, the context has none, never the
// plugin's directory (where herdr runs the action).
func TestCaptureKeepsOnlyARealDirectory(t *testing.T) {
	ws := t.TempDir()
	for _, tc := range []struct{ workspace, pane, want string }{
		{ws, filepath.Join(ws, "gone"), ws},
		{"relative/dir", "", ""},
		{filepath.Join(ws, "gone"), "", ""},
	} {
		c, err := Capture(env(map[string]string{"HERDR_SOCKET_PATH": "/run/h.sock", "HERDR_PLUGIN_CONTEXT_JSON": pluginContext(t, tc.workspace, tc.pane)}))
		if err != nil || c.Cwd != tc.want {
			t.Errorf("workspace %q, pane %q: cwd %q, want %q (%v)", tc.workspace, tc.pane, c.Cwd, tc.want, err)
		}
	}
}

func TestCaptureFromAPane(t *testing.T) {
	c, err := Capture(env(map[string]string{"HERDR_SOCKET_PATH": "/run/h.sock", "HERDR_WORKSPACE_ID": "w2", "HERDR_PANE_ID": "w2:p1"}))
	if err != nil || c.WorkspaceID != "w2" || c.PaneID != "w2:p1" || c.Source != "pane" || c.Cwd == "" {
		t.Fatalf("context = %+v %v", c, err)
	}
	if _, err := Capture(env(nil)); !errors.Is(err, ErrNoContext) {
		t.Fatalf("no socket: %v", err)
	}
}

// TestSelection: no selection opens the picker; an explicit one is frozen
// with its generations; an explicitly empty one is refused. Neither is
// ever "all agents".
func TestSelection(t *testing.T) {
	base := Context{Version: ContextVersion, SocketPath: "/run/h.sock", WorkspaceID: "w1"}
	ts := []Target{{BindingID: "b1", Generation: 3, PaneID: "w1:p1", Kind: "claude"}, {PaneID: "w1:p4", Session: "s-9", Kind: "codex"}}
	picked := base.WithTargets("board", ts)
	ts[0].Generation = 4 // the caller's slice changes later; the context keeps what it froze
	got, pick, err := picked.Selection()
	if err != nil || pick || len(got) != 2 || got[0].Generation != 3 || picked.Origin != "board" {
		t.Fatalf("explicit selection = %+v %v %v", got, pick, err)
	}
	if e := Expect(got); len(e) != 1 || e["b1"] != 3 {
		t.Fatalf("expect = %v", e)
	}
	if picked.Header() != "workspace w1 · 2 picked" {
		t.Fatalf("header = %q", picked.Header())
	}
	if _, pick, err := base.WithTargets("board", nil).Selection(); pick || !errors.Is(err, ErrEmptySelection) {
		t.Fatalf("empty selection: pick %v, err %v", pick, err)
	}
	// The difference survives the trip through NHM_CONTEXT.
	for _, c := range []Context{base, base.WithTargets("menu", nil), picked} {
		data, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		back, ok, err := FromPopup(env(map[string]string{ContextEnv: string(data)}))
		if err != nil || !ok || !reflect.DeepEqual(back, c) {
			t.Fatalf("round trip of %+v = %+v %v", c, back, err)
		}
		_, pick1, err1 := c.Selection()
		_, pick2, err2 := back.Selection()
		if pick1 != pick2 || !errors.Is(err2, err1) {
			t.Fatalf("selection changed on the trip: %v %v / %v %v", pick1, err1, pick2, err2)
		}
	}
}

// TestAgentTarget: a bound agent is its binding at the snapshot's
// generation; an unbound one is its pane and agent session.
func TestAgentTarget(t *testing.T) {
	sess := &herdr.AgentSession{Kind: "id", Value: "s-1"}
	b := AgentTarget(snapshot.Agent{PaneID: "w1:p1", Kind: "claude", Session: sess,
		Binding: &snapshot.BindingRef{ID: "bnd-1", Generation: 2}})
	u := AgentTarget(snapshot.Agent{PaneID: "w1:p2", Kind: "codex", Session: sess})
	if !b.Bound() || b.Key() != "binding:bnd-1@2" || b.PaneID != "w1:p1" {
		t.Fatalf("bound = %+v %s", b, b.Key())
	}
	if u.Bound() || u.Key() != "pane:w1:p2#id:s-1" || u.Kind != "codex" {
		t.Fatalf("unbound = %+v %s", u, u.Key())
	}
}

// TestFromPopupRefuses: a context from a newer nhm, or with a selection
// this nhm cannot act on safely, is an error, not a guess.
func TestFromPopupRefuses(t *testing.T) {
	for name, raw := range map[string]string{
		"newer":              `{"version":2,"socket_path":"/s"}`,
		"negative version":   `{"version":-1,"socket_path":"/s"}`,
		"relative dir":       `{"version":1,"socket_path":"/s","cwd":"proj"}`,
		"targets, no mode":   `{"version":1,"socket_path":"/s","targets":[{"binding_id":"b1","generation":1}]}`,
		"unknown mode":       `{"version":1,"socket_path":"/s","target_mode":"all"}`,
		"no generation":      `{"version":1,"socket_path":"/s","target_mode":"explicit","targets":[{"binding_id":"b1"}]}`,
		"neither":            `{"version":1,"socket_path":"/s","target_mode":"explicit","targets":[{"kind":"claude"}]}`,
		"twice":              `{"version":1,"socket_path":"/s","target_mode":"explicit","targets":[{"binding_id":"b1","generation":2},{"binding_id":"b1","generation":2}]}`,
		"twice, unbound one": `{"version":1,"socket_path":"/s","target_mode":"explicit","targets":[{"pane_id":"w1:p2"},{"pane_id":"w1:p2"}]}`,
	} {
		if _, _, err := FromPopup(env(map[string]string{ContextEnv: raw})); err == nil {
			t.Errorf("%s: accepted %s", name, raw)
		}
	}
	// nhm 0.1.5 wrote no version: read as version 0, with no selection.
	old, ok, err := FromPopup(env(map[string]string{ContextEnv: `{"socket_path":"/s","workspace_id":"w1","cwd":"/p"}`}))
	if err != nil || !ok || old.Version != 0 || old.WorkspaceID != "w1" {
		t.Fatalf("0.1.5 context = %+v %v %v", old, ok, err)
	}
	if _, pick, err := old.Selection(); !pick || err != nil {
		t.Fatalf("0.1.5 context selection: %v %v", pick, err)
	}
	// A context from another instance of the same socket is refused.
	c := Context{Version: ContextVersion, SocketPath: "/s", InstanceID: "i-1"}
	if c.CheckInstance("i-1") != nil || c.CheckInstance("i-2") == nil || c.CheckInstance("") == nil || old.CheckInstance("i-2") != nil {
		t.Fatal("CheckInstance")
	}
	// The same agent at another generation is another target.
	two := `{"version":1,"socket_path":"/s","target_mode":"explicit","targets":[{"binding_id":"b1","generation":2},{"binding_id":"b1","generation":3}]}`
	if _, _, err := FromPopup(env(map[string]string{ContextEnv: two})); err != nil {
		t.Fatalf("two generations: %v", err)
	}
}

type opener struct {
	got  herdr.PluginPaneOpenParams
	fail error
}

func (o *opener) PluginPaneOpen(_ context.Context, p herdr.PluginPaneOpenParams) (*herdr.PluginPane, error) {
	o.got = p
	return nil, o.fail
}

func TestOpenHandsTheContextOver(t *testing.T) {
	o := &opener{}
	c := Context{SocketPath: "/run/h.sock", WorkspaceID: "w1", PaneID: "w1:p3"}
	if err := Open(context.Background(), o, "board", c); err != nil {
		t.Fatal(err)
	}
	if o.got.PluginID != "nhm" || o.got.Entrypoint != "board" || o.got.Placement != "popup" || o.got.TargetPaneID != "" {
		t.Fatalf("params = %+v", o.got)
	}
	back, ok, err := FromPopup(env(map[string]string{ContextEnv: o.got.Env[ContextEnv]}))
	if err != nil || !ok || !reflect.DeepEqual(back, c) {
		t.Fatalf("round trip = %+v %v %v", back, ok, err)
	}
	if err := Open(context.Background(), o, "memory", c); err == nil {
		t.Fatal("an undeclared popup opened")
	}
	o.fail = &herdr.Error{Code: herdr.CodeUIBusy, Message: "settings open"}
	if err := Open(context.Background(), o, "menu", c); !errors.Is(err, ErrBusy) {
		t.Fatalf("busy: %v", err)
	}
	if _, ok, _ := FromPopup(env(nil)); ok {
		t.Fatal("a hand-run popup claims a context")
	}
}

func TestBindWritesTheBlockOnce(t *testing.T) {
	config := "[ui]\nsidebar_width = 30\n\n# >>> acfs palette >>>\n[[keys.command]]\nkey = \"prefix+a\"\ntype = \"popup\"\ncommand = \"acfs palette\"\n# <<< acfs palette <<<\n\n[keys]\nprefix = \"ctrl+b\"\n"
	out, err := Bind(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "acfs palette") || !strings.Contains(out, "sidebar_width = 30") || !strings.Contains(out, `prefix = "ctrl+b"`) {
		t.Fatalf("bind = %s", out)
	}
	for _, k := range Keys {
		if strings.Count(out, `key = "`+k.Key+`"`) != 1 || !strings.Contains(out, `command = "`+k.Action+`"`) {
			t.Fatalf("%s missing or doubled:\n%s", k.Key, out)
		}
	}
	again, err := Bind(out)
	if err != nil || again != out {
		t.Fatalf("bind twice changed the file:\n%s\n---\n%s %v", out, again, err)
	}
	back, err := Unbind(out)
	if err != nil || strings.Contains(back, BlockBegin) || strings.Contains(back, "prefix+a") {
		t.Fatalf("unbind = %s %v", back, err)
	}
	if empty, err := Bind(""); err != nil || !strings.HasPrefix(empty, BlockBegin) {
		t.Fatalf("empty config = %q %v", empty, err)
	}
}

func TestBindRefusesATakenKey(t *testing.T) {
	if _, err := Bind("[keys]\nsettings = \"prefix+d\"\n"); err == nil || !strings.Contains(err.Error(), "prefix+d") {
		t.Fatalf("taken key: %v", err)
	}
	if _, err := Bind("# >>> nhm >>>\nkey = \"prefix+a\"\n"); err == nil {
		t.Fatal("an unterminated block was accepted")
	}
}

func TestConfigPath(t *testing.T) {
	if p, _ := ConfigPath(env(map[string]string{"HERDR_CONFIG_PATH": "/x/c.toml"})); p != "/x/c.toml" {
		t.Fatalf("explicit = %s", p)
	}
	if p, _ := ConfigPath(env(map[string]string{"XDG_CONFIG_HOME": "/x"})); p != "/x/herdr/config.toml" {
		t.Fatalf("xdg = %s", p)
	}
}

func TestContextJSONKeys(t *testing.T) {
	data, _ := json.Marshal(Context{Version: ContextVersion, SocketPath: "s", WorkspaceID: "w"})
	if string(data) != `{"version":1,"socket_path":"s","workspace_id":"w"}` {
		t.Fatalf("json = %s", data)
	}
	data, _ = json.Marshal(Context{Version: ContextVersion, SocketPath: "s"}.WithTargets("board", []Target{{BindingID: "b", Generation: 2}}))
	if string(data) != `{"version":1,"socket_path":"s","origin":"board","target_mode":"explicit","targets":[{"binding_id":"b","generation":2}]}` {
		t.Fatalf("json = %s", data)
	}
}

// herdr's server PATH lacks ~/.local/bin: a plugin process puts the
// user's tool directories that exist in front, once (nhm-sbq).
func TestPluginPath(t *testing.T) {
	exists := map[string]bool{"/h/.local/bin": true, "/h/.cargo/bin": true}
	isDir := func(d string) bool { return exists[d] }
	server := "/h/.cargo/bin:/usr/bin:/bin"
	cases := []struct {
		name    string
		vars    map[string]string
		want    string
		changed bool
	}{
		{"not a plugin", map[string]string{"HOME": "/h", "PATH": server}, server, false},
		{"plugin action", map[string]string{"HOME": "/h", "PATH": server, "HERDR_PLUGIN_CONTEXT_JSON": "{}"}, "/h/.local/bin:" + server, true},
		{"event hook", map[string]string{"HOME": "/h", "PATH": server, "HERDR_PLUGIN_EVENT_JSON": "{}"}, "/h/.local/bin:" + server, true},
		{"popup child", map[string]string{"HOME": "/h", "PATH": server, ContextEnv: "{}"}, "/h/.local/bin:" + server, true},
		{"already there", map[string]string{"HOME": "/h", "PATH": "/h/.local/bin/:" + server, ContextEnv: "{}"}, "/h/.local/bin/:" + server, false},
		{"no HOME", map[string]string{"PATH": server, ContextEnv: "{}"}, server, false},
		{"empty PATH", map[string]string{"HOME": "/h", ContextEnv: "{}"}, "/h/.local/bin:/h/.cargo/bin", true},
	}
	for _, tc := range cases {
		got, changed := PluginPath(env(tc.vars), isDir)
		if got != tc.want || changed != tc.changed {
			t.Errorf("%s: PluginPath = %q, %v; want %q, %v", tc.name, got, changed, tc.want, tc.changed)
		}
	}
}
