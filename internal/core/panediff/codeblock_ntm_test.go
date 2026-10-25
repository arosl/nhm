// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/codeblock/parser_test.go at commit 9d5ed977; see LICENSE in
// this directory and PROVENANCE.md. Local changes: the package (nhm's
// panediff); only the tests of what codeblock_ntm.go keeps (the tests of
// Extraction, CodeBlock's String and DetectLanguage are left out); the
// file path tests as tables without subtests; plus nhm's test of the
// language filter.

package panediff

import (
	"testing"
)

func TestParseFilePathComment(t *testing.T) {
	for _, c := range []struct {
		text, wantPath string
		wantIsNew      bool
	}{
		{"```python\n# src/auth.py\ndef auth():\n    pass\n```", "src/auth.py", false},
		{"```javascript\n// src/utils.js\nfunction util() {}\n```", "src/utils.js", false},
		{"```html\n<!-- templates/index.html -->\n<html></html>\n```", "templates/index.html", false},
		{"```go\npackage mypackage\n\nfunc Do() {}\n```", "mypackage/mypackage.go", true},
		{"```go\npackage main\n\nfunc main() {}\n```", "main.go", true},
		// No path detectable, isNew defaults to false
		{"```python\nprint('hello')\n```", "", false},
	} {
		blocks := ExtractFromText(c.text)
		if len(blocks) != 1 || blocks[0].FilePath != c.wantPath || blocks[0].IsNew != c.wantIsNew {
			t.Errorf("%q = %+v, want %q new=%v", c.text, blocks, c.wantPath, c.wantIsNew)
		}
	}
}

func TestLooksLikeFilePath(t *testing.T) {
	for _, c := range []struct {
		input    string
		expected bool
	}{
		{"src/main.go", true}, {"file.py", true}, {"path/to/file.js", true}, {"./relative.txt", true},
		{"just text", false}, {"", false}, {"single", false}, {"path/", true},
	} {
		if got := looksLikeFilePath(c.input); got != c.expected {
			t.Errorf("looksLikeFilePath(%q) = %v, want %v", c.input, got, c.expected)
		}
	}
}

// The language filter keeps the blocks of its languages, by their
// canonical names (py is python).
func TestParseLanguageFilter(t *testing.T) {
	text := "```py\na = 1\n```\n```go\nx := 1\n```\n```\nplain\n```"
	blocks := NewParser().WithLanguageFilter([]string{"python"}).Parse(text)
	if len(blocks) != 1 || blocks[0].Language != "python" || blocks[0].Content != "a = 1" {
		t.Errorf("python filter = %+v", blocks)
	}
	blocks = NewParser().WithLanguageFilter([]string{"text", "golang", "go"}).Parse(text)
	if len(blocks) != 2 || blocks[0].Language != "go" || blocks[1].Language != "text" {
		t.Errorf("go and text filter = %+v", blocks)
	}
}

func TestParseSimpleBlock(t *testing.T) {
	text := "Some text\n```python\ndef hello():\n    print('hello')\n```\nMore text"

	blocks := ExtractFromText(text)

	if len(blocks) != 1 {
		t.Fatalf("Expected 1 block, got %d", len(blocks))
	}

	block := blocks[0]
	if block.Language != "python" {
		t.Errorf("Language = %q, want %q", block.Language, "python")
	}
	if block.Content != "def hello():\n    print('hello')" {
		t.Errorf("Content = %q, want %q", block.Content, "def hello():\n    print('hello')")
	}
}

func TestParseMultipleBlocks(t *testing.T) {
	text := `# Header

` + "```go" + `
package main

func main() {
    fmt.Println("Hello")
}
` + "```" + `

Some text here.

` + "```bash" + `
echo "Hello World"
` + "```" + `
`

	blocks := ExtractFromText(text)

	if len(blocks) != 2 {
		t.Fatalf("Expected 2 blocks, got %d", len(blocks))
	}

	if blocks[0].Language != "go" {
		t.Errorf("Block 0 language = %q, want %q", blocks[0].Language, "go")
	}
	if blocks[1].Language != "bash" {
		t.Errorf("Block 1 language = %q, want %q", blocks[1].Language, "bash")
	}
}

func TestParseNoLanguage(t *testing.T) {
	text := "```\nplain text\n```"

	blocks := ExtractFromText(text)

	if len(blocks) != 1 {
		t.Fatalf("Expected 1 block, got %d", len(blocks))
	}

	if blocks[0].Language != "text" {
		t.Errorf("Language = %q, want %q", blocks[0].Language, "text")
	}
}

func TestNormalizeLanguage(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"js", "javascript"},
		{"JS", "javascript"},
		{"ts", "typescript"},
		{"py", "python"},
		{"rb", "ruby"},
		{"sh", "bash"},
		{"shell", "bash"},
		{"yml", "yaml"},
		{"", "text"},
		{"go", "go"},
		{"rust", "rust"},
		{"cobol", "cobol"}, // not in languageMap, fallback return
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := normalizeLanguage(tc.input)
			if got != tc.expected {
				t.Errorf("normalizeLanguage(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestParseLineNumbers(t *testing.T) {
	text := `Line 1
Line 2
` + "```python" + `
code line 1
code line 2
` + "```" + `
Line after`

	blocks := ExtractFromText(text)

	if len(blocks) != 1 {
		t.Fatalf("Expected 1 block, got %d", len(blocks))
	}

	// Code block starts at line 3 (0-indexed: lines 0, 1, then fence)
	if blocks[0].StartLine != 3 {
		t.Errorf("StartLine = %d, want %d", blocks[0].StartLine, 3)
	}
	if blocks[0].EndLine != 6 {
		t.Errorf("EndLine = %d, want %d", blocks[0].EndLine, 6)
	}
}

func TestParseEmptyBlocks(t *testing.T) {
	text := "```python\n```"

	blocks := ExtractFromText(text)

	if len(blocks) != 1 {
		t.Fatalf("Expected 1 block, got %d", len(blocks))
	}

	if blocks[0].Content != "" {
		t.Errorf("Content = %q, want empty string", blocks[0].Content)
	}
}

func TestParseNestedFences(t *testing.T) {
	// Note: We don't support nested fences - this tests current behavior
	text := "```markdown\n```python\nprint('hello')\n```\n```"

	blocks := ExtractFromText(text)

	// The inner fence should be captured as part of the content
	// This depends on regex matching behavior
	if len(blocks) == 0 {
		t.Error("Expected at least one block")
	}
}

func TestParseVariableLengthFences(t *testing.T) {
	text := "````markdown\n```python\nprint('hello')\n```\n````"
	blocks := ExtractFromText(text)
	if len(blocks) != 1 {
		t.Fatalf("Expected 1 block, got %d", len(blocks))
	}
	if blocks[0].Language != "markdown" {
		t.Errorf("Language = %q, want markdown", blocks[0].Language)
	}
	expectedContent := "```python\nprint('hello')\n```"
	if blocks[0].Content != expectedContent {
		t.Errorf("Content = %q, want %q", blocks[0].Content, expectedContent)
	}
}
