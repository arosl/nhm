package hooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/arosl/nhm/internal/store"
)

// A candidate is one hook call's evidence: a bounded row in
// hook_candidates. The binding changes only after nhm verifies a
// candidate against herdr (plan-v8 2.7, "hooks report evidence; nhm
// reconciles it").

// Candidate limits.
const (
	// pendingTTL: a candidate that no reconcile could verify in this time
	// is closed as expired.
	pendingTTL = 10 * time.Minute
	// settleWindow: herdr's own SessionStart hook runs beside nhm's, so
	// herdr may report the new session a moment later. A session mismatch
	// younger than this stays pending.
	settleWindow = 3 * time.Second
	// keepProcessed: processed rows are deleted after this time.
	keepProcessed = 7 * 24 * time.Hour
	// maxRows bounds the table; the oldest rows go first.
	maxRows = 2000
	// pruneEvery: an agent hook prunes when its candidate id is a
	// multiple of this, so the table stays near maxRows+pruneEvery.
	pruneEvery = 64
)

// CandidateFacts is what a candidate row keeps of a payload. It never
// holds a token or a prompt.
type CandidateFacts struct {
	HookEventName string `json:"hook_event_name"`
	SessionID     string `json:"session_id,omitempty"`
	Source        string `json:"source,omitempty"`
	Model         string `json:"model,omitempty"`
	ToolName      string `json:"tool_name,omitempty"`
	MailName      string `json:"mail_name,omitempty"`
	MailProject   string `json:"mail_project_key,omitempty"`
	Entrypoint    string `json:"entrypoint,omitempty"`
	// TokenRef names the secrets file that holds the registration token
	// until a binding takes it.
	TokenRef string `json:"token_ref,omitempty"`
}

// Candidate is one row of hook_candidates.
type Candidate struct {
	ID          int64
	PaneID      string
	LaunchID    string
	Session     string
	HookEvent   string
	HookEventID string
	Facts       CandidateFacts
	ReceivedAt  time.Time
}

// recordCandidate inserts a candidate. A second call with the same
// (pane, event, event id) returns the first row's id.
func recordCandidate(ctx context.Context, db *sql.DB, c Candidate) (int64, error) {
	facts, err := json.Marshal(c.Facts)
	if err != nil {
		return 0, fmt.Errorf("hooks: candidate: %w", err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO hook_candidates (pane_id, launch_id, native_session, hook_event,
		hook_event_id, payload_json, received_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (pane_id, hook_event, hook_event_id) DO NOTHING`,
		c.PaneID, nullStr(c.LaunchID), nullStr(c.Session), c.HookEvent, c.HookEventID, string(facts),
		store.FormatTime(c.ReceivedAt))
	if err != nil {
		return 0, fmt.Errorf("hooks: candidate: %w", err)
	}
	var id int64
	err = db.QueryRowContext(ctx, `SELECT candidate_id FROM hook_candidates
		WHERE pane_id = ? AND hook_event = ? AND hook_event_id = ?`, c.PaneID, c.HookEvent, c.HookEventID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("hooks: candidate: %w", err)
	}
	return id, nil
}

// pendingCandidates returns a pane's unprocessed candidates, oldest
// first.
func pendingCandidates(ctx context.Context, db *sql.DB, paneID string) ([]Candidate, error) {
	rows, err := db.QueryContext(ctx, `SELECT candidate_id, pane_id, launch_id, native_session, hook_event,
		hook_event_id, payload_json, received_at FROM hook_candidates
		WHERE pane_id = ? AND processed_at IS NULL ORDER BY candidate_id`, paneID)
	if err != nil {
		return nil, fmt.Errorf("hooks: candidates: %w", err)
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		var launch, session sql.NullString
		var facts, received string
		if err := rows.Scan(&c.ID, &c.PaneID, &launch, &session, &c.HookEvent, &c.HookEventID, &facts, &received); err != nil {
			return nil, fmt.Errorf("hooks: candidates: %w", err)
		}
		c.LaunchID, c.Session = launch.String, session.String
		if err := json.Unmarshal([]byte(facts), &c.Facts); err != nil {
			return nil, fmt.Errorf("hooks: candidate %d: %w", c.ID, err)
		}
		if c.ReceivedAt, err = time.Parse(store.TimeFormat, received); err != nil {
			return nil, fmt.Errorf("hooks: candidate %d: %w", c.ID, err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// closeCandidate marks a candidate processed with its result.
func closeCandidate(ctx context.Context, db *sql.DB, id int64, result string, now time.Time) error {
	_, err := db.ExecContext(ctx, `UPDATE hook_candidates SET processed_at = ?, result = ?
		WHERE candidate_id = ? AND processed_at IS NULL`, store.FormatTime(now), result, id)
	if err != nil {
		return fmt.Errorf("hooks: close candidate %d: %w", id, err)
	}
	return nil
}

// CandidateResult is a candidate's outcome, for tests and doctor.
type CandidateResult struct {
	ID        int64
	HookEvent string
	Processed bool
	Result    string
}

// CandidateResults lists a pane's candidates with their outcome, oldest
// first.
func CandidateResults(ctx context.Context, db *store.DB, paneID string) ([]CandidateResult, error) {
	rows, err := db.SQL().QueryContext(ctx, `SELECT candidate_id, hook_event, processed_at, result
		FROM hook_candidates WHERE pane_id = ? ORDER BY candidate_id`, paneID)
	if err != nil {
		return nil, fmt.Errorf("hooks: candidates: %w", err)
	}
	defer rows.Close()
	var out []CandidateResult
	for rows.Next() {
		var r CandidateResult
		var processed, result sql.NullString
		if err := rows.Scan(&r.ID, &r.HookEvent, &processed, &result); err != nil {
			return nil, fmt.Errorf("hooks: candidates: %w", err)
		}
		r.Processed, r.Result = processed.Valid, result.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// prune closes expired pending candidates and deletes old rows, so the
// table stays bounded. It returns the token refs of the rows it closed
// or deleted that still hold one, for the caller to delete.
func prune(ctx context.Context, db *sql.DB, now time.Time) ([]string, error) {
	var refs []string
	collect := func(query string, args ...any) error {
		rows, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var facts string
			if err := rows.Scan(&facts); err != nil {
				return err
			}
			var f CandidateFacts
			if json.Unmarshal([]byte(facts), &f) == nil && f.TokenRef != "" {
				refs = append(refs, f.TokenRef)
			}
		}
		return rows.Err()
	}
	expired := store.FormatTime(now.Add(-pendingTTL))
	if err := collect(`SELECT payload_json FROM hook_candidates WHERE processed_at IS NULL AND received_at < ?`, expired); err != nil {
		return nil, fmt.Errorf("hooks: prune: %w", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE hook_candidates SET processed_at = ?, result = 'expired: never verified'
		WHERE processed_at IS NULL AND received_at < ?`, store.FormatTime(now), expired); err != nil {
		return nil, fmt.Errorf("hooks: prune: %w", err)
	}
	old := store.FormatTime(now.Add(-keepProcessed))
	if _, err := db.ExecContext(ctx, `DELETE FROM hook_candidates WHERE processed_at IS NOT NULL AND processed_at < ?`, old); err != nil {
		return nil, fmt.Errorf("hooks: prune: %w", err)
	}
	if err := collect(`SELECT payload_json FROM hook_candidates WHERE candidate_id NOT IN
		(SELECT candidate_id FROM hook_candidates ORDER BY candidate_id DESC LIMIT ?)`, maxRows); err != nil {
		return nil, fmt.Errorf("hooks: prune: %w", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM hook_candidates WHERE candidate_id NOT IN
		(SELECT candidate_id FROM hook_candidates ORDER BY candidate_id DESC LIMIT ?)`, maxRows); err != nil {
		return nil, fmt.Errorf("hooks: prune: %w", err)
	}
	return refs, nil
}

var errPending = errors.New("pending")

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
