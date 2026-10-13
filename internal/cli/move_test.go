package cli

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/store"
)

// moveWorld is a herdr session for nhm move. Workspaces: w1 "swarm", w2
// "other", w3 and w4 both "dup". Agents: IcyKnoll (claude, native
// session sess-a) in w1:p1; BlueLake (codex, no session) in w1:p2;
// RedFox's pane w1:p3 holds a codex where its binding has a claude.
// pane.move puts a pane into the new tab w2:t9 as w2:p5, with the native
// session moveSession (the same one when "").
type moveWorld struct {
	mu          sync.Mutex
	agents      map[string]map[string]any
	tabs        map[string]map[string]any
	moveSession string
	moveFails   bool
	renamed     []string
}

func agentJSON(pane, tab, ws, kind, name, session string) map[string]any {
	a := map[string]any{"pane_id": pane, "tab_id": tab, "workspace_id": ws, "terminal_id": "t-" + pane,
		"agent_status": "idle", "focused": false, "revision": 1, "agent": kind, "name": name}
	if session != "" {
		a["agent_session"] = map[string]any{"source": "hook", "agent": kind, "kind": "session_id", "value": session}
	}
	return a
}

func moveInstance(t *testing.T) (*herdrtest.Server, store.Instance, *moveWorld, string) {
	t.Helper()
	s, inst := cliInstance(t)
	w := &moveWorld{
		agents: map[string]map[string]any{
			"w1:p1": agentJSON("w1:p1", "w1:t1", "w1", "claude", "icyknoll", "sess-a"),
			"w1:p2": agentJSON("w1:p2", "w1:t2", "w1", "codex", "bluelake", ""),
			"w1:p3": agentJSON("w1:p3", "w1:t3", "w1", "codex", "redfox", ""),
		},
		tabs: map[string]map[string]any{},
	}
	for _, id := range []string{"w1:t1", "w1:t2", "w1:t3"} {
		w.tabs[id] = map[string]any{"tab_id": id, "workspace_id": "w1", "number": 1, "label": "1", "focused": false, "pane_count": 1, "agent_status": "idle"}
	}
	ws := func(id, label string) map[string]any {
		return map[string]any{"workspace_id": id, "number": 1, "label": label, "focused": false, "pane_count": 1, "tab_count": 1,
			"active_tab_id": id + ":t1", "agent_status": "idle"}
	}
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		var panes, agents, tabs []any
		for _, id := range slices.Sorted(maps.Keys(w.agents)) {
			panes, agents = append(panes, w.agents[id]), append(agents, w.agents[id])
		}
		for _, id := range slices.Sorted(maps.Keys(w.tabs)) {
			tabs = append(tabs, w.tabs[id])
		}
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "layouts": []any{}, "tabs": tabs, "panes": panes, "agents": agents,
			"workspaces": []any{ws("w1", "swarm"), ws("w2", "other"), ws("w3", "dup"), ws("w4", "dup")},
		}}, nil
	})
	s.Handle("agent.get", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal(r.Params, &p)
		w.mu.Lock()
		defer w.mu.Unlock()
		a, ok := w.agents[p.Target]
		if !ok {
			return nil, &herdrtest.Error{Code: "agent_not_found", Message: "agent not found: " + p.Target}
		}
		return map[string]any{"type": "agent_info", "agent": a}, nil
	})
	s.Handle("pane.process_info", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		return map[string]any{"type": "pane_process_info", "process_info": map[string]any{"pane_id": p.PaneID}}, nil
	})
	s.Handle("pane.move", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.moveFails {
			return nil, &herdrtest.Error{Code: "pane_not_found", Message: "pane not found"}
		}
		old := w.agents[p.PaneID]
		delete(w.agents, p.PaneID)
		session := w.moveSession
		if session == "" {
			session = old["agent_session"].(map[string]any)["value"].(string)
		}
		moved := agentJSON("w2:p5", "w2:t9", "w2", old["agent"].(string), old["name"].(string), session)
		w.agents["w2:p5"] = moved
		w.tabs["w2:t9"] = map[string]any{"tab_id": "w2:t9", "workspace_id": "w2", "number": 9, "label": "9", "focused": false, "pane_count": 1, "agent_status": "idle"}
		return map[string]any{"type": "pane_move", "move_result": map[string]any{
			"changed": true, "pane": moved, "previous_pane_id": p.PaneID, "previous_tab_id": old["tab_id"],
			"previous_workspace_id": "w1", "focused_pane_id": "w1:p1", "target_layout": map[string]any{},
			"closed_tab_id": old["tab_id"], "closed_workspace_id": nil,
		}}, nil
	})
	s.Handle("agent.rename", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Target string `json:"target"`
			Name   string `json:"name"`
		}
		_ = json.Unmarshal(r.Params, &p)
		w.mu.Lock()
		defer w.mu.Unlock()
		w.agents[p.Target]["name"] = p.Name
		return map[string]any{"type": "agent_info", "agent": w.agents[p.Target]}, nil
	})
	s.Handle("tab.get", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			TabID string `json:"tab_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		w.mu.Lock()
		defer w.mu.Unlock()
		return map[string]any{"type": "tab_info", "tab": w.tabs[p.TabID]}, nil
	})
	s.Handle("tab.rename", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			TabID string `json:"tab_id"`
			Label string `json:"label"`
		}
		_ = json.Unmarshal(r.Params, &p)
		w.mu.Lock()
		defer w.mu.Unlock()
		w.tabs[p.TabID]["label"] = p.Label
		w.renamed = append(w.renamed, p.TabID+" "+p.Label)
		return map[string]any{"type": "tab_info", "tab": w.tabs[p.TabID]}, nil
	})

	project := t.TempDir()
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bs := bindings.NewStore(db)
	for _, b := range []struct {
		name, pane, tab, kind, alias, session string
	}{
		{"IcyKnoll", "w1:p1", "w1:t1", "claude", "icyknoll", "sess-a"},
		{"BlueLake", "w1:p2", "w1:t2", "codex", "bluelake", ""},
		{"RedFox", "w1:p3", "w1:t3", "claude", "redfox", "sess-r"},
	} {
		occ := bindings.Occupant{WorkspaceID: "w1", TabID: b.tab, PaneID: b.pane, HerdrAlias: b.alias, Kind: b.kind}
		if b.session != "" {
			occ.NativeSession = &bindings.NativeSession{Value: b.session}
		}
		if _, err := bs.Create(context.Background(), bindings.New{MailName: b.name, MailProjectKey: project, ProjectID: "proj-x",
			Provenance: bindings.Created, Occupant: occ}); err != nil {
			t.Fatal(err)
		}
	}
	return s, inst, w, project
}

func binding(t *testing.T, inst store.Instance, name string) bindings.Binding {
	t.Helper()
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r, err := bindings.Resolve(context.Background(), bindings.NewStore(db), nil, bindings.Query{Target: name}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	return r.Binding
}

func moves(s *herdrtest.Server) []string {
	var out []string
	for _, r := range s.Requests() {
		if r.Method == "pane.move" {
			out = append(out, string(r.Params))
		}
	}
	return out
}

func TestMoveVerb(t *testing.T) {
	s, inst, w, project := moveInstance(t)
	before := binding(t, inst, "IcyKnoll")
	// ACFS's Bash owns the server: nhm moves nothing.
	if code, _, errOut := run("move", "IcyKnoll", "--workspace", "other", "--socket", s.SocketPath); code != exitRefused || !strings.Contains(errOut, "owner is acfs-bash") {
		t.Fatalf("move under the Bash = %d %s", code, errOut)
	}
	if _, err := core.SwitchOwner(context.Background(), inst.Dir, core.OwnerNhm, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := run("move", "IcyKnoll", "--workspace", "other", "--dry-run", "--socket", s.SocketPath)
	if code != exitOK || out != "would move IcyKnoll (claude) from w1:p1 to a new tab of workspace w2\n" || len(moves(s)) != 0 {
		t.Fatalf("dry run = %d %q %s; moves %v", code, out, errOut, moves(s))
	}

	code, out, errOut = run("move", "IcyKnoll", "--workspace", "other", "--socket", s.SocketPath, "--json")
	if code != exitOK {
		t.Fatalf("move = %d %s %s", code, out, errOut)
	}
	if got := moves(s); len(got) != 1 || got[0] != `{"pane_id":"w1:p1","destination":{"type":"new_tab","workspace_id":"w2"}}` {
		t.Fatalf("pane.move params = %v", got)
	}
	res := decodeEnvelope(t, out)["result"].(map[string]any)
	after := binding(t, inst, "IcyKnoll")
	if after.PaneID != "w2:p5" || after.WorkspaceID != "w2" || after.Generation != before.Generation || after.ID != before.ID ||
		after.Slot == "" || res["to_pane_id"] != "w2:p5" || res["slot"] != after.Slot {
		t.Fatalf("after the move: binding %+v, result %v", after, res)
	}
	if acts, _ := res["actions"].([]any); !slices.Contains(acts, any("herdr closed tab w1:t1: its last pane moved")) ||
		!slices.Contains(acts, any("IcyKnoll moved from w1:p1 to w2:p5, slot "+after.Slot)) {
		t.Errorf("actions = %v", res["actions"])
	}
	w.mu.Lock()
	renamed := slices.Clone(w.renamed)
	w.mu.Unlock()
	if !slices.Equal(renamed, []string{"w2:t9 IcyKnoll"}) {
		t.Errorf("tab labels = %v", renamed)
	}
	if rec, ok, err := agentmail.ReadIdentity(os.Getenv("XDG_CONFIG_HOME"), project, "w2:p5"); err != nil || !ok || rec.Name != "IcyKnoll" {
		t.Errorf("identity file for the new pane = %+v %v %v", rec, ok, err)
	}
	if code, out, _ := run("resolve", "IcyKnoll", "--socket", s.SocketPath); code != 0 || !strings.Contains(out, "w2:p5") || !strings.Contains(out, "verification: verified") {
		t.Errorf("resolve after the move = %d %s", code, out)
	}
}

func TestMoveRefusals(t *testing.T) {
	s, inst, w, _ := moveInstance(t)
	if _, err := core.SwitchOwner(context.Background(), inst.Dir, core.OwnerNhm, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"IcyKnoll"}, "Usage: nhm move"},
		{[]string{"--workspace", "other"}, "Usage: nhm move"},
		{[]string{"Nobody", "--workspace", "other"}, "no such binding"},
		{[]string{"IcyKnoll", "--workspace", "swarm"}, "in workspace w1 already"},
		{[]string{"IcyKnoll", "--workspace", "dup"}, "names 2 workspaces"},
		{[]string{"IcyKnoll", "--workspace", "nope"}, "no such workspace"},
		{[]string{"BlueLake", "--workspace", "other"}, "cannot tell BlueLake by its process or its native session"},
		{[]string{"RedFox", "--workspace", "other"}, "RedFox is mismatch in w1:p3, not verified"},
	} {
		code, _, errOut := run(append(append([]string{"move"}, tc.args...), "--socket", s.SocketPath)...)
		if code != exitRefused || !strings.Contains(errOut, tc.want) {
			t.Errorf("move %v = %d %s; want %q", tc.args, code, errOut, tc.want)
		}
	}
	if got := moves(s); len(got) != 0 {
		t.Fatalf("a refused move called pane.move: %v", got)
	}

	// herdr refuses the move: nothing changed.
	w.mu.Lock()
	w.moveFails = true
	w.mu.Unlock()
	if code, _, errOut := run("move", "IcyKnoll", "--workspace", "other", "--socket", s.SocketPath); code != exitError || !strings.Contains(errOut, "herdr did not move w1:p1") {
		t.Errorf("a failed pane.move = %d %s", code, errOut)
	}
	// herdr moved the pane, but another session shows there: the binding
	// stays, and the message says what happened.
	w.mu.Lock()
	w.moveFails, w.moveSession = false, "sess-other"
	w.mu.Unlock()
	code, _, errOut := run("move", "IcyKnoll", "--workspace", "other", "--socket", s.SocketPath)
	if code != exitError || !strings.Contains(errOut, "herdr moved w1:p1 to w2:p5, but the binding did not follow") || !strings.Contains(errOut, "nhm maintain") {
		t.Errorf("a move the binding cannot follow = %d %s", code, errOut)
	}
	if b := binding(t, inst, "IcyKnoll"); b.PaneID != "w1:p1" {
		t.Errorf("the binding moved without proof: %+v", b)
	}
}
