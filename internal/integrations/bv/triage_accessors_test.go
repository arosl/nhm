// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/bv/triage_accessors_test.go at commit 9d5ed977; see LICENSE in
// this directory and PROVENANCE.md. Local changes: this header. Left out:
// TestTriageAccessors_CachePrimed, which reads accessors of the triage
// cache that live in files nhm does not take.

package bv

import (
	"path/filepath"
	"testing"
	"time"
)

// primeTriageCache seeds the package-level triage cache with synthetic data,
// enabling tests of accessor functions without running the external bv CLI.
// The dir must already be an absolute path (use t.TempDir()).
// Returns a cleanup function to restore the original cache state.
func primeTriageCache(t *testing.T, dir string) func() {
	t.Helper()

	absDir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("abs dir: %v", err)
	}

	triageCacheMu.Lock()
	origCache := triageCache
	origDir := triageCacheDir
	origTime := triageCacheTime
	origTTL := triageCacheTTL

	triageCache = syntheticTriageResponse()
	triageCacheDir = absDir
	triageCacheTime = time.Now()
	triageCacheTTL = 5 * time.Minute
	triageCacheMu.Unlock()

	return func() {
		triageCacheMu.Lock()
		triageCache = origCache
		triageCacheDir = origDir
		triageCacheTime = origTime
		triageCacheTTL = origTTL
		triageCacheMu.Unlock()
	}
}

func syntheticTriageResponse() *TriageResponse {
	return &TriageResponse{
		GeneratedAt: time.Date(2026, 2, 5, 12, 0, 0, 0, time.UTC),
		DataHash:    "abc123",
		Triage: TriageData{
			Meta: TriageMeta{
				Version:     "1.0.0",
				Phase2Ready: true,
				IssueCount:  42,
			},
			QuickRef: TriageQuickRef{
				OpenCount:       10,
				ActionableCount: 7,
				BlockedCount:    3,
				InProgressCount: 5,
				TopPicks: []TriageTopPick{
					{ID: "bd-aaa", Title: "Fix auth", Score: 0.95, Reasons: []string{"high impact"}, Unblocks: 3},
					{ID: "bd-bbb", Title: "Add tests", Score: 0.80, Reasons: []string{"unblocks work"}, Unblocks: 1},
					{ID: "bd-ccc", Title: "Refactor", Score: 0.75, Reasons: []string{"reduces debt"}, Unblocks: 0},
				},
			},
			Recommendations: []TriageRecommendation{
				{ID: "bd-aaa", Title: "Fix auth", Type: "bug", Status: "open", Priority: 1, Score: 0.95, Action: "Fix it", Reasons: []string{"high impact"}},
				{ID: "bd-bbb", Title: "Add tests", Type: "task", Status: "open", Priority: 2, Score: 0.80, Action: "Write tests", Reasons: []string{"unblocks work"}},
				{ID: "bd-ccc", Title: "Refactor", Type: "chore", Status: "open", Priority: 3, Score: 0.75, Action: "Clean up", Reasons: []string{"reduces debt"}},
				{ID: "bd-ddd", Title: "Docs", Type: "task", Status: "open", Priority: 3, Score: 0.60, Action: "Document", Reasons: []string{"needed"}},
			},
			QuickWins: []TriageRecommendation{
				{ID: "bd-eee", Title: "Quick fix", Score: 1.2, Action: "Do it"},
				{ID: "bd-fff", Title: "Easy win", Score: 0.9, Action: "Ship it"},
			},
			BlockersToClear: []BlockerToClear{
				{ID: "bd-ggg", Title: "Blocker A", UnblocksCount: 5, UnblocksIDs: []string{"bd-1", "bd-2", "bd-3", "bd-4", "bd-5"}, Actionable: true},
				{ID: "bd-hhh", Title: "Blocker B", UnblocksCount: 2, UnblocksIDs: []string{"bd-6", "bd-7"}, Actionable: false, BlockedBy: []string{"bd-iii"}},
			},
			ProjectHealth: &ProjectHealth{
				StatusDistribution: map[string]int{"open": 10, "closed": 32},
				GraphMetrics:       &GraphMetrics{TotalNodes: 42, TotalEdges: 38, Density: 0.044},
			},
			Commands: map[string]string{
				"claim_top":    "br update bd-aaa --status in_progress",
				"list_blocked": "br blocked --json",
			},
		},
	}
}

func TestGetNextRecommendation_EmptyRecs(t *testing.T) {
	dir := t.TempDir()
	cleanup := primeTriageCache(t, dir)
	defer cleanup()

	// Override cache with empty recommendations
	triageCacheMu.Lock()
	triageCache.Triage.Recommendations = nil
	triageCacheMu.Unlock()

	rec, err := GetNextRecommendation(dir)
	if err != nil {
		t.Fatalf("GetNextRecommendation: %v", err)
	}
	if rec != nil {
		t.Errorf("expected nil recommendation for empty list, got %v", rec)
	}
}

func TestGetTriage_ExpiredCacheFallsThrough(t *testing.T) {
	dir := t.TempDir()
	cleanup := primeTriageCache(t, dir)
	defer cleanup()

	// Expire the cache
	triageCacheMu.Lock()
	triageCacheTime = time.Now().Add(-10 * time.Minute)
	triageCacheMu.Unlock()

	// This should miss the cache and attempt to run bv (which will fail on temp dir)
	_, err := GetTriage(dir)
	if err == nil {
		t.Log("GetTriage returned nil error — bv may be installed and found a project")
	}
	// We exercised the expired-cache code path either way
}

func TestGetTriage_DirMismatchMissesCache(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	cleanup := primeTriageCache(t, dir1)
	defer cleanup()

	// Request triage for a different directory — should miss cache
	_, err := GetTriage(dir2)
	if err == nil {
		t.Log("GetTriage returned nil error for different dir — bv may be installed")
	}
	// Exercised the cache-miss-by-directory code path
}
