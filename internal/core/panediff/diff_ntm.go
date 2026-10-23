// Ported from ntm (github.com/Dicklesworthstone/ntm)
// internal/output/diff.go at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: the package (nhm's
// panediff), and ComputeDiff's and DiffResult's comments.

// Package panediff is nhm diff's comparison of two panes' output: ntm's
// ComputeDiff, and ntm's code block parser for --code-only.
package panediff

import (
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// DiffResult is ntm's comparison of two panes' output: the line counts,
// the similarity (0 to 1) and diff-match-patch's patch text.
type DiffResult struct {
	Pane1       string  `json:"pane1"`
	Pane2       string  `json:"pane2"`
	LineCount1  int     `json:"lines1"`
	LineCount2  int     `json:"lines2"`
	Similarity  float64 `json:"similarity"`
	UnifiedDiff string  `json:"diff,omitempty"`
}

// ComputeDiff compares two outputs, as ntm diff does: a character diff,
// its Levenshtein distance over the longer text for the similarity, and
// the patch text.
func ComputeDiff(pane1, content1, pane2, content2 string) *DiffResult {
	dmp := diffmatchpatch.New()

	// Compute diffs
	// Using character-based diff for precision, but could use line-based if performance is an issue
	diffs := dmp.DiffMain(content1, content2, true)

	// Compute similarity (0-1)
	dist := dmp.DiffLevenshtein(diffs)
	maxLen := len(content1)
	if len(content2) > maxLen {
		maxLen = len(content2)
	}
	similarity := 0.0
	if maxLen > 0 {
		similarity = 1.0 - (float64(dist) / float64(maxLen))
		if similarity < 0 {
			similarity = 0
		}
	}

	// Create unified diff (patches)
	patches := dmp.PatchMake(content1, diffs)
	unified := dmp.PatchToText(patches)

	return &DiffResult{
		Pane1:       pane1,
		Pane2:       pane2,
		LineCount1:  countLines(content1),
		LineCount2:  countLines(content2),
		Similarity:  similarity,
		UnifiedDiff: unified,
	}
}

// countLines counts the number of lines in a string.
// Empty strings return 0, trailing newlines don't count as extra lines.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	// Remove trailing newline to avoid counting an empty final line
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return 0 // String was just a newline
	}
	return len(strings.Split(s, "\n"))
}
