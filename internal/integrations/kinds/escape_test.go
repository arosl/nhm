package kinds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The kinds whose "!" the sandbox proved (nhm-ubi: Claude Code 2.1.310,
// Codex 0.164.0, pi 0.8.0, each typed through nhm send) carry it; the
// others say in Cannot that nobody checked theirs.
func TestShellEscapeOfTheKinds(t *testing.T) {
	r := NewRegistry()
	for _, name := range []string{"claude", "codex", "pi"} {
		a, _ := r.Get(name)
		if a.ShellEscape != "!" {
			t.Errorf("%s's shell escape = %q", name, a.ShellEscape)
		}
		if !strings.Contains(strings.Join(a.Cannot, "; "), `a "!" shell command that a person types`) {
			t.Errorf("%s's Cannot does not name a person's \"!\": %q", name, a.Cannot)
		}
	}
	for _, name := range []string{"agy", "omp", "opencode", "grok"} {
		a, _ := r.Get(name)
		if a.ShellEscape != "" || !strings.Contains(strings.Join(a.Cannot, "; "), "its shell escape: not checked") {
			t.Errorf("%s: shell escape %q, Cannot %q", name, a.ShellEscape, a.Cannot)
		}
	}
	if a := Lookup("unregistered"); a.ShellEscape != "" || a.RunsAsShell("!x") {
		t.Errorf("an unregistered kind = %+v", a)
	}
}

// c is the one-rune string of r: the test spells invisible runes by code.
func c(r rune) string { return string(r) }

// What the sandbox saw each kind run: "!" first, or after white space
// (pi and Codex skip it; one rule for all), "!!" too; control and format
// characters before it are skipped as well; not on a later line, and not
// a fullwidth "!" (U+FF01).
func TestRunsAsShell(t *testing.T) {
	pi := Lookup("pi")
	for _, text := range []string{"!x", "!!x", "  !x", "\t!x", "\n!x", "\r\n!x", c(0xa0) + "!x", c(0x3000) + "!x",
		"\x7f!x", "\x01!x", c(0x200b) + "!x", c(0xfeff) + "!x"} {
		if !pi.RunsAsShell(text) {
			t.Errorf("%q does not run as a shell command", text)
		}
	}
	for _, text := range []string{"", " ", "x", "x!", "hello\n!x", c(0xff01) + "x", "`!x`"} {
		if pi.RunsAsShell(text) {
			t.Errorf("%q runs as a shell command", text)
		}
	}
	if Lookup("omp").RunsAsShell("!x") {
		t.Error("omp, whose escape nobody checked, runs \"!x\"")
	}
}

// A config kind may name its escape; white space or a control character
// in it does not load.
func TestConfigShellEscape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("[kinds.mycli]\nflags = [\"my\"]\nshell_escape = \"$\"\n")
	r := NewRegistry()
	if err := r.LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	if m, _ := r.ByFlag("my"); m.ShellEscape != "$" || !m.RunsAsShell(" $ ls") || m.RunsAsShell("!ls") {
		t.Fatalf("mycli = %+v", m)
	}
	// A leading or trailing space, a tab and a newline (TOML escapes), a
	// no-break space.
	for _, bad := range []string{` !`, `! `, `\t`, `!\n`, c(0xa0)} {
		write("[kinds.mycli]\nshell_escape = \"" + bad + "\"\n")
		if err := NewRegistry().LoadConfig(path); err == nil || !strings.Contains(err.Error(), "shell_escape") {
			t.Errorf("shell escape %q: %v", bad, err)
		}
	}
}
