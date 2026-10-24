package enrich

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/store"
)

// receiptSubmitted is the receipt status of a confirmed submission
// (internal/core/send.StatusSubmitted; send imports enrich).
const receiptSubmitted = "submitted"

// ContextGeneration names the context an agent holds now: its binding
// generation, its native session, and its last compaction and resume. A
// clear displaces the binding into a new generation; a compaction or an
// uncertain resume changes the times. Each change is a new context, so
// earlier blocks no longer count as present.
func ContextGeneration(b bindings.Binding) string {
	parts := []string{fmt.Sprintf("g%d", b.Generation)}
	if b.NativeSession != nil {
		parts = append(parts, "s="+b.NativeSession.Value)
	}
	if b.CompactedAt != nil {
		parts = append(parts, "c="+store.FormatTime(*b.CompactedAt))
	}
	if b.ResumedAt != nil {
		parts = append(parts, "r="+store.FormatTime(*b.ResumedAt))
	}
	return digest(strings.Join(parts, ";"))[:16]
}

// Given is the set of item keys that a binding's context generation has:
// items included in a receipt that was submitted. A possibly submitted
// target does not count: "submitted" is the only delivery evidence.
func Given(ctx context.Context, db *store.DB, bindingID, ctxGen string) (map[string]bool, error) {
	rows, err := db.SQL().QueryContext(ctx, `SELECT i.items_json FROM injections i
		JOIN receipts r ON r.operation_id = i.operation_id AND r.target_index = i.target_index
		WHERE i.binding_id = ? AND i.context_generation = ? AND r.status = ?`, bindingID, ctxGen, receiptSubmitted)
	if err != nil {
		return nil, fmt.Errorf("enrich: given: %w", err)
	}
	defer rows.Close()
	given := map[string]bool{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var items []Item
		if err := json.Unmarshal([]byte(data), &items); err != nil {
			return nil, fmt.Errorf("enrich: given: %w", err)
		}
		for _, it := range items {
			if it.Status == Included {
				given[it.Key()] = true
			}
		}
	}
	return given, rows.Err()
}

// Exposure is one target's injection, recorded with its receipt.
type Exposure struct {
	OperationID       string
	TargetIndex       int
	BindingID         string
	Generation        int64
	ContextGeneration string
	BeadID            string
	Plan              Plan
	Retrieval         Retrieval
}

// WithoutText is e without the text that a private session keeps out of
// the store (core/privacy): the queries, made of the prompt, and the
// items' labels. The items' keys stay, so a later send still knows what
// the agent was given, and the feedback still finds the rules.
func (e Exposure) WithoutText() Exposure {
	e.Retrieval.CM.Query, e.Retrieval.CASS.Query = "", ""
	e.Plan.CM, e.Plan.CASS = unlabeled(e.Plan.CM), unlabeled(e.Plan.CASS)
	return e
}

func unlabeled(items []Item) []Item {
	if items == nil {
		return nil
	}
	out := make([]Item, len(items))
	for i, it := range items {
		it.Label = ""
		out[i] = it
	}
	return out
}

// Record writes an exposure: one row per enabled source, even when every
// item was suppressed as already present.
func Record(ctx context.Context, db *store.DB, e Exposure, now time.Time) error {
	for _, s := range []struct {
		source string
		rep    Report
		items  []Item
	}{{SourceCM, e.Retrieval.CM, e.Plan.CM}, {SourceCASS, e.Retrieval.CASS, e.Plan.CASS}} {
		if !s.rep.Enabled {
			continue
		}
		items := s.items
		if items == nil {
			items = []Item{}
		}
		data, err := json.Marshal(items)
		if err != nil {
			return err
		}
		bytes := 0
		for _, it := range items {
			if it.Status == Included {
				bytes += it.Bytes
			}
		}
		if _, err := db.SQL().ExecContext(ctx, `INSERT INTO injections (operation_id, binding_id, source, query,
			items_json, bytes, created_at, target_index, generation, context_generation, mode, bead_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.OperationID, nullStr(e.BindingID), s.source, s.rep.Query, string(data), bytes, store.FormatTime(now),
			e.TargetIndex, e.Generation, e.ContextGeneration, string(s.rep.Mode), nullStr(e.BeadID)); err != nil {
			return fmt.Errorf("enrich: record: %w", err)
		}
	}
	return nil
}

// TargetInjection is what one target got from one source.
type TargetInjection struct {
	TargetIndex int    `json:"target_index"`
	Source      string `json:"source"`
	Items       []Item `json:"items"`
}

// Exposures reads an operation's injection rows.
func Exposures(ctx context.Context, db *store.DB, opID string) ([]TargetInjection, error) {
	rows, err := db.SQL().QueryContext(ctx, `SELECT target_index, source, items_json FROM injections
		WHERE operation_id = ? ORDER BY target_index, source`, opID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TargetInjection
	for rows.Next() {
		var t TargetInjection
		var idx sql.NullInt64
		var data string
		if err := rows.Scan(&idx, &t.Source, &data); err != nil {
			return nil, err
		}
		t.TargetIndex = int(idx.Int64)
		if err := json.Unmarshal([]byte(data), &t.Items); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
