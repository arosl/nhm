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
func TestDiffComparesTwoAgents(t *testing.T) {
	s, asked := diffServer(t)
	one, two := stripControl(diffTexts["w1:p1"]), stripControl(diffTexts["w1:p2"])
	want := panediff.ComputeDiff("IcyKnoll", one, "BlueLake", two)
	head := fmt.Sprintf("Comparing IcyKnoll vs BlueLake:\n  Lines: 6 vs 6\n  Similarity: %.1f%%\n", want.Similarity*100)
	code, out, errOut := run("diff", "swarm", "IcyKnoll", "BlueLake", "--socket", s.SocketPath)
	if code != 0 || out != head {
		t.Fatalf("diff = %d %q %q, want %q", code, out, errOut, head)
	}
	if got := asked(); fmt.Sprint(got) != "[1000 1000]" {
		t.Errorf("lines read = %v", got)
	}
	// -u adds the patch text; a kind and a pane id name panes too, and a
	// bound pane shows its agent's name.
	code, out, _ = run("diff", "swarm", "cc", "w1:p2", "-u", "--socket", s.SocketPath)
	if code != 0 || out != head+"\nDiff:\n"+want.UnifiedDiff+"\n" {
		t.Fatalf("diff -u = %d %q", code, out)
	}
	// --code-only compares the fenced blocks, as "// <language>" and
	// the content.
	code, out, _ = run("diff", "swarm", "IcyKnoll", "BlueLake", "--code-only", "--json", "--socket", s.SocketPath)
	res := envelopeOf(t, out)["result"].(map[string]any)
	cw := panediff.ComputeDiff("IcyKnoll", "// go\nfmt.Println(1)\n\n", "BlueLake", "// golang\nfmt.Println(2)\n\n")
	if code != 0 || res["pane1"] != "IcyKnoll" || res["pane2"] != "BlueLake" || res["lines1"] != float64(cw.LineCount1) ||
		res["similarity"] != cw.Similarity || res["diff"] != cw.UnifiedDiff {
		t.Fatalf("diff --code-only --json = %d %s, want %+v", code, out, cw)
	}
	// Without a session: the calling pane's workspace.
	t.Setenv("HERDR_WORKSPACE_ID", "w1")
	if code, out, _ := run("diff", "IcyKnoll", "BlueLake", "--socket", s.SocketPath); code != 0 || out != head {
		t.Fatalf("diff without a session = %d %q", code, out)
	}
	asked()
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"swarm", "1", "2"}, "tmux pane indexes"},
		{[]string{"swarm", "IcyKnoll", "NoSuchAgent"}, "NoSuchAgent"},
		{[]string{"nosuch", "IcyKnoll", "BlueLake"}, "no such workspace"},
		{[]string{"IcyKnoll"}, "Usage: nhm diff"},
		{[]string{"a", "b", "c", "d"}, "Usage: nhm diff"},
	} {
		code, _, errOut := run(append(append([]string{"diff"}, c.args...), "--socket", s.SocketPath)...)
		if code != exitRefused || !strings.Contains(errOut, c.want) {
			t.Errorf("diff %v = %d %q, want %q", c.args, code, errOut, c.want)
		}
	}
	if got := asked(); len(got) > 1 {
		t.Errorf("refused diffs read panes: %v", got)
	}
}
