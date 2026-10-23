// Taken from ntm (github.com/Dicklesworthstone/ntm)
// internal/assign/matcher_priority_batch_test.go at commit 9d5ed977; see
// LICENSE in this directory and PROVENANCE.md. Local changes: this header;
// the agent import path, with ntm's tmux.Agent* constants written as
// agent.AgentType* constants, as in matcher.go; in
// TestMatcherPriorityBatchDeduplicatesIdentities the local agent is
// "worker", since "agent" now names the package (nhm-y55).

package assign

import (
	"reflect"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/ui/tui/agent"
)

func TestMatcherPriorityBatchKeepsSpecialistCapacity(t *testing.T) {
	for _, strategy := range []Strategy{StrategyQuality, StrategyDependency} {
		t.Run(string(strategy), func(t *testing.T) {
			m := &Matcher{matrix: NewCapabilityMatrix(), config: DefaultMatcherConfig()}
			m.matrix.base[agent.AgentTypeClaudeCode][TaskFeature] = .95
			m.matrix.base[agent.AgentTypeClaudeCode][TaskRefactor] = .9
			m.matrix.base[agent.AgentTypeCodex][TaskFeature] = .8
			m.matrix.base[agent.AgentTypeCodex][TaskRefactor] = .1
			beads := []Bead{
				{ID: "flexible", Priority: 2, TaskType: TaskFeature},
				{ID: "specialist-only", Priority: 3, TaskType: TaskRefactor},
			}
			agents := []Agent{
				{ID: "specialist", AgentType: agent.AgentTypeClaudeCode, Idle: true},
				{ID: "generalist", AgentType: agent.AgentTypeCodex, Idle: true},
			}
			beforeBeads, beforeAgents := append([]Bead(nil), beads...), append([]Agent(nil), agents...)
			got := m.AssignTasks(beads, agents, strategy)
			if len(got) != 2 {
				t.Fatalf("got %d assignments, want 2: %+v", len(got), got)
			}
			if got[0].Bead.ID != "flexible" || got[0].Agent.ID != "generalist" || got[1].Agent.ID != "specialist" {
				t.Fatalf("did not repair the greedy choice: %+v", got)
			}
			if !reflect.DeepEqual(beads, beforeBeads) || !reflect.DeepEqual(agents, beforeAgents) {
				t.Fatal("mutated caller inputs")
			}
		})
	}
}

func TestMatcherPriorityBatchOptimizesAcrossAgents(t *testing.T) {
	m := &Matcher{matrix: NewCapabilityMatrix(), config: DefaultMatcherConfig()}
	m.matrix.base[agent.AgentTypeClaudeCode][TaskFeature] = .9
	m.matrix.base[agent.AgentTypeClaudeCode][TaskRefactor] = .85
	m.matrix.base[agent.AgentTypeCodex][TaskFeature] = .8
	m.matrix.base[agent.AgentTypeCodex][TaskRefactor] = .3
	got := m.AssignTasks([]Bead{
		{ID: "one", Priority: 2, TaskType: TaskFeature},
		{ID: "two", Priority: 2, TaskType: TaskRefactor},
	}, []Agent{
		{ID: "a", AgentType: agent.AgentTypeClaudeCode, Idle: true},
		{ID: "b", AgentType: agent.AgentTypeCodex, Idle: true},
	}, StrategyQuality)
	if len(got) != 2 || got[0].Agent.ID != "b" || got[1].Agent.ID != "a" {
		t.Fatalf("want total score 1.65, not greedy 1.2: %+v", got)
	}
}

func TestMatcherPriorityBatchPreservesPriorityAndDependencyOrder(t *testing.T) {
	m := &Matcher{matrix: NewCapabilityMatrix(), config: DefaultMatcherConfig()}
	beads := []Bead{
		{ID: "low", Priority: 4, TaskType: TaskFeature},
		{ID: "ordinary", Priority: 2, TaskType: TaskFeature},
		{ID: "blocker", Priority: 2, TaskType: TaskFeature, UnblocksIDs: []string{"x", "y"}},
		{ID: "critical", Priority: 0, TaskType: TaskRefactor},
	}
	agents := []Agent{
		{ID: "a", AgentType: agent.AgentTypeClaudeCode, Idle: true},
		{ID: "b", AgentType: agent.AgentTypeCodex, Idle: true},
	}
	quality := m.AssignTasks(beads, agents, StrategyQuality)
	dependency := m.AssignTasks(beads, agents, StrategyDependency)
	if len(quality) != 2 || quality[0].Bead.ID != "critical" || quality[1].Bead.ID != "ordinary" {
		t.Fatalf("quality discarded priority or input tie order: %+v", quality)
	}
	if len(dependency) != 2 || dependency[0].Bead.ID != "critical" || dependency[1].Bead.ID != "blocker" {
		t.Fatalf("dependency discarded priority or unblock order: %+v", dependency)
	}
	if !strings.Contains(dependency[1].Reason, "unblocks 2 items") {
		t.Fatalf("lost dependency explanation: %s", dependency[1].Reason)
	}
}

func TestMatcherPriorityBatchDeduplicatesIdentities(t *testing.T) {
	m := &Matcher{matrix: NewCapabilityMatrix(), config: DefaultMatcherConfig()}
	bead := Bead{ID: "one", TaskType: TaskFeature}
	worker := Agent{ID: "a", AgentType: agent.AgentTypeClaudeCode, Idle: true}
	for _, strategy := range []Strategy{StrategyQuality, StrategyDependency} {
		got := m.AssignTasks([]Bead{bead, bead}, []Agent{worker, {ID: "b", AgentType: agent.AgentTypeCodex, Idle: true}}, strategy)
		if len(got) != 1 {
			t.Fatalf("%s assigned a duplicate bead: %+v", strategy, got)
		}
		got = m.AssignTasks([]Bead{bead, {ID: "two", TaskType: TaskFeature}}, []Agent{worker, worker}, strategy)
		if len(got) != 1 {
			t.Fatalf("%s reused a worker identity: %+v", strategy, got)
		}
	}
}

func TestMatcherPriorityBatchKeepsConfidenceAndAvailabilityGates(t *testing.T) {
	m := &Matcher{matrix: NewCapabilityMatrix(), config: DefaultMatcherConfig()}
	beads := []Bead{{ID: "one", Priority: 2, TaskType: TaskFeature}, {ID: "two", Priority: 2, TaskType: TaskFeature}}
	agents := []Agent{
		{ID: "busy", AgentType: agent.AgentTypeCodex},
		{ID: "full", AgentType: agent.AgentTypeCodex, Idle: true, ContextUsage: .95},
		{ID: "weak", AgentType: agent.AgentTypeCodex, Idle: true, ContextUsage: .8},
		{ID: "eligible", AgentType: agent.AgentTypeCodex, Idle: true, ContextUsage: .5},
	}
	for _, strategy := range []Strategy{StrategyQuality, StrategyDependency} {
		got := m.AssignTasks(beads, agents, strategy)
		if len(got) != 1 || got[0].Agent.ID != "eligible" || got[0].Score < m.config.MinConfidence {
			t.Fatalf("%s relaxed eligibility to fill the batch: %+v", strategy, got)
		}
	}
}
