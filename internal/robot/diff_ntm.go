// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/robot.go (DiffOutput, DiffTimeframe, DiffFiles,
// DiffConflict, DiffAgentInfo, DiffAgentHints) at commit 9d5ed977; see
// LICENSE in this directory and PROVENANCE.md. Local changes: only the
// output types; the session read (GetDiff) is nhm's, on herdr's panes
// (internal/cli).

package robot

// DiffOutput is the structured output for --robot-diff.
type DiffOutput struct {
	RobotResponse
	Session       string          `json:"session"`
	Timeframe     DiffTimeframe   `json:"timeframe"`
	Files         DiffFiles       `json:"files"`
	AgentActivity []DiffAgentInfo `json:"agent_activity"`
	AgentHints    *DiffAgentHints `json:"_agent_hints,omitempty"`
}

// DiffTimeframe describes the analysis time window.
type DiffTimeframe struct {
	Since      string `json:"since"`       // Duration string (e.g., "15m")
	SinceTS    string `json:"since_ts"`    // RFC3339 timestamp
	CapturedAt string `json:"captured_at"` // RFC3339 timestamp
}

// DiffFiles categorizes files by modification status.
type DiffFiles struct {
	Modified           []string       `json:"modified"`
	PotentialConflicts []DiffConflict `json:"potential_conflicts"`
	Clean              []string       `json:"clean"`
}

// DiffConflict represents a potential file conflict.
type DiffConflict struct {
	File            string   `json:"file"`
	LikelyModifiers []string `json:"likely_modifiers"`
	Reason          string   `json:"reason"`
	Confidence      float64  `json:"confidence"`
}

// DiffAgentInfo provides activity info for a single agent pane.
type DiffAgentInfo struct {
	Pane        string `json:"pane"`
	AgentType   string `json:"agent_type"`
	State       string `json:"state"`
	OutputLines int    `json:"output_lines"`
	ActiveTime  string `json:"active_time,omitempty"`
}

// DiffAgentHints provides actionable hints for AI agents.
type DiffAgentHints struct {
	Summary          string   `json:"summary"`
	ConflictWarnings []string `json:"conflict_warnings,omitempty"`
	SuggestedActions []string `json:"suggested_actions,omitempty"`
}
