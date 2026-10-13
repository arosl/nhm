// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/bv/bv_pure_helpers_test.go at commit 9d5ed977; see LICENSE in
// this directory and PROVENANCE.md. Local changes: this header. Left out
// with markdown.go and client.go (not taken): TestRenderRecommendation,
// TestRenderHealthSummary, TestNewBVClientWithOptionsDefaults.

package bv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// bv.go: extractStatusField
// =============================================================================

func TestExtractStatusField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		payload    map[string]interface{}
		wantStatus string
		wantOK     bool
	}{
		{"valid status", map[string]interface{}{"status": "open"}, "open", true},
		{"missing key", map[string]interface{}{"other": "val"}, "", false},
		{"not a string", map[string]interface{}{"status": 42}, "", false},
		{"empty string", map[string]interface{}{"status": ""}, "", false},
		{"whitespace only", map[string]interface{}{"status": "   "}, "", false},
		{"trimmed", map[string]interface{}{"status": "  closed  "}, "closed", true},
		{"nil payload", nil, "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := extractStatusField(tc.payload)
			if ok != tc.wantOK {
				t.Errorf("extractStatusField(%v) ok = %v, want %v", tc.payload, ok, tc.wantOK)
			}
			if got != tc.wantStatus {
				t.Errorf("extractStatusField(%v) = %q, want %q", tc.payload, got, tc.wantStatus)
			}
		})
	}
}

// =============================================================================
// triage.go: normalizeTriageDir
// =============================================================================

func TestNormalizeTriageDir(t *testing.T) {
	t.Parallel()

	t.Run("absolute path returned as-is", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		got, err := normalizeTriageDir(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		abs, _ := filepath.Abs(dir)
		if got != abs {
			t.Errorf("normalizeTriageDir(%q) = %q, want %q", dir, got, abs)
		}
	})

	t.Run("empty uses cwd", func(t *testing.T) {
		t.Parallel()
		got, err := normalizeTriageDir("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == "" {
			t.Error("expected non-empty result for empty input")
		}
		if !filepath.IsAbs(got) {
			t.Errorf("expected absolute path, got %q", got)
		}
	})

	t.Run("subdir resolves to beads root", func(t *testing.T) {
		t.Parallel()
		projectDir := t.TempDir()
		subDir := filepath.Join(projectDir, "internal", "robot")
		if err := os.MkdirAll(filepath.Join(projectDir, ".beads"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(subDir, 0o755); err != nil {
			t.Fatal(err)
		}

		got, err := normalizeTriageDir(subDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != projectDir {
			t.Errorf("normalizeTriageDir(%q) = %q, want %q", subDir, got, projectDir)
		}
	})
}

// =============================================================================
// triage.go: InvalidateTriageCache
// =============================================================================

func TestInvalidateTriageCachePure(t *testing.T) {
	dir := t.TempDir()
	cleanup := primeTriageCache(t, dir)
	defer cleanup()

	// Cache should be valid after priming
	if !IsCacheValid() {
		t.Fatal("cache should be valid after priming")
	}

	InvalidateTriageCache()

	if IsCacheValid() {
		t.Error("cache should be invalid after invalidation")
	}
}

func TestGetTriageWithTimeoutIncludesRunLockWait(t *testing.T) {
	InvalidateTriageCache()
	triageRunMu.Lock()
	defer triageRunMu.Unlock()

	start := time.Now()
	_, err := GetTriageWithTimeout(t.TempDir(), 20*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("GetTriageWithTimeout returned nil error while triage run lock was held")
	}
	if !strings.Contains(err.Error(), "bv timed out after 20ms") {
		t.Fatalf("GetTriageWithTimeout error = %q, want timeout", err.Error())
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("GetTriageWithTimeout waited %s for held run lock, want bounded wait", elapsed)
	}
}

// =============================================================================
// triage.go: IsCacheValid
// =============================================================================

func TestIsCacheValid(t *testing.T) {
	// Start with no cache
	InvalidateTriageCache()
	if IsCacheValid() {
		t.Error("empty cache should not be valid")
	}

	// Prime cache
	dir := t.TempDir()
	cleanup := primeTriageCache(t, dir)
	defer cleanup()

	if !IsCacheValid() {
		t.Error("primed cache should be valid")
	}
}

// =============================================================================
// triage.go: GetCacheAge
// =============================================================================

func TestGetCacheAge(t *testing.T) {
	// No cache => age is 0
	InvalidateTriageCache()
	if age := GetCacheAge(); age != 0 {
		t.Errorf("empty cache age = %v, want 0", age)
	}

	// Prime cache
	dir := t.TempDir()
	cleanup := primeTriageCache(t, dir)
	defer cleanup()

	time.Sleep(10 * time.Millisecond)
	age := GetCacheAge()
	if age < 10*time.Millisecond {
		t.Errorf("cache age = %v, expected >= 10ms", age)
	}
}

// =============================================================================
// bv.go: getNoDBState / setNoDBState
// =============================================================================

func TestGetSetNoDBState(t *testing.T) {
	// Unknown dir returns false
	if getNoDBState("/nonexistent/path/for/test") {
		t.Error("unknown dir should return false")
	}

	// Set and retrieve
	setNoDBState("/test/dir/for/nodb", true)
	if !getNoDBState("/test/dir/for/nodb") {
		t.Error("expected true after set")
	}

	// Overwrite with false
	setNoDBState("/test/dir/for/nodb", false)
	if getNoDBState("/test/dir/for/nodb") {
		t.Error("expected false after overwrite")
	}
}
