// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/summary/generator_test.go, internal/summary/summary_test.go and
// internal/summary/generator_branches_test.go at commit 9d5ed977; see
// LICENSE in this directory and PROVENANCE.md. Local changes: the
// summarizer, thread and prompt tests left out (nhm has neither); the
// tables kept and compared as ntm's; TestSummarizeSessionUTC is nhm's.

package summary

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSummarizeSessionMissingOutput(t *testing.T) {
	if _, err := SummarizeSession(context.Background(), Options{}); err == nil {
		t.Fatalf("expected error for missing outputs")
	}
}

func TestSummarizeSessionBriefFallback(t *testing.T) {
	output := strings.Join([]string{
		"## Accomplishments", "- Implemented SummarizeSession", "- Added tests", "",
		"## Changes", "- Updated internal/summary/generator.go", "- Modified internal/cli/summary.go", "",
		"## Pending", "- Wire into CLI", "- Add docs", "",
		"## Errors", "- Failed: lint error in file", "",
		"## Decisions", "- Using regex parsing", "",
		"Created internal/summary/generator.go", "Modified internal/cli/summary.go",
	}, "\n")
	summary, err := SummarizeSession(context.Background(), Options{
		Session: "demo", Outputs: []AgentOutput{{AgentID: "a1", AgentType: "cc", Output: output}}, Format: FormatBrief,
	})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if len(summary.Accomplishments) == 0 || !strings.Contains(summary.Text, "Accomplishments") {
		t.Fatalf("summary = %+v", summary)
	}
	var created, modified bool
	for _, f := range summary.Files {
		created = created || f.Path == "internal/summary/generator.go" && f.Action == FileActionCreated
		modified = modified || f.Path == "internal/cli/summary.go" && f.Action == FileActionModified
	}
	if !created || !modified {
		t.Fatalf("expected file changes extracted (created=%v modified=%v)", created, modified)
	}
}

func TestSummarizeSessionIncludesGitChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	projectDir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = projectDir
	if err := cmd.Run(); err != nil {
		t.Skipf("git init failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "new.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	summary, err := SummarizeSession(context.Background(), Options{
		Session: "demo", Outputs: []AgentOutput{{AgentID: "a1", AgentType: "cc", Output: "did work"}},
		Format: FormatBrief, ProjectDir: projectDir, IncludeGitDiff: true,
	})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if !slices.Contains(summary.Files, FileChange{Path: "new.txt", Action: FileActionCreated}) {
		t.Fatalf("expected git-created file in summary, got: %#v", summary.Files)
	}
}

func TestSummarizeSessionCancellationStopsGitEnrichment(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "git-started")
	const script = "#!/bin/sh\nprintf ready > \"$NHM_SUMMARY_GIT_STARTED\"\nexec sleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NHM_SUMMARY_GIT_STARTED", started)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := SummarizeSession(ctx, Options{
			Session: "stopping", Outputs: []AgentOutput{{AgentID: "a1", AgentType: "cc", Output: "Finished the task"}},
			Format: FormatHandoff, ProjectDir: dir, IncludeGitDiff: true,
		})
		done <- err
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("summary returned before the git subprocess started: %v", err)
		case <-ctx.Done():
			t.Fatal("git enrichment never started")
		case <-ticker.C:
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("summary cancellation = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("summary ignored owner cancellation while git was running")
	}
}

func TestSummarizeSessionStructuredJSON(t *testing.T) {
	output := `{
  "summary": {
    "accomplishments": ["Implemented API"],
    "changes": ["Refactored router"],
    "pending": ["Add tests"],
    "errors": ["Error: foo"],
    "decisions": ["Use cobra"],
    "files": {
      "created": ["cmd/ntm/main.go"],
      "modified": ["internal/cli/root.go"],
      "deleted": ["old.txt"]
    }
  }
}`
	summary, err := SummarizeSession(context.Background(), Options{
		Session: "json", Outputs: []AgentOutput{{AgentID: "a1", AgentType: "cc", Output: output}}, Format: FormatDetailed,
	})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if !slices.Equal(summary.Accomplishments, []string{"Implemented API"}) {
		t.Fatalf("unexpected accomplishments: %+v", summary.Accomplishments)
	}
	for _, want := range []FileChange{
		{Path: "cmd/ntm/main.go", Action: FileActionCreated},
		{Path: "internal/cli/root.go", Action: FileActionModified},
		{Path: "old.txt", Action: FileActionDeleted},
	} {
		if !slices.Contains(summary.Files, want) {
			t.Errorf("%+v not in %+v", want, summary.Files)
		}
	}
}

func TestSummarizeSessionHandoffFormat(t *testing.T) {
	output := "Completed: Implemented session summarizer\nNext: Add wiring"
	summary, err := SummarizeSession(context.Background(), Options{
		Session: "handoff", Outputs: []AgentOutput{{AgentID: "a1", AgentType: "cc", Output: output}}, Format: FormatHandoff,
	})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if summary.Handoff == nil || !strings.Contains(summary.Text, "goal:") || !strings.Contains(summary.Text, "now:") {
		t.Fatalf("handoff = %+v, text %q", summary.Handoff, summary.Text)
	}
}

// The generated time and the handoff's stamps are UTC (rule 8).
func TestSummarizeSessionUTC(t *testing.T) {
	summary, err := SummarizeSession(context.Background(), Options{
		Session: "utc", Outputs: []AgentOutput{{AgentID: "a1", Output: "Fixed the parser"}}, Format: FormatHandoff,
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.GeneratedAt.Location() != time.UTC || summary.Handoff.CreatedAt.Location() != time.UTC ||
		summary.Handoff.Date != summary.Handoff.CreatedAt.Format("2006-01-02") {
		t.Errorf("times = %v %v %s", summary.GeneratedAt, summary.Handoff.CreatedAt, summary.Handoff.Date)
	}
}

func TestExtractSectionItems(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		expected []string
	}{
		{"basic accomplishments", "## Accomplishments\n- Did thing one\n- Did thing two\n## Changes\n- Changed something",
			[]string{"Did thing one", "Did thing two"}},
		{"alternative header format", "Completed:\n- Task A\n- Task B", []string{"Task A", "Task B"}},
		{"numbered list", "Done\n1. First item\n2. Second item", []string{"First item", "Second item"}},
		{"checkbox items", "Summary\n[x] Completed task\n[ ] Incomplete task", []string{"Completed task", "Incomplete task"}},
		{"empty section", "## Accomplishments\n## Changes", nil},
		{"no matching section", "Some random text without sections", nil},
	}
	for _, tc := range tests {
		if got := extractSectionItems(tc.text, accomplishmentHeaders); !slices.Equal(got, tc.expected) {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.expected)
		}
	}
}

func TestExtractKeyActions(t *testing.T) {
	tests := []struct {
		name     string
		lines    []string
		expected int
	}{
		{"implemented patterns", []string{"Implemented the new API endpoint", "Fixed the login bug", "Added unit tests for auth module"}, 3},
		{"created patterns", []string{"Created new config file", "Completed the migration", "Resolved merge conflict"}, 3},
		{"no matching patterns", []string{"This is just a comment", "Looking at the code"}, 0},
		{"long lines filtered", []string{"Implemented " + strings.Repeat("x", 250)}, 0},
		{"empty lines", []string{"", "  ", "\t"}, 0},
		{"file-only action line", []string{"Created internal/a.go"}, 0},
	}
	for _, tc := range tests {
		if got := extractKeyActions(tc.lines); len(got) != tc.expected {
			t.Errorf("%s: got %q, want %d", tc.name, got, tc.expected)
		}
	}
}

func TestExtractChangeHighlights(t *testing.T) {
	tests := []struct {
		name     string
		lines    []string
		expected int
	}{
		{"update patterns", []string{"Updated the database schema", "Modified the config parser", "Refactored authentication logic"}, 3},
		{"change patterns", []string{"Changed error handling", "Rewrote the test suite", "Renamed variable for clarity"}, 3},
		{"no matching patterns", []string{"This is documentation", "A simple comment"}, 0},
	}
	for _, tc := range tests {
		if got := extractChangeHighlights(tc.lines); len(got) != tc.expected {
			t.Errorf("%s: got %q, want %d", tc.name, got, tc.expected)
		}
	}
}

func TestExtractErrorLines(t *testing.T) {
	tests := []struct {
		name     string
		lines    []string
		expected int
	}{
		{"error patterns", []string{"Error: connection refused", "Failed to compile module", "Panic: nil pointer dereference"}, 3},
		{"exception patterns", []string{"Exception in thread main"}, 1},
		{"no matching patterns", []string{"All tests passed", "Build successful"}, 0},
		{"empty lines", []string{"", "  "}, 0},
	}
	for _, tc := range tests {
		if got := extractErrorLines(tc.lines); len(got) != tc.expected {
			t.Errorf("%s: got %q, want %d", tc.name, got, tc.expected)
		}
	}
}

func TestExtractPendingInline(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  []string
	}{
		{"todo patterns", []string{"TODO: fix the bug", "todo implement feature"}, []string{"fix the bug", "implement feature"}},
		{"next patterns", []string{"Next: add tests", "next: refactor code"}, []string{"add tests", "refactor code"}},
		{"pending patterns", []string{"Pending: review changes", "Remaining: documentation"}, []string{"review changes", "documentation"}},
		{"no matching patterns", []string{"This is done", "Complete"}, nil},
	}
	for _, tc := range tests {
		if got := extractPendingInline(tc.lines); !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestExtractFileChanges(t *testing.T) {
	tests := []struct {
		name      string
		lines     []string
		expectMin int
	}{
		{"go file paths", []string{"Created internal/summary/generator.go", "Modified cmd/ntm/main.go"}, 2},
		{"various extensions", []string{"Updated src/components/Button.tsx", "Added tests/unit/test_main.py", "Changed config/settings.yaml"}, 3},
		{"relative paths", []string{"Edited ./internal/cli/root.go"}, 1},
		{"no file paths", []string{"Just some text without files", "More text here"}, 0},
	}
	for _, tc := range tests {
		if got := extractFileChanges(tc.lines); len(got) < tc.expectMin {
			t.Errorf("%s: got %+v, want at least %d", tc.name, got, tc.expectMin)
		}
	}
}

func TestInferFileAction(t *testing.T) {
	tests := []struct{ line, path, want string }{
		{"Created internal/a.go", "internal/a.go", FileActionCreated},
		{"new file: b.go", "b.go", FileActionCreated},
		{"Updated internal/a.go", "internal/a.go", FileActionModified},
		{"Removed old.go", "old.go", FileActionDeleted},
		{"Reading config.yaml", "config.yaml", FileActionRead},
		{"Opened x.go to look", "x.go", FileActionRead},
		{"see x.go", "x.go", FileActionUnknown},
	}
	for _, tc := range tests {
		if got := inferFileAction(tc.line, tc.path); got != tc.want {
			t.Errorf("inferFileAction(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

func TestBuildHandoffSummary(t *testing.T) {
	summary := &SessionSummary{
		Session:         "handoff-test",
		Accomplishments: []string{"Completed auth system"},
		Pending:         []string{"Add unit tests"},
		Errors:          []string{"Build failed once"},
		Files: []FileChange{
			{Path: "internal/auth.go", Action: FileActionCreated},
			{Path: "internal/cli.go", Action: FileActionModified},
			{Path: "old.go", Action: FileActionDeleted},
			{Path: "read.go", Action: FileActionRead},
		},
	}
	h := buildHandoffSummary(summary)
	if h.Session != "handoff-test" || h.Goal != "Completed auth system" || h.Now != "Add unit tests" ||
		!slices.Equal(h.Blockers, []string{"Build failed once"}) || h.Status != "blocked" || h.Outcome != "PARTIAL_MINUS" ||
		!slices.Equal(h.Files.Created, []string{"internal/auth.go"}) || !slices.Equal(h.Files.Modified, []string{"internal/cli.go"}) ||
		!slices.Equal(h.Files.Deleted, []string{"old.go"}) {
		t.Errorf("handoff = %+v", h)
	}
	if h := buildHandoffSummary(&SessionSummary{Session: "s"}); h.Goal != "Session summary" || h.Now != "Review recent changes" ||
		h.Status != "partial" || h.Outcome != "PARTIAL_PLUS" {
		t.Errorf("empty handoff = %+v", h)
	}
}

func TestTruncateToTokens(t *testing.T) {
	if got := truncateToTokens("Hello world", 100); got != "Hello world" {
		t.Errorf("short = %q", got)
	}
	if got := truncateToTokens(strings.Repeat("a", 1000), 10); got != strings.Repeat("a", 40)+"\n\n[Summary truncated due to token limit]" {
		t.Errorf("long = %q", got)
	}
}

func TestMergeFileChanges(t *testing.T) {
	existing := []FileChange{{Path: "a.go", Action: FileActionCreated}}
	incoming := []FileChange{
		{Path: "a.go", Action: FileActionCreated, Context: "new file"},
		{Path: "b.go", Action: FileActionModified, Context: "updated"},
	}
	got := mergeFileChanges(existing, incoming)
	if len(got) != 2 || got[0].Context != "new file" || got[1].Path != "b.go" {
		t.Fatalf("merged = %+v", got)
	}
}

func TestAggregateOutputsBasic(t *testing.T) {
	outputs := []AgentOutput{
		{AgentID: "agent1", Output: "\nSome chat text.\n{\n\t\"accomplishments\": [\"Fixed bug A\"],\n\t\"changes\": [{\"path\": \"main.go\", \"action\": \"modified\"}]\n}\nSome more text.\n- Next: Fix bug B\n"},
		{AgentID: "agent2", Output: "\n## Accomplishments\n- Added feature C\n\n## Files\n- created utils.go\n"},
	}
	data := aggregateOutputs(outputs)
	if !slices.Equal(data.accomplishments, []string{"Fixed bug A", "Added feature C"}) || !slices.Equal(data.pending, []string{"Fix bug B"}) {
		t.Errorf("data = %+v", data)
	}
	files := map[string]string{}
	for _, f := range data.files {
		files[f.Path] = f.Action
	}
	if files["main.go"] != "modified" || files["utils.go"] != "created" {
		t.Errorf("files = %v", files)
	}
}

func TestExtractCompleteJSON(t *testing.T) {
	input := []string{"prefix", "{", `  "key": "value",`, `  "nested": {`, `    "a": 1`, `  }`, "}", "suffix"}
	jsonStr, next := extractCompleteJSON(input, 1)
	if next != 7 || !isValidJSON(jsonStr) {
		t.Errorf("got %q %d", jsonStr, next)
	}
}

func TestFormats(t *testing.T) {
	s := &SessionSummary{Session: "s", Accomplishments: []string{"a1", "a2", "a3", "a4"}, Errors: []string{"e1", "e2", "e3"},
		Files: []FileChange{{Path: "x.go", Action: "modified", Context: "edited x.go"}}}
	if got := formatBrief(s); got != "Session s summary\nAccomplishments: a1; a2; a3\nFiles: modified x.go\nErrors: e1; e2" {
		t.Errorf("brief = %q", got)
	}
	if got := formatBrief(&SessionSummary{}); got != "Session (unknown) summary" {
		t.Errorf("empty brief = %q", got)
	}
	want := "## Session Summary: s\n\n## Accomplishments\n- a1\n- a2\n- a3\n- a4\n\n## Files\n- modified x.go — edited x.go\n\n## Errors\n- e1\n- e2\n- e3"
	if got := formatDetailed(s); got != want {
		t.Errorf("detailed = %q", got)
	}
	if formatHandoff(nil) != "" {
		t.Error("nil handoff")
	}
}

func TestRenderSummary(t *testing.T) {
	if RenderSummary(nil, FormatBrief) != "" {
		t.Error("nil")
	}
	s := &SessionSummary{Session: "s", Format: FormatBrief, Accomplishments: []string{"Implemented auth flow"}, Text: "kept"}
	if got := RenderSummary(s, FormatBrief); !strings.Contains(got, "Implemented auth flow") {
		t.Errorf("brief = %q", got)
	}
	if got := RenderSummary(s, FormatDetailed); !strings.HasPrefix(got, "## Session Summary: s") {
		t.Errorf("detailed = %q", got)
	}
	if got := RenderSummary(s, FormatHandoff); !strings.Contains(got, "goal: Implemented auth flow") {
		t.Errorf("handoff = %q", got)
	}
	if got := RenderSummary(s, "unknown"); got != "kept" {
		t.Errorf("unknown = %q", got)
	}
	if got := RenderSummary(&SessionSummary{Session: "n", Accomplishments: []string{"Did things"}}, "unknown"); got == "" {
		t.Error("unknown without text")
	}
	if got := RenderSummary(s, ""); !strings.Contains(got, "Implemented auth flow") {
		t.Errorf("default = %q", got)
	}
}

func TestCleanContextLineAndBullets(t *testing.T) {
	if got := cleanContextLine("- " + strings.Repeat("x", 130)); got != strings.Repeat("x", 120)+"..." {
		t.Errorf("long = %q", got)
	}
	if got := cleanContextLine("[x] done item"); got != "done item" {
		t.Errorf("bullet = %q", got)
	}
	if got := parseBulletItem("- ab"); got != "" {
		t.Errorf("short = %q", got)
	}
	if got := parseBulletItem("3. third"); got != "third" {
		t.Errorf("numbered = %q", got)
	}
}
