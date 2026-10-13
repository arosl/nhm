package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

// fitWorld is a herdr server whose plugin panes play the probe: each pane
// that nhm's fit entrypoint opens writes the probe's events to the file
// its env names. Fields change how it misbehaves.
type fitWorld struct {
	mu          sync.Mutex
	s           *herdrtest.Server
	version     string
	protocol    int
	plugins     []map[string]any
	busy        bool // a popup is open already
	silent      bool // the probe never starts
	loseKeys    bool // send_keys reaches no probe
	guardBroken bool // the shell is not the foreground group
	stays       bool // a pane stays after its program ends
	probes      map[string]probeOut
	popup       *probeOut
	panes       []string
	next        int
}

type probeOut struct{ file, nonce string }

func (p probeOut) write(t *testing.T, events ...fitEvent) {
	t.Helper()
	f, err := os.OpenFile(p.file, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Errorf("probe file: %v", err)
		return
	}
	defer f.Close()
	for _, e := range events {
		e.Nonce = p.nonce
		data, _ := json.Marshal(e)
		_, _ = f.Write(append(data, '\n'))
	}
}

func nhmPlugin(panes ...string) map[string]any {
	specs := []any{}
	for _, id := range panes {
		specs = append(specs, map[string]any{"id": id, "title": id, "command": []string{"../nhm", "ui", id}, "placement": "popup"})
	}
	return map[string]any{"plugin_id": "nhm", "name": "nhm", "version": "0.1.7", "manifest_path": "/p/herdr-plugin.toml",
		"plugin_root": "/p", "enabled": true, "panes": specs}
}

func newFitWorld(t *testing.T) *fitWorld {
	t.Helper()
	w := &fitWorld{s: herdrtest.New(t), version: "0.9.3", protocol: 22, probes: map[string]probeOut{},
		plugins: []map[string]any{nhmPlugin("palette", "menu", "fit")}}
	pane := func(id string) map[string]any {
		return map[string]any{"pane_id": id, "tab_id": "w9:t1", "workspace_id": "w9", "terminal_id": "t-" + id,
			"agent_status": "unknown", "focused": false, "revision": 1}
	}
	w.s.Handle("ping", func(herdrtest.Request) (any, *herdrtest.Error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		return map[string]any{"type": "pong", "version": w.version, "protocol": w.protocol}, nil
	})
	w.s.Handle("plugin.list", func(herdrtest.Request) (any, *herdrtest.Error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		return map[string]any{"type": "plugin_list", "plugins": w.plugins}, nil
	})
	w.s.Handle("plugin.pane.open", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p herdr.PluginPaneOpenParams
		_ = json.Unmarshal(r.Params, &p)
		w.mu.Lock()
		defer w.mu.Unlock()
		if p.PluginID != "nhm" || p.Entrypoint != fitEntrypoint {
			return nil, &herdrtest.Error{Code: "plugin_pane_not_found", Message: p.Entrypoint}
		}
		out := probeOut{file: p.Env[fitOutEnv], nonce: p.Env[fitNonceEnv]}
		if !w.silent {
			out.write(t, fitEvent{Event: "start", PID: 1 << 22, Term: "xterm-256color", Profile: "truecolor"},
				fitEvent{Event: "size", Width: 72, Height: 25})
		}
		if p.Placement == "popup" {
			if w.busy || w.popup != nil {
				return nil, &herdrtest.Error{Code: herdr.CodeUIBusy, Message: "a popup is open"}
			}
			w.popup = &out
			return map[string]any{"type": "ok"}, nil
		}
		w.next++
		id := fmt.Sprintf("w9:p%d", w.next+1)
		w.probes[id], w.panes = out, append(w.panes, id)
		return map[string]any{"type": "plugin_pane_opened", "plugin_pane": map[string]any{"plugin_id": "nhm",
			"entrypoint": fitEntrypoint, "pane": pane(id)}}, nil
	})
	w.s.Handle("popup.close", func(herdrtest.Request) (any, *herdrtest.Error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.popup == nil {
			return nil, &herdrtest.Error{Code: herdr.CodePopupNotOpen, Message: "no popup"}
		}
		w.popup.write(t, fitEvent{Event: "exit", Reason: "hup"})
		w.popup = nil
		return map[string]any{"type": "ok"}, nil
	})
	w.s.Handle("pane.send_keys", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string   `json:"pane_id"`
			Keys   []string `json:"keys"`
		}
		_ = json.Unmarshal(r.Params, &p)
		w.mu.Lock()
		defer w.mu.Unlock()
		out, ok := w.probes[p.PaneID]
		if !ok {
			return nil, &herdrtest.Error{Code: herdr.CodePaneNotFound, Message: p.PaneID}
		}
		for _, k := range p.Keys {
			if !w.loseKeys {
				// bubbletea calls the space key " ".
				out.write(t, fitEvent{Event: "key", Key: strings.ReplaceAll(k, "space", " ")})
			}
			if k == "q" && !w.stays {
				w.panes = slices.DeleteFunc(w.panes, func(id string) bool { return id == p.PaneID })
			}
		}
		return map[string]any{"type": "ok"}, nil
	})
	w.s.Handle("pane.process_info", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		w.mu.Lock()
		defer w.mu.Unlock()
		fg := 77
		if w.guardBroken {
			fg = 78
		}
		return map[string]any{"type": "pane_process_info", "process_info": map[string]any{"pane_id": p.PaneID,
			"shell_pid": 77, "foreground_process_group_id": fg}}, nil
	})
	w.s.Handle("workspace.create", func(herdrtest.Request) (any, *herdrtest.Error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.panes = append(w.panes, "w9:p1")
		return map[string]any{"type": "workspace_created",
			"workspace": map[string]any{"workspace_id": "w9", "number": 9, "label": "nhm-fit", "focused": true, "pane_count": 1,
				"tab_count": 1, "active_tab_id": "w9:t1", "agent_status": "unknown"},
			"tab": map[string]any{"tab_id": "w9:t1", "workspace_id": "w9", "number": 1, "label": "1", "focused": true,
				"pane_count": 1, "agent_status": "unknown"},
			"root_pane": pane("w9:p1")}, nil
	})
	w.s.Handle("workspace.close", func(herdrtest.Request) (any, *herdrtest.Error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.panes = nil
		return map[string]any{"type": "ok"}, nil
	})
	w.s.Handle("plugin.pane.close", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		w.mu.Lock()
		defer w.mu.Unlock()
		w.panes = slices.DeleteFunc(w.panes, func(id string) bool { return id == p.PaneID })
		return map[string]any{"type": "plugin_pane_closed"}, nil
	})
	w.s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		panes := []any{}
		for _, id := range w.panes {
			panes = append(panes, pane(id))
		}
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{"protocol": 22, "version": "0.9.3",
			"workspaces": []any{}, "tabs": []any{}, "layouts": []any{}, "panes": panes, "agents": []any{}}}, nil
	})
	return w
}

func (w *fitWorld) run(t *testing.T, placements bool) map[string]doctorCheck {
	t.Helper()
	e := fitEnv{h: herdr.New(w.s.SocketPath), dir: t.TempDir(), wait: 300 * time.Millisecond, poll: 10 * time.Millisecond}
	got := map[string]doctorCheck{}
	for _, c := range e.checks(context.Background(), placements) {
		if _, dup := got[c.Name]; dup {
			t.Errorf("two checks named %s", c.Name)
		}
		got[c.Name] = c
	}
	return got
}

func (w *fitWorld) called(method string) int {
	n := 0
	for _, r := range w.s.Requests() {
		if r.Method == method {
			n++
		}
	}
	return n
}

func TestHerdrFitPasses(t *testing.T) {
	w := newFitWorld(t)
	got := w.run(t, true)
	for name, want := range map[string]string{
		"herdr protocol": checkOK, "herdr version": checkOK, "nhm plugin": checkOK, "popup": checkOK,
		"popup keys": checkSkip, "tab": checkOK, "kill guard": checkOK, "split": checkOK, "overlay": checkOK,
	} {
		if got[name].Result != want {
			t.Errorf("%s = %+v, want %s", name, got[name], want)
		}
	}
	if !strings.Contains(got["popup"].Detail, "72x25 (truecolor") || !strings.Contains(got["tab"].Detail, "keys a, esc, space arrived; its pane closed") {
		t.Errorf("details: popup %q, tab %q", got["popup"].Detail, got["tab"].Detail)
	}
	if w.called("popup.close") != 1 || w.called("workspace.close") != 1 || w.called("plugin.pane.close") != 0 {
		t.Errorf("popup.close %d, workspace.close %d, plugin.pane.close %d", w.called("popup.close"), w.called("workspace.close"), w.called("plugin.pane.close"))
	}
}

// Without --placements, the one effect on the server is the popup.
func TestHerdrFitWithoutPlacementsOpensOnlyThePopup(t *testing.T) {
	w := newFitWorld(t)
	got := w.run(t, false)
	if got["popup"].Result != checkOK || got["placements"].Result != checkSkip || got["tab"].Name != "" {
		t.Fatalf("checks = %+v", got)
	}
	for _, m := range []string{"workspace.create", "pane.send_keys", "plugin.pane.close", "workspace.close"} {
		if n := w.called(m); n != 0 {
			t.Errorf("%s called %d times", m, n)
		}
	}
	if w.called("plugin.pane.open") != 1 {
		t.Errorf("plugin.pane.open called %d times", w.called("plugin.pane.open"))
	}
}

func TestHerdrFitFindsMisfits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		set    func(*fitWorld)
		check  string
		result string
		detail string
	}{
		{"protocol", func(w *fitWorld) { w.protocol = 21 }, "herdr protocol", checkFail, "speaks protocol 21"},
		{"old herdr", func(w *fitWorld) { w.version = "0.9.2" }, "herdr version", checkFail, "older than"},
		{"new herdr", func(w *fitWorld) { w.version = "0.10.0" }, "herdr version", checkWarn, "newer than the release X0 checked"},
		{"no plugin", func(w *fitWorld) { w.plugins = nil }, "nhm plugin", checkFail, "no plugin nhm"},
		{"disabled", func(w *fitWorld) { w.plugins[0]["enabled"] = false }, "nhm plugin", checkFail, "disabled"},
		{"old plugin", func(w *fitWorld) { w.plugins = []map[string]any{nhmPlugin("menu")} }, "nhm plugin", checkFail, `no "fit" pane`},
		{"busy", func(w *fitWorld) { w.busy = true }, "popup", checkSkip, "a popup is open already"},
		{"silent", func(w *fitWorld) { w.silent = true }, "popup", checkFail, "did not start"},
		{"keys", func(w *fitWorld) { w.loseKeys = true }, "tab", checkFail, `the probe saw [], not ["a" "esc" " "]`},
		{"guard", func(w *fitWorld) { w.guardBroken = true }, "kill guard", checkFail, "is not its foreground group"},
		{"stays", func(w *fitWorld) { w.stays = true }, "split", checkWarn, "stayed after the program ended"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newFitWorld(t)
			tc.set(w)
			got := w.run(t, true)
			if c := got[tc.check]; c.Result != tc.result || !strings.Contains(c.Detail, tc.detail) {
				t.Errorf("%s = %+v; want %s with %q", tc.check, c, tc.result, tc.detail)
			}
			switch tc.name {
			case "protocol":
				if len(got) != 1 {
					t.Errorf("checks went on after a protocol misfit: %v", got)
				}
			case "no plugin", "disabled", "old plugin":
				if w.called("plugin.pane.open") != 0 {
					t.Error("a probe was opened without nhm's fit pane")
				}
			case "busy":
				// The user's popup stays open.
				if w.called("popup.close") != 0 {
					t.Error("popup.close closed a popup the check did not open")
				}
			case "silent":
				if w.called("popup.close") != 1 {
					t.Error("the check left its silent popup open")
				}
			case "stays":
				if w.called("plugin.pane.close") == 0 {
					t.Error("a pane that stayed was not closed")
				}
			}
		})
	}
}

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"0.9.3", "0.9.3", 0}, {"0.9.2", "0.9.3", -1}, {"0.10.0", "0.9.3", 1}, {"v1.0", "0.9.9", 1},
		{"0.9.3-rc1", "0.9.3", 0}, {"0.9", "0.9.0", 0},
	} {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestDoctorFitFlags(t *testing.T) {
	for _, args := range [][]string{{"--placements"}, {"--herdr-fit", "--no-mail"}} {
		if code, _, errOut := runVerb(runDoctor, args...); code != exitRefused || !strings.Contains(errOut, "--herdr-fit") {
			t.Errorf("doctor %v = %d %s", args, code, errOut)
		}
	}
	w := newFitWorld(t)
	code, out, errOut := runVerb(runDoctor, "--herdr-fit", "--socket", w.s.SocketPath, "--json")
	env := decodeEnvelope(t, out)
	if code != exitOK || env["result"].(map[string]any)["failed"].(float64) != 0 {
		t.Fatalf("doctor --herdr-fit = %d %s %s", code, out, errOut)
	}
}
