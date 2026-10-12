package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// ErrOperationIDConflict means an operation id was claimed before with a
// different request (plan-v8 section 1: `operation_id_conflict`).
var ErrOperationIDConflict = errors.New("store: operation_id_conflict")

// Operation statuses that the store itself sets. The verbs add their own.
const (
	OperationClaimed = "claimed"
)

// Operation is one row of the operations table.
type Operation struct {
	ID          string
	Verb        string
	Fingerprint string
	ScopeJSON   string
	RequestJSON string
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CompletedAt *time.Time
}

// ClaimRequest is what a mutating verb claims before any external effect.
type ClaimRequest struct {
	// ID is the caller's --op-id, or "" for a new id.
	ID      string
	Verb    string
	Scope   any
	Request any
}

// Claim is the result of ClaimOperation. When Existing is true, the caller
// must not execute anything: it reports the stored progress instead.
type Claim struct {
	Operation Operation
	Existing  bool
}

var operationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// NewOperationID returns a fresh, sortable operation id.
func NewOperationID(now time.Time) (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("store: operation id: %w", err)
	}
	return fmt.Sprintf("op-%d-%s", now.UTC().UnixMilli(), hex.EncodeToString(b)), nil
}

// Fingerprint is the SHA-256 of the verb and the request's JSON. The
// same id with a different fingerprint is a conflict.
func Fingerprint(verb string, requestJSON []byte) string {
	h := sha256.New()
	h.Write([]byte(verb))
	h.Write([]byte{0})
	h.Write(requestJSON)
	return hex.EncodeToString(h.Sum(nil))
}

// ClaimOperation claims an operation id in one insert. The first caller
// gets Existing false and executes. A later claim of the same id with the
// same verb and request gets the stored row with Existing true. A later
// claim with a different verb or request gets ErrOperationIDConflict.
func (db *DB) ClaimOperation(ctx context.Context, req ClaimRequest, now time.Time) (Claim, error) {
	if req.Verb == "" {
		return Claim{}, errors.New("store: claim: empty verb")
	}
	id := req.ID
	if id == "" {
		var err error
		if id, err = NewOperationID(now); err != nil {
			return Claim{}, err
		}
	} else if !operationIDPattern.MatchString(id) {
		return Claim{}, fmt.Errorf("store: claim: invalid operation id %q", id)
	}
	scopeJSON, err := json.Marshal(req.Scope)
	if err != nil {
		return Claim{}, fmt.Errorf("store: claim: scope: %w", err)
	}
	requestJSON, err := json.Marshal(req.Request)
	if err != nil {
		return Claim{}, fmt.Errorf("store: claim: request: %w", err)
	}
	fp := Fingerprint(req.Verb, requestJSON)
	ts := FormatTime(now)

	res, err := db.sql.ExecContext(ctx, `INSERT INTO operations
		(operation_id, verb, fingerprint, scope_json, request_json, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (operation_id) DO NOTHING`,
		id, req.Verb, fp, string(scopeJSON), string(requestJSON), OperationClaimed, ts, ts)
	if err != nil {
		return Claim{}, fmt.Errorf("store: claim %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Claim{}, fmt.Errorf("store: claim %s: %w", id, err)
	}
	op, err := db.Operation(ctx, id)
	if err != nil {
		return Claim{}, err
	}
	if n == 1 {
		return Claim{Operation: op}, nil
	}
	if op.Verb != req.Verb || op.Fingerprint != fp {
		return Claim{Operation: op, Existing: true}, fmt.Errorf("%w: %s was claimed by %s with a different request", ErrOperationIDConflict, id, op.Verb)
	}
	return Claim{Operation: op, Existing: true}, nil
}

// Operation reads one operation.
func (db *DB) Operation(ctx context.Context, id string) (Operation, error) {
	var op Operation
	var created, updated string
	var completed sql.NullString
	err := db.sql.QueryRowContext(ctx, `SELECT operation_id, verb, fingerprint, scope_json, request_json,
		status, created_at, updated_at, completed_at FROM operations WHERE operation_id = ?`, id).
		Scan(&op.ID, &op.Verb, &op.Fingerprint, &op.ScopeJSON, &op.RequestJSON, &op.Status, &created, &updated, &completed)
	if err != nil {
		return Operation{}, fmt.Errorf("store: operation %s: %w", id, err)
	}
	if op.CreatedAt, err = time.Parse(TimeFormat, created); err != nil {
		return Operation{}, fmt.Errorf("store: operation %s: created_at: %w", id, err)
	}
	if op.UpdatedAt, err = time.Parse(TimeFormat, updated); err != nil {
		return Operation{}, fmt.Errorf("store: operation %s: updated_at: %w", id, err)
	}
	if completed.Valid {
		t, err := time.Parse(TimeFormat, completed.String)
		if err != nil {
			return Operation{}, fmt.Errorf("store: operation %s: completed_at: %w", id, err)
		}
		op.CompletedAt = &t
	}
	return op, nil
}

// SetOperationStatus records an operation's progress. done also sets
// completed_at.
func (db *DB) SetOperationStatus(ctx context.Context, id, status string, done bool, now time.Time) error {
	ts := FormatTime(now)
	var completed any
	if done {
		completed = ts
	}
	res, err := db.sql.ExecContext(ctx, `UPDATE operations SET status = ?, updated_at = ?,
		completed_at = COALESCE(?, completed_at) WHERE operation_id = ?`, status, ts, completed, id)
	if err != nil {
		return fmt.Errorf("store: operation %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("store: operation %s: not found", id)
	}
	return nil
}
