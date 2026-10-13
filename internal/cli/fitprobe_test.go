package cli

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestFitProbeModel(t *testing.T) {
	var got []fitEvent
	m := tea.Model(fitProbe{write: func(e fitEvent) { got = append(got, e) }, ttl: time.Minute})
	step := func(msg tea.Msg) tea.Cmd {
		var cmd tea.Cmd
		m, cmd = m.Update(msg)
		return cmd
	}
	step(tea.WindowSizeMsg{Width: 72, Height: 25})
	step(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	step(tea.KeyMsg{Type: tea.KeyEsc})
	if !strings.Contains(m.View(), "72x25") || !strings.Contains(m.View(), "a esc") {
		t.Errorf("view = %q", m.View())
	}
	if cmd := step(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}); cmd == nil || cmd() != tea.Quit() {
		t.Error("q did not quit")
	}
	var kinds []string
	for _, e := range got {
		kinds = append(kinds, e.Event+":"+e.Key+e.Reason)
	}
	if want := []string{"size:", "key:a", "key:esc", "key:q", "exit:q"}; !slices.Equal(kinds, want) || got[0].Width != 72 {
		t.Errorf("events = %v, want %v", kinds, want)
	}
	// The time limit ends a probe that nobody closes.
	got = nil
	if cmd := step(fitProbeTimeout{}); cmd == nil || len(got) != 1 || got[0].Reason != "ttl" {
		t.Errorf("ttl: %v", got)
	}
}

func TestRunFitProbeRefusesBadOutput(t *testing.T) {
	t.Setenv(fitOutEnv, "")
	t.Setenv(fitNonceEnv, "")
	if code, _, errOut := runVerb(func(_ []string, _, stderr io.Writer) int { return runFitProbe(stderr) }); code != exitRefused ||
		!strings.Contains(errOut, "does not run by hand") {
		t.Errorf("no env = %d %s", code, errOut)
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fitNonceEnv, "n")
	for _, path := range []string{filepath.Join(dir, "missing"), link, dir} {
		t.Setenv(fitOutEnv, path)
		if code, _, _ := runVerb(func(_ []string, _, stderr io.Writer) int { return runFitProbe(stderr) }); code != exitError {
			t.Errorf("%s = %d", path, code)
		}
	}
	if data, _ := os.ReadFile(target); len(data) != 0 {
		t.Errorf("the probe wrote through a symlink: %q", data)
	}
}
