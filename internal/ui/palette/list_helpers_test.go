// Taken from ntm (github.com/Dicklesworthstone/ntm) internal/palette/list_helpers_test.go
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: this header; TestIntsToStrings is left out with
// intsToStrings (it served ntm's history record, which the core keeps).

package palette

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// ---------------------------------------------------------------------------
// toggleListKey
// ---------------------------------------------------------------------------

func TestToggleListKey(t *testing.T) {
	t.Parallel()

	t.Run("add_to_empty_list_append", func(t *testing.T) {
		t.Parallel()
		got, added := toggleListKey(nil, "a", false)
		if !added {
			t.Error("expected added=true")
		}
		if len(got) != 1 || got[0] != "a" {
			t.Errorf("got %v, want [a]", got)
		}
	})

	t.Run("add_to_empty_list_prepend", func(t *testing.T) {
		t.Parallel()
		got, added := toggleListKey(nil, "a", true)
		if !added {
			t.Error("expected added=true")
		}
		if len(got) != 1 || got[0] != "a" {
			t.Errorf("got %v, want [a]", got)
		}
	})

	t.Run("add_prepend_to_existing", func(t *testing.T) {
		t.Parallel()
		got, added := toggleListKey([]string{"b", "c"}, "a", true)
		if !added {
			t.Error("expected added=true")
		}
		if len(got) != 3 || got[0] != "a" {
			t.Errorf("got %v, want [a b c]", got)
		}
	})

	t.Run("add_append_to_existing", func(t *testing.T) {
		t.Parallel()
		got, added := toggleListKey([]string{"b", "c"}, "a", false)
		if !added {
			t.Error("expected added=true")
		}
		if len(got) != 3 || got[2] != "a" {
			t.Errorf("got %v, want [b c a]", got)
		}
	})

	t.Run("remove_existing", func(t *testing.T) {
		t.Parallel()
		got, added := toggleListKey([]string{"a", "b", "c"}, "b", false)
		if added {
			t.Error("expected added=false (removed)")
		}
		if len(got) != 2 || got[0] != "a" || got[1] != "c" {
			t.Errorf("got %v, want [a c]", got)
		}
	})

	t.Run("remove_first", func(t *testing.T) {
		t.Parallel()
		got, added := toggleListKey([]string{"a", "b"}, "a", false)
		if added {
			t.Error("expected added=false")
		}
		if len(got) != 1 || got[0] != "b" {
			t.Errorf("got %v, want [b]", got)
		}
	})

	t.Run("remove_last", func(t *testing.T) {
		t.Parallel()
		got, added := toggleListKey([]string{"a", "b"}, "b", false)
		if added {
			t.Error("expected added=false")
		}
		if len(got) != 1 || got[0] != "a" {
			t.Errorf("got %v, want [a]", got)
		}
	})

	t.Run("empty_key_noop", func(t *testing.T) {
		t.Parallel()
		orig := []string{"a", "b"}
		got, added := toggleListKey(orig, "", false)
		if added {
			t.Error("expected added=false for empty key")
		}
		if len(got) != 2 {
			t.Errorf("got %v, want unchanged", got)
		}
	})
}

// ---------------------------------------------------------------------------
// removeListKey
// ---------------------------------------------------------------------------

func TestRemoveListKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		list []string
		key  string
		want []string
	}{
		{"remove_middle", []string{"a", "b", "c"}, "b", []string{"a", "c"}},
		{"remove_first", []string{"a", "b"}, "a", []string{"b"}},
		{"remove_last", []string{"a", "b"}, "b", []string{"a"}},
		{"not_found", []string{"a", "b"}, "c", []string{"a", "b"}},
		{"empty_list", []string{}, "a", []string{}},
		{"nil_list", nil, "a", nil},
		{"empty_key", []string{"a", "b"}, "", []string{"a", "b"}},
		{"remove_only_item", []string{"a"}, "a", []string{}},
		{"remove_duplicates", []string{"a", "b", "a"}, "a", []string{"b"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := removeListKey(tc.list, tc.key)
			if len(got) != len(tc.want) {
				t.Fatalf("removeListKey(%v, %q) len = %d, want %d", tc.list, tc.key, len(got), len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("removeListKey(%v, %q)[%d] = %q, want %q", tc.list, tc.key, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ensureListKey
// ---------------------------------------------------------------------------

func TestEnsureListKey(t *testing.T) {
	t.Parallel()

	t.Run("append_new_key", func(t *testing.T) {
		t.Parallel()
		got := ensureListKey([]string{"a"}, "b", false)
		if len(got) != 2 || got[1] != "b" {
			t.Errorf("got %v, want [a b]", got)
		}
	})

	t.Run("prepend_new_key", func(t *testing.T) {
		t.Parallel()
		got := ensureListKey([]string{"a"}, "b", true)
		if len(got) != 2 || got[0] != "b" {
			t.Errorf("got %v, want [b a]", got)
		}
	})

	t.Run("already_exists", func(t *testing.T) {
		t.Parallel()
		orig := []string{"a", "b"}
		got := ensureListKey(orig, "a", false)
		if len(got) != 2 {
			t.Errorf("got %v, want unchanged [a b]", got)
		}
	})

	t.Run("empty_key_noop", func(t *testing.T) {
		t.Parallel()
		orig := []string{"a"}
		got := ensureListKey(orig, "", false)
		if len(got) != 1 {
			t.Errorf("got %v, want unchanged [a]", got)
		}
	})

	t.Run("nil_list", func(t *testing.T) {
		t.Parallel()
		got := ensureListKey(nil, "a", false)
		if len(got) != 1 || got[0] != "a" {
			t.Errorf("got %v, want [a]", got)
		}
	})

	t.Run("nil_list_prepend", func(t *testing.T) {
		t.Parallel()
		got := ensureListKey(nil, "a", true)
		if len(got) != 1 || got[0] != "a" {
			t.Errorf("got %v, want [a]", got)
		}
	})
}

// ---------------------------------------------------------------------------
// renderMatchHighlighted
// ---------------------------------------------------------------------------

func TestRenderMatchHighlighted(t *testing.T) {
	t.Parallel()

	base := lipgloss.NewStyle()
	match := lipgloss.NewStyle().Bold(true)

	t.Run("empty_query", func(t *testing.T) {
		t.Parallel()
		got := renderMatchHighlighted("hello world", "", base, match)
		stripped := stripANSI(got)
		if stripped != "hello world" {
			t.Errorf("got %q, want %q", stripped, "hello world")
		}
	})

	t.Run("empty_text", func(t *testing.T) {
		t.Parallel()
		got := renderMatchHighlighted("", "query", base, match)
		stripped := stripANSI(got)
		if stripped != "" {
			t.Errorf("got %q, want empty", stripped)
		}
	})

	t.Run("whitespace_query", func(t *testing.T) {
		t.Parallel()
		got := renderMatchHighlighted("hello", "  ", base, match)
		stripped := stripANSI(got)
		if stripped != "hello" {
			t.Errorf("got %q, want %q", stripped, "hello")
		}
	})

	t.Run("match_found", func(t *testing.T) {
		t.Parallel()
		got := renderMatchHighlighted("hello world", "world", base, match)
		stripped := stripANSI(got)
		if !strings.Contains(stripped, "hello") || !strings.Contains(stripped, "world") {
			t.Errorf("got %q, want both 'hello' and 'world'", stripped)
		}
	})

	t.Run("case_insensitive_match", func(t *testing.T) {
		t.Parallel()
		got := renderMatchHighlighted("Hello World", "hello", base, match)
		stripped := stripANSI(got)
		if !strings.Contains(stripped, "Hello") {
			t.Errorf("case insensitive match failed, got %q", stripped)
		}
	})

	t.Run("no_match_returns_full_text", func(t *testing.T) {
		t.Parallel()
		got := renderMatchHighlighted("hello world", "xyz", base, match)
		stripped := stripANSI(got)
		if stripped != "hello world" {
			t.Errorf("got %q, want %q", stripped, "hello world")
		}
	})

	t.Run("needle_longer_than_text", func(t *testing.T) {
		t.Parallel()
		got := renderMatchHighlighted("hi", "hello world", base, match)
		stripped := stripANSI(got)
		if stripped != "hi" {
			t.Errorf("got %q, want %q", stripped, "hi")
		}
	})

	t.Run("match_at_start", func(t *testing.T) {
		t.Parallel()
		got := renderMatchHighlighted("hello world", "hello", base, match)
		stripped := stripANSI(got)
		if stripped != "hello world" {
			t.Errorf("got %q, want %q", stripped, "hello world")
		}
	})

	t.Run("match_at_end", func(t *testing.T) {
		t.Parallel()
		got := renderMatchHighlighted("hello world", "world", base, match)
		stripped := stripANSI(got)
		if stripped != "hello world" {
			t.Errorf("got %q, want %q", stripped, "hello world")
		}
	})
}
