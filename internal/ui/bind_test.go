package ui

import (
	"strings"
	"testing"
)

func TestUsesKey(t *testing.T) {
	for _, tc := range []struct {
		config string
		want   bool
	}{
		{"[keys]\nnew_tab = 'prefix+a'\n", true},
		{"[keys]\nnew_tab = \"Prefix+A\"\n", true},
		{"[keys]\nzoom = [\"ctrl+z\", 'prefix+a']\n", true},
		{"[keys]\nzoom = [\n  \"ctrl+z\",\n  \"prefix+a\",\n]\n", true},
		{"[[keys.command]]\nkey = \"prefix+a\"\n", true},
		{"# zoom = \"prefix+a\"\n", false},
		{"[keys]\nzoom = \"prefix+ab\"\n", false},
		{"[[keys.command]]\ncommand = \"echo prefix+a\"\n", false},
		// Written another way that herdr reads as the same key.
		{"[keys]\nnew_tab = \"prefix\\u002Ba\"\n", true},
		{"[keys]\nnew_tab = \"Prefix + A\"\n", true},
		// Inside a multi-line string it is text, not a key.
		{"[x]\nnote = \"\"\"\nzoom = \"prefix+a\"\n\"\"\"\n", false},
		// A trailing comment is not a value; a # inside a string is.
		{"[keys]\nnew_tab = 'x' # 'prefix+a'\n", false},
		{"[keys]\nnew_tab = \"prefix+a\" # was ctrl+t\n", true},
		{"[keys]\nnew_tab = \"x#\" # \"prefix+a\"\n", false},
		{"[keys]\nzoom = [\"x\", # 'prefix+a'\n  'y']\n", false},
	} {
		if got := usesKey(tc.config, "prefix+a") != ""; got != tc.want {
			t.Errorf("usesKey(%q) = %v, want %v", tc.config, got, tc.want)
		}
	}
}

func TestBindRefusesWhatHerdrWouldIgnore(t *testing.T) {
	_, err := Bind("[keys]\ncommand = [{ key = \"prefix+x\", type = \"shell\", command = \"x\" }]\n")
	if err == nil || !strings.Contains(err.Error(), "would not parse") {
		t.Fatalf("an inline keys.command array: %v", err)
	}
	if _, err := Bind("[keys]\nnew_tab = 'prefix+d'\n"); err == nil || !strings.Contains(err.Error(), "prefix+d") {
		t.Fatalf("a single-quoted taken key: %v", err)
	}
}

func TestMarkersInsideMultilineStringsAreText(t *testing.T) {
	for _, quote := range []string{`"""`, `'''`} {
		note := "[x]\nnote = " + quote + "\n" + BlockBegin + "\nkept\n" + BlockEnd + "\n" + quote + "\n"
		got, err := Unbind(note)
		if err != nil || got != note {
			t.Errorf("Unbind changed a %s string: %q %v", quote, got, err)
		}
		bound, err := Bind(note)
		if err != nil || !strings.HasPrefix(bound, note) {
			t.Errorf("Bind changed a %s string: %q %v", quote, bound, err)
		}
		if again, err := Unbind(bound); err != nil || strings.TrimRight(again, "\n") != strings.TrimRight(note, "\n") {
			t.Errorf("Unbind of the bound %s config = %q %v", quote, again, err)
		}
	}
	if got := multiline("", `a = """one""" # """`); got != "" {
		t.Errorf("a closed string and a comment left %q open", got)
	}
	// An escaped \""" does not close a basic multi-line string, so a
	// marker after it is still text (acfs-9hkh.63).
	escaped := "[x]\nnote = \"\"\"\nquote: \\\"\"\"\n" + BlockBegin + "\nkept\n" + BlockEnd + "\n\"\"\"\n"
	if got, err := Unbind(escaped); err != nil || got != escaped {
		t.Errorf("Unbind changed a string with an escaped delimiter: %q %v", got, err)
	}
	for line, want := range map[string]string{
		`a = """x\"""`:      `"""`, // escaped: still open
		`a = """x\\"""`:     "",    // an escaped backslash, then the close
		`a = """x"""""`:     "",    // up to two quotes belong to the string
		`a = '''x\'''`:      "",    // literal strings have no escapes
		`a = """x""""" # '`: "",
	} {
		if got := multiline("", line); got != want {
			t.Errorf("multiline(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestUnbindKeepsACFSBlock(t *testing.T) {
	acfs := "# >>> acfs palette >>>\nx = 1\n# <<< acfs palette <<<\n"
	bound, err := Bind(acfs)
	if err != nil || strings.Contains(bound, "acfs palette") {
		t.Fatalf("Bind kept ACFS's block: %q %v", bound, err)
	}
	got, err := Unbind(acfs + "\n" + Block())
	if err != nil || !strings.Contains(got, "# >>> acfs palette >>>") || strings.Contains(got, BlockBegin) {
		t.Fatalf("Unbind = %q %v", got, err)
	}
}
