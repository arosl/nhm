// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/synthesis_test.go (TestParseGitStatusPorcelain,
// TestConflictDetector_ScoreConflict, TestConflictDetector_FindLikelyModifiers)
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: the reservation cases left out (nhm's detector reads no
// reservation); TestDetectConflicts is nhm's: the threshold, the
// modification time and the order of the modifiers.

package robot

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestParseGitStatusPorcelain(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   []GitFileStatus
	}{
		{"empty output", "", nil},
		{"modified file", " M file.go", []GitFileStatus{{Path: "file.go", Status: "M", Staged: false}}},
		{"staged file", "M  file.go", []GitFileStatus{{Path: "file.go", Status: "M", Staged: true}}},
		{"untracked file", "?? newfile.go", []GitFileStatus{{Path: "newfile.go", Status: "??", Staged: false}}},
		{"multiple files", " M file1.go\nA  file2.go\n?? file3.go", []GitFileStatus{
			{Path: "file1.go", Status: "M", Staged: false},
			{Path: "file2.go", Status: "A", Staged: true},
			{Path: "file3.go", Status: "??", Staged: false},
		}},
		{"renamed file", "R  old.go -> new.go", []GitFileStatus{{Path: "new.go", Status: "R", Staged: true}}},
		{"quoted path", "?? \"a b.go\"", []GitFileStatus{{Path: "a b.go", Status: "??", Staged: false}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseGitStatusPorcelain(tt.output, "/nonexistent")
			if !slices.Equal(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestConflictDetector_ScoreConflict(t *testing.T) {
	tests := []struct {
		name           string
		modifiers      []string
		wantConfidence float64
		wantReason     ConflictReason
	}{
		{"multiple modifiers - high conflict", []string{"%1", "%2"}, 0.9, ReasonConcurrentActivity},
		{"no modifier, no holders", nil, 0.6, ReasonUnclaimedModification},
		{"single modifier, no holders - normal", []string{"%1"}, 0.4, ReasonConcurrentActivity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conflict := &DetectedConflict{LikelyModifiers: tt.modifiers}
			scoreConflict(conflict, len(tt.modifiers))
			if conflict.Confidence != tt.wantConfidence || conflict.Reason != tt.wantReason {
				t.Errorf("score = %v %v, want %v %v", conflict.Confidence, conflict.Reason, tt.wantConfidence, tt.wantReason)
			}
		})
	}
}

func TestConflictDetector_FindLikelyModifiers(t *testing.T) {
	cd := NewConflictDetector()
	now := time.Now()

	// Record activity for two panes
	cd.RecordActivity("%1", "claude", now.Add(-2*time.Minute), now.Add(-1*time.Minute), true)
	cd.RecordActivity("%2", "codex", now.Add(-30*time.Second), now.Add(30*time.Second), true)

	tests := []struct {
		name       string
		modifiedAt time.Time
		wantCount  int
	}{
		{"modification during pane 1 activity", now.Add(-90 * time.Second), 1},
		{"modification during pane 2 activity", now, 1},
		{"modification during both activities", now.Add(-30 * time.Second), 2}, // within tolerance of both
		{"modification outside all activities", now.Add(-10 * time.Minute), 0},
		{"no modification time", time.Time{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := GitFileStatus{Path: "test.go", ModifiedAt: tt.modifiedAt}
			if modifiers := cd.findLikelyModifiers(file); len(modifiers) != tt.wantCount {
				t.Errorf("found %d modifiers, want %d", len(modifiers), tt.wantCount)
			}
		})
	}
}

// DetectConflicts keeps the files at 0.5 or more: a file changed while
// several panes were active, with them in the order recorded, and a file
// changed outside every window or gone; a file that one pane touched is
// dropped. A file's time is its modification time in the repo.
func TestDetectConflicts(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for name, mtime := range map[string]time.Time{"new.go": now, "old.go": now.Add(-2 * time.Hour)} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	files := ParseGitStatusPorcelain(" M new.go\n?? old.go\n D gone.go\n", dir)
	if files[0].ModifiedAt.IsZero() || !files[2].ModifiedAt.IsZero() {
		t.Fatalf("times = %+v", files)
	}
	cd := NewConflictDetector()
	for _, p := range []string{"w1:p3", "w1:p1", "w1:p2"} {
		cd.RecordActivity(p, "claude", now.Add(-15*time.Minute), now, true)
	}
	got := cd.DetectConflicts(files)
	if len(got) != 3 || got[0].Path != "new.go" || got[0].Confidence != 0.9 ||
		!slices.Equal(got[0].LikelyModifiers, []string{"w1:p3", "w1:p1", "w1:p2"}) ||
		got[1].Path != "old.go" || got[1].Reason != ReasonUnclaimedModification || got[1].LikelyModifiers != nil ||
		got[2].Path != "gone.go" || got[2].Confidence != 0.6 {
		t.Errorf("conflicts = %+v", got)
	}
	one := NewConflictDetector()
	one.RecordActivity("w1:p1", "claude", now.Add(-15*time.Minute), now, true)
	if got := one.DetectConflicts(files[:1]); got != nil {
		t.Errorf("one pane = %+v, want none", got)
	}
}
