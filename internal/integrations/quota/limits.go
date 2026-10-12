package quota

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/store"
)

// limitPatterns are what each kind prints when its plan's usage limit or
// the API's rate limit stops it: whole phrases, never a bare "rate
// limit", so an agent's own prose about limits rarely matches. They are
// ACFS's (herdr_agents.sh herdr_agents_limit_pattern). A kind without its
// own gets the generic ones.
var limitPatterns = map[string]*regexp.Regexp{
	"claude":  regexp.MustCompile(`(?i)usage limit reached|you('|’)ve (hit|reached) your ([a-z0-9-]+ )?limit|(5-hour|session|weekly|opus|sonnet) limit reached|rate_limit_error|api error: 429`),
	"codex":   regexp.MustCompile(`(?i)you('|’)ve hit your usage limit|usage limit (has been )?reached|rate limit reached|429 too many requests`),
	"agy":     regexp.MustCompile(`(?i)resource_exhausted|quota (exceeded|exhausted)|(reached|exhausted) (your|the) [a-z0-9. -]{0,30}quota|usage limit reached|429 too many requests`),
	"generic": regexp.MustCompile(`(?i)usage limit reached|rate limit reached|quota exceeded|429 too many requests`),
}

func init() { limitPatterns["gemini"] = limitPatterns["agy"] }

// LimitPattern is a kind's limit pattern.
func LimitPattern(kind string) *regexp.Regexp {
	if re, ok := limitPatterns[kind]; ok {
		return re
	}
	return limitPatterns["generic"]
}

// FindLimit returns the last line of text that shows the kind's limit
// message, trimmed to 200 bytes, or "".
func FindLimit(kind, text string) string {
	re := LimitPattern(kind)
	found := ""
	for _, line := range strings.Split(text, "\n") {
		if re.MatchString(line) {
			found = strings.TrimSpace(line)
		}
	}
	if len(found) > 200 {
		found = found[:200]
	}
	return found
}

// Seen is the limit line last reported per pane, so each line is
// reported once until it leaves the screen. It lives in a JSON file.
type Seen struct {
	path  string
	Lines map[string]string `json:"lines"`
}

// LoadSeen reads the file (a missing one is empty).
func LoadSeen(path string) (*Seen, error) {
	s := &Seen{path: path, Lines: map[string]string{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("quota: %s: %w", path, err)
	}
	if s.Lines == nil {
		s.Lines = map[string]string{}
	}
	return s, nil
}

// Update records what a pane shows now and reports whether it is a new
// limit line to report.
func (s *Seen) Update(pane, line string) bool {
	if line == "" {
		delete(s.Lines, pane)
		return false
	}
	if s.Lines[pane] == line {
		return false
	}
	s.Lines[pane] = line
	return true
}

// Save writes the file.
func (s *Seen) Save() error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	return store.WriteFileAtomic(s.path, data, 0o600)
}

// Store writes the observations into the quota table, one row per pool
// and window, so status and attention see them. An unknown pool writes
// nothing. A pool at a fresh limit-reached record is "limited" in every
// window.
func Store(ctx context.Context, db *sql.DB, obs []Observation) error {
	for _, o := range obs {
		if !o.Known() {
			continue
		}
		limited := o.LimitReached != nil && !o.LimitReached.Stale
		for _, w := range []struct {
			name string
			win  *Window
		}{{"5h", o.FiveHour}, {"weekly", o.Weekly}} {
			if w.win == nil {
				continue
			}
			var used, resets any
			if w.win.UsedPct != nil && !w.win.Reset {
				used = *w.win.UsedPct
			}
			if w.win.ResetsAt != nil {
				resets = w.win.ResetsAt.UTC().Format(time.RFC3339)
			}
			_, err := db.ExecContext(ctx, `INSERT INTO quota (pool_id, window, used_pct, limited, resets_at, source, observed_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (pool_id, window) DO UPDATE SET used_pct = excluded.used_pct, limited = excluded.limited,
				resets_at = excluded.resets_at, source = excluded.source, observed_at = excluded.observed_at`,
				o.Pool, w.name, used, boolInt(limited), resets, o.Source, store.FormatTime(*o.ObservedAt))
			if err != nil {
				return fmt.Errorf("quota: store %s %s: %w", o.Pool, w.name, err)
			}
		}
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
