// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/assign_test.go at commit 9d5ed977: the JSON tests of the
// --robot-assign output types; see LICENSE in this directory and
// PROVENANCE.md. Local changes: this header; imports; nhm's NewResponse in
// place of ntm's NewRobotResponse (nhm-y55).

package robot

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAssignOutput_JSONStructure(t *testing.T) {
	output := AssignOutput{
		RobotResponse: NewResponse(true, "json", time.Now()),
		Session:       "test-session",
		Strategy:      "balanced",
		GeneratedAt:   time.Now().UTC(),
		Recommendations: []AssignRecommend{
			{PaneID: "%12", PaneTarget: "0.2", AgentType: "claude", AssignBead: "bd-abc", BeadTitle: "Fix bug", Priority: "P1", Confidence: 0.85, Reasoning: "test"},
		},
		BlockedBeads: []BlockedBead{
			{ID: "bd-xyz", Title: "Blocked task", BlockedBy: []string{"bd-dep1"}},
		},
		IdleAgents: []string{"%12", "%13"},
		Summary: AssignSummary{
			TotalAgents:     4,
			IdleAgents:      2,
			WorkingAgents:   2,
			ReadyBeads:      5,
			Recommendations: 1,
		},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	// Check top-level fields
	requiredFields := []string{
		"success", "session", "strategy", "generated_at",
		"recommendations", "blocked_beads", "idle_agents", "summary",
	}
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("Missing field %q in JSON output", field)
		}
	}
}

func TestAssignOutput_OmitEmpty(t *testing.T) {
	output := AssignOutput{
		RobotResponse:   NewResponse(true, "json", time.Now()),
		Session:         "test",
		Recommendations: []AssignRecommend{},
		BlockedBeads:    []BlockedBead{},
		IdleAgents:      []string{},
		// UnassignableBeads is nil - should be omitted from top level
		// AgentHints is nil - should be omitted
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	// Parse to check top-level fields only (summary.unassignable_beads is always present)
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	// Top-level unassignable_beads should be omitted when nil (omitempty)
	if _, ok := parsed["unassignable_beads"]; ok {
		t.Error("top-level unassignable_beads should be omitted when nil")
	}
	// _agent_hints should be omitted when nil
	if _, ok := parsed["_agent_hints"]; ok {
		t.Error("_agent_hints should be omitted when nil")
	}
}

func TestAssignRecommend_JSONFields(t *testing.T) {
	rec := AssignRecommend{
		PaneID:     "%12",
		PaneTarget: "0.2",
		AgentType:  "claude",
		Model:      "opus-4.5",
		AssignBead: "bd-abc",
		BeadTitle:  "Fix bug",
		Priority:   "P1",
		Confidence: 0.85,
		Reasoning:  "excels at bug tasks",
	}

	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if parsed["pane_id"] != "%12" || parsed["pane_target"] != "0.2" {
		t.Errorf("pane identity = %v at %v, want %%12 at 0.2", parsed["pane_id"], parsed["pane_target"])
	}
	if _, ok := parsed["agent"]; ok {
		t.Error("legacy agent field must not be serialized")
	}
	if parsed["confidence"].(float64) != 0.85 {
		t.Errorf("confidence = %v, want 0.85", parsed["confidence"])
	}
}

func TestBlockedBeadRequiredDependencyArrayIsNeverNull(t *testing.T) {
	data, err := json.Marshal(BlockedBead{ID: "ntm-blocked", Title: "Blocked", BlockedBy: []string{}})
	if err != nil {
		t.Fatalf("marshal blocked bead: %v", err)
	}
	if strings.Contains(string(data), `"blocked_by":null`) || !strings.Contains(string(data), `"blocked_by":[]`) {
		t.Fatalf("blocked bead JSON = %s, want required empty array", data)
	}
}
