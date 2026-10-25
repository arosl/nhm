// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/synthesis_test.go (TestExtractCodeBlocks,
// TestExtractFileMentions, TestInferFileAction, TestExtractCommands,
// TestParseExitCode, TestCountErrors, TestExtractKeyActions,
// TestCleanActionLine, TestCleanActionLine_Truncation) at commit 9d5ed977;
// see LICENSE in this directory and PROVENANCE.md. Local changes: the
// tables compared whole; TestGenerateSummary is nhm's.

package robot

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestExtractCodeBlocks(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []CodeBlock
	}{
		{"empty content", "", nil},
		{"no code blocks", "Just some text\nwithout code blocks", nil},
		{"single code block with language", "Some text\n```go\nfunc main() {}\n```\nMore text",
			[]CodeBlock{{Language: "go", Content: "func main() {}", LineStart: 2, LineEnd: 4}}},
		{"code block without language", "```\nplain text\n```",
			[]CodeBlock{{Language: "", Content: "plain text", LineStart: 1, LineEnd: 3}}},
		{"multiple code blocks", "```python\nprint('hello')\n```\nSome text\n```javascript\nconsole.log('hi');\n```",
			[]CodeBlock{
				{Language: "python", Content: "print('hello')", LineStart: 1, LineEnd: 3},
				{Language: "javascript", Content: "console.log('hi');", LineStart: 5, LineEnd: 7},
			}},
		{"multiline code block", "```go\npackage main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```",
			[]CodeBlock{{Language: "go", Content: "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}", LineStart: 1, LineEnd: 9}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractCodeBlocks(tt.content); !slices.Equal(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestExtractFileMentions(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantCount int
	}{
		{"empty content", "", 0},
		{"no file paths", "Just some regular text", 0},
		{"single file path", "Modified internal/robot/file.go", 1},
		{"relative path", "Reading ./config.yaml", 1},
		{"multiple paths", "Updated src/main.go and internal/api/handler.go", 2},
		{"path with extension only", "Check main.go for details", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractFileMentions(tt.content); len(got) != tt.wantCount {
				t.Errorf("got %d mentions, want %d: %+v", len(got), tt.wantCount, got)
			}
		})
	}
}

func TestInferFileAction(t *testing.T) {
	tests := []struct {
		line, path, wantAction string
		minConf                float64
	}{
		{"Created internal/robot/file.go", "internal/robot/file.go", FileActionCreated, 0.8},
		{"Creating new file", "file.go", FileActionCreated, 0.8},
		{"Modified src/main.go", "src/main.go", FileActionModified, 0.8},
		{"Updating the handler", "handler.go", FileActionModified, 0.5},
		{"Deleted old/file.go", "old/file.go", FileActionDeleted, 0.8},
		{"Reading config.yaml", "config.yaml", FileActionRead, 0.8},
		{"Some file mentioned", "file.go", FileActionUnknown, 0.0},
	}
	for _, tt := range tests {
		action, conf := inferFileAction(tt.line, tt.path)
		if action != tt.wantAction || conf < tt.minConf {
			t.Errorf("inferFileAction(%q) = %q %.2f, want %q >= %.2f", tt.line, action, conf, tt.wantAction, tt.minConf)
		}
	}
}

func TestExtractCommands(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{"empty content", "", nil},
		{"no commands", "Just some text", nil},
		{"dollar command", "$ go test ./...", []string{"go test ./..."}},
		{"percent command", "% ls -la", []string{"ls -la"}},
		{"multiple commands", "$ git status\n$ git add .\n$ git commit -m 'test'", []string{"git status", "git add .", "git commit -m 'test'"}},
		{"skip python REPL", ">>> print('hello')", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, c := range ExtractCommands(tt.content) {
				got = append(got, c.Command)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
	if c := ExtractCommands("$ make\nexit code: 2"); len(c) != 1 || c[0].ExitCode == nil || *c[0].ExitCode != 2 {
		t.Errorf("exit code = %+v", c)
	}
}

func TestParseExitCode(t *testing.T) {
	tests := []struct {
		line string
		want int // -1: none
	}{
		{"exit code: 0", 0},
		{"exit code: 1", 1},
		{"Exit: 127", 127},
		{"returned 0", 0},
		{"status: 1", 1},
		{"[0]", 0},
		{"no exit code here", -1},
		{"", -1},
	}
	for _, tt := range tests {
		got := parseExitCode(tt.line)
		switch {
		case tt.want < 0 && got != nil:
			t.Errorf("parseExitCode(%q) = %d, want nil", tt.line, *got)
		case tt.want >= 0 && (got == nil || *got != tt.want):
			t.Errorf("parseExitCode(%q) = %v, want %d", tt.line, got, tt.want)
		}
	}
}

func TestCountErrors(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  int
	}{
		{"empty", nil, 0},
		{"no errors", []string{"ok", "success", "done"}, 0},
		{"error:", []string{"error: something went wrong"}, 1},
		{"Error!", []string{"Error! compilation failed"}, 1},
		{"failed:", []string{"build failed: exit 1"}, 1},
		{"fatal:", []string{"fatal: ref not found"}, 1},
		{"panic:", []string{"panic: nil pointer"}, 1},
		{"multiple", []string{"starting build", "error: syntax error", "error: undefined var", "panic: runtime error"}, 3},
		{"case insensitive", []string{"ERROR: caps", "Failed: mixed"}, 2},
	}
	for _, tt := range tests {
		if got := countErrors(tt.lines); got != tt.want {
			t.Errorf("%s: countErrors() = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestExtractKeyActions(t *testing.T) {
	if actions := extractKeyActions(nil, 5); len(actions) != 0 {
		t.Errorf("empty: %v", actions)
	}
	if actions := extractKeyActions([]string{"some output", "Summary: Added authentication middleware", "more output"}, 5); !slices.Contains(actions, "Added authentication middleware") {
		t.Errorf("summary prefix: %v", actions)
	}
	verbs := []string{"Created new handler for /api/users", "Fixed authentication bug in middleware", "Modified the config parser", "Implemented retry logic"}
	if actions := extractKeyActions(verbs, 10); len(actions) < 4 {
		t.Errorf("action verbs: %v", actions)
	}
	if actions := extractKeyActions([]string{"Created file1", "Created file2", "Created file3", "Created file4", "Created file5"}, 3); len(actions) > 3 {
		t.Errorf("respects max: %v", actions)
	}
	if actions := extractKeyActions([]string{"ok", "Created.", "Created a new file for handling authentication requests"}, 5); len(actions) != 1 {
		t.Errorf("skips short lines: %v", actions)
	}
	// Priority 1 items come before priority 2 and 3.
	actions := extractKeyActions([]string{"Testing the new feature now", "Fixed critical security bug", "Added helper function for utils"}, 3)
	if !slices.Equal(actions, []string{"Fixed critical security bug", "Added helper function for utils", "Testing the new feature now"}) {
		t.Errorf("priority ordering: %v", actions)
	}
}

func TestCleanActionLine(t *testing.T) {
	tests := []struct{ input, want string }{
		{"simple text", "simple text"},
		{"- bullet point", "bullet point"},
		{"* asterisk point", "asterisk point"},
		{"• unicode bullet", "unicode bullet"},
		{"1. numbered item", "numbered item"},
		{"[x] checkbox done", "checkbox done"},
		{"[ ] checkbox empty", "checkbox empty"},
		{"  leading spaces  ", "leading spaces"},
	}
	for _, tt := range tests {
		if got := cleanActionLine(tt.input); got != tt.want {
			t.Errorf("cleanActionLine(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
	if got := cleanActionLine(strings.Repeat("a", 100)); len(got) > 83 || !strings.HasSuffix(got, "...") {
		t.Errorf("truncation: %q", got)
	}
}

// GenerateSummary counts each agent's output, totals the files they
// created or changed, lists the conflicts with [] for no agents, and
// keeps the times in UTC.
func TestGenerateSummary(t *testing.T) {
	now := time.Date(2026, 10, 25, 3, 0, 0, 0, time.FixedZone("x", 2*3600))
	data := []AgentActivityData{
		{PaneID: "w1:p1", PaneTitle: "IcyKnoll opus", AgentType: "cc", State: "idle",
			Output: "Created internal/a.go\n$ go test\nerror: boom\n"},
		{PaneID: "w1:p2", AgentType: "cod", State: "active", Output: "Modified internal/a.go and src/b.go\n"},
		{PaneID: "w1:p3", AgentType: "user", State: "unknown"},
	}
	s := GenerateSummary("swarm", 30*time.Minute, now, data, []DetectedConflict{{Path: "x.go"}})
	if s.Session != "swarm" || s.TimeRange.End != now.UTC() || s.TimeRange.End.Location() != time.UTC ||
		s.TimeRange.Start != now.UTC().Add(-30*time.Minute) || s.TimeRange.Duration != 30*time.Minute ||
		s.Timestamp != "2026-10-25T01:00:00Z" {
		t.Errorf("summary = %+v", s)
	}
	if len(s.Agents) != 3 || s.TotalFiles != 2 || s.TotalOutput != 4+2 {
		t.Fatalf("agents = %+v, files %d, output %d", s.Agents, s.TotalFiles, s.TotalOutput)
	}
	a := s.Agents[0]
	if a.OutputLines != 4 || a.OutputChars != len(data[0].Output) || a.Commands != 1 || a.Errors != 1 ||
		!slices.Equal(a.FilesModified, []string{"internal/a.go"}) || a.State != "idle" || a.AgentType != "cc" {
		t.Errorf("agent 0 = %+v", a)
	}
	if a := s.Agents[2]; a.OutputLines != 0 || a.KeyActions != nil || a.State != "unknown" {
		t.Errorf("agent 2 = %+v", a)
	}
	if len(s.Conflicts) != 1 || s.Conflicts[0].Path != "x.go" || s.Conflicts[0].Agents == nil {
		t.Errorf("conflicts = %+v", s.Conflicts)
	}
}
