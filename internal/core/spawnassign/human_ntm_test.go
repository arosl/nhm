package spawnassign

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/integrations/bv"
	"github.com/arosl/nhm/internal/robot"
)

func humanRec(id, status string, prio int) bv.TriageRecommendation {
	return bv.TriageRecommendation{ID: id, Title: "Fix " + id, Type: "task", Status: status, Priority: prio, Score: 0.5}
}

func humanTriage(recs ...bv.TriageRecommendation) *bv.TriageResponse {
	return &bv.TriageResponse{Triage: bv.TriageData{Recommendations: recs}}
}

func humanAgents(n int) []SpawnedAgent {
	out := make([]SpawnedAgent, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, SpawnedAgent{Pane: fmt.Sprintf("0.%d", i), PaneID: fmt.Sprintf("w1:p%d", i), Type: "claude"})
	}
	return out
}

func skipReasons(s HumanSelection) map[string]string {
	out := map[string]string{}
	for _, k := range s.Skipped {
		out[k.BeadID] = k.Reason
	}
	return out
}

// ntm's human classifier leaves out what is not assignable, each bead with
// its reason, and keeps the open ones: blocked, an epic, an operator-gated
// label, in progress, closed, a status ntm does not know, and a bead with
// an active assignment.
func TestSelectHumanClassifies(t *testing.T) {
	blocked := humanRec("b-1", "open", 1)
	blocked.BlockedBy = []string{"b-0"}
	epic := humanRec("b-2", "open", 1)
	epic.Type = "epic"
	gated := humanRec("b-3", "open", 1)
	gated.Labels = []string{"needs-operator"}
	sel := SelectHuman(humanTriage(blocked, epic, gated, humanRec("b-4", "in-progress", 1), humanRec("b-5", "closed", 1),
		humanRec("b-6", "", 1), humanRec("b-7", "open", 1), humanRec("b-8", "Ready", 1)), humanAgents(2),
		HumanOptions{Session: "s", Strategy: "round-robin", Active: map[string]struct{}{"b-7": {}},
			OperatorGated: func(l string) bool { return l == "needs-operator" }})
	want := map[string]string{"b-1": "blocked_by_dependency", "b-2": "container_issue_type", "b-3": "operator_gated",
		"b-4": "already_in_progress", "b-5": "closed_status", "b-6": "not_open_status", "b-7": "already_assigned"}
	got := skipReasons(sel)
	for id, reason := range want {
		if got[id] != reason {
			t.Errorf("%s: reason %q, want %q", id, got[id], reason)
		}
	}
	if len(sel.Items) != 1 || sel.Items[0].ID != "b-8" || sel.Actionable != 1 {
		t.Fatalf("items = %+v, actionable %d", sel.Items, sel.Actionable)
	}
}

// A bead in a valid dependency cycle is left out; a cycle that does not
// validate (one node, a node twice) is ignored. The ready beads are cut to
// ntm's 50.
func TestSelectHumanCyclesAndCut(t *testing.T) {
	sel := SelectHuman(humanTriage(humanRec("c-1", "open", 1), humanRec("c-2", "open", 1), humanRec("c-3", "open", 1)),
		humanAgents(3), HumanOptions{Session: "s", Strategy: "round-robin",
			Cycles: [][]string{{"c-1", "c-2"}, {"c-3"}, {"c-3", "x", "c-3"}}})
	got := skipReasons(sel)
	if got["c-1"] != HumanSkipCycle || got["c-2"] != HumanSkipCycle || got["c-3"] != "" {
		t.Fatalf("skipped = %v", got)
	}
	if len(sel.Items) != 1 || sel.Items[0].ID != "c-3" {
		t.Fatalf("items = %+v", sel.Items)
	}

	var recs []bv.TriageRecommendation
	for i := 0; i < 60; i++ {
		recs = append(recs, humanRec(fmt.Sprintf("n-%02d", i), "open", 2))
	}
	sel = SelectHuman(humanTriage(recs...), humanAgents(60), HumanOptions{Session: "s", Strategy: "round-robin"})
	if len(sel.Items) != HumanReadyBeadLimit {
		t.Fatalf("%d items from 60 ready beads, want the cut to %d", len(sel.Items), HumanReadyBeadLimit)
	}
}

// The default plans with PlanAllocations: each agent gets one bead, the
// agents with a bead come first, and --limit leaves the rest out as
// limit_reached.
func TestSelectHumanBalanced(t *testing.T) {
	agents := humanAgents(2)
	sel := SelectHuman(humanTriage(humanRec("d-1", "open", 0), humanRec("d-2", "open", 1), humanRec("d-3", "open", 2)),
		agents, HumanOptions{Session: "s", Strategy: ""})
	if len(sel.Items) != 2 || len(sel.Agents) != 2 || sel.Items[0].ID == sel.Items[1].ID {
		t.Fatalf("balanced = %+v", sel)
	}
	for _, it := range sel.Items {
		if it.MatchReason == "" || it.Type == "" {
			t.Fatalf("item without ntm's reason or type: %+v", it)
		}
	}
	sel = SelectHuman(humanTriage(humanRec("d-1", "open", 0), humanRec("d-2", "open", 1)), agents,
		HumanOptions{Session: "s", Strategy: "balanced", Limit: 1})
	if len(sel.Items) != 1 || len(sel.Agents) != 2 || len(skipReasons(sel)) != 1 {
		t.Fatalf("limit 1 = %+v", sel)
	}
	for _, k := range sel.Skipped {
		if k.Reason != HumanSkipLimit {
			t.Fatalf("skipped = %+v", sel.Skipped)
		}
	}
}

// The legacy strategies give an agent several beads (round-robin spreads
// four beads over two agents); ntm's coordinator refuses the second for a
// target that has work, so it fails before its claim. "rr" is not
// round-robin on ntm's spawn path: it takes the legacy balanced default,
// which says so in its reasoning.
func TestSelectHumanOneBeadPerAgent(t *testing.T) {
	recs := []bv.TriageRecommendation{humanRec("e-1", "open", 1), humanRec("e-2", "open", 1),
		humanRec("e-3", "open", 1), humanRec("e-4", "open", 1)}
	now := func() time.Time { return time.Unix(0, 0) }
	sel := SelectHuman(humanTriage(recs...), humanAgents(2), HumanOptions{Session: "s", Strategy: "round-robin", Now: now})
	if len(sel.Items) != 2 || len(sel.Failures) != 2 || !strings.HasPrefix(sel.Items[0].MatchReason, "round-robin slot") {
		t.Fatalf("round-robin = items %+v failures %+v", sel.Items, sel.Failures)
	}
	for _, f := range sel.Failures {
		if f.Error != humanTargetOccupied || f.Pane == "" {
			t.Fatalf("failure = %+v", f)
		}
	}
	// ntm runs its assignments in plan order, and each item carries ntm's
	// work prompt.
	if !slices.Equal(sel.Planned, []string{"e-1", "e-2", "e-3", "e-4"}) {
		t.Fatalf("planned = %v", sel.Planned)
	}
	if want := "Work on bead e-1: Fix e-1. Check dependencies first with `br dep tree e-1`."; sel.Items[0].Prompt != want {
		t.Fatalf("prompt = %q", sel.Items[0].Prompt)
	}
	sel = SelectHuman(humanTriage(recs...), humanAgents(2), HumanOptions{Session: "s", Strategy: "rr", Now: now})
	if len(sel.Items) != 2 || len(sel.Failures) != 2 || !strings.Contains(sel.Items[0].MatchReason, "balanced workload") {
		t.Fatalf("rr = items %+v failures %+v", sel.Items, sel.Failures)
	}
}

// ntm's resolveSpawnAssignAgentType and ResolveAgentType: --assign-agent
// wins and takes ntm's long name, or stays as given in lower case if ntm
// does not know it; else the first alias; else every agent.
func TestResolveSpawnAssignAgentType(t *testing.T) {
	for _, c := range []struct {
		flag              string
		cc, cod, gmi, agy bool
		want              string
	}{
		{"", false, false, false, false, ""},
		{"cc", false, true, false, false, "claude"},
		{"claude", false, false, false, false, "claude"},
		{"cod", false, false, false, false, "codex"},
		{"", true, true, false, false, "claude"},
		{"", false, false, true, false, "gemini"},
		{"", false, false, false, true, "antigravity"},
		{" Pi ", false, false, false, false, "pi"},
		{"opencode", false, false, false, false, "oc"},
	} {
		if got := ResolveSpawnAssignAgentType(c.flag, c.cc, c.cod, c.gmi, c.agy); got != c.want {
			t.Errorf("%+v: %q", c, got)
		}
	}
}

// ntm's printAssignmentPromptContext, as lines: CASS's items or its skip,
// then CM's rules or its skip; nothing without enrichment.
func TestHumanPromptContextLines(t *testing.T) {
	got := HumanPromptContextLines("nhm-1", &robot.CASSInjectionInfo{ItemsInjected: 2, TokensAdded: 300},
		&robot.CMInjectionInfo{SkippedReason: "cm not installed"})
	want := []string{"  CASS context injected for nhm-1: 2 item(s), ~300 tokens", "  CM rule injection skipped for nhm-1: cm not installed"}
	if !slices.Equal(got, want) {
		t.Fatalf("lines = %q", got)
	}
	if got := HumanPromptContextLines("nhm-1", nil, nil); len(got) != 0 {
		t.Fatalf("no enrichment = %q", got)
	}
}

// The agents are served in pane order, numerically, as ntm lists the
// panes: round-robin gives the first bead to 0.2, not to 0.10.
func TestSelectHumanPaneOrder(t *testing.T) {
	agents := []SpawnedAgent{{Pane: "0.10", Type: "claude"}, {Pane: "0.2", Type: "codex"}}
	sel := SelectHuman(humanTriage(humanRec("f-1", "open", 1), humanRec("f-2", "open", 1)), agents,
		HumanOptions{Session: "s", Strategy: "round-robin"})
	if len(sel.Items) != 2 || sel.Agents[0].Pane != "0.2" || sel.Items[0].ID != "f-1" || sel.Agents[1].Pane != "0.10" {
		t.Fatalf("agents %+v items %+v", sel.Agents, sel.Items)
	}
	if agents[0].Pane != "0.10" {
		t.Fatal("SelectHuman sorted the caller's slice")
	}
}
