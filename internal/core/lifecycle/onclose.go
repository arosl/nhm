package lifecycle

import (
	"context"
	"slices"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

// Recycle on close (nhm-abn): when an agent's bead closed and the agent
// is between turns with no bead in progress, nhm maintain gives it a
// fresh session before it takes the next one, so a new bead starts with
// a clean context. The operator turns it on ([maintain] recycle_on_close
// in nhm's config): ACFS's per-task rule lets an agent keep its context
// for a follow-up on the same code, and an agent can always ask for its
// own recycle (nhm recycle --self).

// OnCloseSeen is what nhm maintain remembers of one binding.
type OnCloseSeen struct {
	// Generation is the occupant the rest is about: another generation
	// starts afresh.
	Generation int64 `json:"generation"`
	// Closed are the agent's closed beads when nhm first saw this
	// generation: a bead closed before is not this occupant's work.
	Closed []string `json:"closed"`
	// Pending are the newly closed beads the last check saw, with the
	// agent between turns and no bead in progress. A recycle needs the
	// same on the next check, OnCloseEvery or more later: an agent that
	// ends a turn and takes its next bead in the following one is left
	// alone.
	Pending   []string  `json:"pending,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
	// Recycled is the recycle started for this generation: one at most.
	Recycled string `json:"recycled,omitempty"`
}

// OnCloseEvery is the least time between two checks of one binding: each
// check reads the agent's beads with br.
const OnCloseEvery = time.Minute

// OnCloseDue checks one active binding whose agent herdr shows in status,
// and updates seen. It returns the beads that closed since nhm first saw
// the binding's generation when a recycle is due: the agent is idle or
// done, has no bead in progress, and was so on the check before. A
// generation is recycled once. The first check of a generation only
// records its closed beads.
func OnCloseDue(ctx context.Context, f Facts, b bindings.Binding, status herdr.AgentStatus, seen *OnCloseSeen,
	now time.Time) ([]string, error) {
	if seen.Generation != b.Generation {
		closed, err := f.Closed(ctx, b.MailProjectKey, b.MailName)
		if err != nil {
			return nil, err
		}
		*seen = OnCloseSeen{Generation: b.Generation, Closed: sortedCopy(closed), CheckedAt: now}
		return nil, nil
	}
	if seen.Recycled != "" || now.Sub(seen.CheckedAt) < OnCloseEvery {
		return nil, nil
	}
	seen.CheckedAt = now
	if status != herdr.StatusIdle && status != herdr.StatusDone {
		seen.Pending = nil
		return nil, nil
	}
	assigned, err := f.Assigned(ctx, b.MailProjectKey, b.MailName)
	if err != nil {
		return nil, err
	}
	if len(assigned) > 0 {
		seen.Pending = nil
		return nil, nil
	}
	closed, err := f.Closed(ctx, b.MailProjectKey, b.MailName)
	if err != nil {
		return nil, err
	}
	var newly []string
	for _, id := range sortedCopy(closed) {
		if _, found := slices.BinarySearch(seen.Closed, id); !found {
			newly = append(newly, id)
		}
	}
	switch {
	case len(newly) == 0:
		seen.Pending = nil
		return nil, nil
	case !slices.Equal(seen.Pending, newly):
		seen.Pending = newly
		return nil, nil
	}
	return newly, nil
}

func sortedCopy(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}
