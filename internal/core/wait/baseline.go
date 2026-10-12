// Package wait is nhm's wait and interrupt (plan-v8 2.6). herdr tracks
// an agent's lifecycle, not its turns, so nhm keeps its own completion
// loop: a completion counts only when it comes after a baseline, for the
// same binding generation, and nhm says "complete" only when it also saw
// the turn start.
package wait

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

// Baseline is what nhm reads of an agent before a submission (send
// stores it in the receipt's baseline_json) or at the start of a wait
// without --after-op. A completion counts only when its completion_seq
// is past StateChangeSeq.
type Baseline struct {
	BindingID      string            `json:"binding_id"`
	Generation     int64             `json:"generation"`
	PaneID         string            `json:"pane_id"`
	LaunchID       string            `json:"launch_id,omitempty"`
	NativeSession  string            `json:"native_session,omitempty"`
	State          herdr.AgentStatus `json:"state"`
	StateChangeSeq uint64            `json:"state_change_seq"`
	CompletionSeq  *uint64           `json:"completion_seq,omitempty"`
	// Working is true when the agent was already working: a completion
	// after the baseline may then end the earlier turn.
	Working bool `json:"already_working"`
	// SocketInode is the herdr socket's inode; a new inode means herdr
	// restarted and its counters started again. Zero when unknown.
	SocketInode uint64    `json:"socket_inode,omitempty"`
	At          time.Time `json:"at"`
}

// ErrNoSequence means herdr gave no state_change_seq, so nhm cannot tell
// one state from a later one.
var ErrNoSequence = errors.New("wait: herdr gives no state_change_seq for the agent")

// Capture reads a baseline from an agent and its binding.
func Capture(b bindings.Binding, a herdr.AgentInfo, inode uint64, now time.Time) (Baseline, error) {
	if a.StateChangeSeq == nil {
		return Baseline{}, fmt.Errorf("%w: %s", ErrNoSequence, a.PaneID)
	}
	base := Baseline{
		BindingID:      b.ID,
		Generation:     b.Generation,
		PaneID:         a.PaneID,
		LaunchID:       b.LaunchID,
		State:          a.AgentStatus,
		StateChangeSeq: *a.StateChangeSeq,
		CompletionSeq:  a.CompletionSeq,
		Working:        a.AgentStatus == herdr.StatusWorking,
		SocketInode:    inode,
		At:             now.UTC(),
	}
	if a.AgentSession != nil {
		base.NativeSession = a.AgentSession.Value
	}
	return base, nil
}

// SocketInode returns the inode of a Unix socket, or 0 when it cannot
// be read.
func SocketInode(path string) uint64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}

// Submission statuses of a receipt (plan-v8 2.3). Only "submitted" means
// that herdr saw the agent working or blocked after the prompt.
const (
	Submitted         = "submitted"
	PossiblySubmitted = "possibly_submitted"
)

// Receipt is one target of an operation, as send recorded it.
type Receipt struct {
	OperationID string
	TargetIndex int
	BindingID   string
	Generation  int64
	Status      string
	// Baseline is nil when send stopped before it read one (a refusal).
	Baseline *Baseline
}

// ErrNoReceipts means the operation has no receipt rows.
var ErrNoReceipts = errors.New("wait: the operation has no receipts")

// Receipts reads an operation's receipts in target order.
func Receipts(ctx context.Context, db *sql.DB, opID string) ([]Receipt, error) {
	rows, err := db.QueryContext(ctx, `SELECT target_index, binding_id, generation, status, baseline_json
		FROM receipts WHERE operation_id = ? ORDER BY target_index`, opID)
	if err != nil {
		return nil, fmt.Errorf("wait: receipts of %s: %w", opID, err)
	}
	defer rows.Close()
	var out []Receipt
	for rows.Next() {
		r := Receipt{OperationID: opID}
		var binding, base sql.NullString
		var gen sql.NullInt64
		if err := rows.Scan(&r.TargetIndex, &binding, &gen, &r.Status, &base); err != nil {
			return nil, fmt.Errorf("wait: receipts of %s: %w", opID, err)
		}
		r.BindingID, r.Generation = binding.String, gen.Int64
		if base.Valid && base.String != "" {
			var b Baseline
			if err := json.Unmarshal([]byte(base.String), &b); err != nil {
				return nil, fmt.Errorf("wait: receipt %s/%d: baseline: %w", opID, r.TargetIndex, err)
			}
			r.Baseline = &b
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("wait: receipts of %s: %w", opID, err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoReceipts, opID)
	}
	return out, nil
}
