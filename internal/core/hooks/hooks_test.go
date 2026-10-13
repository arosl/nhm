package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/integrations/kinds/claude"
	"github.com/arosl/nhm/internal/integrations/kinds/codex"
	"github.com/arosl/nhm/internal/store"
)

var t0 = time.Date(2026, 10, 12, 4, 0, 0, 0, time.UTC)

// pane is the stub's view of one pane's agent.
type pane struct {
	kind    string // "" when the pane holds no agent
	name    string
	session string
	pid     int
	cwd     string
	ws, tab string // "" means w1 and w1:t1
}

// where is the pane's workspace and tab.
func (p *pane) where() (string, string) {
	if p.ws == "" {
		return "w1", "w1:t1"
	}
	return p.ws, p.tab
}

// tabState is the stub's view of one tab.
type tabState struct {
	label string
	panes int
}

// world is a herdr stub with mutable panes and tabs (w1:t1 to start
// with), and the calls nhm made.
type world struct {
	t     *testing.T
	mu    sync.Mutex
	panes map[string]*pane
	tabs  map[string]*tabState
	calls []string
}

func newWorld(t *testing.T) (*world, *herdr.Client) {
	w := &world{t: t, panes: map[string]*pane{}, tabs: map[string]*tabState{"w1:t1": {label: "1", panes: 1}}}
	s := herdrtest.New(t)
	s.Handle("agent.get", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct{ Target string }
		_ = json.Unmarshal(r.Params, &p)
		w.mu.Lock()
		defer w.mu.Unlock()
		pn, ok := w.panes[p.Target]
		if !ok || pn.kind == "" {
			return nil, &herdrtest.Error{Code: "agent_not_found", Message: "agent target " + p.Target + " not found"}
		}
		return map[string]any{"type": "agent_info", "agent": w.agentJSON(p.Target, pn)}, nil
	})
	s.Handle("agent.rename", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Target string
			Name   *string
		}
		_ = json.Unmarshal(r.Params, &p)
		w.mu.Lock()
		defer w.mu.Unlock()
		pn := w.panes[p.Target]
		name := ""
		if p.Name != nil {
			name = *p.Name
		}
		pn.name = name
		w.calls = append(w.calls, "agent.rename "+p.Target+" "+name)
		return map[string]any{"type": "agent_info", "agent": w.agentJSON(p.Target, pn)}, nil
	})
	s.Handle("pane.rename", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
			Label  string
		}
		_ = json.Unmarshal(r.Params, &p)
		w.mu.Lock()
		defer w.mu.Unlock()
		w.calls = append(w.calls, "pane.rename "+p.PaneID+" "+p.Label)
		return map[string]any{"type": "pane_info", "pane": map[string]any{"pane_id": p.PaneID, "tab_id": "w1:t1",
			"workspace_id": "w1", "terminal_id": "term_1", "agent_status": "idle", "focused": false, "revision": 1}}, nil
	})
	s.Handle("tab.rename", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			TabID string `json:"tab_id"`
			Label string
		}
		_ = json.Unmarshal(r.Params, &p)
		w.mu.Lock()
		defer w.mu.Unlock()
		w.calls = append(w.calls, "tab.rename "+p.TabID+" "+p.Label)
		tb, ok := w.tabs[p.TabID]
		if !ok {
			return nil, &herdrtest.Error{Code: "tab_not_found", Message: "tab " + p.TabID + " not found"}
		}
		tb.label = p.Label
		return map[string]any{"type": "tab_info", "tab": w.tabJSON(p.TabID)}, nil
	})
	s.Handle("tab.get", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			TabID string `json:"tab_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		w.mu.Lock()
		defer w.mu.Unlock()
		if _, ok := w.tabs[p.TabID]; !ok {
			return nil, &herdrtest.Error{Code: "tab_not_found", Message: "tab " + p.TabID + " not found"}
		}
		return map[string]any{"type": "tab_info", "tab": w.tabJSON(p.TabID)}, nil
	})
	s.Handle("session.snapshot", func(r herdrtest.Request) (any, *herdrtest.Error) {
		w.mu.Lock()
		panes, agents, tabs := []any{}, []any{}, []any{}
		for id, pn := range w.panes {
			ws, tb := pn.where()
			panes = append(panes, map[string]any{"pane_id": id, "tab_id": tb, "workspace_id": ws,
				"terminal_id": "term_1", "agent_status": "idle", "focused": false, "revision": 1, "cwd": pn.cwd, "agent": pn.kind})
			if pn.kind != "" {
				agents = append(agents, w.agentJSON(id, pn))
			}
		}
		for id := range w.tabs {
			tabs = append(tabs, w.tabJSON(id))
		}
		w.mu.Unlock()
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{"protocol": 22, "version": "0.9.3",
			"workspaces": []any{}, "panes": panes, "agents": agents, "tabs": tabs}}, nil
	})
	s.Handle("pane.process_info", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		w.mu.Lock()
		defer w.mu.Unlock()
		pn := w.panes[p.PaneID]
		procs := []any{}
		if pn != nil && pn.pid != 0 {
			// A tool the agent runs shares the foreground; the agent's
			// process comes second, so nhm must pick it by name.
			// Claude Code's binary is named after its version.
			procs = append(procs, map[string]any{"pid": pn.pid + 1000, "name": "bash", "argv": []string{"bash"}},
				map[string]any{"pid": pn.pid, "name": "2.1.296", "argv": []string{pn.kind}})
		}
		return map[string]any{"type": "pane_process_info", "process_info": map[string]any{"pane_id": p.PaneID,
			"shell_pid": 10, "foreground_processes": procs}}, nil
	})
	return w, herdr.New(s.SocketPath)
}

// tabJSON is a tab the world has; the caller holds w.mu.
func (w *world) tabJSON(id string) map[string]any {
	ws, _, _ := strings.Cut(id, ":")
	tb := w.tabs[id]
	return map[string]any{"tab_id": id, "workspace_id": ws, "number": 1, "label": tb.label,
		"focused": false, "pane_count": tb.panes, "agent_status": "idle"}
}

func (w *world) agentJSON(id string, pn *pane) map[string]any {
	ws, tb := pn.where()
	a := map[string]any{"pane_id": id, "tab_id": tb, "workspace_id": ws, "terminal_id": "term_1",
		"agent": pn.kind, "agent_status": "idle", "focused": false, "revision": 1}
	if pn.name != "" {
		a["name"] = pn.name
	}
	if pn.session != "" {
		a["agent_session"] = map[string]any{"source": "herdr:" + pn.kind, "agent": pn.kind, "kind": "id", "value": pn.session}
	}
	if pn.cwd != "" {
		a["cwd"] = pn.cwd
	}
	return a
}

func (w *world) set(id string, p pane) {
	w.mu.Lock()
	defer w.mu.Unlock()
	cp := p
	w.panes[id] = &cp
}

func (w *world) get(id string) pane {
	w.mu.Lock()
	defer w.mu.Unlock()
	return *w.panes[id]
}

func (w *world) called(prefix string) bool {
	return w.count(prefix) > 0
}

// count is how many calls start with prefix.
func (w *world) count(prefix string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, c := range w.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// setTab sets tab w1:t1's label and how many panes it holds.
func (w *world) setTab(label string, panes int) {
	w.addTab("w1:t1", label, panes)
}

// addTab adds a tab, or sets one's label and how many panes it holds.
func (w *world) addTab(id, label string, panes int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tabs[id] = &tabState{label: label, panes: panes}
}

// close takes a pane out of herdr's session.
func (w *world) close(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.panes, id)
}

// move is herdr's pane move to another workspace: the same agent and
// process under a new pane id in tab, and herdr's name stays behind.
func (w *world) move(from, to, tab string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	pn := w.panes[from]
	delete(w.panes, from)
	pn.ws, _, _ = strings.Cut(to, ":")
	pn.tab, pn.name = tab, ""
	w.panes[to] = pn
}

type harness struct {
	t     *testing.T
	env   Env
	world *world
	vars  map[string]string
	clock time.Time
}

// newHarness enrolls an instance owned by nhm, with a herdr stub.
func newHarness(t *testing.T, owner string) *harness {
	t.Helper()
	w, client := newWorld(t)
	inst, _, err := store.Enroll(t.TempDir(), "/run/herdr/herdr.sock", "default", t0)
	if err != nil {
		t.Fatal(err)
	}
	if owner == core.OwnerNhm {
		if _, err := core.SwitchOwner(context.Background(), inst.Dir, core.OwnerNhm, t0, nil); err != nil {
			t.Fatal(err)
		}
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h := &harness{t: t, world: w, vars: map[string]string{"HERDR_PANE_ID": "w1:p1"}, clock: t0}
	h.env = Env{DB: db, Bindings: bindings.NewStore(db), Herdr: client, InstanceDir: inst.Dir, ConfigHome: t.TempDir(),
		Getenv: func(k string) string { return h.vars[k] }, Now: func() time.Time { return h.clock },
		Settle: 50 * time.Millisecond}
	old := bindings.ProcStart
	bindings.ProcStart = func(pid int64) (int64, error) { return pid * 10, nil }
	t.Cleanup(func() { bindings.ProcStart = old })
	return h
}

func (h *harness) hook(payload map[string]any, id string) Result {
	h.t.Helper()
	data, _ := json.Marshal(payload)
	p, err := ParseAgentPayload(data)
	if err != nil {
		h.t.Fatal(err)
	}
	res, err := RunAgentHook(context.Background(), h.env, AgentHook{Payload: p, EventID: id})
	if err != nil {
		h.t.Fatalf("hook: %v", err)
	}
	return res
}

func registration(session, name, project string) map[string]any {
	return map[string]any{"hook_event_name": "PostToolUse", "session_id": session,
		"tool_name":  "mcp__mcp-agent-mail__register_agent",
		"tool_input": map[string]any{"project_key": project, "program": "claude-code"},
		// Claude Code passes an MCP result as content blocks.
		"tool_response": []any{map[string]any{"type": "text",
			"text": `{"id":7,"name":"` + name + `","registration_token":"tok-secret-1"}`}}}
}

func sessionStart(session, source string) map[string]any {
	return map[string]any{"hook_event_name": "SessionStart", "session_id": session, "source": source}
}

func (h *harness) binding(name string) bindings.Binding {
	h.t.Helper()
	list, err := h.env.Bindings.List(context.Background(), bindings.Filter{MailName: name})
	if err != nil || len(list) == 0 {
		h.t.Fatalf("binding %s: %v %d", name, err, len(list))
	}
	return list[0]
}

func TestRegistrationBindsAHandStartedAgent(t *testing.T) {
	h := newHarness(t, core.OwnerNhm)
	project := t.TempDir()
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500})
	res := h.hook(registration("sess-a", "IcyKnoll", project), "e1")
	b := h.binding("IcyKnoll")
	if b.Status != bindings.Active || b.PaneID != "w1:p1" || b.Generation != 1 || b.Provenance != bindings.Adopted ||
		b.NativeSession == nil || b.NativeSession.Value != "sess-a" || b.ForegroundPID != 500 || b.ForegroundStart != 5000 {
		t.Fatalf("binding = %+v", b)
	}
	if h.world.get("w1:p1").name != "icyknoll" || !h.world.called("pane.rename w1:p1 IcyKnoll") || !h.world.called("tab.rename w1:t1 IcyKnoll") {
		t.Fatalf("decoration: %v", h.world.calls)
	}
	tok, err := store.ReadSecret(h.env.InstanceDir, b.ID)
	if err != nil || tok != "tok-secret-1" {
		t.Fatalf("token = %q %v", tok, err)
	}
	if _, err := store.ReadSecret(h.env.InstanceDir, "cand-1"); err == nil {
		t.Fatal("the candidate's token file stayed")
	}
	rec, ok, err := agentmail.ReadIdentity(h.env.ConfigHome, b.MailProjectKey, "w1:p1")
	if err != nil || !ok || rec.Name != "IcyKnoll" {
		t.Fatalf("identity file = %+v %v", rec, err)
	}
	// The candidate row keeps no token.
	var facts string
	if err := h.env.DB.SQL().QueryRow(`SELECT payload_json FROM hook_candidates WHERE candidate_id = ?`, res.Candidate).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(facts, "tok-secret") {
		t.Fatalf("token in the candidate row: %s", facts)
	}
	// The same registration again changes nothing.
	h.hook(registration("sess-a", "IcyKnoll", project), "e2")
	if b2 := h.binding("IcyKnoll"); b2.Generation != 1 || b2.ID != b.ID {
		t.Fatalf("second registration: %+v", b2)
	}
}

func TestRegistrationFromANestedRunIsIgnored(t *testing.T) {
	h := newHarness(t, core.OwnerNhm)
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500})
	res := h.hook(registration("sess-nested", "IcyKnoll", t.TempDir()), "e1")
	// Fresh: herdr may still catch up, so the candidate waits.
	if len(res.Report.Actions) != 0 {
		t.Fatalf("actions = %v", res.Report.Actions)
	}
	h.clock = h.clock.Add(5 * time.Second)
	if _, err := ReconcilePane(context.Background(), h.env, "w1:p1"); err != nil {
		t.Fatal(err)
	}
	got, _ := CandidateResults(context.Background(), h.env.DB, "w1:p1")
	if len(got) != 1 || !got[0].Processed || !strings.HasPrefix(got[0].Result, "ignored: herdr reports session") {
		t.Fatalf("candidates = %+v", got)
	}
	if list, _ := h.env.Bindings.List(context.Background(), bindings.Filter{}); len(list) != 0 {
		t.Fatalf("bindings = %+v", list)
	}
	if _, err := store.ReadSecret(h.env.InstanceDir, "cand-1"); err == nil {
		t.Fatal("the rejected candidate's token file stayed")
	}
}

func TestCompactionContinuesAndRemindsTheAgent(t *testing.T) {
	h := newHarness(t, core.OwnerNhm)
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500})
	h.hook(registration("sess-a", "IcyKnoll", t.TempDir()), "e1")
	h.clock = h.clock.Add(time.Minute)
	res := h.hook(sessionStart("sess-a", "compact"), "e2")
	b := h.binding("IcyKnoll")
	if b.Status != bindings.Active || b.Generation != 1 || b.CompactedAt == nil || !b.CompactedAt.Equal(h.clock) {
		t.Fatalf("binding = %+v", b)
	}
	if res.Reminder != "You are IcyKnoll (herdr icyknoll)." {
		t.Fatalf("reminder = %q", res.Reminder)
	}
}

func TestClearDisplacesAndTakesTheMarksOff(t *testing.T) {
	h := newHarness(t, core.OwnerNhm)
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500})
	h.hook(registration("sess-a", "IcyKnoll", t.TempDir()), "e1")
	b := h.binding("IcyKnoll")
	// herdr reports the new session; a name from agent.start would stay.
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-b", pid: 500, name: "icyknoll"})
	res := h.hook(sessionStart("sess-b", "clear"), "e2")
	got := h.binding("IcyKnoll")
	if got.Status != bindings.Displaced || !strings.Contains(got.StatusReason, "cleared") || got.Generation != 1 {
		t.Fatalf("binding = %+v", got)
	}
	if h.world.get("w1:p1").name != "" {
		t.Fatal("the herdr name stayed on the displaced pane")
	}
	if _, ok, err := agentmail.ReadIdentity(h.env.ConfigHome, b.MailProjectKey, "w1:p1"); ok || err != nil {
		t.Fatalf("the identity file stayed (%v)", err)
	}
	if res.Reminder != "" {
		t.Fatalf("reminder after clear = %q", res.Reminder)
	}
}

func TestRestartInPlaceGetsItsNameBack(t *testing.T) {
	h := newHarness(t, core.OwnerNhm)
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500})
	h.hook(registration("sess-a", "IcyKnoll", t.TempDir()), "e1")
	// /exit, then `claude --continue`: the same session, a new process,
	// and herdr dropped the name.
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 777})
	rep, err := RunPluginEvent(context.Background(), h.env, PaneEvent{PaneID: "w1:p1", Agent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	b := h.binding("IcyKnoll")
	if b.ForegroundPID != 777 || b.Generation != 1 || h.world.get("w1:p1").name != "icyknoll" {
		t.Fatalf("binding = %+v, name %q, report %+v", b, h.world.get("w1:p1").name, rep)
	}
	// A second event changes nothing more.
	rep, _ = RunPluginEvent(context.Background(), h.env, PaneEvent{PaneID: "w1:p1"})
	if len(rep.Actions) != 0 {
		t.Fatalf("second event: %v", rep.Actions)
	}
}

// nhm maintain's name pass names a bound agent's tab that still has
// herdr's default label, a number (nhm-qyz). A hook leaves the tab
// alone; a label that someone set, and a tab with other panes, stay.
func TestMaintainLabelsATabLeftAtItsNumber(t *testing.T) {
	h := newHarness(t, core.OwnerNhm)
	ctx := context.Background()
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500})
	if _, err := h.env.Bindings.Create(ctx, bindings.New{MailName: "IcyKnoll", MailProjectKey: "/p", ProjectID: "proj-p",
		Provenance: bindings.Adopted, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p1",
			HerdrAlias: "icyknoll", Kind: "claude", NativeSession: &bindings.NativeSession{Value: "sess-a"},
			ForegroundPID: 500, ForegroundStart: 5000}}); err != nil {
		t.Fatal(err)
	}
	h.world.setTab("5", 1)
	if _, err := ReconcilePane(ctx, h.env, "w1:p1"); err != nil {
		t.Fatal(err)
	}
	if h.world.called("tab.") {
		t.Fatalf("a hook's reconcile touched the tab: %v", h.world.calls)
	}

	h.env.TabLabels = true
	r, err := ReconcilePane(ctx, h.env, "w1:p1")
	if err != nil {
		t.Fatal(err)
	}
	if !h.world.called("tab.rename w1:t1 IcyKnoll") || !slices.Contains(r.Actions, "labelled tab w1:t1 IcyKnoll") || len(r.Warnings) != 0 {
		t.Fatalf("maintain: calls %v, report %+v", h.world.calls, r)
	}
	if r, _ = ReconcilePane(ctx, h.env, "w1:p1"); len(r.Actions) != 0 || h.world.count("tab.rename") != 1 {
		t.Fatalf("second pass: calls %v, report %+v", h.world.calls, r)
	}
	for _, tab := range []struct {
		label string
		panes int
	}{{"work", 1}, {"7", 2}} {
		h.world.setTab(tab.label, tab.panes)
		if r, err = ReconcilePane(ctx, h.env, "w1:p1"); err != nil || len(r.Actions) != 0 || h.world.count("tab.rename") != 1 {
			t.Fatalf("tab %q with %d panes: calls %v, report %+v %v", tab.label, tab.panes, h.world.calls, r, err)
		}
	}
}

// herdr pane move to another workspace gives the pane a new id; the
// binding follows its process there with the same generation, and the
// next reconcile finds it in place (nhm-p1o).
func TestBindingFollowsAPaneMovedToAnotherWorkspace(t *testing.T) {
	h := newHarness(t, core.OwnerNhm)
	ctx := context.Background()
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500})
	h.hook(registration("sess-a", "IcyKnoll", t.TempDir()), "e1")
	b := h.binding("IcyKnoll")

	// herdr pane move w1:p1 --workspace w2 --new-tab: a tab labelled
	// with its number.
	h.world.move("w1:p1", "w2:p1", "w2:t1")
	h.world.addTab("w2:t1", "3", 1)
	r, err := ReconcilePane(ctx, h.env, "w1:p1")
	if err != nil {
		t.Fatal(err)
	}
	nb := h.binding("IcyKnoll")
	if nb.ID != b.ID || nb.Generation != b.Generation || nb.Status != bindings.Active || nb.PaneID != "w2:p1" ||
		nb.WorkspaceID != "w2" || nb.TabID != "w2:t1" || nb.Slot != "0.1" || len(r.Warnings) != 0 {
		t.Fatalf("binding = %+v, report %+v", nb, r)
	}
	if !slices.Contains(r.Actions, "IcyKnoll moved from w1:p1 to w2:p1, slot 0.1") ||
		h.world.get("w2:p1").name != "icyknoll" || !h.world.called("tab.rename w2:t1 IcyKnoll") {
		t.Fatalf("report %+v, calls %v", r, h.world.calls)
	}
	// Agent Mail's identity file names the new pane id, and the old one
	// stays for the moved process's HERDR_PANE_ID.
	for _, p := range []string{"w2:p1", "w1:p1"} {
		if rec, ok, err := agentmail.ReadIdentity(h.env.ConfigHome, nb.MailProjectKey, p); err != nil || !ok || rec.Name != "IcyKnoll" {
			t.Fatalf("identity file for %s = %+v %v %v", p, rec, ok, err)
		}
	}
	if r, err = ReconcilePane(ctx, h.env, "w2:p1"); err != nil || len(r.Actions) != 0 || len(r.Warnings) != 0 {
		t.Fatalf("second reconcile: %+v %v", r, err)
	}
}

// An agent that left its pane, which herdr still has, did not move: the
// binding stays where it is.
func TestAgentThatLeftDoesNotMove(t *testing.T) {
	h := newHarness(t, core.OwnerNhm)
	ctx := context.Background()
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500})
	h.hook(registration("sess-a", "IcyKnoll", t.TempDir()), "e1")
	// The agent exits; the same session resumes in a new process in
	// another workspace: a new occupant, for adopt --handoff-from.
	h.world.set("w1:p1", pane{})
	h.world.set("w2:p1", pane{kind: "claude", session: "sess-a", pid: 900, ws: "w2", tab: "w2:t1"})
	r, err := ReconcilePane(ctx, h.env, "w1:p1")
	if err != nil || len(r.Actions) != 0 {
		t.Fatalf("reconcile: %+v %v", r, err)
	}
	// herdr closes the old pane: the other agent is still a new process.
	h.world.close("w1:p1")
	if r, err = ReconcilePane(ctx, h.env, "w1:p1"); err != nil || len(r.Actions) != 0 {
		t.Fatalf("reconcile after close: %+v %v", r, err)
	}
	if b := h.binding("IcyKnoll"); b.PaneID != "w1:p1" || b.Status != bindings.Active {
		t.Fatalf("binding = %+v", b)
	}
}

func TestDefaultTabLabel(t *testing.T) {
	for label, want := range map[string]bool{"": true, "5": true, "12": true, "IcyKnoll": false, "5a": false, "shell": false} {
		if got := defaultTabLabel(label); got != want {
			t.Errorf("defaultTabLabel(%q) = %v, want %v", label, got, want)
		}
	}
}

func TestAnotherKindDisplaces(t *testing.T) {
	h := newHarness(t, core.OwnerNhm)
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500})
	h.hook(registration("sess-a", "IcyKnoll", t.TempDir()), "e1")
	h.world.set("w1:p1", pane{kind: "codex", session: "thr-1", pid: 600})
	if _, err := RunPluginEvent(context.Background(), h.env, PaneEvent{PaneID: "w1:p1"}); err != nil {
		t.Fatal(err)
	}
	if b := h.binding("IcyKnoll"); b.Status != bindings.Displaced {
		t.Fatalf("binding = %+v", b)
	}
}

func TestReleasedAgentLeavesTheBinding(t *testing.T) {
	h := newHarness(t, core.OwnerNhm)
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500})
	h.hook(registration("sess-a", "IcyKnoll", t.TempDir()), "e1")
	h.world.set("w1:p1", pane{})
	if _, err := RunPluginEvent(context.Background(), h.env, PaneEvent{PaneID: "w1:p1", Released: true}); err != nil {
		t.Fatal(err)
	}
	if b := h.binding("IcyKnoll"); b.Status != bindings.Active {
		t.Fatalf("binding = %+v", b)
	}
}

func TestSpawnedCodexTakesItsFirstSessionAndCompacts(t *testing.T) {
	h := newHarness(t, core.OwnerNhm)
	ctx := context.Background()
	h.world.set("w1:p1", pane{kind: "codex", pid: 600})
	b, err := h.env.Bindings.Create(ctx, bindings.New{MailName: "BlueLake", MailProjectKey: "/p", ProjectID: "proj-p",
		Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p1",
			HerdrAlias: "bluelake", Kind: "codex", LaunchID: "launch-9", ForegroundPID: 600, ForegroundStart: 6000}})
	if err != nil {
		t.Fatal(err)
	}
	// Codex's SessionStart comes at the first turn (ACFS exp.3).
	h.vars["NHM_LAUNCH_ID"] = "launch-9"
	h.world.set("w1:p1", pane{kind: "codex", session: "thr-1", pid: 600, name: "bluelake"})
	res := h.hook(sessionStart("thr-1", "startup"), "e1")
	got, _ := h.env.Bindings.Get(ctx, b.ID)
	if got.NativeSession == nil || got.NativeSession.Value != "thr-1" || got.Generation != 1 {
		t.Fatalf("binding = %+v", got)
	}
	if res.Reminder != "" {
		t.Fatalf("codex got a reminder: %q", res.Reminder)
	}
	h.hook(map[string]any{"hook_event_name": "PostCompact", "session_id": "thr-1", "trigger": "manual"}, "e2")
	got, _ = h.env.Bindings.Get(ctx, b.ID)
	if got.CompactedAt == nil || got.Status != bindings.Active {
		t.Fatalf("after compaction = %+v", got)
	}
}

func TestBashOwnerLeavesCandidatesPending(t *testing.T) {
	h := newHarness(t, core.OwnerBash)
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500})
	res := h.hook(registration("sess-a", "IcyKnoll", t.TempDir()), "e1")
	if res.Report.Ran || res.Report.Owner != core.OwnerBash {
		t.Fatalf("report = %+v", res.Report)
	}
	got, _ := CandidateResults(context.Background(), h.env.DB, "w1:p1")
	if len(got) != 1 || got[0].Processed {
		t.Fatalf("candidates = %+v", got)
	}
	if h.world.called("agent.rename") {
		t.Fatal("renamed under the Bash owner")
	}
}

// While the Bash owns the instance, a hook imports its own pane's
// Bash-spawned agent (acfs-9hkh.41): verified evidence only, no rename,
// and nothing for another pane.
func TestBashOwnerImportsTheHookPane(t *testing.T) {
	h := newHarness(t, core.OwnerBash)
	ctx := context.Background()
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	repo = canonical(repo)
	h.env.AcfsHome = t.TempDir()
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500, cwd: repo, name: "icyknoll"})
	h.world.set("w1:p2", pane{kind: "codex", session: "thr-1", pid: 600, cwd: repo})
	for pane, name := range map[string]string{"w1:p1": "IcyKnoll", "w1:p2": "BlueLake"} {
		if _, err := agentmail.WriteIdentity(h.env.ConfigHome, repo, pane, name, t0); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := RunPluginEvent(ctx, h.env, PaneEvent{PaneID: "w1:p1", Agent: "claude"})
	if err != nil || rep.Ran {
		t.Fatalf("report = %+v %v", rep, err)
	}
	b, err := h.env.Bindings.ActiveByPane(ctx, "w1:p1")
	if err != nil || b.MailName != "IcyKnoll" || b.Provenance != bindings.Imported || b.MailProjectKey != repo {
		t.Fatalf("w1:p1 binding = %+v %v (report %+v)", b, err, rep)
	}
	if _, err := h.env.Bindings.ActiveByPane(ctx, "w1:p2"); !errors.Is(err, bindings.ErrNotFound) {
		t.Fatalf("w1:p2 was imported by w1:p1's hook: %v", err)
	}
	if h.world.called("agent.rename") || h.world.called("pane.rename") {
		t.Fatalf("the import renamed: %v", h.world.calls)
	}
	var ops int
	if err := h.env.DB.SQL().QueryRow(`SELECT count(*) FROM operations`).Scan(&ops); err != nil || ops != 1 {
		t.Fatalf("operations = %d %v", ops, err)
	}
	// The pane is bound now: the next hook claims nothing.
	if _, err := RunPluginEvent(ctx, h.env, PaneEvent{PaneID: "w1:p1"}); err != nil {
		t.Fatal(err)
	}
	if err := h.env.DB.SQL().QueryRow(`SELECT count(*) FROM operations`).Scan(&ops); err != nil || ops != 1 {
		t.Fatalf("operations after a second hook = %d %v", ops, err)
	}
}

func TestFilteredHooksWriteNothing(t *testing.T) {
	h := newHarness(t, core.OwnerNhm)
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500})
	h.vars["CLAUDE_CODE_ENTRYPOINT"] = "sdk-cli"
	if res := h.hook(sessionStart("sess-n", "startup"), "e1"); !strings.HasPrefix(res.Skipped, "nested run") {
		t.Fatalf("nested = %+v", res)
	}
	delete(h.vars, "CLAUDE_CODE_ENTRYPOINT")
	other := registration("sess-a", "IcyKnoll", "/p")
	other["tool_name"] = "mcp__mcp-agent-mail__send_message"
	if res := h.hook(other, "e2"); res.Skipped == "" {
		t.Fatalf("send_message = %+v", res)
	}
	delete(h.vars, "HERDR_PANE_ID")
	if res := h.hook(sessionStart("sess-a", "startup"), "e3"); res.Skipped == "" {
		t.Fatalf("no pane = %+v", res)
	}
	got, _ := CandidateResults(context.Background(), h.env.DB, "w1:p1")
	if len(got) != 0 {
		t.Fatalf("candidates = %+v", got)
	}
}

func TestPruneExpiresAndBounds(t *testing.T) {
	h := newHarness(t, core.OwnerBash)
	ctx := context.Background()
	db := h.env.DB.SQL()
	if _, err := recordCandidate(ctx, db, Candidate{PaneID: "w1:p1", HookEvent: "SessionStart", HookEventID: "old",
		Facts: CandidateFacts{HookEventName: "SessionStart", TokenRef: "cand-77"}, ReceivedAt: t0}); err != nil {
		t.Fatal(err)
	}
	refs, err := prune(ctx, db, t0.Add(time.Hour))
	if err != nil || len(refs) != 1 || refs[0] != "cand-77" {
		t.Fatalf("prune = %v %v", refs, err)
	}
	got, _ := CandidateResults(ctx, h.env.DB, "w1:p1")
	if len(got) != 1 || !got[0].Processed || !strings.HasPrefix(got[0].Result, "expired") {
		t.Fatalf("candidates = %+v", got)
	}
	if _, err := prune(ctx, db, t0.Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ := CandidateResults(ctx, h.env.DB, "w1:p1"); len(got) != 0 {
		t.Fatalf("old rows stayed: %+v", got)
	}
}

func TestParseRegistrationShapes(t *testing.T) {
	in := json.RawMessage(`{"project_key":"/p"}`)
	for name, resp := range map[string]string{
		"object":        `{"name":"IcyKnoll","registration_token":"t"}`,
		"string":        `"{\"name\":\"IcyKnoll\",\"registration_token\":\"t\"}"`,
		"content":       `[{"type":"text","text":"{\"name\":\"IcyKnoll\",\"registration_token\":\"t\"}"}]`,
		"wrapped":       `{"content":[{"type":"text","text":"{\"name\":\"IcyKnoll\",\"registration_token\":\"t\"}"}]}`,
		"structured":    `{"structuredContent":{"name":"IcyKnoll","registration_token":"t"},"content":[]}`,
		"macro session": `{"project":{"slug":"p"},"agent":{"name":"IcyKnoll","registration_token":"t"},"inbox":[{"name":"x"}]}`,
	} {
		r, err := ParseRegistration(AgentPayload{ToolInput: in, ToolResponse: json.RawMessage(resp)})
		if err != nil || r.Name != "IcyKnoll" || r.Token != "t" || r.ProjectKey != "/p" {
			t.Errorf("%s: %+v %v", name, r, err)
		}
	}
	if _, err := ParseRegistration(AgentPayload{ToolResponse: json.RawMessage(`{"name":"IcyKnoll"}`)}); err == nil {
		t.Error("no project accepted")
	}
	if got := RegistrationTool("mcp__agent_mail__macro_start_session"); got != "macro_start_session" {
		t.Errorf("RegistrationTool = %q", got)
	}
}

// A hook stopped by its hard stop between the create and its writes
// leaves a binding without a token or an identity file, and the candidate
// pending. The next reconcile finishes the writes.
func TestReconcileRepairsAStoppedRegistration(t *testing.T) {
	h := newHarness(t, core.OwnerNhm)
	ctx := context.Background()
	project := t.TempDir()
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500})
	id, err := recordCandidate(ctx, h.env.DB.SQL(), Candidate{PaneID: "w1:p1", Session: "sess-a", HookEvent: "PostToolUse",
		HookEventID: "e1", ReceivedAt: h.clock, Facts: CandidateFacts{HookEventName: "PostToolUse", SessionID: "sess-a",
			ToolName: "mcp__mcp-agent-mail__register_agent", MailName: "IcyKnoll", MailProject: project, TokenRef: "cand-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteSecret(h.env.InstanceDir, "cand-1", "tok-secret-1"); err != nil {
		t.Fatal(err)
	}
	b, err := h.env.Bindings.Create(ctx, bindings.New{MailName: "IcyKnoll", MailProjectKey: canonical(project),
		ProjectID: core.ProjectID(canonical(project)), Provenance: bindings.Adopted,
		Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p1", HerdrAlias: "icyknoll", Kind: "claude",
			NativeSession: &bindings.NativeSession{Value: "sess-a"}, ForegroundPID: 500, ForegroundStart: 5000}})
	if err != nil {
		t.Fatal(err)
	}
	// The hook died here.
	if _, err := ReconcilePane(ctx, h.env, "w1:p1"); err != nil {
		t.Fatal(err)
	}
	if tok, err := store.ReadSecret(h.env.InstanceDir, b.ID); err != nil || tok != "tok-secret-1" {
		t.Fatalf("token = %q %v", tok, err)
	}
	if rec, ok, err := agentmail.ReadIdentity(h.env.ConfigHome, b.MailProjectKey, "w1:p1"); err != nil || !ok || rec.Name != "IcyKnoll" {
		t.Fatalf("identity file = %+v %v %v", rec, ok, err)
	}
	got, _ := CandidateResults(ctx, h.env.DB, "w1:p1")
	if len(got) != 1 || got[0].ID != id || !got[0].Processed || !strings.HasPrefix(got[0].Result, "already bound") {
		t.Fatalf("candidates = %+v", got)
	}
	if _, err := store.ReadSecret(h.env.InstanceDir, "cand-1"); err == nil {
		t.Fatal("the candidate's token file stayed")
	}
}

// stoppedRegistration is a binding that a stopped hook created, and its
// pending candidate with token tok.
func stoppedRegistration(t *testing.T, h *harness, tok string) bindings.Binding {
	t.Helper()
	ctx := context.Background()
	project := t.TempDir()
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500})
	if _, err := recordCandidate(ctx, h.env.DB.SQL(), Candidate{PaneID: "w1:p1", Session: "sess-a", HookEvent: "PostToolUse",
		HookEventID: "e1", ReceivedAt: h.clock, Facts: CandidateFacts{HookEventName: "PostToolUse", SessionID: "sess-a",
			ToolName: "mcp__mcp-agent-mail__register_agent", MailName: "IcyKnoll", MailProject: project, TokenRef: "cand-1"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteSecret(h.env.InstanceDir, "cand-1", tok); err != nil {
		t.Fatal(err)
	}
	b, err := h.env.Bindings.Create(ctx, bindings.New{MailName: "IcyKnoll", MailProjectKey: canonical(project),
		ProjectID: core.ProjectID(canonical(project)), Provenance: bindings.Adopted,
		Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p1", HerdrAlias: "icyknoll", Kind: "claude",
			NativeSession: &bindings.NativeSession{Value: "sess-a"}, ForegroundPID: 500, ForegroundStart: 5000}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// When the token cannot be stored under the binding, the candidate's file
// is its only copy: it stays, the candidate stays pending, and a later
// reconcile stores it.
func TestTokenSurvivesAFailedWrite(t *testing.T) {
	h := newHarness(t, core.OwnerNhm)
	ctx := context.Background()
	b := stoppedRegistration(t, h, "tok-only-copy")
	// A directory where the binding's token file goes makes the write fail.
	blocker := filepath.Join(h.env.InstanceDir, store.SecretsDir, b.ID, "x")
	if err := os.MkdirAll(blocker, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcilePane(ctx, h.env, "w1:p1"); err != nil {
		t.Fatal(err)
	}
	if tok, err := store.ReadSecret(h.env.InstanceDir, "cand-1"); err != nil || tok != "tok-only-copy" {
		t.Fatalf("the candidate's token = %q %v", tok, err)
	}
	if got, _ := CandidateResults(ctx, h.env.DB, "w1:p1"); len(got) != 1 || got[0].Processed {
		t.Fatalf("candidates = %+v", got)
	}
	if err := os.RemoveAll(filepath.Join(h.env.InstanceDir, store.SecretsDir, b.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcilePane(ctx, h.env, "w1:p1"); err != nil {
		t.Fatal(err)
	}
	if tok, err := store.ReadSecret(h.env.InstanceDir, b.ID); err != nil || tok != "tok-only-copy" {
		t.Fatalf("binding token = %q %v", tok, err)
	}
	if _, err := store.ReadSecret(h.env.InstanceDir, "cand-1"); err == nil {
		t.Fatal("the candidate's token file stayed after the binding got it")
	}
	if got, _ := CandidateResults(ctx, h.env.DB, "w1:p1"); !got[0].Processed || !strings.HasPrefix(got[0].Result, "already bound") {
		t.Fatalf("candidates = %+v", got)
	}
}

// Agent Mail refuses a token once the name registers again, so a newer
// token on the already-bound path replaces the stored one.
func TestNewerTokenReplacesTheStoredOne(t *testing.T) {
	h := newHarness(t, core.OwnerNhm)
	ctx := context.Background()
	b := stoppedRegistration(t, h, "tok-new")
	if err := store.WriteSecret(h.env.InstanceDir, b.ID, "tok-old"); err != nil {
		t.Fatal(err)
	}
	r, err := ReconcilePane(ctx, h.env, "w1:p1")
	if err != nil {
		t.Fatal(err)
	}
	if tok, err := store.ReadSecret(h.env.InstanceDir, b.ID); err != nil || tok != "tok-new" {
		t.Fatalf("binding token = %q %v", tok, err)
	}
	if !strings.Contains(strings.Join(r.Actions, "\n"), "replaced "+b.ID+"'s registration token") {
		t.Fatalf("actions = %v", r.Actions)
	}
	if _, err := store.ReadSecret(h.env.InstanceDir, "cand-1"); err == nil {
		t.Fatal("the candidate's token file stayed")
	}
}

func TestSubagentNeverBindsThePane(t *testing.T) {
	h := newHarness(t, core.OwnerNhm)
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500})
	// A Task subagent shares the parent's session id and pane.
	reg := registration("sess-a", "IcyKnoll", t.TempDir())
	reg["agent_id"], reg["agent_type"] = "agent-7", "general-purpose"
	if res := h.hook(reg, "e1"); !strings.HasPrefix(res.Skipped, "subagent call") {
		t.Fatalf("subagent registration = %+v", res)
	}
	start := sessionStart("sess-a", "compact")
	start["agent_id"] = "agent-7"
	if res := h.hook(start, "e2"); !strings.HasPrefix(res.Skipped, "subagent call") {
		t.Fatalf("subagent SessionStart = %+v", res)
	}
	if list, _ := h.env.Bindings.List(context.Background(), bindings.Filter{}); len(list) != 0 {
		t.Fatalf("bindings = %+v", list)
	}
	if got, _ := CandidateResults(context.Background(), h.env.DB, "w1:p1"); len(got) != 0 {
		t.Fatalf("candidates = %+v", got)
	}
}

func TestRulesSpellVerdictsAlike(t *testing.T) {
	if codex.Continue != claude.Continue || codex.Displace != claude.Displace || codex.Ignore != claude.Ignore {
		t.Fatal("the claude and codex rules spell their verdicts differently")
	}
}

func TestSessionlessHookProvesNothing(t *testing.T) {
	h := newHarness(t, core.OwnerNhm)
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500})
	h.hook(registration("", "IcyKnoll", t.TempDir()), "e1")
	got, _ := CandidateResults(context.Background(), h.env.DB, "w1:p1")
	if len(got) != 1 || got[0].Result != "ignored: the hook sent no session id" {
		t.Fatalf("candidates = %+v", got)
	}
}

func TestParsePluginEventSpellings(t *testing.T) {
	ev, err := ParsePluginEvent(`{"event":"pane_agent_detected","data":{"pane_id":"w1:p2","released":true,"final_status":"done"}}`)
	if err != nil || ev.PaneID != "w1:p2" || !ev.Released || ev.FinalStatus != herdr.StatusDone {
		t.Fatalf("detected = %+v %v", ev, err)
	}
	ev, err = ParsePluginEvent(`{"event":"pane.agent_status_changed","data":{"pane_id":"w1:p3","agent_status":"idle","agent":"claude"}}`)
	if err != nil || ev.PaneID != "w1:p3" || ev.AgentStatus != herdr.StatusIdle {
		t.Fatalf("status = %+v %v", ev, err)
	}
	if ev, err := ParsePluginEvent(""); err != nil || ev.PaneID != "" {
		t.Fatalf("empty = %+v %v", ev, err)
	}
}
