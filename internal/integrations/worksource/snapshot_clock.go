// Taken from ntm (github.com/Dicklesworthstone/ntm)
// internal/worksource/snapshot_clock.go at commit 9d5ed977; see LICENSE
// in this directory and PROVENANCE.md. Local changes: this header only.

package worksource

import "time"

// NextEligibilityChange bounds reuse of a candidate read. br ready may omit
// deferred work until its deadline without changing the exported JSONL. Source
// hashes alone cannot make an earlier ready set complete after that boundary.
// Inspect every open source row, including rows absent from the candidate set.
func (s *Snapshot) NextEligibilityChange(after time.Time) time.Time {
	var next time.Time
	if s == nil {
		return next
	}
	for _, row := range s.issues {
		if row.Status != "open" || row.DeferUntil == "" {
			continue
		}
		until, err := time.Parse(time.RFC3339, row.DeferUntil)
		if err == nil && until.After(after) && (next.IsZero() || until.Before(next)) {
			next = until
		}
	}
	return next
}
