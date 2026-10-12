package bindings

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/arosl/nhm/internal/store"
)

// Continuation is what a hook proved about the same occupant: a
// compaction, a resume, or a restart in the same pane (plan-v8 2.7). It
// changes facts of the binding, never its generation.
type Continuation struct {
	// CompactedAt or ResumedAt is set when the hook saw that event.
	CompactedAt *time.Time
	ResumedAt   *time.Time
	// NativeSession replaces the binding's when it is not nil: the first
	// session a spawned agent reports, or a resume in place.
	NativeSession *NativeSession
	// ForegroundPID and ForegroundStart replace the binding's when the
	// PID is not zero: the agent restarted in its pane.
	ForegroundPID   int64
	ForegroundStart int64
	// Evidence is appended to the binding's.
	Evidence []Evidence
}

// maxEvidence bounds the evidence a continuation keeps.
const maxEvidence = 20

// Continue records a continuation on an active binding at the expected
// generation. It fails with ErrStale when the binding changed or is no
// longer active.
func (s *Store) Continue(ctx context.Context, id string, expected int64, c Continuation) (Binding, error) {
	b, err := s.Get(ctx, id)
	if err != nil {
		return Binding{}, err
	}
	native := b.NativeSession
	if c.NativeSession != nil {
		native = c.NativeSession
	}
	pid, start := b.ForegroundPID, b.ForegroundStart
	if c.ForegroundPID != 0 {
		pid, start = c.ForegroundPID, c.ForegroundStart
	}
	// Every compaction adds a line; keep the newest, so a long-lived
	// binding stays small.
	evidence := append(b.Evidence, c.Evidence...)
	if len(evidence) > maxEvidence {
		evidence = evidence[len(evidence)-maxEvidence:]
	}
	nativeJSON, evidenceJSON, err := encodeJSON(native, evidence)
	if err != nil {
		return Binding{}, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE bindings SET native_session_json = ?, foreground_pid = ?,
		foreground_start = ?, evidence_json = ?, compacted_at = COALESCE(?, compacted_at),
		resumed_at = COALESCE(?, resumed_at), updated_at = ?
		WHERE binding_id = ? AND generation = ? AND status = 'active'`,
		nativeJSON, nullInt(pid), nullInt(start), evidenceJSON, optTime(c.CompactedAt), optTime(c.ResumedAt),
		store.FormatTime(s.now()), id, expected)
	if err != nil {
		return Binding{}, fmt.Errorf("bindings: continue %s: %w", id, err)
	}
	if err := oneRow(res, id, expected); err != nil {
		return Binding{}, err
	}
	return s.Get(ctx, id)
}

func optTime(t *time.Time) any {
	if t == nil {
		return sql.NullString{}
	}
	return store.FormatTime(*t)
}
