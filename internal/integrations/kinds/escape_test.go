package kinds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The sandbox proved each built-in kind's escape (nhm-ubi: Claude Code
// 2.1.310, Codex 0.164.0, pi 0.8.0; nhm-gb0: omp 18.12.6, opencode
// 1.19.4, agy-real 1.4.6, grok 1.0.67): six run a "!" (omp a "$" too),
// and each says in Cannot that a person can still type one; grok runs
// none. An unregistered kind has "!" by default (the operator's ruling,
// 2026-10-24; nhm-gb0).
func TestShellEscapeOfTheKinds(t *testing.T) {
	r := NewRegistry()
	for _, name := range []string{"claude", "codex", "pi", "omp", "opencode", "agy"} {
		a, _ := r.Get(name)
		if a.ShellEscape != "!" || a.Escape() != "!" || a.EscapeIsDefault() || !a.RunsAsShell("!x") {
			t.Errorf("%s's shell escape = %q", name, a.ShellEscape)
		}
		cannot := strings.Join(a.Cannot, "; ")
		if !strings.Contains(cannot, `a "!" shell command`) || !strings.Contains(cannot, "that a person types") {
			t.Errorf("%s's Cannot does not name a person's \"!\": %q", name, a.Cannot)
		}
	}
	if omp, _ := r.Get("omp"); omp.ShellPrefix("  $ print(1)") != "$" || omp.ShellPrefix("!ls") != "!" {
		t.Errorf("omp's escapes = %q %q", omp.ShellEscape, omp.ExtraEscapes)
	}
	if grok, _ := r.Get("grok"); grok.Escape() != "" || grok.EscapeIsDefault() || grok.RunsAsShell("!x") {
		t.Errorf("grok = %+v", grok)
	}
	if a := Lookup("unregistered"); a.Escape() != "!" || !a.EscapeIsDefault() || !a.RunsAsShell("!x") {
		t.Errorf("an unregistered kind = %+v", a)
	}
	// A kind proved to have none has none, whatever it names.
	if a := (Adapter{Name: "x", ShellEscape: "!", NoShellEscape: true}); a.Escape() != "" || a.EscapeIsDefault() || a.RunsAsShell("!x") {
		t.Errorf("a kind proved to have none = %+v", a)
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
}

// A config kind keeps "!", and an escape it names is one more. It cannot
// name none (only a built-in kind that the sandbox proved has none), and
// white space, a control or a format character in it does not load.
func TestConfigShellEscape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("[kinds.mycli]\nflags = [\"my\"]\n[kinds.other]\nflags = [\"ot\"]\nshell_escape = \"$\"\n")
	r := NewRegistry()
	if err := r.LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	if m, _ := r.ByFlag("my"); m.Escape() != "!" || !m.EscapeIsDefault() || !m.RunsAsShell("!ls") {
		t.Fatalf("mycli = %+v", m)
	}
	if o, _ := r.ByFlag("ot"); o.Escape() != "!" || o.ShellPrefix(" $ ls") != "$" || o.ShellPrefix("!ls") != "!" {
		t.Fatalf("other = %+v", o)
	}
	// None, a leading or trailing space, a tab and a newline (TOML
	// escapes), a no-break space, a zero-width space.
	for _, bad := range []string{``, ` !`, `! `, `\t`, `!\n`, c(0xa0), "!" + c(0x200b)} {
		write("[kinds.mycli]\nshell_escape = \"" + bad + "\"\n")
		if err := NewRegistry().LoadConfig(path); err == nil || !strings.Contains(err.Error(), "shell_escape") {
			t.Errorf("shell escape %q: %v", bad, err)
		}
	}
}
