// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/robot/ack.go
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: the types only. nhm reads acknowledgment from its
// receipts and the completion observation, not from the screen.

package robot

import "time"

// AckOutput is --robot-ack's answer.
type AckOutput struct {
	RobotResponse
	Session       string            `json:"session"`
	SentAt        time.Time         `json:"sent_at"`
	CompletedAt   time.Time         `json:"completed_at"`
	Confirmations []AckConfirmation `json:"confirmations"`
	Pending       []string          `json:"pending"`
	Failed        []AckFailure      `json:"failed"`
	TimeoutMs     int               `json:"timeout_ms"`
	TimedOut      bool              `json:"timed_out"`
	NHM           any               `json:"_nhm,omitempty"`
}

// AckConfirmation is one agent that acknowledged.
type AckConfirmation struct {
	Pane      string `json:"pane"`
	AckType   string `json:"ack_type"`
	AckAt     string `json:"ack_at"`
	LatencyMs int    `json:"latency_ms"`
}

// AckFailure is one agent that cannot acknowledge.
type AckFailure struct {
	Pane   string `json:"pane"`
	Reason string `json:"reason"`
}

// Ack types, ntm's. nhm reports output_started when the turn started and
// prompt_returned when it completed.
const (
	AckPromptReturned = "prompt_returned"
	AckOutputStarted  = "output_started"
)
