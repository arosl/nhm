// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/robot/assign.go
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md. Local
// changes: the output types of --robot-assign only (AssignOutput,
// AssignRecommend, BlockedBead, UnassignableBead, AssignSummary,
// AssignAgentHints), with ntm's JSON keys; PaneID is the herdr pane id and
// PaneTarget the slot N.M; NeverNull is ntm's arrays-never-null rule
// (NormalizeArraysNeverNull, which ntm's encoder runs on every answer) for
// this type (nhm-y55).

package robot

import (
	"time"

	"github.com/arosl/nhm/internal/integrations/worksource"
)

// AssignOutput is the structured output for --robot-assign
type AssignOutput struct {
	RobotResponse
	Session           string             `json:"session"`
	Strategy          string             `json:"strategy"`
	GeneratedAt       time.Time          `json:"generated_at"`
	Recommendations   []AssignRecommend  `json:"recommendations"`
	BlockedBeads      []BlockedBead      `json:"blocked_beads"`
	IdleAgents        []string           `json:"idle_agents"`
	UnassignableBeads []UnassignableBead `json:"unassignable_beads,omitempty"`
	Summary           AssignSummary      `json:"summary"`
	AgentHints        *AssignAgentHints  `json:"_agent_hints,omitempty"`
	// WorkSourceMismatch is the STALE_WORK_COORDINATION remediation receipt.
	WorkSourceMismatch *worksource.StaleError `json:"work_source_mismatch,omitempty"`
}

// NeverNull gives every list of the answer that is nil an empty value, as
// ntm's encoder does before it writes an answer: a caller reads [], never
// null. A list with omitempty stays out either way.
func (o *AssignOutput) NeverNull() {
	if o.Recommendations == nil {
		o.Recommendations = []AssignRecommend{}
	}
	if o.BlockedBeads == nil {
		o.BlockedBeads = []BlockedBead{}
	}
	for i := range o.BlockedBeads {
		if o.BlockedBeads[i].BlockedBy == nil {
			o.BlockedBeads[i].BlockedBy = []string{}
		}
	}
	if o.IdleAgents == nil {
		o.IdleAgents = []string{}
	}
}

// AssignRecommend is a single assignment recommendation
type AssignRecommend struct {
	PaneID     string  `json:"pane_id"`     // The agent's herdr pane id (e.g., "w1:p3")
	PaneTarget string  `json:"pane_target"` // The agent's slot N.M (e.g., "1.2")
	AgentType  string  `json:"agent_type"`  // claude, codex, gemini
	Model      string  `json:"model,omitempty"`
	AssignBead string  `json:"assign_bead"` // Bead ID to assign
	BeadTitle  string  `json:"bead_title"`
	Priority   string  `json:"priority"`   // P0-P4
	Confidence float64 `json:"confidence"` // 0.0-1.0
	Reasoning  string  `json:"reasoning"`
}

// BlockedBead represents a bead that can't be assigned due to dependencies
type BlockedBead struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	BlockedBy []string `json:"blocked_by"`
}

// UnassignableBead represents a bead that can't be assigned for other reasons
type UnassignableBead struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

// AssignSummary provides assignment statistics
type AssignSummary struct {
	TotalAgents       int `json:"total_agents"`
	IdleAgents        int `json:"idle_agents"`
	WorkingAgents     int `json:"working_agents"`
	ReadyBeads        int `json:"ready_beads"`
	BlockedBeads      int `json:"blocked_beads"`
	Recommendations   int `json:"recommendations"`
	UnassignableBeads int `json:"unassignable_beads"`
}

// AssignAgentHints provides actionable suggestions for AI agents
type AssignAgentHints struct {
	Summary           string   `json:"summary,omitempty"`
	SuggestedCommands []string `json:"suggested_commands,omitempty"`
	Warnings          []string `json:"warnings,omitempty"`
}
