// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/assign_test.go and internal/robot/assign_strategy_wire_test.go
// at commit 9d5ed977: the tests of the functions in assign_ntm.go; see
// LICENSE in this directory and PROVENANCE.md. Local changes: this header;
// package and imports; binding ids in place of tmux pane ids where the
// ledger reads them; GetAssign takes nhm's ports (nil here);
// TestGenerateAssignHints_WithRecommendations expects no suggested command,
// as nhm has no --robot-bulk-assign; the DistributeRecommendation lines
// left out of TestPlanAssignments_ConfidenceComesFromPlanner (ntm's
// distribute mode is not taken). Not taken: the tmux observation tests,
// the config, PrintAssign and DistributeRecommendation tests (nhm-y55).

package spawnassign

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/assignment"
	"github.com/arosl/nhm/internal/integrations/bv"
	"github.com/arosl/nhm/internal/integrations/worksource"
	"github.com/arosl/nhm/internal/robot"
)

// =============================================================================
// Task Type Inference Tests
// =============================================================================

func TestFilterDurablyOccupiedAssignAgentsUsesPaneIDAcrossDuplicateLocalIndexes(t *testing.T) {
	idle := []string{"bnd-0000000000000041", "bnd-0000000000000042", "bnd-0000000000000043"}
	active := []*assignment.Assignment{{
		BeadID: "ntm-window-one", Pane: 1, OccupancyKey: "bnd-0000000000000042", DispatchTarget: "bnd-0000000000000042",
	}}
	available, err := filterDurablyOccupiedAssignAgents(idle, active)
	if err != nil || !reflect.DeepEqual(available, []string{"bnd-0000000000000041", "bnd-0000000000000043"}) {
		t.Fatalf("available=%v error=%v, want exact occupied pane removed", available, err)
	}

	available, err = filterDurablyOccupiedAssignAgents(idle, []*assignment.Assignment{{BeadID: "legacy", Pane: 1}})
	if !errors.Is(err, assignment.ErrPaneIdentityMigrationRequired) || available != nil {
		t.Fatalf("legacy available=%v error=%v, want typed migration error", available, err)
	}
}

func TestInferTaskType_BugKeywords(t *testing.T) {
	tests := []struct {
		title    string
		expected string
	}{
		{"Fix authentication bug", "bug"},
		{"Bug: login fails on retry", "bug"},
		{"broken pagination in status", "bug"},
		{"Error handling in tmux client", "bug"},
		{"Crash on empty session name", "bug"},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			bead := bv.BeadPreview{ID: "bd-test", Title: tt.title, Priority: "P1"}
			got := inferTaskType(bead)
			if got != tt.expected {
				t.Errorf("inferTaskType(%q) = %q, want %q", tt.title, got, tt.expected)
			}
		})
	}
}

func TestInferTaskType_TestKeywords(t *testing.T) {
	tests := []struct {
		title    string
		expected string
	}{
		{"Write unit tests for assign module", "testing"},
		{"Add test coverage for robot pkg", "testing"},
		{"Spec: robot env output format", "testing"},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			bead := bv.BeadPreview{ID: "bd-test", Title: tt.title, Priority: "P2"}
			got := inferTaskType(bead)
			if got != tt.expected {
				t.Errorf("inferTaskType(%q) = %q, want %q", tt.title, got, tt.expected)
			}
		})
	}
}

func TestInferTaskType_FeatureKeywords(t *testing.T) {
	tests := []struct {
		title    string
		expected string
	}{
		{"Feature: Add robot-env command", "feature"},
		{"Implement bead assignment engine", "feature"},
		{"Add tab completion support", "feature"},
		{"New ensemble mode catalog", "feature"},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			bead := bv.BeadPreview{ID: "bd-test", Title: tt.title, Priority: "P2"}
			got := inferTaskType(bead)
			if got != tt.expected {
				t.Errorf("inferTaskType(%q) = %q, want %q", tt.title, got, tt.expected)
			}
		})
	}
}

func TestInferTaskType_RefactorKeywords(t *testing.T) {
	tests := []struct {
		title    string
		expected string
	}{
		{"Refactor robot output types", "refactor"},
		{"Cleanup unused helpers in tmux pkg", "refactor"},
		{"Improve code structure patterns", "refactor"},
		{"Consolidate send command logic", "refactor"},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			bead := bv.BeadPreview{ID: "bd-test", Title: tt.title, Priority: "P2"}
			got := inferTaskType(bead)
			if got != tt.expected {
				t.Errorf("inferTaskType(%q) = %q, want %q", tt.title, got, tt.expected)
			}
		})
	}
}

func TestInferTaskType_DocumentationKeywords(t *testing.T) {
	tests := []struct {
		title    string
		expected string
	}{
		{"Update README with ensemble docs", "documentation"},
		{"Add documentation for robot flags", "documentation"},
		{"Comment complex assignment logic", "documentation"},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			bead := bv.BeadPreview{ID: "bd-test", Title: tt.title, Priority: "P3"}
			got := inferTaskType(bead)
			if got != tt.expected {
				t.Errorf("inferTaskType(%q) = %q, want %q", tt.title, got, tt.expected)
			}
		})
	}
}

func TestInferTaskType_AnalysisKeywords(t *testing.T) {
	tests := []struct {
		title    string
		expected string
	}{
		{"Analyze rate limit patterns", "analysis"},
		{"Investigate CI failure", "analysis"},
		{"Research WebSocket alternatives", "analysis"},
		{"Design plugin architecture", "analysis"},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			bead := bv.BeadPreview{ID: "bd-test", Title: tt.title, Priority: "P2"}
			got := inferTaskType(bead)
			if got != tt.expected {
				t.Errorf("inferTaskType(%q) = %q, want %q", tt.title, got, tt.expected)
			}
		})
	}
}

func TestInferTaskType_Default(t *testing.T) {
	// Titles with no matching keywords should default to "task"
	tests := []string{
		"EPIC: Something",
		"Phase 2 milestone",
		"Integration with external service",
		"",
	}

	for _, title := range tests {
		t.Run(title, func(t *testing.T) {
			bead := bv.BeadPreview{ID: "bd-test", Title: title, Priority: "P2"}
			got := inferTaskType(bead)
			if got != "task" {
				t.Errorf("inferTaskType(%q) = %q, want %q", title, got, "task")
			}
		})
	}
}

func TestInferTaskType_PriorityOrder(t *testing.T) {
	// "Fix" matches bug before feature ("fix" contains no feature keyword)
	// "Fix and add test coverage" should match "bug" because "fix" comes first in the rules
	bead := bv.BeadPreview{ID: "bd-test", Title: "Fix and add test coverage", Priority: "P1"}
	got := inferTaskType(bead)
	if got != "bug" {
		t.Errorf("inferTaskType('Fix and add test coverage') = %q, want %q (bug should match before testing)", got, "bug")
	}
}

func TestInferTaskType_CaseInsensitive(t *testing.T) {
	bead := bv.BeadPreview{ID: "bd-test", Title: "BUG: Session Not Found", Priority: "P1"}
	got := inferTaskType(bead)
	if got != "bug" {
		t.Errorf("inferTaskType should be case-insensitive, got %q", got)
	}
}

// =============================================================================
// Priority Parsing Tests
// =============================================================================

func TestParsePriority_Assign(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"P0", 0},
		{"P1", 1},
		{"P2", 2},
		{"P3", 3},
		{"P4", 4},
		{"P5", 2},  // Out of range, defaults to 2
		{"P9", 2},  // Out of range
		{"", 2},    // Empty
		{"Q1", 2},  // Wrong prefix
		{"p1", 2},  // Wrong case (lowercase p)
		{"PP", 2},  // Malformed
		{"P", 2},   // Too short
		{"P12", 2}, // Too long
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parsePriority(tt.input)
			if got != tt.expected {
				t.Errorf("parsePriority(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}

// =============================================================================
// Confidence Calculation Tests
// =============================================================================

func TestCalculateConfidence_SimpleIsCapabilityScore(t *testing.T) {
	bead := bv.BeadPreview{ID: "bd-test", Title: "Fix login bug", Priority: "P1"}
	conf := calculateConfidence("claude", bead)

	if conf < 0 || conf > 1.0 {
		t.Errorf("confidence %f should be between 0.0 and 1.0", conf)
	}
	// Simple strategy confidence is the raw capability score, no adjustments.
	if want := AgentStrength("claude", "bug"); conf != want {
		t.Errorf("confidence %f should equal capability score %f", conf, want)
	}
}

func TestCalculateConfidence_PriorityDoesNotAdjustSimple(t *testing.T) {
	// Simple strategy applies no strategy scoring: priority never shifts confidence.
	p0Bead := bv.BeadPreview{ID: "bd-test", Title: "Some task", Priority: "P0"}
	p3Bead := bv.BeadPreview{ID: "bd-test", Title: "Some task", Priority: "P3"}

	if p0 := calculateConfidence("claude", p0Bead); p0 != calculateConfidence("claude", p3Bead) {
		t.Errorf("simple confidence should ignore priority, P0 gave %f", p0)
	}
}

// =============================================================================
// Reasoning Generation Tests
// =============================================================================

func TestGenerateReasoning_HighStrengthAgent(t *testing.T) {
	bead := bv.BeadPreview{ID: "bd-test", Title: "Fix login bug", Priority: "P0"}
	reasoning := generateReasoning("claude", bead)

	if reasoning == "" {
		t.Error("reasoning should not be empty")
	}

	// Simple strategy names the sequential pairing honestly.
	if !strings.Contains(reasoning, "simple sequential pairing") {
		t.Errorf("reasoning %q should name the simple sequential pairing", reasoning)
	}

	// P0 should get "critical priority" reasoning
	if !strings.Contains(reasoning, "critical priority") {
		t.Errorf("reasoning %q should mention critical priority for P0 bead", reasoning)
	}
}

func TestGenerateReasoning_P1Priority(t *testing.T) {
	bead := bv.BeadPreview{ID: "bd-test", Title: "Some task", Priority: "P1"}
	reasoning := generateReasoning("claude", bead)

	if !strings.Contains(reasoning, "high priority") {
		t.Errorf("reasoning %q should mention high priority for P1", reasoning)
	}
}

func TestGenerateReasoning_DefaultFallback(t *testing.T) {
	// P2 bead produces output without any priority callout.
	bead := bv.BeadPreview{ID: "bd-test", Title: "Integration work", Priority: "P2"}
	reasoning := generateReasoning("claude", bead)

	if reasoning == "" {
		t.Error("reasoning should not be empty for a P2 bead")
	}
}

// =============================================================================
// Agent Hints Generation Tests
// =============================================================================

func TestGenerateAssignHints_NoWork(t *testing.T) {
	hints := generateAssignHints("test", nil, nil, nil, nil)

	if hints.Summary != "No work available to assign" {
		t.Errorf("Summary = %q, want %q", hints.Summary, "No work available to assign")
	}
}

func TestGenerateAssignHints_NoIdleAgents(t *testing.T) {
	beads := []bv.BeadPreview{{ID: "bd-1", Title: "Task 1", Priority: "P1"}}
	hints := generateAssignHints("test", nil, nil, beads, nil)

	if !strings.Contains(hints.Summary, "no idle agents") {
		t.Errorf("Summary %q should mention no idle agents", hints.Summary)
	}
}

func TestGenerateAssignHints_WithRecommendations(t *testing.T) {
	recs := []AssignRecommend{
		{PaneID: "%12", PaneTarget: "0.2", AssignBead: "bd-abc", AgentType: "claude"},
		{PaneID: "%23", PaneTarget: "1.3", AssignBead: "bd-def", AgentType: "codex"},
	}
	idleAgents := []string{"%12", "%23"}
	beads := []bv.BeadPreview{
		{ID: "bd-abc", Title: "Task A"},
		{ID: "bd-def", Title: "Task B"},
	}

	hints := generateAssignHints("test", recs, idleAgents, beads, nil)

	if !strings.Contains(hints.Summary, "2 assignments") {
		t.Errorf("Summary %q should mention 2 assignments", hints.Summary)
	}
	// ntm suggests one --robot-bulk-assign command per recommendation;
	// nhm has no --robot-bulk-assign and suggests none.
	if len(hints.SuggestedCommands) != 0 {
		t.Errorf("SuggestedCommands = %q, want none", hints.SuggestedCommands)
	}
}

func TestGenerateAssignHints_MoreBeadsThanAgents(t *testing.T) {
	recs := []AssignRecommend{{PaneID: "%12", PaneTarget: "0.2", AssignBead: "bd-abc"}}
	idleAgents := []string{"%12"}
	beads := []bv.BeadPreview{
		{ID: "bd-abc", Title: "A"},
		{ID: "bd-def", Title: "B"},
		{ID: "bd-ghi", Title: "C"},
	}

	hints := generateAssignHints("test", recs, idleAgents, beads, nil)

	if len(hints.Warnings) == 0 {
		t.Error("should warn about unassigned beads")
	}
	found := false
	for _, w := range hints.Warnings {
		if strings.Contains(w, "won't be assigned") {
			found = true
			break
		}
	}
	if !found {
		t.Error("warnings should mention beads that won't be assigned")
	}
}

func TestGenerateAssignHints_StaleInProgress(t *testing.T) {
	inProgress := []bv.BeadInProgress{
		{ID: "bd-old", Title: "Stale task", UpdatedAt: time.Now().Add(-48 * time.Hour)},
		{ID: "bd-new", Title: "Fresh task", UpdatedAt: time.Now().Add(-1 * time.Hour)},
	}

	hints := generateAssignHints("test", nil, nil, nil, inProgress)

	found := false
	for _, w := range hints.Warnings {
		if strings.Contains(w, "stale") {
			found = true
			break
		}
	}
	if !found {
		t.Error("should warn about stale in-progress beads")
	}
}

// =============================================================================
// Assignment Generation Tests
// =============================================================================

func TestGenerateAssignments_BasicFlow(t *testing.T) {
	agents := []assignAgentInfo{
		{paneID: "%12", paneTarget: "0.2", agentType: "claude", model: "opus", state: "idle"},
		{paneID: "%22", paneTarget: "1.2", agentType: "codex", model: "gpt4", state: "idle"},
	}
	beads := []bv.BeadPreview{
		{ID: "bd-abc", Title: "Fix bug", Priority: "P1"},
		{ID: "bd-def", Title: "Add feature", Priority: "P2"},
	}
	idleAgents := []string{"%12", "%22"}

	// "simple" pins the historical sequential pairing behavior.
	recs := planAssignments(agents, beads, nil, "simple", idleAgents)

	if len(recs) != 2 {
		t.Fatalf("expected 2 recommendations, got %d", len(recs))
	}

	if recs[0].AssignBead != "bd-abc" {
		t.Errorf("first recommendation bead = %q, want %q", recs[0].AssignBead, "bd-abc")
	}
	if recs[0].PaneID != "%12" || recs[0].PaneTarget != "0.2" {
		t.Errorf("first recommendation pane = %s (%s), want 0.2 (%%12)", recs[0].PaneTarget, recs[0].PaneID)
	}
	if recs[1].AssignBead != "bd-def" {
		t.Errorf("second recommendation bead = %q, want %q", recs[1].AssignBead, "bd-def")
	}
	if recs[1].PaneID != "%22" || recs[1].PaneTarget != "1.2" {
		t.Errorf("second recommendation pane = %s (%s), want 1.2 (%%22)", recs[1].PaneTarget, recs[1].PaneID)
	}
}

func TestGenerateAssignments_MoreAgentsThanBeads(t *testing.T) {
	agents := []assignAgentInfo{
		{paneID: "%12", paneTarget: "0.2", agentType: "claude", state: "idle"},
		{paneID: "%13", paneTarget: "0.3", agentType: "codex", state: "idle"},
		{paneID: "%14", paneTarget: "0.4", agentType: "gemini", state: "idle"},
	}
	beads := []bv.BeadPreview{
		{ID: "bd-abc", Title: "Fix bug", Priority: "P1"},
	}
	idleAgents := []string{"%12", "%13", "%14"}

	recs := planAssignments(agents, beads, nil, "balanced", idleAgents)

	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation (only 1 bead), got %d", len(recs))
	}
}

func TestGenerateAssignments_MoreBeadsThanAgents(t *testing.T) {
	agents := []assignAgentInfo{
		{paneID: "%12", paneTarget: "0.2", agentType: "claude", state: "idle"},
	}
	beads := []bv.BeadPreview{
		{ID: "bd-abc", Title: "Task A", Priority: "P1"},
		{ID: "bd-def", Title: "Task B", Priority: "P2"},
		{ID: "bd-ghi", Title: "Task C", Priority: "P3"},
	}
	idleAgents := []string{"%12"}

	recs := planAssignments(agents, beads, nil, "balanced", idleAgents)

	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation (only 1 idle agent), got %d", len(recs))
	}
	if recs[0].AssignBead != "bd-abc" {
		t.Errorf("should assign first bead, got %q", recs[0].AssignBead)
	}
}

func TestGenerateAssignments_NoIdleAgents(t *testing.T) {
	agents := []assignAgentInfo{
		{paneID: "%12", paneTarget: "0.2", agentType: "claude", state: "working"},
	}
	beads := []bv.BeadPreview{
		{ID: "bd-abc", Title: "Task A", Priority: "P1"},
	}
	idleAgents := []string{} // None idle

	recs := planAssignments(agents, beads, nil, "balanced", idleAgents)

	if len(recs) != 0 {
		t.Errorf("expected 0 recommendations with no idle agents, got %d", len(recs))
	}
}

func TestGenerateAssignments_NoBeads(t *testing.T) {
	agents := []assignAgentInfo{
		{paneID: "%12", paneTarget: "0.2", agentType: "claude", state: "idle"},
	}
	idleAgents := []string{"%12"}

	recs := planAssignments(agents, nil, nil, "balanced", idleAgents)

	if len(recs) != 0 {
		t.Errorf("expected 0 recommendations with no beads, got %d", len(recs))
	}
}

func TestGenerateAssignments_RecommendationFields(t *testing.T) {
	agents := []assignAgentInfo{
		{paneID: "%25", paneTarget: "1.5", agentType: "claude", model: "opus-4.5", state: "idle"},
	}
	beads := []bv.BeadPreview{
		{ID: "bd-xyz", Title: "Fix critical bug", Priority: "P0"},
	}
	idleAgents := []string{"%25"}

	recs := planAssignments(agents, beads, nil, "quality", idleAgents)

	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(recs))
	}

	rec := recs[0]
	if rec.PaneID != "%25" || rec.PaneTarget != "1.5" {
		t.Errorf("pane = %s (%s), want 1.5 (%%25)", rec.PaneTarget, rec.PaneID)
	}
	if rec.AgentType != "claude" {
		t.Errorf("AgentType = %q, want %q", rec.AgentType, "claude")
	}
	if rec.Model != "opus-4.5" {
		t.Errorf("Model = %q, want %q", rec.Model, "opus-4.5")
	}
	if rec.AssignBead != "bd-xyz" {
		t.Errorf("AssignBead = %q, want %q", rec.AssignBead, "bd-xyz")
	}
	if rec.BeadTitle != "Fix critical bug" {
		t.Errorf("BeadTitle = %q, want %q", rec.BeadTitle, "Fix critical bug")
	}
	if rec.Priority != "P0" {
		t.Errorf("Priority = %q, want %q", rec.Priority, "P0")
	}
	if rec.Confidence <= 0 || rec.Confidence > 1.0 {
		t.Errorf("Confidence = %f, should be (0, 1.0]", rec.Confidence)
	}
	if rec.Reasoning == "" {
		t.Error("Reasoning should not be empty")
	}
}

func TestResolveAssignBlockedBeadsPreservesDependencies(t *testing.T) {
	blocked, err := resolveAssignBlockedBeads(t.Context(), "/project", []bv.BeadPreview{
		{ID: "bd-child", Title: "Child task"},
	}, func(_ context.Context, projectDir, beadID string) (*bv.BeadAssignmentDetails, error) {
		if projectDir != "/project" || beadID != "bd-child" {
			t.Fatalf("detail lookup = (%q, %q), want (/project, bd-child)", projectDir, beadID)
		}
		return &bv.BeadAssignmentDetails{
			ID:        beadID,
			Title:     "Canonical child task",
			BlockedBy: []string{"bd-parent", "bd-prerequisite"},
		}, nil
	})
	if err != nil {
		t.Fatalf("resolveAssignBlockedBeads: %v", err)
	}
	if want := []BlockedBead{{
		ID: "bd-child", Title: "Canonical child task", BlockedBy: []string{"bd-parent", "bd-prerequisite"},
	}}; !reflect.DeepEqual(blocked, want) {
		t.Fatalf("blocked beads = %+v, want %+v", blocked, want)
	}
}

func TestResolveAssignBlockedBeadsReturnsDetailErrors(t *testing.T) {
	_, err := resolveAssignBlockedBeads(t.Context(), "/project", []bv.BeadPreview{{ID: "bd-child"}}, func(context.Context, string, string) (*bv.BeadAssignmentDetails, error) {
		return nil, errors.New("database unavailable")
	})
	if err == nil || !strings.Contains(err.Error(), "bd-child") || !strings.Contains(err.Error(), "database unavailable") {
		t.Fatalf("detail error = %v, want contextual lookup error", err)
	}
}

func TestClassifyAssignableActionableRecommendationsExplainsExclusions(t *testing.T) {
	assignable, unassignable := classifyAssignableActionableRecommendationsWithGate([]bv.TriageRecommendation{
		{ID: "bd-ready", Title: "Ready", Status: "open"},
		{ID: "bd-blocked", Title: "Blocked", Status: "open", BlockedBy: []string{"bd-parent"}},
		{ID: "bd-gated", Title: "Gated", Status: "open", Labels: []string{"operator-gated"}},
	}, 0, func(label string) bool { return label == "operator-gated" })
	if got := []string{assignable[0].ID}; !reflect.DeepEqual(got, []string{"bd-ready"}) {
		t.Fatalf("assignable IDs = %v, want [bd-ready]", got)
	}
	if want := []UnassignableBead{
		{ID: "bd-blocked", Title: "Blocked", Reason: "blocked by bd-parent"},
		{ID: "bd-gated", Title: "Gated", Reason: "operator-gated label: operator-gated"},
	}; !reflect.DeepEqual(unassignable, want) {
		t.Fatalf("unassignable beads = %+v, want %+v", unassignable, want)
	}
}

func TestUnassignedBeadsBeyondRecommendations(t *testing.T) {
	ready := []bv.BeadPreview{
		{ID: "bd-1", Title: "First"},
		{ID: "bd-2", Title: "Second"},
		{ID: "bd-3", Title: "Third"},
	}
	// The planner may skip earlier beads: assigning only bd-2 leaves bd-1 and
	// bd-3 unassigned, keyed by bead ID rather than position. With exactly
	// one idle agent (fully consumed), the honest reason is capacity.
	if got, want := unassignedBeadsBeyondRecommendations(ready, []AssignRecommend{{AssignBead: "bd-2"}}, 1), []UnassignableBead{
		{ID: "bd-1", Title: "First", Reason: "no idle agent available"},
		{ID: "bd-3", Title: "Third", Reason: "no idle agent available"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unassignable capacity beads = %+v, want %+v", got, want)
	}
	// With idle agents to spare, the planner's confidence gate skipped the
	// beads — "no idle agent available" would be a lie.
	if got, want := unassignedBeadsBeyondRecommendations(ready, []AssignRecommend{{AssignBead: "bd-2"}}, 3), []UnassignableBead{
		{ID: "bd-1", Title: "First", Reason: "skipped by planner: no agent matched above the confidence threshold"},
		{ID: "bd-3", Title: "Third", Reason: "skipped by planner: no agent matched above the confidence threshold"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unassignable confidence-gated beads = %+v, want %+v", got, want)
	}
	if got := unassignedBeadsBeyondRecommendations(ready, []AssignRecommend{
		{AssignBead: "bd-1"}, {AssignBead: "bd-2"}, {AssignBead: "bd-3"},
	}, 3); got != nil {
		t.Fatalf("unassignable capacity beads = %+v, want nil when every bead is recommended", got)
	}
}

// =============================================================================
// AgentStrength Tests
// =============================================================================

func TestAgentStrength_ReturnsValidRange(t *testing.T) {
	agentTypes := []string{"claude", "codex", "gemini", "unknown"}
	taskTypes := []string{"bug", "testing", "feature", "refactor", "documentation", "analysis", "task"}

	for _, agent := range agentTypes {
		for _, task := range taskTypes {
			t.Run(fmt.Sprintf("%s_%s", agent, task), func(t *testing.T) {
				score := AgentStrength(agent, task)
				if score < 0 || score > 1.0 {
					t.Errorf("AgentStrength(%q, %q) = %f, should be [0, 1.0]", agent, task, score)
				}
			})
		}
	}
}

func TestAssignOptions_Defaults(t *testing.T) {
	opts := AssignOptions{
		Session: "test",
	}

	if opts.Strategy != "" {
		t.Error("Strategy should default to empty (normalized to 'balanced' at runtime)")
	}
	if len(opts.Beads) != 0 {
		t.Error("Beads should default to empty")
	}
}

func TestFilterAssignableActionableRecommendationsIsolatesConcurrentProjectPolicies(t *testing.T) {
	projectA := t.TempDir()
	projectB := t.TempDir()
	if err := bv.ConfigureProjectOperatorGatedLabels(projectA, []string{"project-a-gate"}); err != nil {
		t.Fatalf("configure project A gates: %v", err)
	}
	if err := bv.ConfigureProjectOperatorGatedLabels(projectB, []string{"project-b-gate"}); err != nil {
		t.Fatalf("configure project B gates: %v", err)
	}
	recommendations := []bv.TriageRecommendation{
		{ID: "a-only", Status: "open", Labels: []string{"project-a-gate"}},
		{ID: "b-only", Status: "open", Labels: []string{"project-b-gate"}},
		{ID: "safe", Status: "open"},
	}

	assertIDs := func(projectDir string, want []string) error {
		for range 100 {
			filtered := filterAssignableActionableRecommendationsForProject(projectDir, recommendations, 0)
			got := make([]string, 0, len(filtered))
			for _, recommendation := range filtered {
				got = append(got, recommendation.ID)
			}
			if !reflect.DeepEqual(got, want) {
				return fmt.Errorf("project %s filtered IDs = %v, want %v", projectDir, got, want)
			}
		}
		return nil
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, check := range []struct {
		project string
		want    []string
	}{
		{project: projectA, want: []string{"b-only", "safe"}},
		{project: projectB, want: []string{"a-only", "safe"}},
	} {
		check := check
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- assertIDs(check.project, check.want)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestGetAssignCanceledContextReturnsStructuredFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	output, err := GetAssign(ctx, AssignOptions{Session: "unused"}, nil)
	if err != nil {
		t.Fatalf("GetAssign returned transport error: %v", err)
	}
	if output == nil || output.Success || output.ErrorCode != ErrCodeTimeout || !strings.Contains(output.Error, "canceled") {
		t.Fatalf("canceled assignment output = %+v", output)
	}
}

func TestSetAssignErrorClassifiesWrappedCancellationAsTimeout(t *testing.T) {
	for _, cause := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		errors.Join(context.Canceled, bv.ErrNotInstalled),
	} {
		output := &AssignOutput{RobotResponse: newRobotResponse(true)}
		setAssignError(output, fmt.Errorf("observe candidates: %w", cause), "internal hint")
		if output.Success || output.ErrorCode != ErrCodeTimeout || output.Hint != "Retry the command after cancellation" {
			t.Fatalf("setAssignError(%v) = %+v, want TIMEOUT failure", cause, output.RobotResponse)
		}
	}
}

func TestSetAssignErrorClassifiesMissingDependenciesAndInternalFailures(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode string
	}{
		{
			name:     "bv missing",
			err:      fmt.Errorf("triage failed: %w", bv.ErrNotInstalled),
			wantCode: robot.ErrCodeDependencyMissing,
		},
		{
			name:     "br missing",
			err:      fmt.Errorf("ready scan failed: %w", &exec.Error{Name: "br", Err: exec.ErrNotFound}),
			wantCode: robot.ErrCodeDependencyMissing,
		},
		{
			name:     "malformed output",
			err:      errors.New("malformed bv output"),
			wantCode: robot.ErrCodeInternalError,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := &AssignOutput{RobotResponse: newRobotResponse(true)}
			setAssignError(output, test.err, "repair assignment dependencies")
			if output.Success || output.ErrorCode != test.wantCode || output.Hint != "repair assignment dependencies" {
				t.Fatalf("setAssignError(%v) = %+v, want %s failure", test.err, output.RobotResponse, test.wantCode)
			}
		})
	}
}

// GH #283: a tracker or checkout that changed while work was read is a typed,
// retryable refusal, not an INTERNAL_ERROR with the code buried in its text.
func TestSetAssignErrorTypesStaleWorkSource(t *testing.T) {
	output := &AssignOutput{RobotResponse: newRobotResponse(true)}
	err := fmt.Errorf("read actionable Beads work: %w", &worksource.StaleError{Reason: "tracker changed during source capture"})
	setAssignError(output, err, "generic hint")
	if output.Success || output.ErrorCode != ErrCodeStaleWorkCoordination || output.Hint != staleWorkHint {
		t.Fatalf("setAssignError(stale) = %+v, want %s with the stale-work hint", output.RobotResponse, ErrCodeStaleWorkCoordination)
	}
}

// Wiring proof for bd-ws2-wire-or-delete-ykmcz.4: --dist-strategy /
// --strategy route through the real graph-aware planner in internal/assign,
// "simple" is the honest name for the historical sequential pairing, and the
// default is pinned so nothing changes silently for users who never pass the
// flag.

// TestAssignStrategyDefaultPinnedToSimple pins the default strategy name.
// The historical default ("balanced") silently behaved as sequential pairing;
// after wiring the real planners, the default must be the honest name for
// that same sequential behavior so users who never pass --strategy or
// --dist-strategy see no silent change in assignment output.
func TestAssignStrategyDefaultPinnedToSimple(t *testing.T) {
	if assignStrategyDefault != "simple" {
		t.Fatalf("assignStrategyDefault = %q, want %q (changing this silently changes default assignment output; see CHANGELOG)", assignStrategyDefault, "simple")
	}
	if got := normalizeAssignStrategy(""); got != "simple" {
		t.Fatalf("normalizeAssignStrategy(\"\") = %q, want %q", got, "simple")
	}
	if got := normalizeAssignStrategy("  Quality "); got != "quality" {
		t.Fatalf("normalizeAssignStrategy(\"  Quality \") = %q, want %q", got, "quality")
	}
	for _, name := range []string{"simple", "balanced", "speed", "quality", "dependency"} {
		if !isValidAssignStrategy(name) {
			t.Errorf("isValidAssignStrategy(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "graph", "ready", "round-robin"} {
		if isValidAssignStrategy(name) {
			t.Errorf("isValidAssignStrategy(%q) = true, want false", name)
		}
	}
}

// strategyFixture returns agents and beads crafted so the strategies provably
// disagree. Capability matrix (assign.DefaultCapabilities):
//
//	bug:           claude 0.80, codex 0.90, gemini 0.75
//	documentation: claude 0.85, codex 0.70, gemini 0.90
//	refactor:      claude 0.95, codex 0.75, gemini 0.75
//
// Priorities are distinct so the planner's priority pre-sort is deterministic.
func strategyFixture() ([]assignAgentInfo, []bv.BeadPreview, []string) {
	agents := []assignAgentInfo{
		{paneID: "%1", paneTarget: "0.1", agentType: "claude", state: "idle"},
		{paneID: "%2", paneTarget: "0.2", agentType: "codex", state: "idle"},
		{paneID: "%3", paneTarget: "0.3", agentType: "gemini", state: "idle"},
	}
	beads := []bv.BeadPreview{
		{ID: "bd-bug", Title: "Fix crash bug", Priority: "P0"},
		{ID: "bd-doc", Title: "Write documentation guide", Priority: "P1"},
		{ID: "bd-ref", Title: "Refactor storage layer", Priority: "P2"},
	}
	idle := []string{"%1", "%2", "%3"}
	return agents, beads, idle
}

func pairingOf(recs []AssignRecommend) map[string]string {
	pairing := make(map[string]string, len(recs))
	for _, rec := range recs {
		pairing[rec.AssignBead] = rec.PaneID
	}
	return pairing
}

// TestPlanAssignments_SimpleIsSequential pins the "simple" strategy to the
// historical sequential behavior: next ready bead to next idle agent,
// regardless of capability fit.
func TestPlanAssignments_SimpleIsSequential(t *testing.T) {
	agents, beads, idle := strategyFixture()
	recs := planAssignments(agents, beads, nil, "simple", idle)
	if len(recs) != 3 {
		t.Fatalf("expected 3 recommendations, got %d", len(recs))
	}
	want := map[string]string{"bd-bug": "%1", "bd-doc": "%2", "bd-ref": "%3"}
	got := pairingOf(recs)
	for bead, pane := range want {
		if got[bead] != pane {
			t.Errorf("simple pairing %s -> %s, want %s (sequential)", bead, got[bead], pane)
		}
	}
	for _, rec := range recs {
		if !strings.Contains(rec.Reasoning, "simple sequential pairing") {
			t.Errorf("simple reasoning %q must name the sequential pairing", rec.Reasoning)
		}
	}
}

// TestPlanAssignments_QualityDiffersFromSimple proves the planner is actually
// running: quality sends each bead to the best-scoring agent, which is a
// provably different plan from sequential pairing on the same fixture.
func TestPlanAssignments_QualityDiffersFromSimple(t *testing.T) {
	agents, beads, idle := strategyFixture()

	simple := pairingOf(planAssignments(agents, beads, nil, "simple", idle))
	quality := pairingOf(planAssignments(agents, beads, nil, "quality", idle))

	// Best capability matches: bug -> codex, docs -> gemini, refactor -> claude.
	want := map[string]string{"bd-bug": "%2", "bd-doc": "%3", "bd-ref": "%1"}
	for bead, pane := range want {
		if quality[bead] != pane {
			t.Errorf("quality pairing %s -> %s, want %s (capability argmax)", bead, quality[bead], pane)
		}
	}

	if quality["bd-bug"] == simple["bd-bug"] && quality["bd-doc"] == simple["bd-doc"] && quality["bd-ref"] == simple["bd-ref"] {
		t.Fatal("quality plan must differ from the sequential simple plan on this fixture")
	}
}

// TestPlanAssignments_BalancedDiffersFromSimple proves "balanced" no longer
// silently means sequential: it scores pairings against the capability matrix.
func TestPlanAssignments_BalancedDiffersFromSimple(t *testing.T) {
	agents, beads, idle := strategyFixture()

	simple := pairingOf(planAssignments(agents, beads, nil, "simple", idle))
	balanced := pairingOf(planAssignments(agents, beads, nil, "balanced", idle))

	// Balanced spreads work by assignment count then score; with equal counts
	// the P0 bug goes to codex (0.90 beats claude's 0.80) — not to claude as
	// the sequential pairing would.
	if balanced["bd-bug"] != "%2" {
		t.Errorf("balanced sends bd-bug to %s, want %%2 (codex, best bug score)", balanced["bd-bug"])
	}
	if simple["bd-bug"] != "%1" {
		t.Fatalf("fixture broke: simple should send bd-bug to %%1, got %s", simple["bd-bug"])
	}
	if balanced["bd-bug"] == simple["bd-bug"] {
		t.Fatal("balanced plan must differ from the sequential simple plan on this fixture")
	}
}

// TestPlanAssignments_SpeedDiffersFromQuality proves the strategies disagree
// with each other, not just with simple: speed takes the first acceptable
// agent in pane order while quality takes the capability argmax.
func TestPlanAssignments_SpeedDiffersFromQuality(t *testing.T) {
	agents, beads, idle := strategyFixture()

	speed := pairingOf(planAssignments(agents, beads, nil, "speed", idle))
	quality := pairingOf(planAssignments(agents, beads, nil, "quality", idle))

	// Speed: the P0 bug goes to the first available agent (claude), even
	// though codex scores higher.
	if speed["bd-bug"] != "%1" {
		t.Errorf("speed sends bd-bug to %s, want %%1 (first available agent)", speed["bd-bug"])
	}
	if quality["bd-bug"] != "%2" {
		t.Errorf("quality sends bd-bug to %s, want %%2 (capability argmax)", quality["bd-bug"])
	}
	if speed["bd-bug"] == quality["bd-bug"] {
		t.Fatal("speed and quality must disagree on bd-bug for this fixture")
	}
}

// TestPlanAssignments_DependencyIsGraphAware proves the dependency strategy
// consumes the bead graph's unblocks fan-out — a signal the pre-wire
// sequential code never even received.
func TestPlanAssignments_DependencyIsGraphAware(t *testing.T) {
	agents := []assignAgentInfo{
		{paneID: "%1", paneTarget: "0.1", agentType: "claude", state: "idle"},
	}
	idle := []string{"%1"}
	beads := []bv.BeadPreview{
		{ID: "bd-leaf", Title: "Improve pipeline logging", Priority: "P2"},
		{ID: "bd-blocker", Title: "Improve cache layer", Priority: "P2"},
	}
	unblocks := map[string][]string{
		"bd-blocker": {"bd-a", "bd-b", "bd-c"},
	}

	simple := pairingOf(planAssignments(agents, beads, unblocks, "simple", idle))
	dependency := pairingOf(planAssignments(agents, beads, unblocks, "dependency", idle))

	if _, ok := simple["bd-leaf"]; !ok {
		t.Fatalf("simple should assign the first listed bead bd-leaf, got %v", simple)
	}
	if _, ok := dependency["bd-blocker"]; !ok {
		t.Fatalf("dependency should assign the blocker that unblocks 3 items, got %v", dependency)
	}
	if _, stillLeaf := dependency["bd-leaf"]; stillLeaf {
		t.Fatal("dependency assigned the leaf bead over the blocker; unblocks signal was ignored")
	}
}

// TestPlanAssignments_ConfidenceComesFromPlanner asserts confidence is the
// planner's number, propagated instead of discarded.
func TestPlanAssignments_ConfidenceComesFromPlanner(t *testing.T) {
	agents, beads, idle := strategyFixture()

	quality := planAssignments(agents, beads, nil, "quality", idle)
	for _, rec := range quality {
		if rec.AssignBead == "bd-bug" {
			// Quality confidence equals the raw pair score: codex bug = 0.90.
			if rec.Confidence != 0.90 {
				t.Errorf("quality confidence for bd-bug = %v, want 0.90 (codex bug capability)", rec.Confidence)
			}
		}
	}

	speed := planAssignments(agents, beads, nil, "speed", idle)
	for _, rec := range speed {
		if rec.AssignBead == "bd-bug" {
			// Speed confidence uses the planner's (score+0.9)/2 formula:
			// claude bug 0.80 -> 0.85 (allow float64 rounding).
			if rec.Confidence < 0.8499 || rec.Confidence > 0.8501 {
				t.Errorf("speed confidence for bd-bug = %v, want ~0.85 ((0.80+0.90)/2)", rec.Confidence)
			}
		}
	}
}

// TestPlanAssignments_OneBeadPerPane pins the bulk-assign contract: the
// planner adapter never recommends two beads for the same pane.
func TestPlanAssignments_OneBeadPerPane(t *testing.T) {
	agents := []assignAgentInfo{
		{paneID: "%1", paneTarget: "0.1", agentType: "claude", state: "idle"},
		{paneID: "%2", paneTarget: "0.2", agentType: "codex", state: "idle"},
	}
	idle := []string{"%1", "%2"}
	beads := []bv.BeadPreview{
		{ID: "bd-1", Title: "Refactor storage layer", Priority: "P0"},
		{ID: "bd-2", Title: "Refactor config loader", Priority: "P1"},
		{ID: "bd-3", Title: "Refactor cache layer", Priority: "P2"},
		{ID: "bd-4", Title: "Refactor auth flow", Priority: "P3"},
	}
	for _, strategy := range []string{"simple", "balanced", "speed", "quality", "dependency"} {
		recs := planAssignments(agents, beads, nil, strategy, idle)
		seenPane := make(map[string]string)
		seenBead := make(map[string]bool)
		for _, rec := range recs {
			if prev, dup := seenPane[rec.PaneID]; dup {
				t.Errorf("strategy %s recommended pane %s twice (%s and %s)", strategy, rec.PaneID, prev, rec.AssignBead)
			}
			seenPane[rec.PaneID] = rec.AssignBead
			if seenBead[rec.AssignBead] {
				t.Errorf("strategy %s recommended bead %s twice", strategy, rec.AssignBead)
			}
			seenBead[rec.AssignBead] = true
		}
		if len(recs) > len(agents) {
			t.Errorf("strategy %s produced %d recommendations for %d panes", strategy, len(recs), len(agents))
		}
	}
}
