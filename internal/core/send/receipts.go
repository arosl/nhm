package send

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/store"
)

// Receipt is one target of a send: what was frozen, what was sent, the
// baseline read before the submission, and the result.
type Receipt struct {
	OperationID   string         `json:"operation_id"`
	TargetIndex   int            `json:"target_index"`
	Key           string         `json:"target"`
	BindingID     string         `json:"binding_id,omitempty"`
	MailName      string         `json:"mail_name,omitempty"`
	PaneID        string         `json:"pane_id,omitempty"`
	Kind          string         `json:"kind,omitempty"`
	Generation    int64          `json:"generation,omitempty"`
	Status        string         `json:"submission"`
	PayloadSHA256 string         `json:"payload_sha256,omitempty"`
	PayloadBytes  int64          `json:"payload_bytes,omitempty"`
	Baseline      *wait.Baseline `json:"baseline,omitempty"`
	Result        ReceiptResult  `json:"result"`
	ErrorCode     string         `json:"error_code,omitempty"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

// ReceiptResult is the receipt's result_json: why a target was refused,
// the gate, herdr's state after the submission, and the completion
// observation once a wait ran.
type ReceiptResult struct {
	Key        string `json:"target"`
	MailName   string `json:"mail_name,omitempty"`
	PaneID     string `json:"pane_id,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Gate       Gate   `json:"gate,omitempty"`
	Reason     string `json:"reason,omitempty"`
	StateAfter string `json:"state_after,omitempty"`
	// Completion is the observation of a wait after the send.
	Completion *Completion `json:"completion_observation,omitempty"`
	// TaskResult is "unknown" unless a result contract supplies it.
	TaskResult string `json:"task_result"`
}

// Completion observation values (plan-v8 2.12).
const (
	ObsNotObserved    = "not_observed"
	ObsStarted        = "started"
	ObsCompleted      = "completed"
	ObsAmbiguous      = "ambiguous"
	ObsReplaced       = "replaced"
	ObsContinuityLost = "continuity_lost"
)

// Completion is a completion observation of one target.
type Completion struct {
	Observation  string  `json:"observation"`
	Correlation  string  `json:"correlation"`
	StartedSeq   *uint64 `json:"started_seq,omitempty"`
	CompletedSeq *uint64 `json:"completed_seq,omitempty"`
}

// receiptDB writes and reads the receipts table. No transaction stays
// open across a socket call.
type receiptDB struct {
	db *store.DB
}

func (r receiptDB) insert(ctx context.Context, rc Receipt) error {
	res, _ := json.Marshal(rc.Result)
	_, err := r.db.SQL().ExecContext(ctx, `INSERT INTO receipts (operation_id, target_index, binding_id, generation,
		status, payload_sha256, payload_bytes, result_json, error_code, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rc.OperationID, rc.TargetIndex, nullStr(rc.BindingID), nullInt(rc.Generation), rc.Status, nullStr(rc.PayloadSHA256),
		rc.PayloadBytes, string(res), nullStr(rc.ErrorCode), store.FormatTime(rc.UpdatedAt))
	if err != nil {
		return fmt.Errorf("send: receipt %s/%d: %w", rc.OperationID, rc.TargetIndex, err)
	}
	return nil
}

// update writes a receipt's status, baseline, result and error code.
func (r receiptDB) update(ctx context.Context, rc Receipt) error {
	res, _ := json.Marshal(rc.Result)
	var base any
	if rc.Baseline != nil {
		data, _ := json.Marshal(rc.Baseline)
		base = string(data)
	}
	_, err := r.db.SQL().ExecContext(ctx, `UPDATE receipts SET status = ?, baseline_json = COALESCE(?, baseline_json),
		result_json = ?, error_code = ?, updated_at = ? WHERE operation_id = ? AND target_index = ?`,
		rc.Status, base, string(res), nullStr(rc.ErrorCode), store.FormatTime(rc.UpdatedAt), rc.OperationID, rc.TargetIndex)
	if err != nil {
		return fmt.Errorf("send: receipt %s/%d: %w", rc.OperationID, rc.TargetIndex, err)
	}
	return nil
}

// Receipts reads an operation's receipts in target order.
func Receipts(ctx context.Context, db *store.DB, opID string) ([]Receipt, error) {
	rows, err := db.SQL().QueryContext(ctx, `SELECT target_index, binding_id, generation, status, payload_sha256,
		payload_bytes, baseline_json, result_json, error_code, updated_at
		FROM receipts WHERE operation_id = ? ORDER BY target_index`, opID)
	if err != nil {
		return nil, fmt.Errorf("send: receipts of %s: %w", opID, err)
	}
	defer rows.Close()
	var out []Receipt
	for rows.Next() {
		rc := Receipt{OperationID: opID}
		var binding, digest, base, result, code sql.NullString
		var gen, bytes sql.NullInt64
		var updated string
		if err := rows.Scan(&rc.TargetIndex, &binding, &gen, &rc.Status, &digest, &bytes, &base, &result, &code, &updated); err != nil {
			return nil, fmt.Errorf("send: receipts of %s: %w", opID, err)
		}
		rc.BindingID, rc.Generation, rc.PayloadSHA256, rc.PayloadBytes, rc.ErrorCode = binding.String, gen.Int64, digest.String, bytes.Int64, code.String
		if base.Valid && base.String != "" {
			var b wait.Baseline
			if err := json.Unmarshal([]byte(base.String), &b); err != nil {
				return nil, fmt.Errorf("send: receipt %s/%d: baseline: %w", opID, rc.TargetIndex, err)
			}
			rc.Baseline = &b
		}
		if result.Valid && result.String != "" {
			if err := json.Unmarshal([]byte(result.String), &rc.Result); err != nil {
				return nil, fmt.Errorf("send: receipt %s/%d: result: %w", opID, rc.TargetIndex, err)
			}
		}
		rc.Key, rc.MailName, rc.PaneID, rc.Kind = rc.Result.Key, rc.Result.MailName, rc.Result.PaneID, rc.Result.Kind
		rc.UpdatedAt, _ = time.Parse(store.TimeFormat, updated)
		out = append(out, rc)
	}
	return out, rows.Err()
}

// RecordCompletion stores a wait's observation in a receipt.
func RecordCompletion(ctx context.Context, db *store.DB, opID string, index int, c Completion, now time.Time) error {
	var result sql.NullString
	if err := db.SQL().QueryRowContext(ctx, `SELECT result_json FROM receipts WHERE operation_id = ? AND target_index = ?`,
		opID, index).Scan(&result); err != nil {
		return fmt.Errorf("send: receipt %s/%d: %w", opID, index, err)
	}
	var r ReceiptResult
	if result.Valid && result.String != "" {
		if err := json.Unmarshal([]byte(result.String), &r); err != nil {
			return fmt.Errorf("send: receipt %s/%d: result: %w", opID, index, err)
		}
	}
	r.Completion = &c
	data, _ := json.Marshal(r)
	_, err := db.SQL().ExecContext(ctx, `UPDATE receipts SET result_json = ?, updated_at = ? WHERE operation_id = ? AND target_index = ?`,
		string(data), store.FormatTime(now), opID, index)
	if err != nil {
		return fmt.Errorf("send: receipt %s/%d: %w", opID, index, err)
	}
	return nil
}

// Observation maps a wait outcome to a completion observation.
func Observation(r wait.Result) Completion {
	c := Completion{Observation: ObsNotObserved, Correlation: "none", StartedSeq: r.StartedSeq, CompletedSeq: r.CompletedSeq}
	switch r.Outcome {
	case wait.Complete:
		c.Observation, c.Correlation = ObsCompleted, wait.Strong
	case wait.Ambiguous:
		c.Observation, c.Correlation = ObsAmbiguous, wait.Weak
	case wait.Replaced:
		c.Observation = ObsReplaced
	case wait.ContinuityLost:
		c.Observation = ObsContinuityLost
	default:
		if r.StartedSeq != nil {
			c.Observation, c.Correlation = ObsStarted, wait.Weak
		}
	}
	return c
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
