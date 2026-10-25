// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/robot/types.go,
// internal/robot/robot.go, internal/robot/capabilities.go and
// internal/robot/registry.go at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: only the types of the read
// surfaces nhm serves (--robot-status, --robot-snapshot, --robot-activity,
// --robot-tail, --robot-capabilities, --robot-version), each with ntm's
// JSON keys and with the optional fields nhm cannot fill left out; NHM
// fields hold nhm's extras under "_nhm"; NewResponse takes the output
// format.

// Package robot holds ntm's robot output types, which nhm's --robot-*
// flags return (plan-v8 2.12). The JSON keys and the meaning of every
// field are ntm's at the tracked commit; nhm adds nothing outside "_nhm".
package robot

import "time"

// EnvelopeVersion is the version of the robot envelope.
const EnvelopeVersion = "1.0.0"

// Schema ids and versions, as ntm writes them.
const (
	StatusSchemaID           = "ntm:robot:status:v1"
	StatusSchemaVersion      = "ntm.robot.status.v2"
	SnapshotSchemaID         = "ntm:robot:snapshot:v1"
	SnapshotSchemaVersion    = "ntm.robot.snapshot.v1"
	AttentionContractVersion = "1.2.0"
)

// RobotResponse is the envelope every robot output embeds.
type RobotResponse struct {
	Success      bool          `json:"success"`
	Timestamp    string        `json:"timestamp"`
	Version      string        `json:"version,omitempty"`
	OutputFormat string        `json:"output_format,omitempty"`
	Meta         *ResponseMeta `json:"_meta,omitempty"`
	Error        string        `json:"error,omitempty"`
	ErrorCode    string        `json:"error_code,omitempty"`
	Hint         string        `json:"hint,omitempty"`
}

// ResponseMeta is optional metadata about a response.
type ResponseMeta struct {
	DurationMs int64  `json:"duration_ms,omitempty"`
	ExitCode   int    `json:"exit_code,omitempty"`
	Command    string `json:"command,omitempty"`
}

// NewResponse returns an envelope stamped now.
func NewResponse(success bool, format string, now time.Time) RobotResponse {
	return RobotResponse{Success: success, Timestamp: now.UTC().Format(time.RFC3339),
		Version: EnvelopeVersion, OutputFormat: format}
}

// Error codes nhm returns, ntm's names.
const (
	ErrCodeSessionNotFound   = "SESSION_NOT_FOUND"
	ErrCodePaneNotFound      = "PANE_NOT_FOUND"
	ErrCodeInternalError     = "INTERNAL_ERROR"
	ErrCodeInvalidFlag       = "INVALID_FLAG"
	ErrCodeDependencyMissing = "DEPENDENCY_MISSING"
	// ErrCodeResourceBusy is ntm's code for a spawn that its admission
	// refuses.
	ErrCodeResourceBusy = "RESOURCE_BUSY"
	// ErrCodeDestructiveCommandBlocked is ntm's code for a send that its
	// guard refused; nhm's guard refuses a prompt that the agent would run
	// as a shell command (nhm-ubi).
	ErrCodeDestructiveCommandBlocked = "DESTRUCTIVE_COMMAND_BLOCKED"
)

// SystemInfo is the runtime block of --robot-status. TmuxOK is false:
// nhm runs on herdr, which "_nhm" describes.
type SystemInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	TmuxOK    bool   `json:"tmux_available"`
}

// VersionOutput is --robot-version: ntm's (internal/robot/robot.go).
type VersionOutput struct {
	RobotResponse
	System SystemInfo `json:"system"`
	NHM    any        `json:"_nhm,omitempty"`
}

// StatusOutput is --robot-status.
type StatusOutput struct {
	RobotResponse
	SchemaID        string                `json:"schema_id"`
	SchemaVersion   string                `json:"schema_version"`
	GeneratedAt     time.Time             `json:"generated_at"`
	OverallStatus   string                `json:"overall_status,omitempty"`
	System          SystemInfo            `json:"system"`
	Sessions        []StatusSessionHeader `json:"sessions"`
	Pagination      *PaginationInfo       `json:"pagination,omitempty"`
	AgentHints      *AgentHints           `json:"_agent_hints,omitempty"`
	Summary         StatusSummary         `json:"summary"`
	DegradedSources []string              `json:"degraded_sources,omitempty"`
	AgentMail       *AgentMailSummary     `json:"agent_mail,omitempty"`
	NHM             any                   `json:"_nhm,omitempty"`
}

// StatusSessionHeader is one session (a herdr workspace) in --robot-status.
type StatusSessionHeader struct {
	Name       string              `json:"name"`
	Attached   bool                `json:"attached,omitempty"`
	AgentCount int                 `json:"agent_count"`
	Health     StatusSessionHealth `json:"health"`
}

// StatusSessionHealth is a session's cheap health.
type StatusSessionHealth struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// StatusSummary counts sessions and agents.
type StatusSummary struct {
	TotalSessions    int            `json:"total_sessions"`
	TotalAgents      int            `json:"total_agents"`
	AttachedCount    int            `json:"attached_count"`
	ClaudeCount      int            `json:"claude_count"`
	CodexCount       int            `json:"codex_count"`
	GeminiCount      int            `json:"gemini_count"`
	AntigravityCount int            `json:"antigravity_count"`
	GrokCount        int            `json:"grok_count"`
	CursorCount      int            `json:"cursor_count"`
	WindsurfCount    int            `json:"windsurf_count"`
	AiderCount       int            `json:"aider_count"`
	OpencodeCount    int            `json:"opencode_count"`
	OmpCount         int            `json:"omp_count"`
	OllamaCount      int            `json:"ollama_count"`
	AgentsByState    map[string]int `json:"agents_by_state"`
	AgentsByType     map[string]int `json:"agents_by_type"`
	ReadyWork        int            `json:"ready_work"`
	InProgress       int            `json:"in_progress"`
	HealthScore      float64        `json:"health_score"`
	HealthStatus     string         `json:"health_status,omitempty"`
}

// AgentMailSummary is Agent Mail's state in --robot-status.
type AgentMailSummary struct {
	Available          bool   `json:"available"`
	ServerURL          string `json:"server_url,omitempty"`
	SessionsRegistered int    `json:"sessions_registered,omitempty"`
	TotalUnread        int    `json:"total_unread,omitempty"`
	UrgentMessages     int    `json:"urgent_messages,omitempty"`
	TotalLocks         int    `json:"total_locks,omitempty"`
	Error              string `json:"error,omitempty"`
}

// SnapshotOutput is --robot-snapshot.
type SnapshotOutput struct {
	RobotResponse
	SchemaID                 string                   `json:"schema_id"`
	SchemaVersion            string                   `json:"schema_version"`
	Timestamp                string                   `json:"ts"`
	AttentionContractVersion string                   `json:"attention_contract_version"`
	LatestCursor             int64                    `json:"latest_cursor"`
	ReplayWindow             SnapshotReplayWindowInfo `json:"replay_window"`
	Sessions                 []SnapshotSession        `json:"sessions"`
	ActiveIncidents          []SnapshotIncident       `json:"active_incidents"`
	DegradedSources          []string                 `json:"degraded_sources,omitempty"`
	Pagination               *PaginationInfo          `json:"pagination,omitempty"`
	AgentHints               *AgentHints              `json:"_agent_hints,omitempty"`
	Summary                  StatusSummary            `json:"summary"`
	MailUnread               int                      `json:"mail_unread,omitempty"`
	Alerts                   []string                 `json:"alerts"`
	NHM                      any                      `json:"_nhm,omitempty"`
}

// SnapshotReplayWindowInfo describes event replay; nhm has none.
type SnapshotReplayWindowInfo struct {
	Supported    bool   `json:"supported"`
	Reason       string `json:"reason,omitempty"`
	OldestCursor int64  `json:"oldest_cursor"`
	LatestCursor int64  `json:"latest_cursor"`
	EventCount   int    `json:"event_count"`
}

// SnapshotIncident is ntm's incident shape; nhm reports none yet.
type SnapshotIncident struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Status       string   `json:"status"`
	Severity     string   `json:"severity"`
	SessionNames []string `json:"session_names"`
	AgentIDs     []string `json:"agent_ids"`
	AlertCount   int      `json:"alert_count"`
	EventCount   int      `json:"event_count"`
	StartedAt    string   `json:"started_at"`
	LastEventAt  string   `json:"last_event_at"`
}

// SnapshotSession is one session in --robot-snapshot.
type SnapshotSession struct {
	Name     string          `json:"name"`
	Attached bool            `json:"attached"`
	Agents   []SnapshotAgent `json:"agents"`
}

// SnapshotAgent is one agent in --robot-snapshot.
type SnapshotAgent struct {
	Pane             string  `json:"pane"`
	Type             string  `json:"type"`
	Variant          string  `json:"variant,omitempty"`
	TypeConfidence   float64 `json:"type_confidence"`
	TypeMethod       string  `json:"type_method"`
	State            string  `json:"state"`
	LastOutputAgeSec int     `json:"last_output_age_sec"`
	OutputTailLines  int     `json:"output_tail_lines"`
	CurrentBead      *string `json:"current_bead,omitempty"`
	PendingMail      int     `json:"pending_mail"`
	NHM              any     `json:"_nhm,omitempty"`
}

// SourceHealthEntry is the freshness of one source of an output.
type SourceHealthEntry struct {
	Source           string   `json:"source"`
	Status           string   `json:"status"` // fresh, stale, unavailable, unknown
	CollectedAt      string   `json:"collected_at"`
	FreshnessSec     int      `json:"freshness_sec"`
	StaleAfterSec    int      `json:"stale_after_sec"`
	DegradedFeatures []string `json:"degraded_features,omitempty"`
	LastError        string   `json:"last_error,omitempty"`
	LastErrorAt      string   `json:"last_error_at,omitempty"`
	Provenance       string   `json:"provenance"` // live, cached, derived
}

// ActivityOutput is --robot-activity.
type ActivityOutput struct {
	RobotResponse
	Session      string                       `json:"session"`
	CapturedAt   time.Time                    `json:"captured_at"`
	Agents       []AgentActivityInfo          `json:"agents"`
	Summary      ActivitySummary              `json:"summary"`
	SourceHealth map[string]SourceHealthEntry `json:"source_health,omitempty"`
}

// Activity states, ntm's enum.
const (
	ActivityGenerating = "GENERATING"
	ActivityWaiting    = "WAITING"
	ActivityThinking   = "THINKING"
	ActivityError      = "ERROR"
	ActivityStalled    = "STALLED"
	ActivityUnknown    = "UNKNOWN"
)

// AgentActivityInfo is one agent's activity.
type AgentActivityInfo struct {
	Pane                  string  `json:"pane"`
	PaneIdx               int     `json:"pane_idx"`
	AgentType             string  `json:"agent_type"`
	State                 string  `json:"state"`
	Confidence            float64 `json:"confidence"`
	Velocity              float64 `json:"velocity"`
	StateSince            string  `json:"state_since,omitempty"`
	CaptureCollectedAt    string  `json:"capture_collected_at,omitempty"`
	CaptureProvenance     string  `json:"capture_provenance,omitempty"`
	CaptureError          string  `json:"capture_error,omitempty"`
	ObservationState      string  `json:"observation_state"`
	ObservationFreshness  string  `json:"observation_freshness"`
	ObservationConfidence float64 `json:"observation_confidence"`
	SafeToDispatch        bool    `json:"safe_to_dispatch"`
	NHM                   any     `json:"_nhm,omitempty"`
}

// ActivitySummary counts agents by state.
type ActivitySummary struct {
	TotalAgents int            `json:"total_agents"`
	ByState     map[string]int `json:"by_state"`
}

// TailOutput is --robot-tail.
type TailOutput struct {
	RobotResponse
	Session      string                       `json:"session"`
	CapturedAt   time.Time                    `json:"captured_at"`
	Panes        map[string]PaneOutput        `json:"panes"`
	SourceHealth map[string]SourceHealthEntry `json:"source_health,omitempty"`
}

// PaneOutput is one pane's captured text.
type PaneOutput struct {
	Type                  string   `json:"type"`
	State                 string   `json:"state"` // active, idle, unknown
	Lines                 []string `json:"lines"`
	Truncated             bool     `json:"truncated"`
	ObservationState      string   `json:"observation_state"`
	ObservationFreshness  string   `json:"observation_freshness"`
	ObservationConfidence float64  `json:"observation_confidence"`
	SafeToDispatch        bool     `json:"safe_to_dispatch"`
	CaptureCollectedAt    string   `json:"capture_collected_at,omitempty"`
	CaptureProvenance     string   `json:"capture_provenance,omitempty"`
	CaptureError          string   `json:"capture_error,omitempty"`
}

// CapabilitiesOutput is --robot-capabilities.
type CapabilitiesOutput struct {
	RobotResponse
	Version    string             `json:"version"`
	Commands   []RobotCommandInfo `json:"commands"`
	Categories []string           `json:"categories"`
	NHM        any                `json:"_nhm,omitempty"`
}

// RobotCommandInfo describes one robot command. An unsupported one is
// listed with Unavailable and its reason, never left out.
type RobotCommandInfo struct {
	Name                string           `json:"name"`
	Flag                string           `json:"flag"`
	Category            string           `json:"category"`
	Summary             string           `json:"summary,omitempty"`
	Unavailable         bool             `json:"unavailable,omitempty"`
	UnavailableReason   string           `json:"unavailable_reason,omitempty"`
	OutputFormats       []string         `json:"output_formats"`
	DefaultOutputFormat string           `json:"default_output_format"`
	SchemaID            string           `json:"schema_id,omitempty"`
	SchemaSource        string           `json:"schema_source"`
	Parameters          []RobotParameter `json:"parameters,omitempty"`
}

// RobotParameter is one flag of a robot command.
type RobotParameter struct {
	Name        string `json:"name"`
	Flag        string `json:"flag"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Default     string `json:"default,omitempty"`
	Description string `json:"description"`
}

// ErrorOutput is the failure envelope of any robot flag.
type ErrorOutput struct {
	RobotResponse
}

// NewError returns a failure envelope.
func NewError(code, msg, hint, format string, now time.Time) ErrorOutput {
	r := NewResponse(false, format, now)
	r.ErrorCode, r.Error, r.Hint = code, msg, hint
	return ErrorOutput{RobotResponse: r}
}
