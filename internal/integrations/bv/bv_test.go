// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/bv/bv_test.go at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: this header; imports. Kept:
// getProjectRoot, TestIsInstalled, TestDriftStatusString, TestNotInstalled,
// the containsSubstring helpers, TestIsNoBeadsDBError, TestContainsString,
// TestIsNotInitialized. Left out with the readers nhm does not take: the
// insights test cache and the tests of insights, priority, plan (covered by
// TestGetPlanContextRequiresPlanTracksEnvelope), recipes, drift, graph
// position, health and HasLocalBeadsDB.

package bv

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// getProjectRoot finds the project root by looking for .beads directory
func getProjectRoot() string {
	dir, _ := os.Getwd()
	for dir != "/" {
		if _, err := os.Stat(filepath.Join(dir, ".beads")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	return ""
}

func TestIsInstalled(t *testing.T) {
	// This test verifies the function works - actual result depends on environment
	result := IsInstalled()
	t.Logf("bv installed: %v", result)
}

func TestDriftStatusString(t *testing.T) {
	tests := []struct {
		status DriftStatus
		want   string
	}{
		{DriftOK, "OK"},
		{DriftCritical, "critical"},
		{DriftWarning, "warning"},
		{DriftNoBaseline, "no baseline"},
		{DriftStatus(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := tt.status.String()
			if got != tt.want {
				t.Errorf("DriftStatus(%d).String() = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestNotInstalled(t *testing.T) {
	// Test error behavior when bv is not in PATH
	// We can't easily test this without modifying PATH, so just verify the error exists
	if ErrNotInstalled == nil {
		t.Error("ErrNotInstalled should not be nil")
	}
	if ErrNoBaseline == nil {
		t.Error("ErrNoBaseline should not be nil")
	}
}

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestIsNoBeadsDBError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stderr string
		want   bool
	}{
		{"no beads database found", "Error: no beads database found in current directory", true},
		{"use br --no-db", "Use 'br --no-db' to run without a database", true},
		{"uppercase variant", "NO BEADS DATABASE FOUND", true},
		{"mixed case", "No Beads Database Found", true},
		{"unrelated error", "connection timeout", false},
		{"empty string", "", false},
		{"partial match", "no beads", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isNoBeadsDBError(tc.stderr); got != tc.want {
				t.Errorf("isNoBeadsDBError(%q) = %v, want %v", tc.stderr, got, tc.want)
			}
		})
	}
}

func TestContainsString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		list  []string
		value string
		want  bool
	}{
		{"found in list", []string{"a", "b", "c"}, "b", true},
		{"not found", []string{"a", "b", "c"}, "d", false},
		{"empty list", []string{}, "a", false},
		{"nil list", nil, "a", false},
		{"empty value in list", []string{"", "a"}, "", true},
		{"single element match", []string{"x"}, "x", true},
		{"single element no match", []string{"x"}, "y", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := containsString(tc.list, tc.value); got != tc.want {
				t.Errorf("containsString(%v, %q) = %v, want %v", tc.list, tc.value, got, tc.want)
			}
		})
	}
}

// TestHasLocalBeadsDB verifies the recovery-list gating predicate. When the
// directory has no local .beads/ directory, recovery list reads must short-
// circuit empty rather than letting br walk up into a parent repo (#130).
func TestIsNotInitialized(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"br list --json: exit status 1: no .beads directory found", true},
		{"beads workspace not initialized", true},
		{"br list: {\"error\":{\"code\":\"NOT_INITIALIZED\",\"message\":\"...\"}}", true},
		{"No beads database found in this project", true},
		{"exit status 1: run 'br init' to create a workspace", true},
		// bv's stderr, verbatim, where no .beads directory exists.
		{"bv --robot-insights: exit status 1: Error loading beads: failed to read beads directory: open /p/.beads: no such file or directory\nMake sure you are in a project initialized with 'br init'.", true},
		// bv's stderr, verbatim, for an unreadable .beads: a fault, not an
		// empty project, despite the same trailing br init hint.
		{"bv --robot-insights: exit status 1: Error loading beads: failed to inspect redirect file /p/.beads/redirect: stat /p/.beads/redirect: permission denied\nMake sure you are in a project initialized with 'br init'.", false},
		{"br list --json: exit status 1: database disk image is malformed", false},
		{"exec: \"br\": executable file not found in $PATH", false},
		{"", false},
	}
	for _, tt := range tests {
		var err error
		if tt.msg != "" {
			err = errors.New(tt.msg)
		}
		if got := IsNotInitialized(err); got != tt.want {
			t.Errorf("IsNotInitialized(%q) = %v, want %v", tt.msg, got, tt.want)
		}
	}
}
