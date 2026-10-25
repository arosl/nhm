// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/handoff/types.go (HandoffVersion, Handoff, TaskRecord,
// FileChanges, ReservationTransfer, ReservationSnapshot, New,
// WithGoalAndNow, WithStatus, AddTask, MarkCreated, MarkModified,
// MarkDeleted), internal/handoff/quality.go (QualityReport,
// QualityDimensionScore) and internal/handoff/constants.go (the status
// and outcome values) at commit 9d5ed977; see LICENSE in this directory
// and PROVENANCE.md. Local changes: only what summary's handoff format
// uses (the handoff verb is nhm-2p6.23); New stamps UTC (rule 8), where
// ntm stamps local time.

// Package handoff is ntm's YAML handoff format.
package handoff

import "time"

// HandoffVersion tracks the format version for backwards compatibility and migrations.
const HandoffVersion = "1.0"

// Status values for handoff status field.
const (
	// StatusComplete indicates the session completed all planned work.
	StatusComplete = "complete"
	// StatusPartial indicates the session completed some work but not all.
	StatusPartial = "partial"
	// StatusBlocked indicates the session is blocked and cannot proceed.
	StatusBlocked = "blocked"
)

// Outcome values for handoff outcome field.
const (
	// OutcomeSucceeded indicates all goals were achieved.
	OutcomeSucceeded = "SUCCEEDED"
	// OutcomePartialPlus indicates most goals achieved, some work remains.
	OutcomePartialPlus = "PARTIAL_PLUS"
	// OutcomePartialMinus indicates some goals achieved but significant work remains.
	OutcomePartialMinus = "PARTIAL_MINUS"
	// OutcomeFailed indicates the session failed to achieve its goals.
	OutcomeFailed = "FAILED"
)

// Handoff represents a complete context handoff between sessions.
// The Goal and Now fields are REQUIRED and used by the status line integration.
type Handoff struct {
	// Metadata
	Version   string    `yaml:"version"`    // Format version for migrations
	Session   string    `yaml:"session"`    // Session identifier
	Date      string    `yaml:"date"`       // Date in YYYY-MM-DD format
	CreatedAt time.Time `yaml:"created_at"` // Precise creation timestamp
	UpdatedAt time.Time `yaml:"updated_at"` // Last update timestamp

	// Status tracking
	Status  string `yaml:"status"`  // complete|partial|blocked
	Outcome string `yaml:"outcome"` // SUCCEEDED|PARTIAL_PLUS|PARTIAL_MINUS|FAILED

	// Core fields (REQUIRED for status line)
	Goal string `yaml:"goal"` // What this session accomplished - REQUIRED
	Now  string `yaml:"now"`  // What next session should do first - REQUIRED
	Test string `yaml:"test"` // Command to verify this work

	// Work tracking
	DoneThisSession []TaskRecord `yaml:"done_this_session,omitempty"`

	// Context for future self
	Blockers  []string          `yaml:"blockers,omitempty"`
	Questions []string          `yaml:"questions,omitempty"`
	Decisions map[string]string `yaml:"decisions,omitempty"`
	Findings  map[string]string `yaml:"findings,omitempty"`

	// What worked and what didn't
	Worked []string `yaml:"worked,omitempty"`
	Failed []string `yaml:"failed,omitempty"`

	// Next steps
	Next []string `yaml:"next,omitempty"`

	// File tracking
	Files FileChanges `yaml:"files,omitempty"`

	// Integration fields - populated during recovery
	ActiveBeads      []string `yaml:"active_beads,omitempty"`       // From BV
	AgentMailThreads []string `yaml:"agent_mail_threads,omitempty"` // From Agent Mail
	CMMemories       []string `yaml:"cm_memories,omitempty"`        // From CM

	// Agent info for multi-agent sessions
	AgentID   string `yaml:"agent_id,omitempty"`
	AgentType string `yaml:"agent_type,omitempty"` // cc, cod, gmi
	PaneID    string `yaml:"pane_id,omitempty"`

	// Token context at time of handoff
	TokensUsed int     `yaml:"tokens_used,omitempty"`
	TokensMax  int     `yaml:"tokens_max,omitempty"`
	TokensPct  float64 `yaml:"tokens_pct,omitempty"`

	// Machine-readable quality score for compacted handoff consumers.
	Quality *QualityReport `yaml:"quality,omitempty"`

	// File reservation transfer instructions (optional)
	ReservationTransfer *ReservationTransfer `yaml:"reservation_transfer,omitempty"`
}

// TaskRecord represents a completed task with associated file changes.
type TaskRecord struct {
	Task  string   `yaml:"task"`
	Files []string `yaml:"files,omitempty"`
}

// FileChanges tracks file modifications during a session.
type FileChanges struct {
	Created  []string `yaml:"created,omitempty"`
	Modified []string `yaml:"modified,omitempty"`
	Deleted  []string `yaml:"deleted,omitempty"`
}

// ReservationTransfer describes how to transfer file reservations to a new session.
type ReservationTransfer struct {
	FromAgent          string                `yaml:"from_agent,omitempty"`
	ProjectKey         string                `yaml:"project_key,omitempty"`
	TTLSeconds         int                   `yaml:"ttl_seconds,omitempty"`
	GracePeriodSeconds int                   `yaml:"grace_period_seconds,omitempty"`
	CreatedAt          time.Time             `yaml:"created_at,omitempty"`
	Reservations       []ReservationSnapshot `yaml:"reservations,omitempty"`
}

// ReservationSnapshot captures a single reservation for transfer.
type ReservationSnapshot struct {
	ID          int       `yaml:"id,omitempty"`
	ProjectID   int       `yaml:"project_id,omitempty"`
	AgentName   string    `yaml:"agent_name,omitempty"`
	CreatedAt   time.Time `yaml:"created_at,omitempty"`
	PathPattern string    `yaml:"path_pattern"`
	Exclusive   bool      `yaml:"exclusive,omitempty"`
	Reason      string    `yaml:"reason,omitempty"`
	ExpiresAt   time.Time `yaml:"expires_at,omitempty"`
}

// QualityReport is a deterministic handoff quality score.
type QualityReport struct {
	Score      int                     `yaml:"score" json:"score"`
	Status     string                  `yaml:"status" json:"status"`
	ScoredAt   time.Time               `yaml:"scored_at" json:"scored_at"`
	Dimensions []QualityDimensionScore `yaml:"dimensions" json:"dimensions"`
	Reasons    []string                `yaml:"reasons,omitempty" json:"reasons,omitempty"`
	Hints      []string                `yaml:"hints,omitempty" json:"hints,omitempty"`
}

// QualityDimensionScore is a single deterministic scoring dimension.
type QualityDimensionScore struct {
	Name    string   `yaml:"name" json:"name"`
	Score   int      `yaml:"score" json:"score"`
	Reasons []string `yaml:"reasons,omitempty" json:"reasons,omitempty"`
	Hints   []string `yaml:"hints,omitempty" json:"hints,omitempty"`
}

// New creates a new Handoff with defaults populated.
func New(session string) *Handoff {
	now := time.Now().UTC()
	return &Handoff{
		Version:   HandoffVersion,
		Session:   session,
		Date:      now.Format("2006-01-02"),
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// WithGoalAndNow is a convenience method for setting the required fields.
func (h *Handoff) WithGoalAndNow(goal, now string) *Handoff {
	h.Goal = goal
	h.Now = now
	return h
}

// WithStatus sets the status and outcome fields.
func (h *Handoff) WithStatus(status, outcome string) *Handoff {
	h.Status = status
	h.Outcome = outcome
	return h
}

// AddTask adds a completed task record.
func (h *Handoff) AddTask(task string, files ...string) *Handoff {
	h.DoneThisSession = append(h.DoneThisSession, TaskRecord{
		Task:  task,
		Files: files,
	})
	return h
}

// MarkCreated adds a file to the created list.
func (h *Handoff) MarkCreated(files ...string) *Handoff {
	h.Files.Created = append(h.Files.Created, files...)
	return h
}

// MarkModified adds a file to the modified list.
func (h *Handoff) MarkModified(files ...string) *Handoff {
	h.Files.Modified = append(h.Files.Modified, files...)
	return h
}

// MarkDeleted adds a file to the deleted list.
func (h *Handoff) MarkDeleted(files ...string) *Handoff {
	h.Files.Deleted = append(h.Files.Deleted, files...)
	return h
}
