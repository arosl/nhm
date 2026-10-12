// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/robot/robot.go
// (SendOutput, SendError, RedactionSummary, CASSInjectionInfo),
// internal/robot/cm_inject.go (CMInjectionInfo),
// internal/robot/operation_idempotency.go (the admission states, the error
// codes, OperationAdmission, OperationInfo, SendReceiptOutput) and
// internal/robot/send_idempotency.go (SendReceiptOutcome) at commit
// 9d5ed977; see LICENSE in this directory and PROVENANCE.md. Local
// changes: only the fields nhm fills; CMInjectionInfo without its
// unexported client; nhm's receipt fields under "_nhm".

package robot

import "time"

// Admission states of a target, ntm's.
const (
	AdmissionNotAttempted = "not_attempted"
	AdmissionSubmitted    = "submitted"
	AdmissionRejected     = "rejected"
	AdmissionUnknown      = "unknown"
)

// Error codes of the durable operations, ntm's.
const (
	ErrCodeIdempotencyConflict     = "IDEMPOTENCY_CONFLICT"
	ErrCodeOperationInProgress     = "OPERATION_IN_PROGRESS"
	ErrCodeOperationOutcomeUnknown = "OPERATION_OUTCOME_UNKNOWN"
	ErrCodeNotFound                = "NOT_FOUND"
)

// ErrCodeNotOwner is nhm's: the herdr instance is owned by ACFS's Bash
// (plan-v8 section 1, F.24), so nhm's mutators refuse.
const ErrCodeNotOwner = "NOT_OWNER"

// Operation statuses, ntm's.
const (
	OperationInProgress = "in_progress"
	OperationCompleted  = "completed"
)

// SendError is one target that a send did not reach.
type SendError struct {
	Pane  string `json:"pane"`
	Error string `json:"error"`
}

// RedactionSummary is ntm's redaction report; nhm does not redact.
type RedactionSummary struct {
	Mode     string `json:"mode"`
	Findings int    `json:"findings"`
}

// CASSInjectionInfo reports CASS context injection.
type CASSInjectionInfo struct {
	Enabled       bool   `json:"enabled"`
	ItemsFound    int    `json:"items_found"`
	ItemsInjected int    `json:"items_injected"`
	TokensAdded   int    `json:"tokens_added"`
	SkippedReason string `json:"skipped_reason,omitempty"`
}

// CMInjectionInfo reports CM memory injection.
type CMInjectionInfo struct {
	Enabled       bool     `json:"enabled"`
	RulesInjected []string `json:"rules_injected,omitempty"`
	TokensAdded   int      `json:"tokens_added"`
	SkippedReason string   `json:"skipped_reason,omitempty"`
}

// OperationAdmission is one target's admission.
type OperationAdmission struct {
	Target string `json:"target"`
	State  string `json:"state"`
	Error  string `json:"error,omitempty"`
}

// OperationInfo is the durable operation of a send.
type OperationInfo struct {
	OperationID       string               `json:"operation_id"`
	Kind              string               `json:"kind"`
	Status            string               `json:"status"`
	Replayed          bool                 `json:"replayed,omitempty"`
	PayloadSHA256     string               `json:"payload_sha256"`
	PayloadBytes      int64                `json:"payload_bytes"`
	Admissions        []OperationAdmission `json:"admissions,omitempty"`
	CreatedAt         time.Time            `json:"created_at"`
	DispatchStartedAt *time.Time           `json:"dispatch_started_at,omitempty"`
	CompletedAt       *time.Time           `json:"completed_at,omitempty"`
}

// SendOutput is --robot-send's answer.
type SendOutput struct {
	RobotResponse
	Session         string             `json:"session"`
	SentAt          time.Time          `json:"sent_at"`
	Blocked         bool               `json:"blocked"`
	Redaction       RedactionSummary   `json:"redaction"`
	Warnings        []string           `json:"warnings"`
	Targets         []string           `json:"targets"`
	Successful      []string           `json:"successful"`
	Failed          []SendError        `json:"failed"`
	MessagePreview  string             `json:"message_preview"`
	DryRun          bool               `json:"dry_run,omitempty"`
	WouldSendTo     []string           `json:"would_send_to,omitempty"`
	CASSInjection   *CASSInjectionInfo `json:"cass_injection,omitempty"`
	MemoryInjection *CMInjectionInfo   `json:"memory_injection,omitempty"`
	Operation       *OperationInfo     `json:"operation,omitempty"`
	NHM             any                `json:"_nhm,omitempty"`
}

// SendReceiptOutcome is the recorded result of a send operation.
type SendReceiptOutcome struct {
	Success    bool        `json:"success"`
	SentAt     time.Time   `json:"sent_at"`
	Targets    []string    `json:"targets"`
	Successful []string    `json:"successful"`
	Failed     []SendError `json:"failed"`
	Error      string      `json:"error,omitempty"`
	ErrorCode  string      `json:"error_code,omitempty"`
}

// SendReceiptOutput is --robot-send-receipt's answer.
type SendReceiptOutput struct {
	RobotResponse
	Session   string              `json:"session,omitempty"`
	Warnings  []string            `json:"warnings,omitempty"`
	Operation *OperationInfo      `json:"operation,omitempty"`
	Outcome   *SendReceiptOutcome `json:"outcome,omitempty"`
	NHM       any                 `json:"_nhm,omitempty"`
}
