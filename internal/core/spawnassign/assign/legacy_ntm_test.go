// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/cli/assign_test.go, coverage_boost_test.go, cli_helpers_test.go,
// cli_helpers_extra_test.go, activity_test.go, omp_test.go and
// helpers_batch3_test.go (in part); see LICENSE in this directory and
// PROVENANCE.md. Local changes: this header; only the tests of the
// functions in legacy_ntm.go are kept. They call LegacyAssignments where
// ntm calls generateAssignmentsLegacy, or generateAssignmentsEnhanced with
// a strategy that is not balanced (the same code), and LegacyAgentType on
// the pane's type where ntm calls detectAgentTypeFromPane on the pane.
// makeTestAgent builds a LegacyAgent with the key and target that ntm
// derives from a pane with only an index ("0.<index>"); Pane is
// Agent.Index. The checks of the agent name, status, prompt flag and time
// are left out, as the caller fills them; the multi-window test checks the
// key and target that ntm's name is built from. A note in
// TestCalculateMatchConfidence_TestingTask gives nhm's task type. These
// tests are nhm's: TestUsesAllocationPlanner,
// TestLegacyAssignmentsUnknownStrategyIsBalanced,
// TestCalculateMatchConfidenceFixedPatternOrder,
// TestLegacyBalancedStartsFromLoad, TestLegacyBalancedLastAssignedFromLoad
// and TestLegacyBalancedLeastRecentUsesEachPickTime.

package assign

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/integrations/bv"
	"github.com/arosl/nhm/internal/ui/tui/agent"
)

// legacyAssign runs the legacy strategies with no load and the wall clock.
func legacyAssign(strategy string, agents []LegacyAgent, beads []bv.BeadPreview) []LegacyAssignment {
	return LegacyAssignments(strategy, agents, beads, LegacyLoad{}, time.Now)
}

func TestParsePriorityString(t *testing.T) {

	tests := []struct {
		name     string
		input    string
		expected int
	}{
		// Valid priorities
		{name: "P0 critical", input: "P0", expected: 0},
		{name: "P1 high", input: "P1", expected: 1},
		{name: "P2 medium", input: "P2", expected: 2},
		{name: "P3 low", input: "P3", expected: 3},
		{name: "P4 backlog", input: "P4", expected: 4},

		// Invalid - returns default (2)
		{name: "P5 out of range", input: "P5", expected: 2},
		{name: "P9 out of range", input: "P9", expected: 2},
		{name: "empty string", input: "", expected: 2},
		{name: "just P", input: "P", expected: 2},
		{name: "lowercase p0", input: "p0", expected: 2},
		{name: "no P prefix", input: "0", expected: 2},
		{name: "too long", input: "P01", expected: 2},
		{name: "word priority", input: "high", expected: 2},
		{name: "negative-like", input: "P-1", expected: 2},
		{name: "spaces", input: " P1", expected: 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := ParsePriorityString(tc.input)
			if result != tc.expected {
				t.Errorf("ParsePriorityString(%q) = %d; want %d", tc.input, result, tc.expected)
			}
		})
	}
}

func TestGenerateAssignmentsLegacyPreservesMultiWindowPaneIdentity(t *testing.T) {
	agents := []LegacyAgent{
		{Key: "%50", Target: "0.1", Index: 1, AgentType: "codex"},
		{Key: "%51", Target: "1.1", Index: 1, AgentType: "claude"},
	}
	beads := []bv.BeadPreview{{ID: "ntm-a", Title: "First"}, {ID: "ntm-b", Title: "Second"}}
	items := legacyAssign("speed", agents, beads)
	if len(items) != 2 {
		t.Fatalf("assignments=%d, want 2", len(items))
	}
	if items[0].Agent.Target != "0.1" || items[0].Agent.Key != "%50" || items[1].Agent.Target != "1.1" || items[1].Agent.Key != "%51" {
		t.Fatalf("multi-window assignments lost physical identity: %+v", items)
	}
	if items[0].Agent.Key == items[1].Agent.Key {
		t.Fatalf("multi-window agents share identity %q", items[0].Agent.Key)
	}
}

// =============================================================================
// calculateMatchConfidence tests
// =============================================================================

func TestCalculateMatchConfidence_ClaudeAnalysis(t *testing.T) {
	bead := bv.BeadPreview{ID: "b1", Title: "Analyze performance bottleneck", Priority: "P1"}
	got := calculateMatchConfidence("claude", bead, "balanced")
	if got < 0.8 {
		t.Errorf("claude+analysis confidence = %.2f, want >= 0.8", got)
	}
}

func TestCalculateMatchConfidence_CodexFeature(t *testing.T) {
	bead := bv.BeadPreview{ID: "b2", Title: "Implement user login feature", Priority: "P1"}
	got := calculateMatchConfidence("codex", bead, "balanced")
	if got < 0.8 {
		t.Errorf("codex+feature confidence = %.2f, want >= 0.8", got)
	}
}

func TestCalculateMatchConfidence_GeminiDocs(t *testing.T) {
	bead := bv.BeadPreview{ID: "b3", Title: "Update documentation for API", Priority: "P2"}
	got := calculateMatchConfidence("gemini", bead, "balanced")
	if got < 0.8 {
		t.Errorf("gemini+docs confidence = %.2f, want >= 0.8", got)
	}
}

func TestCalculateMatchConfidence_SpeedStrategy(t *testing.T) {
	bead := bv.BeadPreview{ID: "b4", Title: "Generic task", Priority: "P2"}
	got := calculateMatchConfidence("claude", bead, "speed")
	if got < 0.7 {
		t.Errorf("speed strategy confidence = %.2f, want >= 0.7", got)
	}
}

func TestCalculateMatchConfidence_DependencyHighPriority(t *testing.T) {
	bead := bv.BeadPreview{ID: "b5", Title: "Generic task", Priority: "P0"}
	got := calculateMatchConfidence("claude", bead, "dependency")
	if got < 0.7 {
		t.Errorf("dependency+P0 confidence = %.2f, want >= 0.7", got)
	}
}

func TestCalculateMatchConfidence_UnknownAgent(t *testing.T) {
	bead := bv.BeadPreview{ID: "b6", Title: "Some task", Priority: "P2"}
	got := calculateMatchConfidence("unknown_agent", bead, "balanced")
	if got < 0.5 || got > 0.8 {
		t.Errorf("unknown agent confidence = %.2f, want ~0.7 (base)", got)
	}
}

func TestCalculateMatchConfidence_BugTask(t *testing.T) {
	bead := bv.BeadPreview{ID: "b7", Title: "Fix broken login", Priority: "P1"}
	got := calculateMatchConfidence("codex", bead, "balanced")
	if got < 0.7 {
		t.Errorf("codex+bug confidence = %.2f, want >= 0.7", got)
	}
}

func TestCalculateMatchConfidence_TestingTask(t *testing.T) {
	bead := bv.BeadPreview{ID: "b8", Title: "Add test coverage", Priority: "P2"}
	got := calculateMatchConfidence("claude", bead, "balanced")
	// "test" → testing, claude has no specific testing strength → base
	// (nhm: "add" makes it feature, 0.8; see
	// TestCalculateMatchConfidenceFixedPatternOrder)
	if got < 0.5 {
		t.Errorf("claude+testing confidence = %.2f, want >= 0.5", got)
	}
}

// =============================================================================
// buildReasoning tests
// =============================================================================

func TestBuildReasoning_ClaudeRefactor(t *testing.T) {
	bead := bv.BeadPreview{ID: "r1", Title: "Refactor authentication module", Priority: "P0"}
	got := buildReasoning("claude", bead, "balanced")
	if !strings.Contains(got, "Claude excels") {
		t.Errorf("buildReasoning(claude+refactor) = %q, want Claude excels mention", got)
	}
	if !strings.Contains(got, "critical priority") {
		t.Errorf("buildReasoning(P0) = %q, want critical priority", got)
	}
}

func TestBuildReasoning_CodexImplement(t *testing.T) {
	bead := bv.BeadPreview{ID: "r2", Title: "Implement new feature", Priority: "P1"}
	got := buildReasoning("codex", bead, "speed")
	if !strings.Contains(got, "Codex excels") {
		t.Errorf("buildReasoning(codex+implement) = %q, want Codex excels", got)
	}
	if !strings.Contains(got, "speed") {
		t.Errorf("buildReasoning(speed) = %q, want speed mention", got)
	}
}

func TestBuildReasoning_GeminiDoc(t *testing.T) {
	bead := bv.BeadPreview{ID: "r3", Title: "Update docs", Priority: "P2"}
	got := buildReasoning("gemini", bead, "quality")
	if !strings.Contains(got, "Gemini excels") {
		t.Errorf("buildReasoning(gemini+doc) = %q, want Gemini excels", got)
	}
}

func TestBuildReasoning_NoMatch(t *testing.T) {
	bead := bv.BeadPreview{ID: "r4", Title: "Generic task", Priority: "P3"}
	got := buildReasoning("unknown", bead, "round_robin")
	if got != "available agent matched to available work" {
		t.Errorf("buildReasoning(no match) = %q", got)
	}
}

func TestBuildReasoning_DependencyStrategy(t *testing.T) {
	bead := bv.BeadPreview{ID: "r5", Title: "Generic work", Priority: "P2"}
	got := buildReasoning("claude", bead, "dependency")
	if !strings.Contains(got, "unblocks") {
		t.Errorf("buildReasoning(dependency) = %q, want unblocks mention", got)
	}
}

// =============================================================================
// generateAssignmentsEnhanced tests — the legacy strategy branches
// =============================================================================

// helpers to build test agents/beads
func makeTestAgent(paneIndex int, agentType string) LegacyAgent {
	target := "0." + strconv.Itoa(paneIndex)
	return LegacyAgent{
		Key:       target,
		Target:    target,
		Index:     paneIndex,
		AgentType: agentType,
	}
}

func makeTestBead(id, title, priority string) bv.BeadPreview {
	return bv.BeadPreview{ID: id, Title: title, Priority: priority}
}

// --- Round-Robin strategy ---

func TestGenerateAssignmentsEnhanced_RoundRobin_Basic(t *testing.T) {
	agents := []LegacyAgent{
		makeTestAgent(0, "claude"),
		makeTestAgent(1, "codex"),
		makeTestAgent(2, "gemini"),
	}
	beads := []bv.BeadPreview{
		makeTestBead("b1", "Task 1", "P1"),
		makeTestBead("b2", "Task 2", "P1"),
		makeTestBead("b3", "Task 3", "P2"),
		makeTestBead("b4", "Task 4", "P2"),
		makeTestBead("b5", "Task 5", "P3"),
	}
	got := legacyAssign("round-robin", agents, beads)

	if len(got) != 5 {
		t.Fatalf("round-robin: got %d assignments, want 5", len(got))
	}
	// Verify round-robin pattern: b1→agent0, b2→agent1, b3→agent2, b4→agent0, b5→agent1
	expectedPanes := []int{0, 1, 2, 0, 1}
	for i, a := range got {
		if a.Agent.Index != expectedPanes[i] {
			t.Errorf("assignment[%d].Pane = %d, want %d", i, a.Agent.Index, expectedPanes[i])
		}
		if a.Score != 1.0 {
			t.Errorf("assignment[%d].Score = %.2f, want 1.0", i, a.Score)
		}
		if a.BeadID != beads[i].ID {
			t.Errorf("assignment[%d].BeadID = %q, want %q", i, a.BeadID, beads[i].ID)
		}
	}
}

func TestGenerateAssignmentsEnhanced_RoundRobin_NoAgents(t *testing.T) {
	beads := []bv.BeadPreview{makeTestBead("b1", "Task 1", "P1")}
	got := legacyAssign("round-robin", nil, beads)
	if len(got) != 0 {
		t.Errorf("round-robin with no agents: got %d assignments, want 0", len(got))
	}
}

func TestGenerateAssignmentsEnhanced_RoundRobin_NoBeads(t *testing.T) {
	agents := []LegacyAgent{makeTestAgent(0, "claude")}
	got := legacyAssign("round-robin", agents, nil)
	if len(got) != 0 {
		t.Errorf("round-robin with no beads: got %d assignments, want 0", len(got))
	}
}

func TestGenerateAssignmentsEnhanced_RoundRobin_SingleAgent(t *testing.T) {
	agents := []LegacyAgent{makeTestAgent(0, "claude")}
	beads := []bv.BeadPreview{
		makeTestBead("b1", "Task 1", "P1"),
		makeTestBead("b2", "Task 2", "P2"),
		makeTestBead("b3", "Task 3", "P3"),
	}
	got := legacyAssign("round-robin", agents, beads)
	if len(got) != 3 {
		t.Fatalf("single agent round-robin: got %d, want 3", len(got))
	}
	for i, a := range got {
		if a.Agent.Index != 0 {
			t.Errorf("assignment[%d].Pane = %d, want 0", i, a.Agent.Index)
		}
	}
}

// --- Quality strategy ---

func TestGenerateAssignmentsEnhanced_Quality_BestMatch(t *testing.T) {
	agents := []LegacyAgent{
		makeTestAgent(0, "claude"),
		makeTestAgent(1, "codex"),
	}
	beads := []bv.BeadPreview{
		makeTestBead("b1", "Implement new API endpoint", "P1"), // implementation → codex should score well
	}
	got := legacyAssign("quality", agents, beads)
	if len(got) != 1 {
		t.Fatalf("quality: got %d assignments, want 1", len(got))
	}
	// Just verify it picks one agent and sets reasoning
	if got[0].Score <= 0 {
		t.Errorf("quality: score = %.2f, want > 0", got[0].Score)
	}
	if got[0].Reasoning == "" {
		t.Error("quality: reasoning should not be empty")
	}
}

func TestGenerateAssignmentsEnhanced_Quality_MoreBeadsThanAgents(t *testing.T) {
	agents := []LegacyAgent{
		makeTestAgent(0, "claude"),
		makeTestAgent(1, "codex"),
	}
	beads := []bv.BeadPreview{
		makeTestBead("b1", "Analyze code", "P1"),
		makeTestBead("b2", "Implement feature", "P1"),
		makeTestBead("b3", "Write docs", "P2"), // No agent left
	}
	got := legacyAssign("quality", agents, beads)
	// Quality uses usedAgents map — each agent can only be assigned once
	if len(got) != 2 {
		t.Errorf("quality with 3 beads/2 agents: got %d assignments, want 2", len(got))
	}
	// Verify no duplicate pane assignments
	panes := make(map[int]bool)
	for _, a := range got {
		if panes[a.Agent.Index] {
			t.Errorf("quality: duplicate pane assignment %d", a.Agent.Index)
		}
		panes[a.Agent.Index] = true
	}
}

func TestGenerateAssignmentsEnhanced_Quality_NoAgents(t *testing.T) {
	beads := []bv.BeadPreview{makeTestBead("b1", "Task", "P1")}
	got := legacyAssign("quality", nil, beads)
	if len(got) != 0 {
		t.Errorf("quality with no agents: got %d, want 0", len(got))
	}
}

// --- Speed strategy ---

func TestGenerateAssignmentsEnhanced_Speed_FirstAvailable(t *testing.T) {
	agents := []LegacyAgent{
		makeTestAgent(0, "claude"),
		makeTestAgent(1, "codex"),
		makeTestAgent(2, "gemini"),
	}
	beads := []bv.BeadPreview{
		makeTestBead("b1", "Task 1", "P1"),
		makeTestBead("b2", "Task 2", "P2"),
		makeTestBead("b3", "Task 3", "P2"),
	}
	got := legacyAssign("speed", agents, beads)
	if len(got) != 3 {
		t.Fatalf("speed: got %d assignments, want 3", len(got))
	}
	// Speed assigns each bead to first available → b1→agent0, b2→agent1, b3→agent2
	for i, a := range got {
		if a.Agent.Index != i {
			t.Errorf("speed assignment[%d].Pane = %d, want %d", i, a.Agent.Index, i)
		}
	}
	// Speed score is (calculateMatchConfidence + 0.9) / 2 → always > 0.45
	for i, a := range got {
		if a.Score < 0.45 {
			t.Errorf("speed assignment[%d].Score = %.2f, want >= 0.45", i, a.Score)
		}
	}
}

func TestGenerateAssignmentsEnhanced_Speed_MoreBeadsThanAgents(t *testing.T) {
	agents := []LegacyAgent{
		makeTestAgent(0, "claude"),
	}
	beads := []bv.BeadPreview{
		makeTestBead("b1", "Task 1", "P1"),
		makeTestBead("b2", "Task 2", "P1"),
		makeTestBead("b3", "Task 3", "P2"),
	}
	got := legacyAssign("speed", agents, beads)
	// Speed uses usedAgents — only 1 agent available
	if len(got) != 1 {
		t.Errorf("speed with 3 beads/1 agent: got %d, want 1", len(got))
	}
}

// --- Dependency strategy ---

func TestGenerateAssignmentsEnhanced_Dependency_PriorityBoost(t *testing.T) {
	agents := []LegacyAgent{
		makeTestAgent(0, "claude"),
		makeTestAgent(1, "codex"),
	}
	beads := []bv.BeadPreview{
		makeTestBead("b1", "Critical fix", "P0"),
	}
	got := legacyAssign("dependency", agents, beads)
	if len(got) != 1 {
		t.Fatalf("dependency: got %d assignments, want 1", len(got))
	}
	// P0 gets a +0.1 boost (capped at 0.95)
	if got[0].Score < 0.7 {
		t.Errorf("dependency+P0: score = %.2f, want >= 0.7", got[0].Score)
	}
	if !strings.Contains(got[0].Reasoning, "unblocks") {
		t.Errorf("dependency: reasoning = %q, want 'unblocks' mention", got[0].Reasoning)
	}
}

func TestGenerateAssignmentsEnhanced_Dependency_MoreBeadsThanAgents(t *testing.T) {
	agents := []LegacyAgent{
		makeTestAgent(0, "claude"),
	}
	beads := []bv.BeadPreview{
		makeTestBead("b1", "Task 1", "P1"),
		makeTestBead("b2", "Task 2", "P2"),
	}
	got := legacyAssign("dependency", agents, beads)
	// Only 1 agent, so only 1 assignment
	if len(got) != 1 {
		t.Errorf("dependency 2 beads/1 agent: got %d, want 1", len(got))
	}
}

func TestGenerateAssignmentsEnhanced_Dependency_LowPriority(t *testing.T) {
	agents := []LegacyAgent{
		makeTestAgent(0, "claude"),
	}
	beads := []bv.BeadPreview{
		makeTestBead("b1", "Low priority task", "P3"),
	}
	got := legacyAssign("dependency", agents, beads)
	if len(got) != 1 {
		t.Fatalf("dependency P3: got %d, want 1", len(got))
	}
	// P3 → ParsePriorityString returns 3, no boost (priority > 1)
	if got[0].Score > 0.95 {
		t.Errorf("dependency P3: score = %.2f, should not be boosted to max", got[0].Score)
	}
}

// --- Balanced (default) branch of the legacy strategies ---

func TestGenerateAssignmentsEnhanced_DefaultIsBalanced(t *testing.T) {
	agents := []LegacyAgent{
		makeTestAgent(0, "claude"),
		makeTestAgent(1, "codex"),
	}
	beads := []bv.BeadPreview{
		makeTestBead("b1", "Task 1", "P1"),
		makeTestBead("b2", "Task 2", "P2"),
	}
	// Unknown strategy falls through to default (balanced)
	got := legacyAssign("unknown_strategy", agents, beads)
	if len(got) != 2 {
		t.Fatalf("default strategy: got %d, want 2", len(got))
	}
	// Both agents should be used (balanced spread)
	panes := make(map[int]bool)
	for _, a := range got {
		panes[a.Agent.Index] = true
	}
	if len(panes) != 2 {
		t.Errorf("default strategy: used %d agents, want 2", len(panes))
	}
}

// --- Common field verification ---

func TestGenerateAssignmentsEnhanced_CommonFields(t *testing.T) {
	agents := []LegacyAgent{makeTestAgent(5, "claude")}
	beads := []bv.BeadPreview{makeTestBead("bd-abc", "My Task", "P1")}
	got := legacyAssign("round-robin", agents, beads)
	if len(got) != 1 {
		t.Fatalf("common fields: got %d, want 1", len(got))
	}
	a := got[0]
	if a.BeadID != "bd-abc" {
		t.Errorf("BeadID = %q, want 'bd-abc'", a.BeadID)
	}
	if a.BeadTitle != "My Task" {
		t.Errorf("BeadTitle = %q, want 'My Task'", a.BeadTitle)
	}
	if a.Agent.Index != 5 {
		t.Errorf("Pane = %d, want 5", a.Agent.Index)
	}
	if a.Agent.AgentType != "claude" {
		t.Errorf("AgentType = %q, want 'claude'", a.Agent.AgentType)
	}
}

// =============================================================================
// cli_helpers_test.go: calculateMatchConfidence and buildReasoning tables
// =============================================================================

func TestCalculateMatchConfidence(t *testing.T) {

	tests := []struct {
		name      string
		agentType string
		bead      bv.BeadPreview
		strategy  string
		wantMin   float64
		wantMax   float64
	}{
		{
			name:      "claude analysis task",
			agentType: "claude",
			bead:      bv.BeadPreview{Title: "Analyze the codebase for issues"},
			strategy:  "balanced",
			wantMin:   0.85,
			wantMax:   1.0,
		},
		{
			name:      "codex feature task",
			agentType: "codex",
			bead:      bv.BeadPreview{Title: "Implement user authentication feature"},
			strategy:  "balanced",
			wantMin:   0.85,
			wantMax:   1.0,
		},
		{
			name:      "gemini docs task",
			agentType: "gemini",
			bead:      bv.BeadPreview{Title: "Update documentation for API"},
			strategy:  "balanced",
			wantMin:   0.85,
			wantMax:   1.0,
		},
		{
			name:      "unknown agent generic task",
			agentType: "unknown",
			bead:      bv.BeadPreview{Title: "Do some work"},
			strategy:  "balanced",
			wantMin:   0.6,
			wantMax:   0.8,
		},
		{
			name:      "speed strategy raises confidence",
			agentType: "claude",
			bead:      bv.BeadPreview{Title: "Generic task"},
			strategy:  "speed",
			wantMin:   0.75,
			wantMax:   0.95,
		},
		{
			name:      "dependency strategy with P0",
			agentType: "claude",
			bead:      bv.BeadPreview{Title: "Fix critical bug", Priority: "P0"},
			strategy:  "dependency",
			wantMin:   0.75,
			wantMax:   0.95,
		},
		{
			name:      "dependency strategy with P1",
			agentType: "codex",
			bead:      bv.BeadPreview{Title: "Fix high-priority bug", Priority: "P1"},
			strategy:  "dependency",
			wantMin:   0.75,
			wantMax:   0.95,
		},
		{
			name:      "codex bug task",
			agentType: "codex",
			bead:      bv.BeadPreview{Title: "Fix broken login bug"},
			strategy:  "balanced",
			wantMin:   0.75,
			wantMax:   0.85,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := calculateMatchConfidence(tc.agentType, tc.bead, tc.strategy)
			if got < tc.wantMin || got > tc.wantMax {
				t.Errorf("calculateMatchConfidence(%q, %q, %q) = %f; want [%f, %f]",
					tc.agentType, tc.bead.Title, tc.strategy, got, tc.wantMin, tc.wantMax)
			}
		})
	}
}

func TestBuildReasoning(t *testing.T) {

	tests := []struct {
		name      string
		agentType string
		bead      bv.BeadPreview
		strategy  string
		wantSub   string
	}{
		{
			name:      "claude refactor",
			agentType: "claude",
			bead:      bv.BeadPreview{Title: "Refactor the auth module"},
			strategy:  "balanced",
			wantSub:   "Claude excels",
		},
		{
			name:      "codex implement",
			agentType: "codex",
			bead:      bv.BeadPreview{Title: "Implement new feature"},
			strategy:  "balanced",
			wantSub:   "Codex excels",
		},
		{
			name:      "gemini docs",
			agentType: "gemini",
			bead:      bv.BeadPreview{Title: "Update documentation"},
			strategy:  "balanced",
			wantSub:   "Gemini excels",
		},
		{
			name:      "P0 priority",
			agentType: "claude",
			bead:      bv.BeadPreview{Title: "Something", Priority: "P0"},
			strategy:  "balanced",
			wantSub:   "critical priority",
		},
		{
			name:      "P1 priority",
			agentType: "codex",
			bead:      bv.BeadPreview{Title: "Something", Priority: "P1"},
			strategy:  "balanced",
			wantSub:   "high priority",
		},
		{
			name:      "speed strategy",
			agentType: "claude",
			bead:      bv.BeadPreview{Title: "Something"},
			strategy:  "speed",
			wantSub:   "optimizing for speed",
		},
		{
			name:      "quality strategy",
			agentType: "claude",
			bead:      bv.BeadPreview{Title: "Something"},
			strategy:  "quality",
			wantSub:   "optimizing for quality",
		},
		{
			name:      "dependency strategy",
			agentType: "claude",
			bead:      bv.BeadPreview{Title: "Something"},
			strategy:  "dependency",
			wantSub:   "prioritizing unblocks",
		},
		{
			name:      "no special match",
			agentType: "unknown",
			bead:      bv.BeadPreview{Title: "Something", Priority: "P3"},
			strategy:  "",
			wantSub:   "available agent matched",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildReasoning(tc.agentType, tc.bead, tc.strategy)
			if !strings.Contains(got, tc.wantSub) {
				t.Errorf("buildReasoning(%q, %q, %q) = %q; want substring %q",
					tc.agentType, tc.bead.Title, tc.strategy, got, tc.wantSub)
			}
		})
	}
}

// TestOmpAssignmentScoring pins omp's profile on both CLI assignment scorers:
// the capability matrix used by allocation/quality planning and the
// strategy confidence table, so --assign never scores omp as unprofiled.
func TestOmpAssignmentScoring(t *testing.T) {
	if got := GetAgentScoreByString("omp", string(TaskFeature)); got != 0.88 {
		t.Fatalf("matrix omp feature = %.2f, want 0.88", got)
	}
	for title, want := range map[string]float64{
		"Implement the new export endpoint": 0.85,
		"Investigate slow startup":          0.8,
	} {
		bead := bv.BeadPreview{Title: title}
		got := calculateMatchConfidence("omp", bead, "balanced")
		if got != want {
			t.Errorf("calculateMatchConfidence(omp, %q) = %.2f, want %.2f", title, got, want)
		}
		if unprofiled := calculateMatchConfidence("not-a-harness", bead, "balanced"); got == unprofiled {
			t.Errorf("omp scored like an unprofiled agent (%.2f) for %q", got, title)
		}
	}
}

// =============================================================================
// assign.go: inferTaskTypeFromBead
// =============================================================================

func TestInferTaskTypeFromBead(t *testing.T) {

	tests := []struct {
		name  string
		title string
		want  string
	}{
		{"bug keyword", "Fix broken login page", "bug"},
		{"error keyword", "Handle error in API response", "bug"},
		{"crash keyword", "Crash on startup with nil pointer", "bug"},
		{"test keyword", "Add unit tests for config package", "testing"},
		{"coverage keyword", "Improve coverage for cli module", "testing"},
		{"doc keyword", "Update README documentation", "documentation"},
		{"refactor keyword", "Refactor auth middleware", "refactor"},
		{"cleanup keyword", "Cleanup deprecated endpoints", "refactor"},
		{"analyze keyword", "Analyze performance bottlenecks", "analysis"},
		{"feature keyword", "Implement dark mode toggle", "feature"},
		{"add keyword", "Add export functionality", "feature"},
		{"no match defaults to task", "Miscellaneous work item", "task"},
		{"empty title", "", "task"},
		{"case insensitive", "FIX LOGIN BUG", "bug"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bead := bv.BeadPreview{Title: tc.title}
			got := InferTaskTypeFromBead(bead)
			if got != tc.want {
				t.Errorf("InferTaskTypeFromBead(%q) = %q, want %q", tc.title, got, tc.want)
			}
		})
	}
}

// =============================================================================
// activity.go: detectAgentTypeFromPane
// =============================================================================

func TestDetectAgentTypeFromPane(t *testing.T) {

	tests := []struct {
		name      string
		agentType agent.AgentType
		want      string
	}{
		{"claude", agent.AgentTypeClaudeCode, "claude"},
		{"codex", agent.AgentTypeCodex, "codex"},
		{"gemini", agent.AgentTypeGemini, "gemini"},
		{"grok alias canonicalized", agent.AgentType("xai_grok_build"), "grok"},
		{"cursor", agent.AgentTypeCursor, "cursor"},
		{"windsurf", agent.AgentTypeWindsurf, "windsurf"},
		{"aider", agent.AgentTypeAider, "aider"},
		{"ollama", agent.AgentTypeOllama, "ollama"},
		{"codex alias canonicalized", agent.AgentType("openai-codex"), "codex"},
		{"user", agent.AgentTypeUser, "user"},
		{"unknown", agent.AgentTypeUnknown, "unknown"},
		{"empty type", "", "unknown"},
		{"arbitrary", "something-else", "unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := LegacyAgentType(tc.agentType)
			if got != tc.want {
				t.Errorf("LegacyAgentType(%v) = %q, want %q", tc.agentType, got, tc.want)
			}
		})
	}
}

func TestDetectAgentTypeFromPane_GrokAlias(t *testing.T) {
	agentType := agent.AgentType("xai-grok-build")
	if got := LegacyAgentType(agentType); got != "grok" {
		t.Fatalf("LegacyAgentType(%q) = %q, want grok", agentType, got)
	}
}

// From omp_test.go: an omp pane is named omp.
func TestDetectAgentTypeFromPane_Omp(t *testing.T) {
	if got := LegacyAgentType(agent.AgentTypeOmp); got != "omp" {
		t.Fatalf("LegacyAgentType = %q", got)
	}
}

// =============================================================================
// nhm's tests of the local changes
// =============================================================================

// ntm's human verb plans balanced (empty, any case, trimmed) with
// PlanAllocations and sends every other string to the legacy strategies.
func TestUsesAllocationPlanner(t *testing.T) {
	for _, strategy := range []string{"", "balanced", "BALANCED", " Balanced "} {
		if !UsesAllocationPlanner(strategy) {
			t.Errorf("UsesAllocationPlanner(%q) = false, want true", strategy)
		}
	}
	for _, strategy := range []string{"speed", "quality", "dependency", "round-robin", "rr", "roundrobin", "unknown"} {
		if UsesAllocationPlanner(strategy) {
			t.Errorf("UsesAllocationPlanner(%q) = true, want false", strategy)
		}
	}
}

// The legacy switch does not use ParseStrategy: rr, roundrobin and a
// padded round-robin take the balanced branch, as in ntm.
func TestLegacyAssignmentsUnknownStrategyIsBalanced(t *testing.T) {
	agents := []LegacyAgent{makeTestAgent(0, "claude")}
	beads := []bv.BeadPreview{makeTestBead("b1", "Task 1", "P2"), makeTestBead("b2", "Task 2", "P2")}
	for _, strategy := range []string{"rr", "roundrobin", " round-robin", "ROUND-ROBIN"} {
		got := legacyAssign(strategy, agents, beads)
		wantBalanced := strategy != "ROUND-ROBIN"
		if len(got) != 2 {
			t.Fatalf("%q: got %d assignments, want 2", strategy, len(got))
		}
		if isBalanced := strings.Contains(got[0].Reasoning, "balanced workload"); isBalanced != wantBalanced {
			t.Errorf("%q: reasoning %q, balanced=%v, want %v", strategy, got[0].Reasoning, isBalanced, wantBalanced)
		}
	}
}

// A title with patterns of two task types gets the later type in the list,
// every time. ntm gives either at random: "Add test coverage" is testing
// (claude 0.7) or feature (claude 0.8) there, and always feature here.
func TestCalculateMatchConfidenceFixedPatternOrder(t *testing.T) {
	bead := bv.BeadPreview{Title: "Add test coverage"}
	for i := 0; i < 50; i++ {
		if got := calculateMatchConfidence("claude", bead, "balanced"); got != 0.8 {
			t.Fatalf("run %d: claude on %q = %.2f, want 0.8 (feature)", i, bead.Title, got)
		}
	}
}

// The balanced branch starts from the caller's load: the agent that
// already holds work gets the next bead last. A nil clock is time.Now.
func TestLegacyBalancedStartsFromLoad(t *testing.T) {
	agents := []LegacyAgent{makeTestAgent(0, "claude"), makeTestAgent(1, "claude")}
	beads := []bv.BeadPreview{makeTestBead("b1", "Task 1", "P2")}
	load := LegacyLoad{Active: map[string]int{"0.0": 1}}
	got := LegacyAssignments("balanced-legacy", agents, beads, load, nil)
	if len(got) != 1 || got[0].Agent.Key != "0.1" {
		t.Fatalf("got %+v, want b1 on the idle agent 0.1", got)
	}
}

// The balanced branch starts from the load's times too: of two agents with
// one active assignment each, the one assigned longer ago gets the bead,
// though its target is higher. Without the times, the lower target wins.
func TestLegacyBalancedLastAssignedFromLoad(t *testing.T) {
	agents := []LegacyAgent{makeTestAgent(1, "claude"), makeTestAgent(2, "claude")}
	beads := []bv.BeadPreview{makeTestBead("b1", "Task 1", "P2")}
	at := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	load := LegacyLoad{
		Active:       map[string]int{"0.1": 1, "0.2": 1},
		LastAssigned: map[string]time.Time{"0.1": at, "0.2": at.Add(-time.Hour)},
	}
	got := LegacyAssignments("balanced-legacy", agents, beads, load, nil)
	if len(got) != 1 || got[0].Agent.Key != "0.2" {
		t.Fatalf("got %+v, want b1 on 0.2, assigned longer ago", got)
	}
	load.LastAssigned = nil
	got = LegacyAssignments("balanced-legacy", agents, beads, load, nil)
	if len(got) != 1 || got[0].Agent.Key != "0.1" {
		t.Fatalf("without times: got %+v, want b1 on the lower target 0.1", got)
	}
}

// Each balanced pick takes its own time, as ntm's time.Now does, so the
// least recently assigned agent wins a later tie, not the lower target.
// codex (x) takes the first feature bead on score, claude (y) the second on
// load; both score 0.7 on the testing bead, and x, assigned first, wins it.
// One time for every pick would give it to y, the lower target.
func TestLegacyBalancedLeastRecentUsesEachPickTime(t *testing.T) {
	agents := []LegacyAgent{
		{Key: "y", Target: "0.1", Index: 1, AgentType: "claude"},
		{Key: "x", Target: "0.2", Index: 2, AgentType: "codex"},
	}
	beads := []bv.BeadPreview{
		makeTestBead("b1", "Implement feature", "P2"),
		makeTestBead("b2", "Implement feature", "P2"),
		makeTestBead("b3", "Run spec tests", "P2"),
	}
	picks := func(now func() time.Time) string {
		var keys []string
		for _, a := range LegacyAssignments("balanced-legacy", agents, beads, LegacyLoad{}, now) {
			keys = append(keys, a.Agent.Key)
		}
		return strings.Join(keys, ",")
	}
	clock := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	tick := func() time.Time {
		clock = clock.Add(time.Second)
		return clock
	}
	if got := picks(tick); got != "x,y,x" {
		t.Fatalf("picks = %s, want x,y,x (x was assigned first, so x is least recent)", got)
	}
	fixed := func() time.Time { return clock }
	if got := picks(fixed); got != "x,y,y" {
		t.Fatalf("picks with one time = %s, want x,y,y (the test must tell the two apart)", got)
	}
}
