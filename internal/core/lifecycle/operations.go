package lifecycle

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/arosl/nhm/internal/store"
)

// RecentOperation is one operation as the menu's recent-operations view
// shows it: the record and, where the verb keeps them, its per-target
// receipts in the core's words. Completed is nil while the operation has
// no final outcome: still running, or its process died partway.
type RecentOperation struct {
	OperationID string            `json:"operation_id"`
	Verb        string            `json:"verb"`
	Status      string            `json:"status"`
	Scope       json.RawMessage   `json:"scope,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	Completed   *time.Time        `json:"completed_at,omitempty"`
	Targets     []OperationTarget `json:"targets"`
}

// OperationTarget is one receipt of an operation.
type OperationTarget struct {
	Index      int             `json:"target_index"`
	BindingID  string          `json:"binding_id,omitempty"`
	Generation int64           `json:"generation,omitempty"`
	Status     string          `json:"status"`
	ErrorCode  string          `json:"error_code,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// RecentOperations lists the newest operations, newest first, with their
// receipts. It only reads; limit <= 0 is 20.
func RecentOperations(ctx context.Context, db *store.DB, limit int) ([]RecentOperation, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.SQL().QueryContext(ctx, `SELECT operation_id, verb, status, scope_json, created_at, updated_at, completed_at
		FROM operations ORDER BY created_at DESC, operation_id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("recent operations: %w", err)
	}
	ops := []RecentOperation{}
	for rows.Next() {
		var op RecentOperation
		var scope sql.NullString
		var created, updated string
		var completed sql.NullString
		if err := rows.Scan(&op.OperationID, &op.Verb, &op.Status, &scope, &created, &updated, &completed); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if scope.Valid && json.Valid([]byte(scope.String)) {
			op.Scope = json.RawMessage(scope.String)
		}
		op.CreatedAt, _ = time.Parse(store.TimeFormat, created)
		op.UpdatedAt, _ = time.Parse(store.TimeFormat, updated)
		if completed.Valid {
			t, _ := time.Parse(store.TimeFormat, completed.String)
			op.Completed = &t
		}
		op.Targets = []OperationTarget{}
		ops = append(ops, op)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range ops {
		if ops[i].Targets, err = operationTargets(ctx, db, ops[i].OperationID); err != nil {
			return nil, err
		}
	}
	return ops, nil
}

// ErrNoOperation means no operation has the id (yet).
var ErrNoOperation = errors.New("no such operation")

// ReadOperation reads one operation with its receipts: a surface that
// started an operation in another process follows it by id. It only
// reads.
func ReadOperation(ctx context.Context, db *store.DB, id string) (RecentOperation, error) {
	row, err := db.Operation(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return RecentOperation{}, fmt.Errorf("%w: %s", ErrNoOperation, id)
	}
	if err != nil {
		return RecentOperation{}, err
	}
	op := RecentOperation{OperationID: row.ID, Verb: row.Verb, Status: row.Status, CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt, Completed: row.CompletedAt}
	if json.Valid([]byte(row.ScopeJSON)) {
		op.Scope = json.RawMessage(row.ScopeJSON)
	}
	if op.Targets, err = operationTargets(ctx, db, id); err != nil {
		return RecentOperation{}, err
	}
	return op, nil
}

func operationTargets(ctx context.Context, db *store.DB, id string) ([]OperationTarget, error) {
	rows, err := db.SQL().QueryContext(ctx, `SELECT target_index, binding_id, generation, status, error_code, result_json, updated_at
		FROM receipts WHERE operation_id = ? ORDER BY target_index`, id)
	if err != nil {
		return nil, fmt.Errorf("recent operations: receipts: %w", err)
	}
	defer rows.Close()
	out := []OperationTarget{}
	for rows.Next() {
		var t OperationTarget
		var binding, code, result sql.NullString
		var gen sql.NullInt64
		var updated string
		if err := rows.Scan(&t.Index, &binding, &gen, &t.Status, &code, &result, &updated); err != nil {
			return nil, err
		}
		t.BindingID, t.Generation, t.ErrorCode = binding.String, gen.Int64, code.String
		if result.Valid && json.Valid([]byte(result.String)) {
			t.Result = json.RawMessage(result.String)
		}
		t.UpdatedAt, _ = time.Parse(store.TimeFormat, updated)
		out = append(out, t)
	}
	return out, rows.Err()
}
