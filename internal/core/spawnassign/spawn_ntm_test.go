// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/spawn_test.go at commit 9d5ed977: containsAnyStr and its
// helpers, TestNormalizeAssignStrategyStrict, TestGenerateWorkPrompt,
// TestFinalizeSpawnAssignmentOutputRequiresEveryEligibleAgent,
// TestSpawnAssignmentOutput_SchemaStability, TestSpawnOutput_ModeField,
// skillMatchTriageFixture and the strategy tests through
// TestSpawnTaskTypeForRecommendationLadder; see LICENSE in this directory
// and PROVENANCE.md. Local changes: this header; package and imports; the
// finalize test's "partial canonical single-window pane" case is left out
// (tmux canonicalizes pane "0" as "0.0"; nhm's slot is always "N.M") and
// its envelope is nhm's robot.NewResponse. The tests of GetSpawn's tmux
// lifecycle are not taken; nhm's assignment-loop tests are in
// spawnassign_test.go.

package spawnassign

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/integrations/bv"
	"github.com/arosl/nhm/internal/robot"
)

// containsAnyStr checks if s contains any of the substrings
func containsAnyStr(s string, subs ...string) bool {
	for _, sub := range subs {
		if containsSubstringSpawn(s, sub) {
			return true
		}
	}
	return false
}

// containsSubstringSpawn is a simple contains check
func containsSubstringSpawn(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(len(s) > 0 && len(sub) > 0 && findSubstringSpawn(s, sub)))
}

// findSubstringSpawn checks if sub is in s
func findSubstringSpawn(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestNormalizeAssignStrategyStrict(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"top-n", "top-n"},
		{"topn", "top-n"},
		{"TOP-N", "top-n"},
		{"diverse", "diverse"},
		{"DIVERSE", "diverse"},
		{"dependency-aware", "dependency-aware"},
		{"dependency", "dependency-aware"},
		{"skill-matched", "skill-matched"},
		{"skill", "skill-matched"},
		{"  top-n  ", "top-n"}, // Whitespace trimmed
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, err := normalizeAssignStrategyStrict(tc.input)
			if err != nil {
				t.Fatalf("normalizeAssignStrategyStrict(%q): %v", tc.input, err)
			}
			if got != tc.expected {
				t.Errorf("normalizeAssignStrategyStrict(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}

	for _, input := range []string{"", "   ", "invalid"} {
		t.Run("invalid_"+input, func(t *testing.T) {
			if got, err := normalizeAssignStrategyStrict(input); err == nil || got != "" {
				t.Fatalf("normalizeAssignStrategyStrict(%q) = %q, %v; want explicit error", input, got, err)
			}
		})
	}
}

// TestGenerateWorkPrompt validates work prompt generation
func TestGenerateWorkPrompt(t *testing.T) {

	item := workItem{
		ID:       "test-123",
		Title:    "Fix authentication bug",
		Priority: 1,
		Score:    0.85,
		Type:     "bug",
		Reasons:  []string{"High priority", "Unblocks 3 items"},
	}

	prompt := generateWorkPrompt(item)

	// Validate prompt contains key elements
	if !containsAnyStr(prompt, "test-123") {
		t.Error("Prompt should contain bead ID")
	}
	if !containsAnyStr(prompt, "Fix authentication bug") {
		t.Error("Prompt should contain bead title")
	}
	if !containsAnyStr(prompt, "br show test-123") {
		t.Error("Prompt should contain br show command")
	}
	if !containsAnyStr(prompt, "in_progress") {
		t.Error("Prompt should mention in_progress status")
	}
	if !containsAnyStr(prompt, "High priority") {
		t.Error("Prompt should contain reasons")
	}
	if !containsAnyStr(prompt, "br close test-123 --reason \"Completed\"") {
		t.Error("Prompt should contain completion command")
	}

	t.Logf("Generated prompt:\n%s", prompt)
}

func TestFinalizeSpawnAssignmentOutputRequiresEveryEligibleAgent(t *testing.T) {
	agents := []SpawnedAgent{
		{Pane: "0.0", Name: "One", Type: "claude"},
		{Pane: "0.1", Name: "Two", Type: "codex"},
		{Pane: "0.2", Name: "Operator", Type: "user"},
	}
	tests := []struct {
		name        string
		assignments []SpawnAssignment
		wantSuccess bool
		wantFailed  int
	}{
		{name: "zero", wantFailed: 2},
		{
			name: "partial",
			assignments: []SpawnAssignment{{
				Pane: "0.0", AgentType: "claude", BeadID: "bd-one", Claimed: true, PromptSent: true,
			}},
			wantFailed: 1,
		},
		{
			name: "complete",
			assignments: []SpawnAssignment{
				{Pane: "0.0", AgentType: "claude", BeadID: "bd-one", Claimed: true, PromptSent: true},
				{Pane: "0.1", AgentType: "codex", BeadID: "bd-two", Claimed: true, PromptSent: true},
			},
			wantSuccess: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := &SpawnOutput{
				RobotResponse: robot.NewResponse(true, "json", time.Now()),
				Agents:        append([]SpawnedAgent(nil), agents...),
				Assignments:   append([]SpawnAssignment(nil), test.assignments...),
			}
			finalizeSpawnAssignmentOutput(output)
			if len(output.Assignments) != 2 {
				t.Fatalf("assignments=%+v, want every non-user agent represented", output.Assignments)
			}
			failed := 0
			for _, assignment := range output.Assignments {
				if assignment.ClaimError != "" || assignment.PromptError != "" || !assignment.Claimed || !assignment.PromptSent {
					failed++
				}
			}
			if failed != test.wantFailed {
				t.Fatalf("failed=%d assignments=%+v, want %d", failed, output.Assignments, test.wantFailed)
			}
			if output.Success != test.wantSuccess {
				t.Fatalf("success=%v, want %v; output=%+v", output.Success, test.wantSuccess, output)
			}
			if !test.wantSuccess && output.ErrorCode != "ASSIGNMENT_FAILED" {
				t.Fatalf("error_code=%q, want ASSIGNMENT_FAILED", output.ErrorCode)
			}
		})
	}
}

func TestSpawnAssignmentOutput_SchemaStability(t *testing.T) {

	// Create a test assignment
	assignment := SpawnAssignment{
		Pane:        "0.1",
		AgentType:   "claude",
		BeadID:      "test-bead",
		BeadTitle:   "Test Bead Title",
		Priority:    "P1",
		Claimed:     true,
		PromptSent:  true,
		ClaimError:  "",
		PromptError: "",
	}

	// Marshal and unmarshal to validate JSON schema
	data, err := json.Marshal(assignment)
	if err != nil {
		t.Fatalf("Failed to marshal assignment: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal assignment: %v", err)
	}

	// Validate required fields
	requiredFields := []string{"pane", "agent_type", "bead_id", "bead_title", "priority", "claimed", "prompt_sent"}
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("Missing required field: %s", field)
		}
	}

	// Validate omitempty fields are not present when empty
	omitEmptyFields := []string{"claim_error", "prompt_error"}
	for _, field := range omitEmptyFields {
		if _, ok := parsed[field]; ok {
			t.Errorf("Field %s should be omitted when empty", field)
		}
	}

	t.Logf("Assignment JSON: %s", string(data))
}

// TestSpawnOutput_ModeField validates mode field is set correctly
func TestSpawnOutput_ModeField(t *testing.T) {

	// Test output struct with mode field
	output := SpawnOutput{
		Session:        "test-session",
		Mode:           "orchestrator",
		AssignStrategy: "top-n",
		Assignments: []SpawnAssignment{
			{Pane: "0.1", BeadID: "test-1", Claimed: true},
		},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Failed to marshal output: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal output: %v", err)
	}

	// Validate mode is present
	if parsed["mode"] != "orchestrator" {
		t.Errorf("Mode should be 'orchestrator', got %v", parsed["mode"])
	}

	// Validate assign_strategy is present
	if parsed["assign_strategy"] != "top-n" {
		t.Errorf("AssignStrategy should be 'top-n', got %v", parsed["assign_strategy"])
	}

	// Validate assignments array is present
	if _, ok := parsed["assignments"]; !ok {
		t.Error("Missing assignments field")
	}

	t.Logf("Output with mode: %s", string(data))
}

// skillMatchTriageFixture is a discriminating fixture for the strategy
// difference tests: top-n order (by triage score) is refactor, docs, bug —
// but capability matching pairs codex with the bug and gemini with the docs.
func skillMatchTriageFixture() *bv.TriageResponse {
	return &bv.TriageResponse{
		Triage: bv.TriageData{
			Recommendations: []bv.TriageRecommendation{
				{ID: "bd-refactor", Title: "Restructure config loading", Type: "refactor", Priority: 1, Score: 0.95},
				{ID: "bd-docs", Title: "Document the robot surface", Type: "docs", Priority: 2, Score: 0.90},
				{ID: "bd-bug", Title: "Broken pane resolution", Type: "bug", Priority: 1, Score: 0.85},
			},
		},
	}
}

// TestGetWorkItemsForStrategySkillMatchedDiffersFromTopN is the C4-style
// difference test: the same fixture must produce DIFFERENT positional
// assignments under skill-matched than under top-n, proving skill-matched
// no longer silently falls back.
func TestGetWorkItemsForStrategySkillMatchedDiffersFromTopN(t *testing.T) {
	triage := skillMatchTriageFixture()
	agents := []SpawnedAgent{
		{Pane: "0.1", Type: "cod"},
		{Pane: "0.2", Type: "gmi"},
		{Pane: "0.3", Type: "cc"},
	}

	topN := getWorkItemsForStrategy(triage, "top-n", agents)
	matched := getWorkItemsForStrategy(triage, "skill-matched", agents)

	if len(topN) != 3 || len(matched) != 3 {
		t.Fatalf("expected 3 items each, got top-n=%d skill-matched=%d", len(topN), len(matched))
	}

	// top-n pairs positionally by triage score: cod→refactor, gmi→docs, cc→bug.
	wantTopN := []string{"bd-refactor", "bd-docs", "bd-bug"}
	for i, want := range wantTopN {
		if topN[i].ID != want {
			t.Fatalf("top-n[%d] = %s, want %s", i, topN[i].ID, want)
		}
	}

	// Capability matrix: cod scores bug 0.90 > refactor 0.75 > docs 0.70;
	// gmi scores docs 0.90; cc scores refactor 0.95.
	wantMatched := []string{"bd-bug", "bd-docs", "bd-refactor"}
	for i, want := range wantMatched {
		if matched[i].ID != want {
			t.Fatalf("skill-matched[%d] = %s, want %s (full: %+v)", i, matched[i].ID, want, matched)
		}
	}

	same := true
	for i := range topN {
		if topN[i].ID != matched[i].ID {
			same = false
			break
		}
	}
	if same {
		t.Fatal("skill-matched produced identical positional assignments to top-n on a discriminating fixture (silent fallback)")
	}

	for i, item := range matched {
		if item.MatchReason == "" {
			t.Fatalf("skill-matched[%d] (%s) has no MatchReason recorded", i, item.ID)
		}
		if !strings.Contains(item.MatchReason, "skill-matched") {
			t.Fatalf("skill-matched[%d] MatchReason = %q, want skill-matched rationale", i, item.MatchReason)
		}
		if len(item.Reasons) == 0 || !strings.Contains(item.Reasons[len(item.Reasons)-1], "skill-matched") {
			t.Fatalf("skill-matched[%d] Reasons missing rationale: %v", i, item.Reasons)
		}
	}
	// Genuine matching must state the capability score, not a fallback note.
	if !strings.Contains(matched[0].MatchReason, "capability") {
		t.Fatalf("MatchReason %q does not cite a capability score", matched[0].MatchReason)
	}
}

// TestGetSkillMatchedWorkItemsNoSignalIsLoud verifies the degenerate class
// (unknown agent type, uniform 0.5 scores) is recorded loudly instead of
// silently masquerading as skill matching.
func TestGetSkillMatchedWorkItemsNoSignalIsLoud(t *testing.T) {
	triage := skillMatchTriageFixture()
	agents := []SpawnedAgent{{Pane: "0.1", Type: "totally-unknown-agent"}}

	items := getSkillMatchedWorkItems(triage, agents)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	// Uniform scores keep triage order (top item first).
	if items[0].ID != "bd-refactor" {
		t.Fatalf("degenerate matching picked %s, want triage-order bd-refactor", items[0].ID)
	}
	if !strings.Contains(items[0].MatchReason, "no discriminating capability signal") {
		t.Fatalf("degenerate matching MatchReason = %q, want loud no-signal note", items[0].MatchReason)
	}
}

// TestGetWorkItemsForStrategyUnknownStrategyIsLoud verifies that a strategy
// value leaking past strict normalization records a fallback reason.
func TestGetWorkItemsForStrategyUnknownStrategyIsLoud(t *testing.T) {
	triage := skillMatchTriageFixture()
	agents := []SpawnedAgent{{Pane: "0.1", Type: "cc"}}

	items := getWorkItemsForStrategy(triage, "not-a-strategy", agents)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if !strings.Contains(items[0].MatchReason, "fell back to top-n") {
		t.Fatalf("unknown strategy MatchReason = %q, want loud fallback note", items[0].MatchReason)
	}
}

// TestSpawnTaskTypeForRecommendationLadder covers the explicit-type, label,
// and title-inference rungs.
func TestSpawnTaskTypeForRecommendationLadder(t *testing.T) {
	cases := []struct {
		name string
		rec  bv.TriageRecommendation
		want string
	}{
		{"explicit type wins", bv.TriageRecommendation{Type: "refactor", Title: "fix bug"}, "refactor"},
		{"label used when type generic", bv.TriageRecommendation{Type: "task", Labels: []string{"lane", "docs"}, Title: "misc"}, "docs"},
		{"title inference last", bv.TriageRecommendation{Type: "task", Title: "Fix broken spawn crash"}, "bug"},
		{"generic stays task", bv.TriageRecommendation{Type: "task", Title: "misc chores galore maybe"}, "task"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(spawnTaskTypeForRecommendation(tc.rec)); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
