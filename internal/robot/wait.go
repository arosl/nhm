// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/robot/wait.go
// and internal/robot/types.go (ErrCodeTimeout, ErrCodeNotImplemented) at
// commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md. Local
// changes: the types only, with the fields nhm fills (no wait_id,
// wake_payload, cursor_info, productivity or convergence_streak); the
// "AGENT_ERROR" code named; nhm's per-agent results under "_nhm".

package robot

// Error codes of --robot-wait, ntm's.
const (
	ErrCodeTimeout        = "TIMEOUT"
	ErrCodeNotImplemented = "NOT_IMPLEMENTED"
	ErrCodeAgentError     = "AGENT_ERROR"
)

// Wait conditions nhm answers, ntm's names.
const (
	WaitConditionIdle       = "idle"
	WaitConditionComplete   = "complete"
	WaitConditionGenerating = "generating"
	WaitConditionHealthy    = "healthy"
)

// WaitResponse is the JSON output for --robot-wait.
type WaitResponse struct {
	RobotResponse
	Session       string  `json:"session"`
	Condition     string  `json:"condition"`
	WaitedSeconds float64 `json:"waited_seconds"`
	// agents is a required array and must survive the timeout path too, where
	// only AgentsPending used to be set.
	Agents        []WaitAgentInfo `json:"agents"`
	AgentsPending []string        `json:"agents_pending,omitempty"`
	NHM           any             `json:"_nhm,omitempty"`
}

// WaitAgentInfo describes an agent's state when the wait completed or timed out.
type WaitAgentInfo struct {
	Pane      string `json:"pane"`
	State     string `json:"state"`
	MetAt     string `json:"met_at,omitempty"` // RFC3339 timestamp
	AgentType string `json:"agent_type,omitempty"`
}
