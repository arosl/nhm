// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/tui_parity_test.go (TestDetectErrors, TestParseCodeBlocks,
// TestExtractFileReferences, TestIsLikelyFilePath) at commit 9d5ed977; see
// LICENSE in this directory and PROVENANCE.md. Local changes: the
// exported names; TestInspectFileReferences also checks the sorted order
// and the limit of 20.

package robot

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestInspectErrors(t *testing.T) {
	var fifteen, ten []string
	for i := 0; i < 15; i++ {
		fifteen = append(fifteen, "error: line")
		if i < 10 {
			ten = append(ten, "error: line")
		}
	}
	tests := []struct {
		name     string
		lines    []string
		expected []string
	}{
		{"no errors", []string{"normal line", "another line", "all good"}, nil},
		{"error colon", []string{"error: something went wrong", "normal"}, []string{"error: something went wrong"}},
		{"Error capitalized", []string{"Error: file not found", "normal"}, []string{"Error: file not found"}},
		{"ERROR uppercase", []string{"ERROR: critical failure", "normal"}, []string{"ERROR: critical failure"}},
		{"failed colon", []string{"failed: to connect", "normal"}, []string{"failed: to connect"}},
		{"panic", []string{"panic: runtime error", "normal"}, []string{"panic: runtime error"}},
		{"exception", []string{"exception: null pointer", "normal"}, []string{"exception: null pointer"}},
		{"traceback", []string{"Traceback (most recent call last):", "normal"}, []string{"Traceback (most recent call last):"}},
		{"multiple errors", []string{"error: first error", "normal line", "Error: second error", "more normal"},
			[]string{"error: first error", "Error: second error"}},
		{"long error truncated", []string{"error: " + strings.Repeat("x", 250)}, []string{"error: " + strings.Repeat("x", 193) + "..."}},
		{"max 10 errors", fifteen, ten},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := InspectErrors(tt.lines); !slices.Equal(got, tt.expected) {
				t.Errorf("InspectErrors = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestInspectCodeBlocks(t *testing.T) {
	tests := []struct {
		name     string
		lines    []string
		expected []CodeBlockInfo
	}{
		{"no code blocks", []string{"normal line", "another line"}, nil},
		{"single code block", []string{"some text", "```go", "func main() {}", "```", "more text"},
			[]CodeBlockInfo{{Language: "go", LineStart: 1, LineEnd: 3}}},
		{"code block no language", []string{"```", "some code", "```"}, []CodeBlockInfo{{LineStart: 0, LineEnd: 2}}},
		{"multiple code blocks", []string{"```python", "print('hello')", "```", "text between", "```bash", "echo hello", "```"},
			[]CodeBlockInfo{{Language: "python", LineStart: 0, LineEnd: 2}, {Language: "bash", LineStart: 4, LineEnd: 6}}},
		// Unclosed blocks are not included.
		{"unclosed code block", []string{"```go", "func main() {}", "no closing"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := InspectCodeBlocks(tt.lines); !slices.Equal(got, tt.expected) {
				t.Errorf("InspectCodeBlocks = %+v, want %+v", got, tt.expected)
			}
		})
	}
}

func TestInspectFileReferences(t *testing.T) {
	tests := []struct {
		name     string
		lines    []string
		contains []string
	}{
		{"no files", []string{"hello world", "foo bar"}, nil},
		{"go file", []string{"editing main.go now"}, []string{"main.go"}},
		{"absolute path", []string{"file at /usr/local/bin/app"}, []string{"/usr/local/bin/app"}},
		{"relative path", []string{"see ./src/main.go for details"}, []string{"./src/main.go"}},
		{"multiple files", []string{"updated config.yaml and script.sh"}, []string{"config.yaml", "script.sh"}},
		{"various extensions", []string{"file.py file.js file.ts file.tsx file.jsx file.json file.md"},
			[]string{"file.py", "file.js", "file.ts", "file.tsx", "file.jsx", "file.json", "file.md"}},
		{"quoted paths", []string{`reading "path/to/file.go" now`}, []string{"path/to/file.go"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InspectFileReferences(tt.lines)
			for _, want := range tt.contains {
				if !slices.Contains(got, want) {
					t.Errorf("%q not in %q", want, got)
				}
			}
			if !slices.IsSorted(got) {
				t.Errorf("%q is not sorted", got)
			}
		})
	}
	var many []string
	for i := 0; i < 25; i++ {
		many = append(many, fmt.Sprintf("f%02d.go", 24-i))
	}
	if got := InspectFileReferences(many); len(got) != 20 || got[0] != "f00.go" || got[19] != "f19.go" {
		t.Errorf("25 files = %q, want the first 20 in order", got)
	}
}

func TestIsLikelyFilePath(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"main.go", true},
		{"config.yaml", true},
		{"script.sh", true},
		{"/usr/bin/app", true},
		{"./src/main.go", true},
		{"../parent/file.py", true},
		{"hello", false},
		{"foobar", false},
		{"123", false},
		{"file.unknownext", false},
		{"path/to/file.ts", true},
	}
	for _, tt := range tests {
		if got := isLikelyFilePath(tt.input); got != tt.expected {
			t.Errorf("isLikelyFilePath(%q) = %v, want %v", tt.input, got, tt.expected)
		}
	}
}
