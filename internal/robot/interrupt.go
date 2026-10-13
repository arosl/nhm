// Derived from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/interrupt.go at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: the types only, with the
// fields nhm fills (no redaction summary; last_output stays empty, as
// nhm reads no screen); the method names; nhm's per-agent results under
// "_nhm".

package robot

import "time"

// InterruptOutput is the structured output for --robot-interrupt
type InterruptOutput struct {
	RobotResponse
	Session        string               `json:"session"`
	InterruptedAt  time.Time            `json:"interrupted_at"`
	CompletedAt    time.Time            `json:"completed_at"`
	Interrupted    []string             `json:"interrupted"`
	PreviousStates map[string]PaneState `json:"previous_states"`
	Method         string               `json:"method"`
	MessageSent    bool                 `json:"message_sent"`
	Message        string               `json:"message,omitempty"`
	Warnings       []string             `json:"warnings,omitempty"`
	ReadyForInput  []string             `json:"ready_for_input"`
	Failed         []InterruptError     `json:"failed"`
	TimeoutMs      int                  `json:"timeout_ms"`
	TimedOut       bool                 `json:"timed_out"`
	DryRun         bool                 `json:"dry_run,omitempty"`
	WouldAffect    []string             `json:"would_affect,omitempty"`
	// Operation is the durable operation, present when the caller gave
	// --op-id.
	Operation *OperationInfo `json:"operation,omitempty"`
	NHM       any            `json:"_nhm,omitempty"`
}

// PaneState captures the state of a pane before interruption
type PaneState struct {
	State                 string  `json:"state"`       // active, idle, error, unknown
	LastOutput            string  `json:"last_output"` // Truncated last output (for context)
	AgentType             string  `json:"agent_type"`  // claude, codex, gemini, user, unknown
	ObservationFreshness  string  `json:"observation_freshness"`
	ObservationConfidence float64 `json:"observation_confidence"`
	ObservedAt            string  `json:"observed_at"`
	ObservationError      string  `json:"observation_error,omitempty"`
}

// InterruptError represents a failed interrupt attempt
type InterruptError struct {
	Pane   string `json:"pane"`
	Reason string `json:"reason"`
}

// Interrupt methods, ntm's.
const (
	InterruptMethodCtrlC         = "ctrl_c"
	InterruptMethodCtrlCThenSend = "ctrl_c_then_send"
)
