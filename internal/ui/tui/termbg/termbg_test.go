package termbg_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	bubbletea = "github.com/charmbracelet/bubbletea"
	lipgloss  = "github.com/charmbracelet/lipgloss"
	termbg    = "github.com/arosl/nhm/internal/ui/tui/termbg"
	nhmCmd    = "github.com/arosl/nhm/cmd/nhm"
	hookCmd   = "github.com/arosl/nhm/cmd/nhm-hook"
)

// silentPty returns script(1), which runs a program in a pty that answers
// no query, or skips the test.
func silentPty(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds programs and runs them in a pty")
	}
	script, err := exec.LookPath("script")
	if err != nil {
		t.Skip("no script(1) to give the program a silent pty")
	}
	return script
}

// build builds a package into dir and returns the binary's path.
func build(t *testing.T, dir, pkg string) string {
	t.Helper()
	bin := filepath.Join(dir, filepath.Base(pkg))
	if out, err := exec.Command("go", "build", "-o", bin, pkg).CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, out)
	}
	return bin
}

// underPty runs a command line in script's pty and returns how long it
// took.
func underPty(t *testing.T, script, cmdline string) time.Duration {
	t.Helper()
	start := time.Now()
	if out, err := exec.Command(script, "-qec", cmdline, "/dev/null").CombinedOutput(); err != nil {
		t.Errorf("%s under script: %v\n%q", cmdline, err, out)
	}
	return time.Since(start)
}

// TestStartsWithoutProbe runs two programs that link bubbletea in a pty
// that answers no query: one with termbg, which must start at once, and
// one without, which must wait for Lip Gloss's background query. The
// control shows that this environment does make the query; without it, a
// fast start would prove nothing.
func TestStartsWithoutProbe(t *testing.T) {
	script := silentPty(t)
	dir := t.TempDir()
	guarded, bare := build(t, dir, "./testdata/guarded"), build(t, dir, "./testdata/bare")
	t.Run("guarded", func(t *testing.T) {
		t.Parallel()
		if d := underPty(t, script, guarded); d > 2*time.Second {
			t.Errorf("with termbg the program took %v to start; Lip Gloss queried the terminal", d)
		}
	})
	t.Run("bare", func(t *testing.T) {
		t.Parallel()
		if d := underPty(t, script, bare); d < 3*time.Second {
			t.Skipf("without termbg the program took only %v: this pty answers, so the test proves nothing here", d)
		}
	})
}

// TestNhmVersionStartsFast: nhm version in a pty that answers no query
// ends well under a second. It catches any import that brings the
// terminal query back, such as a package that sorts before termbg and
// pulls bubbletea in.
func TestNhmVersionStartsFast(t *testing.T) {
	script := silentPty(t)
	nhm := build(t, t.TempDir(), nhmCmd)
	if d := underPty(t, script, nhm+" version"); d > time.Second {
		t.Errorf("nhm version took %v in a silent pty; something queries the terminal at start", d)
	}
}

// deps lists the packages a command links.
func deps(t *testing.T, cmd string) map[string]bool {
	t.Helper()
	// -e: the import graph is enough, even while a package has errors.
	out, err := exec.Command("go", "list", "-e", "-deps", "-f", "{{.ImportPath}}", cmd).CombinedOutput()
	if err != nil {
		t.Fatalf("go list %s: %v\n%s", cmd, err, out)
	}
	set := map[string]bool{}
	for _, p := range strings.Fields(string(out)) {
		set[p] = true
	}
	return set
}

// TestBinariesLinkTermbg: each shipped binary that links bubbletea links
// termbg too.
func TestBinariesLinkTermbg(t *testing.T) {
	for _, cmd := range []string{nhmCmd, hookCmd} {
		if d := deps(t, cmd); d[bubbletea] && !d[termbg] {
			t.Errorf("%s links bubbletea but not termbg: each verb would query the terminal at start", cmd)
		}
	}
	if !deps(t, nhmCmd)[termbg] {
		t.Errorf("%s does not link termbg", nhmCmd)
	}
}

// TestHookLinksNoTUI: nhm-hook runs on herdr's events with a 2 s budget;
// it links no TUI library at all.
func TestHookLinksNoTUI(t *testing.T) {
	d := deps(t, hookCmd)
	for _, p := range []string{bubbletea, lipgloss, termbg} {
		if d[p] {
			t.Errorf("%s links %s; package ui (which the hook imports) must not import internal/ui/tui", hookCmd, p)
		}
	}
}
