package enrich

import (
	"context"
	"fmt"
	"time"

	"github.com/arosl/nhm/internal/integrations/cass"
	"github.com/arosl/nhm/internal/store"
)

// Similar is one earlier session that the duplicate check found.
type Similar struct {
	Path      string    `json:"path"`
	Agent     string    `json:"agent,omitempty"`
	Title     string    `json:"title,omitempty"`
	CreatedAt time.Time `json:"created_at,omitzero"`
	Score     float64   `json:"score"`
}

// checkLimit is how many sessions the duplicate check reports.
const checkLimit = 3

// CheckDuplicates asks CASS for the same work in the project in the last
// days (ntm's --cass-check). It only warns: the caller reports the
// sessions and sends anyway. One hit per session.
func CheckDuplicates(ctx context.Context, cs CASS, query, workspace string, days int, budget time.Duration) ([]Similar, error) {
	if cs == nil {
		return nil, cass.ErrNotInstalled
	}
	if budget <= 0 {
		budget = DefaultBudget
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	hits, err := cs.Search(ctx, cass.Search{Query: query, Workspace: workspace, Days: days, Limit: checkLimit * 3, MaxContent: 200})
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("over the %s budget", budget)
		}
		return nil, err
	}
	seen := map[string]bool{}
	var out []Similar
	for _, h := range hits {
		if seen[h.SourcePath] || len(out) == checkLimit {
			continue
		}
		seen[h.SourcePath] = true
		out = append(out, Similar{Path: h.SourcePath, Agent: h.Agent, Title: h.Title, CreatedAt: h.CreatedAt(), Score: h.Score})
	}
	return out, nil
}

// DuplicateWarning is the warning a send prints for the sessions found.
func DuplicateWarning(found []Similar, days int) string {
	s := fmt.Sprintf("cass-check: %d earlier session(s) in the last %d days match this prompt; it was sent anyway (--no-cass-check skips the check):", len(found), days)
	for _, f := range found {
		s += fmt.Sprintf("\n  %s (%s) %s", f.Title, f.Agent, f.Path)
	}
	return s
}

// IndexTranscript indexes a kept transcript in place (experiment 5) and
// records indexed_at when CASS lists the session. It returns
// cass.Indexed, cass.NotFoundYet, or cass.Queued (another cass run holds
// the lock and indexes it): a miss is never reported as a lost
// transcript. path is the provider's original file, never nhm's copy.
func IndexTranscript(ctx context.Context, db *store.DB, cs CASS, transcriptID int64, path, workspace, key string, now time.Time) (string, error) {
	if cs == nil {
		return "", cass.ErrNotInstalled
	}
	state, err := cs.IndexSession(ctx, path, workspace, key)
	if err != nil {
		return "", err
	}
	if state == cass.Indexed && db != nil && transcriptID > 0 {
		if _, err := db.SQL().ExecContext(ctx, `UPDATE transcripts SET indexed_at = ? WHERE transcript_id = ?`,
			store.FormatTime(now), transcriptID); err != nil {
			return state, fmt.Errorf("enrich: indexed_at: %w", err)
		}
	}
	return state, nil
}

// ReflectHint is what retire prints for the operator: CM distils the kept
// transcript, and nhm never runs it (it needs an LLM provider).
func ReflectHint(path string) []string {
	return []string{
		fmt.Sprintf("cm reflect --session %q --dry-run --json", path),
		fmt.Sprintf("cm reflect --session %q --json", path),
	}
}
