package ui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/herdr"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestCaptureFromAPluginAction(t *testing.T) {
	ctxJSON := `{"workspace_id":"w1","workspace_label":"swarm","workspace_cwd":"/p","tab_id":"w1:t2","focused_pane_id":"w1:p3",
		"focused_pane_cwd":"/p/sub","focused_pane_agent":"claude","invocation_source":"keybinding"}`
	c, err := Capture(env(map[string]string{"HERDR_SOCKET_PATH": "/run/h.sock", "HERDR_PLUGIN_CONTEXT_JSON": ctxJSON,
		"HERDR_PANE_ID": "w9:p9"}))
	if err != nil {
		t.Fatal(err)
	}
	// The plugin context wins over the action process's own pane.
	if c.PaneID != "w1:p3" || c.WorkspaceID != "w1" || c.Cwd != "/p/sub" || c.Agent != "claude" || c.Source != "keybinding" {
		t.Fatalf("context = %+v", c)
	}
	if c.Header() != "workspace swarm · pane w1:p3" {
		t.Fatalf("header = %q", c.Header())
	}
	if _, err := Capture(env(map[string]string{"HERDR_PLUGIN_CONTEXT_JSON": "{"})); err == nil {
		t.Fatal("broken context accepted")
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
	if err != nil || !ok || back != c {
		t.Fatalf("round trip = %+v %v %v", back, ok, err)
	}
	if err := Open(context.Background(), o, "spawn", c); err == nil {
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
	data, _ := json.Marshal(Context{SocketPath: "s", WorkspaceID: "w"})
	if string(data) != `{"socket_path":"s","workspace_id":"w"}` {
		t.Fatalf("json = %s", data)
	}
}
