package bindings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// An operator pane (nhm-h85k) is a pane where a person works: a shell,
// or the person's own agent session, which registers with Agent Mail to
// read and send mail. nhm never binds it. Every send, nudge, broadcast
// and interrupt of nhm's goes to bound agents only (an unbound pane is
// ntm's user pane), so none reaches the person. checkFree refuses the
// pane for every binding: the registration hook's, the import's,
// adopt's, a handover's and a move's.
//
// The mark is nhm's own state: one meta row per pane, keyed by herdr's
// pane id. herdr never gives a pane id to another pane while it runs and
// keeps the ids across a restart; after a restart it can give a closed
// workspace's id to a new workspace, so nhm maintain drops the mark of a
// pane that herdr no longer has (PruneOperators).

// operatorKeyPrefix starts the meta key of an operator pane.
const operatorKeyPrefix = "operator_pane/"

// ErrOperatorPane means the pane is marked as an operator's: nhm never
// binds it.
var ErrOperatorPane = errors.New("bindings: the pane is an operator's; nhm never binds it")

// OperatorPane is a pane marked as an operator's.
type OperatorPane struct {
	PaneID      string    `json:"pane_id"`
	WorkspaceID string    `json:"workspace_id,omitempty"`
	MarkedAt    time.Time `json:"marked_at"`
}

// MarkOperator marks a pane as an operator's. Marking it again keeps the
// first mark.
func (s *Store) MarkOperator(ctx context.Context, p OperatorPane) (OperatorPane, error) {
	if p.PaneID == "" {
		return OperatorPane{}, errors.New("bindings: an operator mark needs a pane")
	}
	if p.MarkedAt.IsZero() {
		p.MarkedAt = s.now()
	}
	p.MarkedAt = p.MarkedAt.UTC()
	value, err := json.Marshal(p)
	if err != nil {
		return OperatorPane{}, fmt.Errorf("bindings: operator mark: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT (key) DO NOTHING`,
		operatorKeyPrefix+p.PaneID, string(value)); err != nil {
		return OperatorPane{}, fmt.Errorf("bindings: operator mark: %w", err)
	}
	got, ok, err := s.operator(ctx, s.db, p.PaneID)
	if err != nil {
		return OperatorPane{}, err
	}
	if !ok {
		return OperatorPane{}, fmt.Errorf("bindings: operator mark of %s not found after the write", p.PaneID)
	}
	return got, nil
}

// UnmarkOperator clears a pane's operator mark; false when it had none.
func (s *Store) UnmarkOperator(ctx context.Context, paneID string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM meta WHERE key = ?`, operatorKeyPrefix+paneID)
	if err != nil {
		return false, fmt.Errorf("bindings: operator mark: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("bindings: operator mark: %w", err)
	}
	return n > 0, nil
}

// IsOperator reports whether a pane is marked as an operator's.
func (s *Store) IsOperator(ctx context.Context, paneID string) (bool, error) {
	if paneID == "" {
		return false, nil
	}
	_, ok, err := s.operator(ctx, s.db, paneID)
	return ok, err
}

// Operators lists the operator panes by pane id.
func (s *Store) Operators(ctx context.Context) ([]OperatorPane, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT value FROM meta WHERE substr(key, 1, ?) = ?`,
		len(operatorKeyPrefix), operatorKeyPrefix)
	if err != nil {
		return nil, fmt.Errorf("bindings: operator marks: %w", err)
	}
	defer rows.Close()
	out := []OperatorPane{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("bindings: operator marks: %w", err)
		}
		var p OperatorPane
		if err := json.Unmarshal([]byte(value), &p); err != nil {
			return nil, fmt.Errorf("bindings: operator mark %q: %w", value, err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("bindings: operator marks: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PaneID < out[j].PaneID })
	return out, nil
}

// PruneOperators drops the marks of panes that are not in live, herdr's
// pane ids of the moment, and returns them. The caller passes a complete
// list: a mark whose pane is missing from it goes.
func (s *Store) PruneOperators(ctx context.Context, live map[string]bool) ([]OperatorPane, error) {
	marks, err := s.Operators(ctx)
	if err != nil {
		return nil, err
	}
	dropped := []OperatorPane{}
	for _, m := range marks {
		if live[m.PaneID] {
			continue
		}
		if ok, err := s.UnmarkOperator(ctx, m.PaneID); err != nil {
			return dropped, err
		} else if ok {
			dropped = append(dropped, m)
		}
	}
	return dropped, nil
}

// querier is a *sql.DB or a *sql.Tx.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) operator(ctx context.Context, q querier, paneID string) (OperatorPane, bool, error) {
	var value string
	err := q.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, operatorKeyPrefix+paneID).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return OperatorPane{}, false, nil
	}
	if err != nil {
		return OperatorPane{}, false, fmt.Errorf("bindings: operator mark: %w", err)
	}
	var p OperatorPane
	if err := json.Unmarshal([]byte(value), &p); err != nil {
		return OperatorPane{}, false, fmt.Errorf("bindings: operator mark of %s: %w", paneID, err)
	}
	return p, true, nil
}
