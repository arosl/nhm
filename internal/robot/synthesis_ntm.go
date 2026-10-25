// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/synthesis.go (ConflictReason, DetectedConflict,
// ActivityWindow, GitFileStatus, ConflictDetector, RecordActivity,
// parseGitStatusPorcelain, DetectConflicts, analyzeFileConflict,
// findLikelyModifiers, scoreConflict) at commit 9d5ed977; see LICENSE in
// this directory and PROVENANCE.md. Local changes: no Agent Mail: ntm's
// callers (--robot-diff, --robot-summary) build the detector without an
// Agent Mail client, so it never reads a reservation, and scoreConflict
// keeps the cases with no reservation holder; DetectConflicts takes the
// git status that the caller read (git runs in internal/cli); the likely
// modifiers come in the order the panes were recorded, where ntm's map
// order is random; no pruning, since both callers record windows that end
// now.

package robot

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ConflictReason describes why a file conflict was detected.
type ConflictReason string

const (
	// ReasonConcurrentActivity indicates multiple panes had activity while file was modified.
	ReasonConcurrentActivity ConflictReason = "concurrent_activity"
	// ReasonUnclaimedModification indicates a modified file with no known modifier.
	ReasonUnclaimedModification ConflictReason = "unclaimed_modification"
)

// DetectedConflict represents a detected or potential file conflict from synthesis analysis.
type DetectedConflict struct {
	// Path is the file path relative to the repository root.
	Path string `json:"path"`

	// LikelyModifiers are pane IDs that may have modified this file.
	LikelyModifiers []string `json:"likely_modifiers"`

	// GitStatus is the git status code (M=modified, A=added, D=deleted, ??=untracked).
	GitStatus string `json:"git_status"`

	// Confidence is a score from 0.0-1.0 indicating conflict likelihood.
	// 0.9+ = high, 0.7-0.9 = medium, 0.5-0.7 = low
	Confidence float64 `json:"confidence"`

	// Reason explains why this conflict was detected.
	Reason ConflictReason `json:"reason"`

	// ModifiedAt is when the file was last modified (from filesystem).
	ModifiedAt time.Time `json:"modified_at,omitempty"`

	// Details provides additional context for the conflict.
	Details string `json:"details,omitempty"`
}

// ActivityWindow represents a time window of agent activity.
type ActivityWindow struct {
	PaneID    string    `json:"pane_id"`
	AgentType string    `json:"agent_type"`
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	HasOutput bool      `json:"has_output"` // Whether output was detected during window
}

// GitFileStatus represents a file's status from git.
type GitFileStatus struct {
	Path       string    `json:"path"`
	Status     string    `json:"status"` // M, A, D, ??, etc.
	Staged     bool      `json:"staged"`
	ModifiedAt time.Time `json:"modified_at,omitempty"`
}

// ConflictDetector detects potential file conflicts across agents.
type ConflictDetector struct {
	activityWindows map[string][]ActivityWindow // paneID -> windows
	panes           []string                    // pane IDs in the order recorded
}

// NewConflictDetector creates a new conflict detector.
func NewConflictDetector() *ConflictDetector {
	return &ConflictDetector{activityWindows: make(map[string][]ActivityWindow)}
}

// RecordActivity records an activity window for a pane.
func (cd *ConflictDetector) RecordActivity(paneID, agentType string, start, end time.Time, hasOutput bool) {
	if _, ok := cd.activityWindows[paneID]; !ok {
		cd.panes = append(cd.panes, paneID)
	}
	cd.activityWindows[paneID] = append(cd.activityWindows[paneID], ActivityWindow{
		PaneID:    paneID,
		AgentType: agentType,
		Start:     start,
		End:       end,
		HasOutput: hasOutput,
	})
}

// ParseGitStatusPorcelain parses `git status --porcelain` output; a
// file's time is its modification time under repoPath.
func ParseGitStatusPorcelain(output, repoPath string) []GitFileStatus {
	var results []GitFileStatus

	// Don't TrimSpace the whole output - it would remove leading spaces from status codes
	// like " M file.go" where space means "not staged"
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimRight(line, "\r") // Handle CRLF
		if len(line) < 3 {
			continue
		}

		// Format: XY path
		// X = index status, Y = work tree status
		xy := line[:2]
		path := strings.TrimSpace(line[3:])

		// Handle renamed files (path contains " -> ")
		if idx := strings.Index(path, " -> "); idx >= 0 {
			path = path[idx+4:]
		}

		// Unquote if necessary (git quotes paths with spaces or special chars)
		if len(path) >= 2 && path[0] == '"' && path[len(path)-1] == '"' {
			if unquoted, err := strconv.Unquote(path); err == nil {
				path = unquoted
			}
		}

		status := GitFileStatus{
			Path:   path,
			Status: strings.TrimSpace(xy),
			Staged: xy[0] != ' ' && xy[0] != '?',
		}

		// Get file modification time
		fullPath := filepath.Join(repoPath, path)
		if info, err := os.Stat(fullPath); err == nil {
			status.ModifiedAt = info.ModTime()
		}

		results = append(results, status)
	}

	return results
}

// DetectConflicts analyzes git status and activity windows to detect
// conflicts: the files whose confidence is 0.5 or more.
func (cd *ConflictDetector) DetectConflicts(gitStatus []GitFileStatus) []DetectedConflict {
	var conflicts []DetectedConflict
	for _, file := range gitStatus {
		conflict := cd.analyzeFileConflict(file)
		if conflict.Confidence >= 0.5 {
			conflicts = append(conflicts, *conflict)
		}
	}
	return conflicts
}

// analyzeFileConflict analyzes a single file for conflicts.
func (cd *ConflictDetector) analyzeFileConflict(file GitFileStatus) *DetectedConflict {
	conflict := &DetectedConflict{
		Path:       file.Path,
		GitStatus:  file.Status,
		ModifiedAt: file.ModifiedAt,
	}

	// Find panes with activity during file modification window
	conflict.LikelyModifiers = cd.findLikelyModifiers(file)

	// Score the conflict
	scoreConflict(conflict, len(conflict.LikelyModifiers))

	return conflict
}

// findLikelyModifiers returns pane IDs with activity around the file modification time.
func (cd *ConflictDetector) findLikelyModifiers(file GitFileStatus) []string {
	if file.ModifiedAt.IsZero() {
		return nil
	}

	var modifiers []string

	// Look for activity windows that contain the file modification time
	// Use a tolerance window of 60 seconds before and after
	tolerance := 60 * time.Second
	checkStart := file.ModifiedAt.Add(-tolerance)
	checkEnd := file.ModifiedAt.Add(tolerance)

	for _, paneID := range cd.panes {
		for _, w := range cd.activityWindows[paneID] {
			// Check if window overlaps with modification time window
			if w.Start.Before(checkEnd) && w.End.After(checkStart) {
				modifiers = append(modifiers, paneID)
				break
			}
		}
	}

	return modifiers
}

// scoreConflict calculates the conflict confidence score, for a file
// that no reservation holds.
func scoreConflict(conflict *DetectedConflict, modifierCount int) {
	switch {
	case modifierCount > 1:
		// Multiple modifiers - high confidence of conflict
		conflict.Confidence = 0.9
		conflict.Reason = ReasonConcurrentActivity
		conflict.Details = "Multiple agents had activity when this file was modified"

	case modifierCount == 0:
		// Unknown modifier, no reservations
		conflict.Confidence = 0.6
		conflict.Reason = ReasonUnclaimedModification
		conflict.Details = "File modified with no tracked activity or reservations"

	default:
		// Single modifier, no reservations (normal case)
		conflict.Confidence = 0.4
		conflict.Reason = ReasonConcurrentActivity
		conflict.Details = "File modified by single agent without reservation"
	}
}
