package ui

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
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
		// A one-line multi-line string, then a comment (acfs-9hkh.69).
		{"[keys]\nnew_tab = \"\"\"x\"\"\" # 'prefix+a'\n", false},
		{"[keys]\nnew_tab = '''x''' # 'prefix+a'\n", false},
		{"[keys]\nnew_tab = \"\"\"prefix+a\"\"\" # note\n", true},
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
	got, err := Unbind(acfs + "\n" + Block(Keys))
	if err != nil || !strings.Contains(got, "# >>> acfs palette >>>") || strings.Contains(got, BlockBegin) {
		t.Fatalf("Unbind = %q %v", got, err)
	}
}

// ACFS writes its begin marker with a comment after it, and its block
// binds prefix+a: Bind replaces it instead of refusing the key (nhm-t59).
// The unbound block is an opt-out, so only force replaces it.
func TestBindReplacesACFSBlockWithComment(t *testing.T) {
	for _, begin := range []string{
		"# >>> acfs palette >>> managed by `acfs palette bind`; `acfs palette unbind` removes it",
		"# >>> acfs palette >>> unbound by `acfs palette unbind`; `acfs palette bind` binds it again",
	} {
		acfs := begin + "\n[[keys.command]]\nkey = \"prefix+a\"\ntype = \"popup\"\ncommand = \"bash palette.sh\"\n# <<< acfs palette <<<\n"
		force := strings.Contains(begin, "unbound by")
		bound, err := BindKeys(acfs, Keys, force)
		if err != nil || strings.Contains(bound, "acfs palette") || strings.Contains(bound, "palette.sh") {
			t.Fatalf("Bind(%q) = %q %v", begin, bound, err)
		}
	}
	if isMarker("# >>> acfs palettex >>>", acfsBlockBegin) {
		t.Fatal("isMarker matched a longer marker")
	}
}

func TestLoadKeys(t *testing.T) {
	dir := t.TempDir()
	write := func(content string) string {
		t.Helper()
		p := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	keyOf := func(keys []Key) map[string]string {
		m := map[string]string{}
		for _, k := range keys {
			m[k.Name] = k.Key
		}
		return m
	}
	// No file, no [keys]: the defaults.
	for _, path := range []string{filepath.Join(dir, "none.toml"), write("[memory]\ncm = \"off\"\n")} {
		keys, err := LoadKeys(path)
		if err != nil || !slices.Equal(keys, Keys) {
			t.Fatalf("LoadKeys(%s) = %v %v; want the defaults", path, keys, err)
		}
	}
	// A key set, a key emptied, a key left out.
	keys, err := LoadKeys(write("[keys]\npalette = \" ctrl+alt+p \"\nboard = \"\"\n"))
	if got := keyOf(keys); err != nil || got["palette"] != "ctrl+alt+p" || got["board"] != "" || got["menu"] != "prefix+m" {
		t.Fatalf("LoadKeys = %v %v", got, err)
	}
	if keys[1].Action != "nhm.board" || Describe(keys) != "ctrl+alt+p palette, board without a key, prefix+m menu" {
		t.Errorf("keys = %v, %q", keys, Describe(keys))
	}
	// The defaults stay as they were.
	if Keys[0].Key != "prefix+a" || Keys[1].Key != "prefix+d" {
		t.Fatalf("LoadKeys changed the defaults: %v", Keys)
	}
	// Two empty keys are no clash.
	if _, err := LoadKeys(write("[keys]\npalette = \"\"\nboard = \"\"\n")); err != nil {
		t.Errorf("two empty keys: %v", err)
	}
	for content, want := range map[string]string{
		"[keys]\npallete = \"prefix+p\"\n":                        `has no "pallete"`,
		"[keys]\nboard = \"prefix+a\"\n":                          "palette and board are both prefix+a",
		"[keys]\nmenu = \"Prefix + D\"\n":                         "board and menu are both prefix+d",
		"[keys]\nmenu = \"prefix+\\nm\"\n":                        "is not a key",
		"[keys]\nmenu = false\n":                                  "config.toml",
		"[keys]\npalette = \"prefix+x\"\n[keys]\nboard = \"x\"\n": "config.toml",
	} {
		if _, err := LoadKeys(write(content)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("LoadKeys(%q) = %v; want %q", content, err, want)
		}
	}
}

func TestBlockWithoutAKey(t *testing.T) {
	keys := slices.Clone(Keys)
	keys[1].Key = ""
	b := Block(keys)
	if strings.Contains(b, "nhm.board\"") || strings.Count(b, "[[keys.command]]") != 2 || !strings.Contains(b, "[keys] board = \"\"") {
		t.Fatalf("Block = %q", b)
	}
	// The key the board would have had is free for the rest of the config.
	bound, err := BindKeys("[keys]\nzoom = \"prefix+d\"\n", keys, false)
	if err != nil || !strings.Contains(bound, "zoom = \"prefix+d\"") {
		t.Fatalf("Bind with the board's key elsewhere = %q %v", bound, err)
	}
	// Moving a key moves it: a second Bind replaces the first block.
	keys[0].Key = "ctrl+alt+p"
	moved, err := BindKeys(bound, keys, false)
	if err != nil || strings.Contains(moved, "prefix+a") || strings.Count(moved, BlockBegin) != 1 || !strings.Contains(moved, "key = \"ctrl+alt+p\"") {
		t.Fatalf("Bind with a moved key = %q %v", moved, err)
	}
}

// nhm bind --unbind records an opt-out that a later Bind keeps unless
// forced (ACFS acfs-uetm, nhm-7kz); ACFS's unbound palette block counts
// too.
func TestOptOut(t *testing.T) {
	bound, err := Bind("# mine\n")
	if err != nil {
		t.Fatal(err)
	}
	out, err := OptOut(bound)
	if err != nil || out != "# mine\n\n"+optOutBlock || strings.Contains(out, "keys.command") {
		t.Fatalf("OptOut = %q %v", out, err)
	}
	if again, err := OptOut(out); err != nil || again != out {
		t.Errorf("OptOut is not idempotent: %q %v", again, err)
	}
	if fresh, err := OptOut(""); err != nil || fresh != optOutBlock {
		t.Errorf("OptOut of an empty config = %q %v", fresh, err)
	}
	if OptOutBy(out) != "nhm bind --unbind" || OptOutBy(bound) != "" {
		t.Fatalf("OptOutBy = %q, %q", OptOutBy(out), OptOutBy(bound))
	}
	if _, err := Bind(out); !errors.Is(err, ErrOptedOut) || !strings.Contains(err.Error(), "nhm bind --unbind") {
		t.Fatalf("Bind after the opt-out: %v", err)
	}
	forced, err := BindKeys(out, Keys, true)
	if err != nil || forced != bound || OptOutBy(forced) != "" {
		t.Fatalf("Bind --force = %q %v; want %q", forced, err, bound)
	}

	// ACFS's unbound block is an opt-out; its bound block is not.
	acfsOff := "# >>> acfs palette >>> unbound by `acfs palette unbind`; `acfs palette bind` binds it again\n# <<< acfs palette <<<\n"
	acfsOn := "# >>> acfs palette >>> managed by `acfs palette bind`; `acfs palette unbind` removes it\n# <<< acfs palette <<<\n"
	if OptOutBy(acfsOff) != "acfs palette unbind" || OptOutBy(acfsOn) != "" {
		t.Fatalf("OptOutBy(ACFS) = %q, %q", OptOutBy(acfsOff), OptOutBy(acfsOn))
	}
	if _, err := Bind(acfsOff); !errors.Is(err, ErrOptedOut) {
		t.Fatalf("Bind over ACFS's unbound block: %v", err)
	}
	if forced, err := BindKeys(acfsOff, Keys, true); err != nil || OptOutBy(forced) != "" || strings.Contains(forced, "acfs palette") {
		t.Fatalf("Bind --force over ACFS's unbound block = %q %v", forced, err)
	}
	// nhm's opt-out keeps ACFS's block, as Unbind does.
	if kept, err := OptOut(acfsOff); err != nil || !strings.HasPrefix(kept, acfsOff) {
		t.Errorf("OptOut dropped ACFS's block: %q %v", kept, err)
	}
	// A marker inside a multi-line string is text.
	for _, quote := range []string{`"""`, `'''`} {
		note := "[x]\nnote = " + quote + "\n" + optOutBlock + quote + "\n"
		if by := OptOutBy(note); by != "" {
			t.Errorf("OptOutBy found %q in a %s string", by, quote)
		}
	}
}
