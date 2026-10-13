// Derived from ntm (github.com/Dicklesworthstone/ntm)
// internal/bv/assignment_mutex.go at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: this header; the worksource
// import path; ErrAssignmentMutexHeld and AssignmentMutexError are ntm's.
// requireAssignmentMutexes keeps ntm's rules (the requested bead's mutex:
// groups, every other issue holding a group per worksource.ClaimHoldsMutex,
// fail closed on a holder without a verifiable lifecycle, refusals name only
// the groups) but reads the bead's labels from the caller's br show and the
// holders from br list --all, in place of SQL inside ntm's claim transaction
// (nhm's claim goes through br's engine, under the project's claim lock;
// ACFS acfs-f2mc.2).

package bv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/arosl/nhm/internal/integrations/worksource"
)

// ErrAssignmentMutexHeld means another claimed task uses a mutex required by
// this assignment. It is an eligibility refusal, not an uncertain claim.
var ErrAssignmentMutexHeld = errors.New("assignment mutex is held")

// AssignmentMutexError names only the requested task's conflicting groups.
// Holder IDs, owners and private task text must not escape through refusals.
type AssignmentMutexError struct {
	BeadID string   `json:"bead_id"`
	Groups []string `json:"mutex_groups"`
}

func (e *AssignmentMutexError) Error() string {
	if e == nil {
		return ErrAssignmentMutexHeld.Error()
	}
	return fmt.Sprintf("%s: %s: mutex groups unavailable: %s", ErrBeadAssignmentIneligible, e.BeadID, strings.Join(e.Groups, ", "))
}

func (e *AssignmentMutexError) Unwrap() []error {
	return []error{ErrAssignmentMutexHeld, ErrBeadAssignmentIneligible}
}

// requireAssignmentMutexes runs under the project's claim lock, between the
// claim's read of the bead (labels) and its compare-and-set write. Its caller
// must not release the lock between this check and the write. The claim
// itself then holds every requested group; no second lock table, expiry, or
// cleanup protocol is needed. Other nhm claim processes see the committed
// holder before they can pass this gate. Direct tracker edits are not fenced.
func requireAssignmentMutexes(ctx context.Context, normalizedDir, beadID string, labels []string) error {
	if ctx == nil || strings.TrimSpace(normalizedDir) == "" || strings.TrimSpace(beadID) == "" {
		return errors.New("assignment mutex check requires a context, project and bead ID")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	groups := make(map[string]bool)
	for _, label := range labels {
		if key, ok := worksource.MutexKey(label); ok {
			groups[key] = true
		}
	}
	if len(groups) == 0 {
		return ctx.Err()
	}

	// Normalize in Go, using the planner's exact Unicode case/whitespace
	// rules, over every issue's labels: br's own label filter would let
	// differently spelled copies of one group pass each other.
	output, err := runBdContext(ctx, normalizedDir, false, "list", "--json", "--all", "--limit", "100000")
	if err != nil {
		return fmt.Errorf("inspect assignment mutex holders: %w", err)
	}
	// Every holder must be seen: a page that says more follow fails closed.
	var page struct {
		HasMore bool `json:"has_more"`
	}
	if json.Unmarshal([]byte(output), &page) == nil && page.HasMore {
		return fmt.Errorf("%w: mutex holders cannot all be read (br list reports more than one page)", ErrBeadAssignmentIneligible)
	}
	type holder struct {
		ID       string          `json:"id"`
		Status   string          `json:"status"`
		Assignee string          `json:"assignee"`
		Labels   json.RawMessage `json:"labels"`
	}
	rows, err := UnmarshalBdList[holder](output)
	if err != nil {
		return fmt.Errorf("read assignment mutex holders: %w", err)
	}
	conflicts := make(map[string]bool)
	for _, row := range rows {
		id := strings.TrimSpace(row.ID)
		if id == beadID {
			// The exact requested issue was excluded by ID, not actor: one actor
			// may own several panes, so two of its different tasks still conflict.
			continue
		}
		var rowLabels []string
		if len(row.Labels) > 0 && strings.TrimSpace(string(row.Labels)) != "null" {
			if err := json.Unmarshal(row.Labels, &rowLabels); err != nil {
				return fmt.Errorf("read assignment mutex holder labels: %w", err)
			}
		}
		for _, label := range rowLabels {
			if key, ok := worksource.MutexKey(label); ok && groups[key] {
				if id == "" || strings.TrimSpace(row.Status) == "" {
					return fmt.Errorf("%w: mutex holder lifecycle cannot be verified", ErrBeadAssignmentIneligible)
				}
				if worksource.ClaimHoldsMutex(row.Status, row.Assignee) {
					conflicts[key] = true
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(conflicts) == 0 {
		return nil
	}
	heldGroups := make([]string, 0, len(conflicts))
	for key := range conflicts {
		heldGroups = append(heldGroups, key)
	}
	sort.Strings(heldGroups)
	return &AssignmentMutexError{BeadID: beadID, Groups: heldGroups}
}
