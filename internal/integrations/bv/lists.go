// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/bv/bv.go at commit 9d5ed977: GetInProgressListContext and
// GetBlockedListContext, which ntm's --robot-assign reads; see LICENSE in
// this directory and PROVENANCE.md. Local changes: this header; the doc
// comment of GetBlockedListContext without HasLocalBeadsDB, which nhm does
// not take (nhm-y55).

package bv

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// GetInProgressListContext returns in-progress beads without erasing command,
// parse, or cancellation failures. A limit of zero requests the full list.
func GetInProgressListContext(ctx context.Context, dir string, limit int) ([]BeadInProgress, error) {
	if limit < 0 {
		return nil, fmt.Errorf("in-progress limit must not be negative: %d", limit)
	}
	items := make([]BeadInProgress, 0)
	output, err := RunBdContext(ctx, dir, "list", "--status=in_progress", "--json", "--limit", strconv.Itoa(limit))
	if err != nil {
		return nil, fmt.Errorf("list in-progress beads: %w", err)
	}

	var issues []struct {
		ID        string    `json:"id"`
		Title     string    `json:"title"`
		Assignee  string    `json:"assignee"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	if issues, err = UnmarshalBdList[struct {
		ID        string    `json:"id"`
		Title     string    `json:"title"`
		Assignee  string    `json:"assignee"`
		UpdatedAt time.Time `json:"updated_at"`
	}](output); err != nil {
		return nil, fmt.Errorf("parse in-progress beads: %w", err)
	}

	// A zero limit is br's documented unlimited mode. Positive limits are also
	// forwarded to br so the subprocess does not serialize rows the caller will
	// immediately discard.
	for i, issue := range issues {
		if limit > 0 && i >= limit {
			break
		}
		items = append(items, BeadInProgress{
			ID:        issue.ID,
			Title:     issue.Title,
			Assignee:  issue.Assignee,
			UpdatedAt: issue.UpdatedAt,
		})
	}

	return items, nil
}

// GetBlockedListContext returns blocked beads (beads that are blocked by
// dependencies) without erasing command, parse, or cancellation failures.
//
// br walks the filesystem upward to find a workspace root, so this can
// return rows from a parent repo when the directory has no .beads/ of its
// own (#130).
func GetBlockedListContext(ctx context.Context, dir string, limit int) ([]BeadPreview, error) {
	items := make([]BeadPreview, 0)
	output, err := RunBdContext(ctx, dir, "blocked", "--json")
	if err != nil {
		return nil, fmt.Errorf("list blocked beads: %w", err)
	}

	var issues []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if issues, err = UnmarshalBdList[struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}](output); err != nil {
		return nil, fmt.Errorf("parse blocked beads: %w", err)
	}

	// Take up to limit items
	for i, issue := range issues {
		if i >= limit {
			break
		}
		items = append(items, BeadPreview{
			ID:    issue.ID,
			Title: issue.Title,
		})
	}

	return items, nil
}
