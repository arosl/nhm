// Taken from ntm (github.com/Dicklesworthstone/ntm)
// internal/cass/inject_test.go (in part) at commit 9d5ed977; see LICENSE
// in this directory and PROVENANCE.md. Local changes: only
// TestTokenize, TestIsStopWord, TestRemoveCodeBlocks and
// TestExtractKeywords, unchanged.

package cass

import (
	"testing"
)

func TestTokenize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"simple words", "hello world", []string{"hello", "world"}},
		{"with punctuation", "hello, world!", []string{"hello", "world"}},
		{"underscores kept", "my_var_name", []string{"my_var_name"}},
		{"hyphens kept", "my-var-name", []string{"my-var-name"}},
		{"digits", "foo123 bar456", []string{"foo123", "bar456"}},
		{"empty string", "", nil},
		{"only separators", "   ,,, ", nil},
		{"mixed", "Fix bug #42 in auth-flow", []string{"Fix", "bug", "42", "in", "auth-flow"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tokenize(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("tokenize(%q) = %v (len %d), want %v (len %d)", tc.input, got, len(got), tc.want, len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("tokenize(%q)[%d] = %q, want %q", tc.input, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestIsStopWord(t *testing.T) {
	t.Parallel()

	stopWords := []string{"the", "a", "an", "and", "or", "is", "was", "for", "of", "with", "code", "test", "fix"}
	nonStopWords := []string{"authentication", "golang", "database", "refactor", "pagination", "websocket"}

	for _, w := range stopWords {
		t.Run("stop_"+w, func(t *testing.T) {
			t.Parallel()
			if !isStopWord(w) {
				t.Errorf("isStopWord(%q) = false, want true", w)
			}
		})
	}

	for _, w := range nonStopWords {
		t.Run("nonstop_"+w, func(t *testing.T) {
			t.Parallel()
			if isStopWord(w) {
				t.Errorf("isStopWord(%q) = true, want false", w)
			}
		})
	}
}

func TestRemoveCodeBlocks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no code blocks", "hello world", "hello world"},
		{"inline code", "use `fmt.Println` here", "use   here"},
		{"fenced code block", "before\n```go\nfmt.Println()\n```\nafter", "before\n \nafter"},
		{"empty string", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := removeCodeBlocks(tc.input)
			if got != tc.want {
				t.Errorf("removeCodeBlocks(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestExtractKeywords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty prompt", "", nil},
		{"only stop words", "the and for with this that", nil},
		{"short words filtered", "go is an ok", nil},
		{"extracts meaningful words", "authentication database migration golang", []string{"authentication", "database", "migration", "golang"}},
		{"deduplicates", "golang golang golang", []string{"golang"}},
		{"lowercases", "Authentication DATABASE", []string{"authentication", "database"}},
		{"removes code blocks", "check `fmt.Println` and authentication", []string{"check", "authentication"}},
		{"caps at 10", "alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima",
			[]string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ExtractKeywords(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("ExtractKeywords(%q) = %v (len %d), want %v (len %d)", tc.input, got, len(got), tc.want, len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("ExtractKeywords(%q)[%d] = %q, want %q", tc.input, i, got[i], tc.want[i])
				}
			}
		})
	}
}
