package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/robot"
)

// decodeSpawnResponse decodes spawn's JSON, ntm's: no nhm envelope, and
// ntm's generated_at.
func decodeSpawnResponse(t *testing.T, out string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if _, has := m["schema"]; has || m["generated_at"] == nil {
		t.Fatalf("not ntm's response: %v", m)
	}
	return m
}

// spawnNHMResult is nhm's spawn result under spawn's "_nhm".
func spawnNHMResult(t *testing.T, out string) map[string]any {
	t.Helper()
	nhm, _ := decodeSpawnResponse(t, out)["_nhm"].(map[string]any)
	res, _ := nhm["result"].(map[string]any)
	if res == nil {
		t.Fatalf("no _nhm.result: %s", out)
	}
	return res
}

// twoAgents is a spawn of a claude and a codex in a new workspace, the
// prompts staggered 30s apart.
func twoAgents() spawn.Result {
	return spawn.Result{Session: "swarm", WorkspaceID: "w1", WorkspaceNew: true, ProjectRoot: "/p", OperationID: "op-1",
		Admission: spawn.Admission{Decision: spawn.Admit, Reasons: []string{}, Warnings: []string{}},
		Targets: []spawn.Target{
			{Index: 1, Kind: "claude", Model: "opus", Slot: "2.1", MailName: "GreenPike", HerdrName: "greenpike", TabID: "w1:t2",
				PaneID: "w1:p2", Status: spawn.StatusKickoffSubmitted, Ready: true, KickoffOp: "k1"},
			{Index: 2, Kind: "codex", Slot: "2.2", MailName: "RedStone", HerdrName: "redstone", TabID: "w1:t2",
				PaneID: "w1:p3", Status: spawn.StatusKickoffSubmitted, Ready: true, KickoffOp: "k2", DelayMS: 30000},
		},
		Stagger: &robot.SpawnStaggerPlan{Mode: "fixed", IntervalMs: 30000}}
}

func keys(m map[string]any) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// spawn --json is ntm's SpawnResponse: the session's panes after the spawn
// (a pane with no agent of nhm's is "user"), the counts of their types,
// the stagger, the Agent Mail names; nhm's result, scope and warnings
// under _nhm (nhm-2p6.4.8).
func TestSpawnJSONIsSpawnResponse(t *testing.T) {
	res := twoAgents()
	snap := herdr.SessionSnapshot{Panes: []herdr.PaneInfo{
		{PaneID: "w1:p3", TabID: "w1:t2", WorkspaceID: "w1", Label: "redstone"},
		{PaneID: "w1:p2", TabID: "w1:t2", WorkspaceID: "w1", Label: "greenpike"},
		{PaneID: "w1:p1", TabID: "w1:t1", WorkspaceID: "w1", Title: "zsh", Focused: true},
		{PaneID: "w1:p4", TabID: "w1:t1", WorkspaceID: "w1", Title: "codex", Agent: "codex"},
		{PaneID: "w2:p9", TabID: "w2:t9", WorkspaceID: "w2", Label: "other"},
	}}
	bs := []bindings.Binding{
		{PaneID: "w1:p2", TabID: "w1:t2", Slot: "2.1", Kind: "claude", Model: "opus"},
		{PaneID: "w1:p3", TabID: "w1:t2", Slot: "2.2", Kind: "codex"},
	}
	scope := core.Scope{InstanceID: "i1", ProjectRoot: "/p"}
	m := asMap(t, spawnResponseOf(spawnOut{res: res, scope: &scope, panes: panesOf(snap, bs, res), warnings: []string{}}))
	if got := strings.Join(keys(m), ","); got != "_nhm,agent_counts,agent_mail,created,generated_at,panes,session,stagger,working_directory" {
		t.Fatalf("keys = %s", got)
	}
	if m["session"] != "swarm" || m["created"] != true || m["working_directory"] != "/p" {
		t.Fatalf("response = %v", m)
	}
	var panes []string
	for _, p := range m["panes"].([]any) {
		p := p.(map[string]any)
		panes = append(panes, fmt.Sprintf("%v %v %v.%v %v %v %v %v %v", p["pane_id"], p["pane_target"], p["window_index"], p["index"],
			p["title"], p["type"], p["variant"], p["active"], p["prompt_delay_ms"]))
	}
	// A pane with an agent nhm did not start has herdr's agent's type.
	if got := strings.Join(panes, "; "); got != "w1:p1 w1:p1 0.0 zsh user <nil> true <nil>; w1:p4 w1:p4 0.0 codex codex <nil> <nil> <nil>; "+
		"w1:p2 2.1 2.1 greenpike claude opus <nil> <nil>; w1:p3 2.2 2.2 redstone codex <nil> <nil> 30000" {
		t.Fatalf("panes = %s", got)
	}
	counts := m["agent_counts"].(map[string]any)
	if counts["claude"] != 1.0 || counts["codex"] != 2.0 || counts["user"] != 1.0 || counts["total"] != 4.0 ||
		counts["gemini"] != 0.0 || counts["other"] != nil {
		t.Fatalf("agent_counts = %v", counts)
	}
	if s := m["stagger"].(map[string]any); s["enabled"] != true || s["mode"] != "fixed" || s["interval_ms"] != 30000.0 {
		t.Fatalf("stagger = %v", s)
	}
	am := m["agent_mail"].(map[string]any)
	if am["available"] != true || am["project_registered"] != true || am["agents_registered"] != 2.0 ||
		am["agents_failed"] != 0.0 || am["agent_map"].(map[string]any)["w1:p3"] != "RedStone" {
		t.Fatalf("agent_mail = %v", am)
	}
	nhm := m["_nhm"].(map[string]any)
	if nhm["operation_id"] != "op-1" || nhm["scope"].(map[string]any)["instance_id"] != "i1" ||
		len(nhm["result"].(map[string]any)["agents"].([]any)) != 2 || nhm["warnings"] == nil {
		t.Fatalf("_nhm = %v", nhm)
	}

	// The panes could not be read: the spawn's own agents.
	m = asMap(t, spawnResponseOf(spawnOut{res: res}))
	if panes := m["panes"].([]any); len(panes) != 2 || panes[1].(map[string]any)["pane_target"] != "2.2" ||
		m["agent_counts"].(map[string]any)["total"] != 2.0 {
		t.Fatalf("without the snapshot = %v", m)
	}
	// No stagger: no "stagger".
	res.Stagger = &robot.SpawnStaggerPlan{Mode: "none"}
	if m = asMap(t, spawnResponseOf(spawnOut{res: res})); m["stagger"] != nil {
		t.Fatalf("stagger none = %v", m["stagger"])
	}
	// A dry run created nothing.
	m = asMap(t, spawnResponseOf(spawnOut{res: spawn.Result{Session: "swarm", DryRun: true, Targets: res.Targets}}))
	if m["created"] != false || len(m["panes"].([]any)) != 0 || m["agent_mail"] != nil {
		t.Fatalf("dry run = %v", m)
	}
}

// The text is ntm's step lines, with the prompts' schedule inside the
// launch step as ntm prints it; nhm's agent lines go to stderr; an error
// is ntm's "Error: <msg>" and exit 1 (nhm-2p6.4.8).
func TestSpawnText(t *testing.T) {
	var w bytes.Buffer
	printSpawnSteps(&w, twoAgents())
	want := "  Creating session 'swarm'... [OK]\n" +
		"  Creating 2 pane(s)... [OK]\n" +
		"  Launching 2 agent(s)...   → Agent 1 prompt scheduled in 0s\n" +
		"  → Agent 2 prompt scheduled in 30s\n" +
		"[OK]\n" +
		"⏳ Waiting for staggered prompts (max 30s)...\n" +
		"✓ All staggered prompts delivered\n"
	if w.String() != want {
		t.Fatalf("steps =\n%s\nwant\n%s", w.String(), want)
	}

	// An existing session, no stagger, an agent that did not get ready.
	res := twoAgents()
	res.WorkspaceNew, res.Stagger = false, nil
	res.Targets[1].Ready, res.Targets[1].Status, res.Targets[1].Error = false, spawn.StatusUnresolved, "not ready after 60s"
	var stdout, stderr bytes.Buffer
	code := writeSpawnNTM(&stdout, &stderr, false, spawnOut{res: res}, spawnOutcomeError(res))
	if code != exitError || stdout.String() != "  Creating 2 pane(s)... [OK]\n  Launching 2 agent(s)... [FAIL]\n" {
		t.Fatalf("not ready = %d\n%s", code, stdout.String())
	}
	if !strings.Contains(stderr.String(), "RedStone (codex, default model) slot 2.2 pane w1:p3 unresolved: not ready after 60s") ||
		!strings.HasSuffix(stderr.String(), "Error: 1/2 agent(s) ready: codex agent w1:p3: not ready after 60s\n") {
		t.Fatalf("stderr = %s", stderr.String())
	}

	// An error before anything was made is the error alone; a new
	// workspace that was only planned is not made.
	stdout.Reset()
	stderr.Reset()
	refused := spawn.Result{Session: "swarm", WorkspaceNew: true, Targets: []spawn.Target{{Kind: "claude", Slot: "1.1", Status: spawn.StatusPlanned}}}
	if code := writeSpawnNTM(&stdout, &stderr, false, spawnOut{res: refused}, fmt.Errorf("%w: no guard", spawn.ErrRefused)); code != exitError ||
		stdout.Len() != 0 || stderr.String() != "Error: admission refused the spawn: no guard\n" {
		t.Fatalf("refused = %d %q %q", code, stdout.String(), stderr.String())
	}

	// A success to a buffer: no footer, which is for a terminal.
	stdout.Reset()
	stderr.Reset()
	if code := writeSpawnNTM(&stdout, &stderr, false, spawnOut{res: twoAgents()}, nil); code != exitOK ||
		strings.Contains(stdout.String(), "What's next?") || !strings.Contains(stderr.String(), "GreenPike (claude, opus) slot 2.1") {
		t.Fatalf("success = %d\n%s\n%s", code, stdout.String(), stderr.String())
	}
}

// A failure under --json is ntm's agentLifecycleFailureResponse, with
// ntm's codes, and exits 1 (nhm-2p6.4.8).
func TestSpawnFailureResponse(t *testing.T) {
	refused := spawn.Result{Session: "swarm", WorkspaceNew: true, Admission: spawn.Admission{Decision: spawn.Refuse, Reason: "claude guard dcg is missing",
		Reasons: []string{"claude guard dcg is missing"}, Warnings: []string{}},
		Targets: []spawn.Target{{Kind: "claude", Slot: "1.1", Status: spawn.StatusPlanned}}}
	var stdout, stderr bytes.Buffer
	code := writeSpawnNTM(&stdout, &stderr, true, spawnOut{res: refused}, fmt.Errorf("%w: claude guard dcg is missing", spawn.ErrRefused))
	m := decodeSpawnResponse(t, stdout.String())
	if got := strings.Join(keys(m), ","); got != "_nhm,admission,affected_pane_ids,code,error,error_code,generated_at,"+
		"partial_mutation,session,session_may_exist,success" {
		t.Fatalf("keys = %s", got)
	}
	ad := m["admission"].(map[string]any)
	if code != exitError || stderr.Len() != 0 || m["success"] != false || m["error_code"] != "RESOURCE_BUSY" ||
		m["code"] != "RESOURCE_BUSY" || m["partial_mutation"] != false || m["session_may_exist"] != false ||
		len(m["affected_pane_ids"].([]any)) != 0 || ad["decision"] != "refuse" || ad["requested_agents"] != 1.0 ||
		m["error"] != "admission refused the spawn: claude guard dcg is missing" {
		t.Fatalf("refused = %d %v", code, m)
	}

	res := twoAgents()
	res.Targets[1].Ready, res.Targets[1].Status = false, spawn.StatusUnresolved
	for _, c := range []struct {
		err  error
		code string
	}{
		{spawnOutcomeError(res), "TIMEOUT"},
		{fmt.Errorf("spawn: %w", context.DeadlineExceeded), "TIMEOUT"},
		{errors.New("herdr: connection refused"), "INTERNAL_ERROR"},
	} {
		f := asMap(t, spawnFailureOf(spawnOut{res: res, warnings: []string{}}, c.err))
		if f["error_code"] != c.code || f["partial_mutation"] != true || f["session_may_exist"] != true || f["admission"] != nil ||
			fmt.Sprint(f["affected_pane_ids"]) != "[w1:p2 w1:p3]" || f["_nhm"].(map[string]any)["operation_id"] != "op-1" {
			t.Errorf("%v: %v", c.err, f)
		}
	}
	// An agent that failed to start is no timeout.
	res.Targets[1].Status, res.Targets[1].Error = spawn.StatusFailed, "agent.start: pane busy"
	if f := spawnFailureOf(spawnOut{res: res}, spawnOutcomeError(res)); f.ErrorCode != "INTERNAL_ERROR" {
		t.Fatalf("failed start = %s", f.ErrorCode)
	}
}
