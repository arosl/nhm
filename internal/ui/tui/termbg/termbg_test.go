package termbg_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStartsWithoutProbe runs two programs that link bubbletea under
// script(1), whose pty answers no query: one with termbg, which must
// start at once, and one without, which must wait for Lip Gloss's
// background query. The control shows that this environment does make
// the query; without it, a fast start would prove nothing.
func TestStartsWithoutProbe(t *testing.T) {
	if testing.Short() {
		t.Skip("builds two programs and waits five seconds for the control")
	}
	script, err := exec.LookPath("script")
	if err != nil {
		t.Skip("no script(1) to give the programs a silent pty")
	}
	dir := t.TempDir()
	for _, name := range []string{"guarded", "bare"} {
		out, err := exec.Command("go", "build", "-o", filepath.Join(dir, name), "./testdata/"+name).CombinedOutput()
		if err != nil {
			t.Fatalf("go build %s: %v\n%s", name, err, out)
		}
	}
	run := func(t *testing.T, name string) time.Duration {
		start := time.Now()
		cmd := exec.Command(script, "-qec", filepath.Join(dir, name), "/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s under script: %v\n%q", name, err, out)
		}
		return time.Since(start)
	}
	t.Run("guarded", func(t *testing.T) {
		t.Parallel()
		if d := run(t, "guarded"); d > 2*time.Second {
			t.Errorf("with termbg the program took %v to start; Lip Gloss queried the terminal", d)
		}
	})
	t.Run("bare", func(t *testing.T) {
		t.Parallel()
		if d := run(t, "bare"); d < 3*time.Second {
			t.Skipf("without termbg the program took only %v: this pty answers, so the test proves nothing here", d)
		}
	})
}

// TestBinariesLinkTermbg: each shipped binary that links bubbletea links
// termbg too.
func TestBinariesLinkTermbg(t *testing.T) {
	for _, cmd := range []string{"github.com/arosl/nhm/cmd/nhm", "github.com/arosl/nhm/cmd/nhm-hook"} {
		// -e: the import graph is enough, even while a package has errors.
		out, err := exec.Command("go", "list", "-e", "-deps", "-f", "{{.ImportPath}}", cmd).CombinedOutput()
		if err != nil {
			t.Fatalf("go list %s: %v\n%s", cmd, err, out)
		}
		deps := map[string]bool{}
		for _, p := range strings.Fields(string(out)) {
			deps[p] = true
		}
		if deps["github.com/charmbracelet/bubbletea"] && !deps["github.com/arosl/nhm/internal/ui/tui/termbg"] {
			t.Errorf("%s links bubbletea but not termbg: each verb would query the terminal at start", cmd)
		}
	}
}
