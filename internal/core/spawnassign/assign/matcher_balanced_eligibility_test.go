// Taken from ntm (github.com/Dicklesworthstone/ntm)
// internal/assign/matcher_balanced_eligibility_test.go at commit 9d5ed977;
// see LICENSE in this directory and PROVENANCE.md. Local changes: this
// header; the agent import path, with ntm's tmux.Agent* constants written
// as agent.AgentType* constants, as in matcher.go (nhm-y55).

package assign

import (
	"math"
	"reflect"
	"testing"

	"github.com/arosl/nhm/internal/ui/tui/agent"
)

func TestBalancedAssignmentFiltersConfidenceBeforeLoad(t *testing.T) {
	for _, score := range []float64{0.1, math.NaN()} {
		for _, reverse := range []bool{false, true} {
			m := &Matcher{
				matrix: NewCapabilityMatrix(),
				config: DefaultMatcherConfig(),
			}
			m.matrix.base[agent.AgentTypeClaudeCode][TaskFeature] = score
			m.matrix.base[agent.AgentTypeCodex][TaskFeature] = 0.9
			agents := []Agent{
				{ID: "weak", AgentType: agent.AgentTypeClaudeCode, Idle: true},
				{ID: "qualified", AgentType: agent.AgentTypeCodex, Idle: true, Assignments: 5},
			}
			if reverse {
				agents[0], agents[1] = agents[1], agents[0]
			}
			beads := []Bead{
				{ID: "feature-1", TaskType: TaskFeature},
				{ID: "feature-2", TaskType: TaskFeature},
				{ID: "feature-3", TaskType: TaskFeature},
			}
			before := append([]Agent(nil), agents...)
			got := m.AssignTasks(beads, agents, StrategyBalanced)
			if len(got) != len(beads) {
				t.Fatalf("score=%v reverse=%v: got %d assignments, want %d", score, reverse, len(got), len(beads))
			}
			for _, assignment := range got {
				if assignment.Agent.ID != "qualified" || assignment.Score != 0.9 {
					t.Fatalf("ineligible agent selected: %+v", assignment)
				}
			}
			if !reflect.DeepEqual(agents, before) {
				t.Fatal("assignment mutated caller's agents")
			}
		}
	}
}

func TestBalancedAssignmentPreservesFairnessAmongEligibleAgents(t *testing.T) {
	m := &Matcher{matrix: NewCapabilityMatrix(), config: DefaultMatcherConfig()}
	m.matrix.base[agent.AgentTypeClaudeCode][TaskFeature] = m.config.MinConfidence
	m.matrix.base[agent.AgentTypeCodex][TaskFeature] = 0.9
	agents := []Agent{
		{ID: "boundary", AgentType: agent.AgentTypeClaudeCode, Idle: true},
		{ID: "strong", AgentType: agent.AgentTypeCodex, Idle: true, Assignments: 1},
	}
	got := m.AssignTasks([]Bead{
		{ID: "one", TaskType: TaskFeature},
		{ID: "two", TaskType: TaskFeature},
		{ID: "three", TaskType: TaskFeature},
	}, agents, StrategyBalanced)
	var ids []string
	for _, assignment := range got {
		ids = append(ids, assignment.Agent.ID)
	}
	if want := []string{"boundary", "strong", "boundary"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("got %v, want %v (confidence boundary remains eligible)", ids, want)
	}
}

func TestBalancedAssignmentStillRejectsUnavailableAndLowConfidenceAgents(t *testing.T) {
	m := &Matcher{matrix: NewCapabilityMatrix(), config: DefaultMatcherConfig()}
	agents := []Agent{
		{ID: "busy", AgentType: agent.AgentTypeCodex, Idle: false},
		{ID: "full", AgentType: agent.AgentTypeCodex, Idle: true, ContextUsage: 0.95},
		{ID: "low-score", AgentType: agent.AgentTypeCodex, Idle: true, ContextUsage: 0.8},
	}
	got := m.AssignTasks([]Bead{{ID: "one", TaskType: TaskFeature}}, agents, StrategyBalanced)
	if len(got) != 0 {
		t.Fatalf("expected no eligible agents, got %+v", got)
	}
}
