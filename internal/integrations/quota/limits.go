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

	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/store"
)

// LimitPattern is a kind's limit recogniser, from the kind registry: what
// the kind prints when its plan's usage limit or the API's rate limit
// stops it, as whole phrases, never a bare "rate limit", so an agent's own
// prose about limits rarely matches. The patterns are ACFS's
// (herdr_agents.sh herdr_agents_limit_pattern). A kind without its own
// gets the generic one. herdr's "gemini" is the agy CLI's older name.
func LimitPattern(kind string) *regexp.Regexp {
	if kind == "gemini" {
		kind = "agy"
	}
	return kinds.Lookup(kind).LimitPattern()
}

// LastLines returns the last n lines of a screen that are not blank. A
// short session's screen (Claude's) has its text at the top, then a gap of
// blank lines above the prompt box at the bottom, so the screen's last n
// lines can all be gap and box (nhm-1q7).
func LastLines(text string, n int) string {
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			kept = append(kept, line)
		}
	}
	if n = max(n, 0); len(kept) > n {
		kept = kept[len(kept)-n:]
	}
	return strings.Join(kept, "\n")
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
