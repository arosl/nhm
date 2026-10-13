// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/robot/health.go
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: the session form's types only (SessionHealthOutput,
// SessionAgentHealth, SessionHealthSummary), without the service panes
// nhm does not have; the health names as constants; nhm's per-agent
// extras under "_nhm".

package robot

import "time"

// Health values of an agent, ntm's.
const (
	HealthHealthy     = "healthy"
	HealthDegraded    = "degraded"
	HealthUnhealthy   = "unhealthy"
	HealthBlocked     = "blocked"
	HealthRateLimited = "rate_limited"
)

// SessionHealthOutput is the response format for --robot-health=SESSION
type SessionHealthOutput struct {
	RobotResponse
	Session   string               `json:"session"`
	CheckedAt time.Time            `json:"checked_at"`
	Agents    []SessionAgentHealth `json:"agents"`
	Summary   SessionHealthSummary `json:"summary"`
}

// SessionAgentHealth contains health metrics for a single agent pane
type SessionAgentHealth struct {
	Pane int `json:"pane"`
	// PaneTarget is the unambiguous selector for this pane (%ID, or W.P on a
	// multi-window session). Pane is only unique within a window, so a bare
	// index must never be used to address a pane for mutation: the selector
	// grammar reads a bare N as *window* N when the session spans windows.
	PaneTarget       string  `json:"pane_target"`
	AgentType        string  `json:"agent_type"`
	Health           string  `json:"health"`             // healthy, degraded, unhealthy, rate_limited
	IdleSinceSeconds int     `json:"idle_since_seconds"` // seconds since last pane activity
	Restarts         int     `json:"restarts"`
	LastError        string  `json:"last_error,omitempty"`
	RateLimitCount   int     `json:"rate_limit_count"`
	BackoffRemaining int     `json:"backoff_remaining"` // seconds until ready
	Confidence       float64 `json:"confidence"`
	NHM              any     `json:"_nhm,omitempty"`
}

// SessionHealthSummary contains aggregate health counts
type SessionHealthSummary struct {
	Total       int `json:"total"`
	Healthy     int `json:"healthy"`
	Degraded    int `json:"degraded"`
	Unhealthy   int `json:"unhealthy"`
	RateLimited int `json:"rate_limited"`
	// Blocked counts panes parked on an interactive gate screen — they
	// need a human keystroke and are never auto-restart candidates
	// (bd-jf22c / bd-qz5wk).
	Blocked int `json:"blocked"`
}
