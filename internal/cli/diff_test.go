package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/arosl/nhm/internal/core/panediff"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

var diffTexts = map[string]string{
	"w1:p1": "line 1\nline 2\nline 3\n```go\nfmt.Println(1)\n```\n",
	"w1:p2": "\x1b[1mline 1\x1b[0m\nline 2 changed\nline 3\n```golang\nfmt.Println(2)\n```\n",
}

// diffServer is waitInstance's server (IcyKnoll in w1:p1, BlueLake in
// w1:p2) whose panes answer pane.read with diffTexts; the lines each read
// asked for are recorded.
func diffServer(t *testing.T) (*herdrtest.Server, func() []int) {
	t.Helper()
	s, _, _ := waitInstance(t, func(string) map[string]any { return nil })
	s.Handle("session.snapshot", snapshotWithPanes(map[string]string{"w1:p1": "w1:t1", "w1:p2": "w1:t2"}))
	var mu sync.Mutex
	var asked []int
	s.Handle("pane.read", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
			Lines  int    `json:"lines"`
		}
		_ = json.Unmarshal(r.Params, &p)
		mu.Lock()
		asked = append(asked, p.Lines)
		mu.Unlock()
		return map[string]any{"type": "pane_read", "read": map[string]any{"pane_id": p.PaneID, "tab_id": "w1:t1",
			"workspace_id": "w1", "source": "recent", "format": "text", "revision": 3, "text": diffTexts[p.PaneID], "truncated": false}}, nil
	})
	return s, func() []int {
		mu.Lock()
		defer mu.Unlock()
		out := asked
		asked = nil
		return out
	}
}

// nhm diff is ntm's diff on herdr: the two panes' last 1000 lines,
// without control sequences, compared by ntm's ComputeDiff, in ntm's
// words (the operator's ruling, nhm-k9d, 2026-10-23).
// nhm diff is ntm's diff on herdr: the two panes' last 1000 lines,
// without control sequences, compared by ntm's ComputeDiff, in ntm's
// words. A pane without a herdr label is titled with ntm's name,
// <session>__<type>_<n> (IcyKnoll's Claude is swarm__cc_1).
func TestDiffComparesTwoAgents(t *testing.T) {
	s, asked := diffServer(t)
	one, two := stripControl(diffTexts["w1:p1"]), stripControl(diffTexts["w1:p2"])
	want := panediff.ComputeDiff("swarm__cc_1", one, "swarm__cod_1", two)
	head := fmt.Sprintf("Comparing swarm__cc_1 vs swarm__cod_1:\n  Lines: 6 vs 6\n  Similarity: %.1f%%\n", want.Similarity*100)
	code, out, errOut := run("diff", "swarm", "IcyKnoll", "BlueLake", "--socket", s.SocketPath)
	if code != 0 || out != head {
		t.Fatalf("diff = %d %q %q, want %q", code, out, errOut, head)
	}
	if got := asked(); fmt.Sprint(got) != "[1000 1000]" {
		t.Errorf("lines read = %v", got)
	}
	// -u adds the patch text.
	if code, out, _ := run("diff", "swarm", "cc_1", "cod_1", "-u", "--socket", s.SocketPath); code != 0 || out != head+"\nDiff:\n"+want.UnifiedDiff+"\n" {
		t.Fatalf("diff -u = %d %q", code, out)
	}
	// --code-only compares the fenced blocks, as "// <language>" and the
	// content; --json is ntm's bare DiffResult.
	code, out, _ = run("diff", "swarm", "IcyKnoll", "BlueLake", "--code-only", "--json", "--socket", s.SocketPath)
	cw := panediff.ComputeDiff("swarm__cc_1", "// go\nfmt.Println(1)\n\n", "swarm__cod_1", "// golang\nfmt.Println(2)\n\n")
	wantJSON, _ := json.MarshalIndent(cw, "", "  ")
	if code != 0 || out != string(wantJSON)+"\n" {
		t.Fatalf("diff --code-only --json = %d %s, want %s", code, out, wantJSON)
	}
	asked()
}

// ntm's pane forms (index, title, the end of the title, pane id) on
// herdr, beside nhm's slot, name and kind; ntm's session resolution.
func TestDiffNTMPaneForms(t *testing.T) {
	s, asked := diffServer(t)
	head := "Comparing swarm__cc_1 vs swarm__cod_1:\n"
	for _, pair := range [][2]string{
		{"cc_1", "cod_1"}, {"swarm__cc_1", "swarm__cod_1"}, {"1", "w1:p2"}, {"0.1", "1.1"}, {"cc", "codex"}, {"icyknoll", "bluelake"},
	} {
		code, out, errOut := run("diff", "swarm", pair[0], pair[1], "--socket", s.SocketPath)
		if code != 0 || !strings.HasPrefix(out, head) {
			t.Errorf("diff %v = %d %q %q", pair, code, out, errOut)
		}
	}
	// Without a session: the only one; a unique prefix names it.
	for _, args := range [][]string{{"cc_1", "cod_1"}, {"sw", "cc_1", "cod_1"}} {
		if code, out, _ := run(append(append([]string{"diff"}, args...), "--socket", s.SocketPath)...); code != 0 || !strings.HasPrefix(out, head) {
			t.Errorf("diff %v = %d %q", args, code, out)
		}
	}
	asked()
}

// Every error is the root's, as in ntm: "Error: <msg>", or the robot
// error envelope under --json; exit 1.
func TestDiffNTMErrors(t *testing.T) {
	s, asked := diffServer(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"swarm", "IcyKnoll", "NoSuchAgent"}, "Error: pane 'NoSuchAgent' not found in session 'swarm'\n"},
		{[]string{"swarm", "%3", "cc_1"}, "Error: pane '%3' not found in session 'swarm'\n"},
		{[]string{"IcyKnoll"}, "Error: accepts between 2 and 3 arg(s), received 1\n"},
		{[]string{"a", "b", "c", "d"}, "Error: accepts between 2 and 3 arg(s), received 4\n"},
	} {
		code, out, errOut := run(append(append([]string{"diff"}, c.args...), "--socket", s.SocketPath)...)
		if code != 1 || out != "" || errOut != c.want {
			t.Errorf("diff %v = %d %q %q, want %q", c.args, code, out, errOut, c.want)
		}
	}
	if code, _, errOut := run("diff", "nosuch", "a", "b", "--socket", s.SocketPath); code != 1 || !strings.HasPrefix(errOut, `Error: session "nosuch" not found`) {
		t.Errorf("unknown session = %d %q", code, errOut)
	}
	code, out, _ := run("diff", "swarm", "IcyKnoll", "NoSuchAgent", "--socket", s.SocketPath, "--json")
	if code != 1 || !strings.HasPrefix(out, `{"success":false,`) || !strings.Contains(out, "pane 'NoSuchAgent' not found") || strings.Count(out, "\n") != 1 {
		t.Errorf("--json error = %d %q", code, out)
	}
	if got := asked(); len(got) > 2 {
		t.Errorf("refused diffs read panes: %v", got)
	}
	s.Handle("pane.read", func(herdrtest.Request) (any, *herdrtest.Error) {
		return nil, &herdrtest.Error{Code: "pane_not_found", Message: "no such pane"}
	})
	if code, _, errOut := run("diff", "swarm", "cc_1", "cod_1", "--socket", s.SocketPath); code != 1 || !strings.HasPrefix(errOut, "Error: capturing pane 1: ") {
		t.Errorf("failed read = %d %q", code, errOut)
	}
}
