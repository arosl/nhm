// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/config/label_test.go at commit 9d5ed977: TestParseSessionLabel,
// TestFormatSessionName_RoundTrip, TestValidateLabel,
// TestValidateProjectName and TestSessionLabel_DelimiterCollision; see
// LICENSE in this directory and PROVENANCE.md. Local changes
// (nhm-2p6.4.2): this header; the package; no subtests.
package sessionlabel

import (
	"strings"
	"testing"
)

func TestParseSessionLabel(t *testing.T) {
	for _, tt := range []struct {
		input     string
		wantBase  string
		wantLabel string
	}{
		{"myproject", "myproject", ""},
		{"myproject--frontend", "myproject", "frontend"},
		{"my-project--frontend", "my-project", "frontend"},
		{"foo--bar--baz", "foo--bar", "baz"}, // collision rule: split on LAST "--"
		{"proj--my-label", "proj", "my-label"},
		{"--frontend", "", "frontend"},     // degenerate: empty base
		{"myproject--", "myproject", ""},   // degenerate: empty label
		{"a--b", "a", "b"},                 // minimal
		{"abc", "abc", ""},                 // no separator
		{"a-b-c--d-e-f", "a-b-c", "d-e-f"}, // dashes everywhere
	} {
		gotBase, gotLabel := ParseSessionLabel(tt.input)
		if gotBase != tt.wantBase || gotLabel != tt.wantLabel {
			t.Errorf("ParseSessionLabel(%q) = (%q, %q), want (%q, %q)", tt.input, gotBase, gotLabel, tt.wantBase, tt.wantLabel)
		}
	}
}

func TestFormatSessionNameRoundTrip(t *testing.T) {
	// FormatSessionName(ParseSessionLabel(x)) == x for valid labeled names
	for _, input := range []string{"myproject", "myproject--frontend", "my-project--backend", "a--b"} {
		base, label := ParseSessionLabel(input)
		if got := FormatSessionName(base, label); got != input {
			t.Errorf("round-trip failed: input=%q, base=%q, label=%q, got=%q", input, base, label, got)
		}
	}
}

func TestValidateLabel(t *testing.T) {
	for _, label := range []string{"frontend", "backend", "bugfix-123", "my_label", "a", "A1b2C3", "test-run-1"} {
		if err := ValidateLabel(label); err != nil {
			t.Errorf("ValidateLabel(%q) unexpected error: %v", label, err)
		}
	}
	for _, tt := range []struct {
		label       string
		errContains string
	}{
		{"", "empty"},
		{strings.Repeat("a", 51), "50 characters"},
		{"my--label", "separator"},
		{"-bad", "alphanumeric"},
		{"_bad", "alphanumeric"},
		{"bad!", "alphanumeric"},
		{"bad label", "alphanumeric"},
	} {
		err := ValidateLabel(tt.label)
		if err == nil || !strings.Contains(err.Error(), tt.errContains) {
			t.Errorf("ValidateLabel(%q) = %v, want an error containing %q", tt.label, err, tt.errContains)
		}
	}
}

func TestValidateProjectName(t *testing.T) {
	for _, name := range []string{"myproject", "my-project", "my_project", "a", "project123", "foo-bar-baz"} {
		if err := ValidateProjectName(name); err != nil {
			t.Errorf("ValidateProjectName(%q) unexpected error: %v", name, err)
		}
	}
	for _, name := range []string{"my--project", "--frontend", "project--", "a--b--c"} {
		err := ValidateProjectName(name)
		if err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("ValidateProjectName(%q) = %v, want an error containing %q", name, err, "reserved")
		}
	}
}

func TestSessionLabelDelimiterCollision(t *testing.T) {
	base := "my--project--fix" // pathological base with TWO internal separators
	label := "frontend"

	name := FormatSessionName(base, label)
	if want := "my--project--fix--frontend"; name != want {
		t.Fatalf("FormatSessionName(%q, %q) = %q, want %q", base, label, name, want)
	}
	gotBase, gotLabel := ParseSessionLabel(name)
	if gotBase != base || gotLabel != label {
		t.Errorf("ParseSessionLabel(%q) = (%q, %q), want (%q, %q)", name, gotBase, gotLabel, base, label)
	}
	for _, tc := range []struct{ base, label string }{{"myproject", ""}, {"myproject", "frontend"}, {"my-project", "fix-1"}} {
		b, l := ParseSessionLabel(FormatSessionName(tc.base, tc.label))
		if b != tc.base || l != tc.label {
			t.Errorf("round-trip (%q, %q) = (%q, %q)", tc.base, tc.label, b, l)
		}
	}
	if SessionBase("my-project--frontend") != "my-project" || SessionLabel("myproject--frontend") != "frontend" ||
		!HasLabel("a--b") || HasLabel("ab") {
		t.Error("SessionBase, SessionLabel or HasLabel")
	}
}
