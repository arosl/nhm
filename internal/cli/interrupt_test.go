package cli

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/store"
)

// interruptAgent is one bound agent of interruptFixture's workspace w1
// ("swarm"): its pane, tab, kind and herdr pane label.
type interruptAgent struct{ name, tab, pane, kind, label string }

// interruptFixture binds the agents, makes nhm the owner, and records each
// agent.send_keys as "pane=key+key". A pane in failKeys answers send_keys
// with an error.
func interruptFixture(t *testing.T, agents []interruptAgent, failKeys ...string) (*herdrtest.Server, func() []string) {
	t.Helper()
	s, inst := cliInstance(t)
	tabs := map[string]bool{}
	var panes []any
	for _, a := range agents {
		tabs[a.tab] = true
		panes = append(panes, map[string]any{"pane_id": a.pane, "tab_id": a.tab, "workspace_id": "w1", "terminal_id": "t-" + a.pane,
			"label": a.label, "agent": a.kind, "agent_status": "working", "focused": false, "revision": 1})
	}
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "layouts": []any{}, "tabs": []any{}, "panes": panes, "agents": []any{},
			"workspaces": []any{map[string]any{"workspace_id": "w1", "number": 1, "label": "swarm", "focused": true,
				"pane_count": len(agents), "tab_count": len(tabs), "active_tab_id": "w1:t1", "agent_status": "working"}},
		}}, nil
	})
	kindOf := map[string]string{}
	for _, a := range agents {
		kindOf[a.pane] = a.kind
	}
	s.Handle("agent.get", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal(r.Params, &p)
		return map[string]any{"type": "agent_info", "agent": agentMap(p.Target, kindOf[p.Target], "working", 4, 0)}, nil
	})
	var mu sync.Mutex
	var sent []string
	s.Handle("agent.send_keys", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Target string   `json:"target"`
			Keys   []string `json:"keys"`
		}
		_ = json.Unmarshal(r.Params, &p)
		for _, f := range failKeys {
			if f == p.Target {
				return nil, &herdrtest.Error{Code: "pane_not_found", Message: "no pane"}
			}
		}
		mu.Lock()
		sent = append(sent, p.Target+"="+strings.Join(p.Keys, "+"))
		mu.Unlock()
		return map[string]any{"type": "ok"}, nil
	})
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, a := range agents {
		if _, err := bindings.NewStore(db).Create(context.Background(), bindings.New{MailName: a.name, MailProjectKey: "/p", ProjectID: "proj",
			Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: a.tab, PaneID: a.pane,
				HerdrAlias: strings.ToLower(a.name), Kind: a.kind}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := core.SwitchOwner(context.Background(), inst.Dir, core.OwnerNhm, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_SOCKET_PATH", s.SocketPath)
	return s, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), sent...)
	}
}

func decodeInterrupt(t *testing.T, out string) InterruptResponse {
	t.Helper()
	var r InterruptResponse
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("not ntm's InterruptResponse: %v\n%s", err, out)
	}
	return r
}

// TestInterruptKeyPerKind: ntm's key whatever the agent does: Ctrl+C for
// Claude, Codex, opencode and grok (the operator's ruling, 2026-10-25),
// Escape for omp; pi (no ntm kind) keeps its Esc.
func TestInterruptKeyPerKind(t *testing.T) {
	_, sent := interruptFixture(t, []interruptAgent{
		{"IcyKnoll", "w1:t1", "w1:p1", "claude", ""}, {"BlueLake", "w1:t1", "w1:p2", "codex", ""},
		{"RedFox", "w1:t1", "w1:p3", "omp", ""}, {"GreenHill", "w1:t1", "w1:p4", "pi", ""},
		{"TealBay", "w1:t1", "w1:p5", "opencode", ""}, {"GoldRun", "w1:t1", "w1:p6", "grok", ""},
	})
	code, out, errOut := runVerb(verbRun("interrupt"), "swarm")
	if code != exitOK || out != "Sent interrupt to 6 agent pane(s) (Ctrl+C: 4, Escape: 2)\n" {
		t.Fatalf("interrupt = %d %q %s", code, out, errOut)
	}
	if got := strings.Join(sent(), " "); got != "w1:p1=ctrl+c w1:p2=ctrl+c w1:p3=esc w1:p4=esc w1:p5=ctrl+c w1:p6=ctrl+c" {
		t.Fatalf("keys = %s", got)
	}
}

// TestInterruptTextCounts: Ctrl+C alone has ntm's short line; the calling
// pane is nhm's user pane, left out unless --include-self.
func TestInterruptTextCounts(t *testing.T) {
	_, sent := interruptFixture(t, []interruptAgent{
		{"IcyKnoll", "w1:t1", "w1:p1", "claude", ""}, {"BlueLake", "w1:t1", "w1:p2", "codex", ""},
	})
	t.Setenv("HERDR_PANE_ID", "w1:p2")
	if code, out, errOut := runVerb(verbRun("interrupt"), "swarm"); code != exitOK || out != "Sent Ctrl+C to 1 agent pane(s)\n" {
		t.Fatalf("interrupt = %d %q %s", code, out, errOut)
	}
	if code, out, _ := runVerb(verbRun("interrupt"), "swarm", "--include-self"); code != exitOK || out != "Sent Ctrl+C to 2 agent pane(s)\n" {
		t.Fatalf("--include-self = %d %q", code, out)
	}
	if got := strings.Join(sent(), " "); got != "w1:p1=ctrl+c w1:p1=ctrl+c w1:p2=ctrl+c" {
		t.Fatalf("keys = %s", got)
	}
}

// TestInterruptSkipsAgy: ntm leaves antigravity out: no key, no entry, no
// count.
func TestInterruptSkipsAgy(t *testing.T) {
	_, sent := interruptFixture(t, []interruptAgent{
		{"IcyKnoll", "w1:t1", "w1:p1", "claude", ""}, {"TealBay", "w1:t1", "w1:p2", "agy", ""},
	})
	code, out, _ := runVerb(verbRun("interrupt"), "swarm", "--json")
	r := decodeInterrupt(t, out)
	if code != exitOK || r.Interrupted != 1 || r.Skipped != 0 || len(r.Panes) != 1 || r.Panes[0].PaneID != "w1:p1" {
		t.Fatalf("interrupt = %d %s", code, out)
	}
	if got := strings.Join(sent(), " "); got != "w1:p1=ctrl+c" {
		t.Fatalf("keys = %s", got)
	}
}

// TestInterruptTag: --tag keeps the agents whose pane label ends in one of
// the tags (OR, any case, repeatable or a comma list); the others are
// skipped, as ntm counts them.
func TestInterruptTag(t *testing.T) {
	_, sent := interruptFixture(t, []interruptAgent{
		{"IcyKnoll", "w1:t1", "w1:p1", "claude", "IcyKnoll opus[frontend,api]"},
		{"BlueLake", "w1:t1", "w1:p2", "codex", "BlueLake[backend]"},
		{"RedFox", "w1:t1", "w1:p3", "omp", "RedFox"},
	})
	code, out, _ := runVerb(verbRun("interrupt"), "swarm", "--tag", "FRONTEND", "--json")
	r := decodeInterrupt(t, out)
	if code != exitOK || r.Interrupted != 1 || r.Skipped != 2 || r.Panes[0].PaneID != "w1:p1" {
		t.Fatalf("--tag frontend = %d %s", code, out)
	}
	code, out, _ = runVerb(verbRun("interrupt"), "swarm", "--tag", "nope,backend", "--tag", "api")
	if code != exitOK || out != "Sent Ctrl+C to 2 agent pane(s)\n" {
		t.Fatalf("--tag nope,backend --tag api = %d %q", code, out)
	}
	if got := strings.Join(sent(), " "); got != "w1:p1=ctrl+c w1:p1=ctrl+c w1:p2=ctrl+c" {
		t.Fatalf("keys = %s", got)
	}
}

// TestInterruptJSONIsInterruptResponse: ntm's fields, the pane as its
// index (or N.M when the agents span tabs), and nhm's operation under
// _nhm; a failed pane is ntm's partial failure, exit 1.
func TestInterruptJSONIsInterruptResponse(t *testing.T) {
	_, _ = interruptFixture(t, []interruptAgent{
		{"IcyKnoll", "w1:t1", "w1:p1", "claude", ""}, {"BlueLake", "w1:t1", "w1:p2", "codex", ""},
		{"RedFox", "w1:t1", "w1:p3", "opencode", ""},
	}, "w1:p2")
	code, out, errOut := runVerb(verbRun("interrupt"), "swarm", "--json", "--op-id", "op-int-1")
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	for _, k := range []string{"generated_at", "success", "session", "interrupted", "failed", "targeted_panes", "panes", "error", "error_code", "_nhm"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("no %q in %s", k, out)
		}
	}
	r := decodeInterrupt(t, out)
	if code != exitError || errOut != "" || r.Success || r.Session != "swarm" || r.Interrupted != 2 || r.Failed != 1 ||
		r.ErrorCode != "PARTIAL_INTERRUPT" || r.Error != "interrupt partially failed: 2 pane(s) interrupted, 1 failed" {
		t.Fatalf("partial = %d %s %s", code, out, errOut)
	}
	p := r.Panes
	if len(p) != 3 || p[0].Pane != "1" || p[0].Index != 1 || p[0].AgentType != "cc" || p[0].Status != "interrupted" ||
		p[1].AgentType != "cod" || p[1].Status != "failed" || p[1].Error == "" ||
		p[2].AgentType != "oc" || p[2].Status != "interrupted" {
		t.Fatalf("panes = %+v", p)
	}
	if !slices.Equal(r.TargetedPanes, []int{1, 2, 3}) {
		t.Errorf("targeted_panes = %v", r.TargetedPanes)
	}
	if r.NHM.OperationID != "op-int-1" || r.NHM.Status != "partial" || len(r.NHM.Targets) != 3 || r.NHM.Targets[0].MailName != "IcyKnoll" {
		t.Errorf("_nhm = %+v", r.NHM)
	}
	// The text: ntm's count, then its error.
	code, out, errOut = runVerb(verbRun("interrupt"), "swarm")
	if code != exitError || out != "Sent Ctrl+C to 2 agent pane(s)\n" ||
		!strings.HasPrefix(errOut, "Error: interrupt partially failed (2 interrupted, 1 failed): pane 2: ") {
		t.Fatalf("text = %d %q %q", code, out, errOut)
	}
}

// TestInterruptAllFailed: every pane failed is ntm's INTERRUPT_FAILED; the
// pane is N.M once the agents span tabs.
func TestInterruptAllFailed(t *testing.T) {
	_, _ = interruptFixture(t, []interruptAgent{
		{"IcyKnoll", "w1:t1", "w1:p1", "claude", ""}, {"BlueLake", "w1:t2", "w1:p2", "grok", ""},
	}, "w1:p1", "w1:p2")
	code, out, _ := runVerb(verbRun("interrupt"), "swarm", "--json")
	r := decodeInterrupt(t, out)
	if code != exitError || r.ErrorCode != "INTERRUPT_FAILED" || r.Error != "interrupt failed on all 2 targeted pane(s)" ||
		r.Panes[0].Pane != r.NHM.Targets[0].Slot || !strings.Contains(r.Panes[0].Pane, ".") {
		t.Fatalf("all failed = %d %s", code, out)
	}
}

// TestInterruptErrorsAreNTMs: one session argument; a missing session, or
// nhm not the owner, is ntm's error: "Error: …" and exit 1, or under
// --json ntm's failure envelope on one line.
func TestInterruptErrorsAreNTMs(t *testing.T) {
	_, sent := interruptFixture(t, []interruptAgent{{"IcyKnoll", "w1:t1", "w1:p1", "claude", ""}})
	if code, _, errOut := runVerb(verbRun("interrupt")); code != exitError || errOut != "Error: accepts 1 arg(s), received 0\n" {
		t.Fatalf("no session = %d %q", code, errOut)
	}
	code, out, errOut := runVerb(verbRun("interrupt"), "nosuch", "--json")
	if code != exitError || errOut != "" || !strings.HasPrefix(out, `{"success":false,"error":"`) || strings.Count(out, "\n") != 1 {
		t.Fatalf("--json, no such session = %d %q %q", code, out, errOut)
	}
	if code, _, errOut := runVerb(verbRun("interrupt"), "nosuch"); code != exitError || !strings.HasPrefix(errOut, "Error: ") {
		t.Fatalf("no such session = %d %q", code, errOut)
	}
	if code, _, errOut := runVerb(verbRun("interrupt"), "swarm", "--name", "Nobody"); code != exitError || !strings.Contains(errOut, "Nobody has no active binding") {
		t.Fatalf("an unknown name = %d %q", code, errOut)
	}
	if code, _, errOut := runVerb(verbRun("interrupt"), "swarm", "--type", "codex"); code != exitError || !strings.Contains(errOut, "no agent matches the selection") {
		t.Fatalf("a selector that matches none = %d %q", code, errOut)
	}
	if len(sent()) != 0 {
		t.Fatalf("keys went out: %v", sent())
	}
}

// TestInterruptNoAgents: a session without an agent that ntm interrupts
// is no error: ntm sends to none.
func TestInterruptNoAgents(t *testing.T) {
	_, sent := interruptFixture(t, []interruptAgent{{"TealBay", "w1:t1", "w1:p1", "agy", ""}})
	if code, out, errOut := runVerb(verbRun("interrupt"), "swarm"); code != exitOK || out != "Sent Ctrl+C to 0 agent pane(s)\n" {
		t.Fatalf("text = %d %q %q", code, out, errOut)
	}
	code, out, _ := runVerb(verbRun("interrupt"), "swarm", "--json")
	if r := decodeInterrupt(t, out); code != exitOK || !r.Success || r.Interrupted != 0 || r.Panes == nil ||
		strings.Contains(out, "targeted_panes") || !strings.Contains(out, `"panes": []`) {
		t.Fatalf("json = %d %s", code, out)
	}
	if len(sent()) != 0 {
		t.Fatalf("keys went out: %v", sent())
	}
}

// TestInterruptOperationOnce: one operation id sends at most once.
func TestInterruptOperationOnce(t *testing.T) {
	_, sent := interruptFixture(t, []interruptAgent{{"IcyKnoll", "w1:t1", "w1:p1", "claude", ""}})
	if code, out, _ := runVerb(verbRun("interrupt"), "swarm", "--op-id", "op-1"); code != exitOK || out != "Sent Ctrl+C to 1 agent pane(s)\n" {
		t.Fatalf("first = %d %q", code, out)
	}
	code, out, _ := runVerb(verbRun("interrupt"), "swarm", "--op-id", "op-1", "--json")
	r := decodeInterrupt(t, out)
	if code != exitOK || !r.NHM.Existing || r.NHM.Status != "done" || len(r.Panes) != 0 {
		t.Fatalf("repeat = %d %s", code, out)
	}
	if code, out, _ := runVerb(verbRun("interrupt"), "swarm", "--op-id", "op-1"); code != exitOK || !strings.Contains(out, "nothing sent") {
		t.Fatalf("repeat text = %d %q", code, out)
	}
	if got := strings.Join(sent(), " "); got != "w1:p1=ctrl+c" {
		t.Fatalf("keys = %s", got)
	}
}

// TestInterruptNeedsTheOwner: under ACFS's Bash, nhm sends nothing.
func TestInterruptNeedsTheOwner(t *testing.T) {
	s, inst := cliInstance(t)
	_ = inst
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "layouts": []any{}, "tabs": []any{}, "panes": []any{}, "agents": []any{},
			"workspaces": []any{map[string]any{"workspace_id": "w1", "number": 1, "label": "swarm", "focused": true,
				"pane_count": 0, "tab_count": 1, "active_tab_id": "w1:t1", "agent_status": "idle"}},
		}}, nil
	})
	t.Setenv("HERDR_SOCKET_PATH", s.SocketPath)
	code, out, _ := runVerb(verbRun("interrupt"), "swarm", "--json")
	if code != exitError || !strings.Contains(out, "owned by another") {
		t.Fatalf("under the Bash = %d %s", code, out)
	}
}
