package cli

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

// logsServer is waitInstance's server with three agents in workspace w1
// ("swarm"): IcyKnoll (claude, w1:p1) and BlueLake (codex, w1:p2), both
// bound, and an unbound claude in w1:p3. text answers each pane.read;
// the lines each read asked for are recorded.
func logsServer(t *testing.T, text func(pane string) string) (*herdrtest.Server, []bindings.Binding, func() []int) {
	t.Helper()
	s, _, bs := waitInstance(t, func(pane string) map[string]any { return agentMap(pane, "claude", "idle", 1, 0) })
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		agents := []any{agentMap("w1:p1", "claude", "idle", 1, 0), agentMap("w1:p2", "codex", "working", 1, 0),
			agentMap("w1:p3", "claude", "idle", 1, 0)}
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "layouts": []any{}, "tabs": []any{}, "panes": []any{}, "agents": agents,
			"workspaces": []any{map[string]any{"workspace_id": "w1", "number": 1, "label": "swarm", "focused": true,
				"pane_count": 3, "tab_count": 2, "active_tab_id": "w1:t1", "agent_status": "idle"}},
		}}, nil
	})
	var mu sync.Mutex
	var asked []int
	read := func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
			Target string `json:"target"`
			Lines  int    `json:"lines"`
		}
		_ = json.Unmarshal(r.Params, &p)
		pane := p.PaneID + p.Target
		mu.Lock()
		asked = append(asked, p.Lines)
		mu.Unlock()
		return map[string]any{"type": "pane_read", "read": map[string]any{"pane_id": pane, "tab_id": "w1:t1",
			"workspace_id": "w1", "source": "recent", "format": "text", "revision": 3, "text": text(pane), "truncated": false}}, nil
	}
	s.Handle("pane.read", read)
	s.Handle("agent.read", read)
	return s, bs, func() []int {
		mu.Lock()
		defer mu.Unlock()
		out := asked
		asked = nil
		return out
	}
}

// numbered is "line 1" to "line n", one per line.
func numbered(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func logsText(pane string) string {
	switch pane {
	case "w1:p1":
		return "\x1b[1ma\x1b[0m\nerror one\nb\n\n\n"
	case "w1:p2":
		return numbered(250)
	}
	return ""
}

// nhm logs <session> is ntm's logs: every agent of the session, bound or
// not, under its own header, each pane's last 100 lines of at least 200
// read, no blank lines at the end, and ntm's total (the operator's
// ruling, nhm-k9d, 2026-10-23).
func TestLogsSessionReadsEveryAgent(t *testing.T) {
	s, bs, asked := logsServer(t, logsText)
	icy, blue := bs[0].Slot, bs[1].Slot
	code, out, errOut := run("logs", "swarm", "--socket", s.SocketPath)
	if code != 0 || errOut != "" {
		t.Fatalf("logs = %d %q %q", code, out, errOut)
	}
	for _, want := range []string{
		"Logs from session: swarm\nCaptured at: ",
		"=== Pane " + icy + " (claude) IcyKnoll ===\n  a\n  error one\n  b\n\n",
		"=== Pane " + blue + " (codex) BlueLake === [truncated]\n  line 151\n",
		"  line 250\n\n=== Pane w1:p3 (claude) ===\n  (no output)\n\n",
		"Total: 3 panes, 103 lines (1 truncated)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("logs lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "line 150\n") || strings.Contains(out, "\x1b") {
		t.Errorf("logs kept more than 100 lines or a control sequence:\n%s", out)
	}
	if got := asked(); fmt.Sprint(got) != "[200 200 200]" {
		t.Errorf("lines read = %v, want 200 per pane", got)
	}
	// -n keeps each pane's last N lines and reads twice as many.
	code, out, _ = run("logs", "swarm", "-n", "150", "--socket", s.SocketPath)
	if code != 0 || !strings.Contains(out, "  line 101\n") || strings.Contains(out, "  line 100\n") {
		t.Errorf("logs -n 150 = %d\n%s", code, out)
	}
	if got := asked(); fmt.Sprint(got) != "[300 300 300]" {
		t.Errorf("lines read with -n 150 = %v", got)
	}
	// --filter keeps the matching lines; --panes keeps the slots named.
	code, out, _ = run("logs", "swarm", "--filter", "^error", "--panes", icy+",w1:p3", "--socket", s.SocketPath)
	if code != 0 || !strings.Contains(out, "IcyKnoll ===\n  error one\n\n") || strings.Contains(out, "BlueLake") ||
		!strings.Contains(out, "Total: 2 panes, 1 lines\n") {
		t.Errorf("logs --filter --panes = %d\n%s", code, out)
	}
}

// -a is ntm's aggregated view: the lines that are not blank, each with
// its [type:slot] prefix and no headers.
func TestLogsAggregate(t *testing.T) {
	s, bs, _ := logsServer(t, logsText)
	code, out, _ := run("logs", "swarm", "-a", "-n", "2", "--socket", s.SocketPath)
	want := fmt.Sprintf("[cc:%s] error one\n[cc:%s] b\n[cod:%s] line 249\n[cod:%s] line 250\n", bs[0].Slot, bs[0].Slot, bs[1].Slot, bs[1].Slot)
	if code != 0 || out != want {
		t.Fatalf("logs -a = %d %q, want %q", code, out, want)
	}
	code, out, _ = run("logs", "swarm", "--aggregate", "-n", "2", "--json", "--socket", s.SocketPath)
	res := envelopeOf(t, out)["result"].(map[string]any)
	entries := res["entries"].([]any)
	first := entries[0].(map[string]any)
	if code != 0 || len(entries) != 4 || first["pane"] != bs[0].Slot || first["agent_type"] != "claude" || first["line"] != "error one" ||
		res["summary"].(map[string]any)["total_lines"] != float64(4) {
		t.Fatalf("logs -a --json = %d %s", code, out)
	}
}

func TestLogsJSONHasNTMsFields(t *testing.T) {
	s, bs, _ := logsServer(t, logsText)
	code, out, _ := run("logs", "w1", "--limit", "1", "--json", "--socket", s.SocketPath)
	res := envelopeOf(t, out)["result"].(map[string]any)
	panes := res["panes"].([]any)
	p := panes[1].(map[string]any)
	if code != 0 || res["session"] != "swarm" || len(panes) != 3 || p["pane"] != bs[1].Slot || p["pane_id"] != "w1:p2" ||
		p["name"] != "BlueLake" || p["agent_type"] != "codex" || p["line_count"] != float64(1) || p["truncated"] != true ||
		fmt.Sprint(p["lines"]) != "[line 250]" || p["captured_at"] == nil {
		t.Fatalf("logs --json = %d %s", code, out)
	}
	if sum := res["summary"].(map[string]any); sum["total_panes"] != float64(3) || sum["truncated_panes"] != float64(2) {
		t.Fatalf("summary = %v", sum)
	}
}

// logs refuses what herdr cannot answer or ntm would not take: a tmux
// pane index, a bad regex, read's flags with a session, no session.
func TestLogsRefusals(t *testing.T) {
	s, _, _ := logsServer(t, logsText)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"swarm", "--panes", "1"}, "tmux pane indexes"},
		{[]string{"swarm", "--panes", "cc"}, "slot N.M or a herdr pane id"},
		{[]string{"swarm", "--filter", "("}, "invalid filter regex"},
		{[]string{"swarm", "--lines", "5"}, "--lines: read's flags take an agent"},
		{[]string{"swarm", "-n", "0"}, "Usage: nhm logs"},
		{[]string{}, "Usage: nhm logs"},
		{[]string{""}, "Usage: nhm logs"},
		{[]string{"swarm", "other"}, "Usage: nhm logs"},
	} {
		code, out, errOut := run(append(append([]string{"logs"}, c.args...), "--socket", s.SocketPath)...)
		if code != exitRefused || !strings.Contains(errOut, c.want) {
			t.Errorf("logs %v = %d %q %q, want %q", c.args, code, out, errOut, c.want)
		}
	}
}

// nhm's extra: an agent's name, herdr name or pane id runs nhm read.
func TestLogsNameRunsRead(t *testing.T) {
	s, _, _ := logsServer(t, logsText)
	for _, target := range []string{"IcyKnoll", "w1:p1"} {
		code, out, errOut := run("logs", target, "--lines", "5", "--socket", s.SocketPath)
		wantCode, wantOut, wantErr := run("read", target, "--lines", "5", "--socket", s.SocketPath)
		if code != 0 || code != wantCode || out != wantOut || errOut != wantErr {
			t.Errorf("logs %s = %d %q %q; read gives %d %q %q", target, code, out, errOut, wantCode, wantOut, wantErr)
		}
	}
}

// -f prints only the lines that come after the first read, each with its
// prefix and time, or as one JSON object per line. The pane's last 10
// lines are the agent's live region: a spinner redrawn in place never
// prints, and nothing prints twice. Blank lines are left out, as ntm's.
func TestLogsFollowPrintsNewLines(t *testing.T) {
	var mu sync.Mutex
	reads := map[string]int{}
	scroll := []string{"line 1", "", "line 3", "line 4"}
	s, bs, _ := logsServer(t, func(pane string) string {
		mu.Lock()
		defer mu.Unlock()
		reads[pane]++
		r := reads[pane]
		if pane != "w1:p1" {
			return "same\n"
		}
		// The scrollback grows by a line each read; the live region
		// below it is the same box with a spinner that changes.
		text := strings.Join(scroll[:min(r, len(scroll))], "\n") + "\n"
		text += strings.Repeat("│ box │\n", logsLiveLines-1)
		return text + fmt.Sprintf("✻ Working… (%ds)\n", r)
	})
	interval, rounds := logsFollowInterval, logsFollowRounds
	logsFollowInterval, logsFollowRounds = 0, 3
	t.Cleanup(func() { logsFollowInterval, logsFollowRounds = interval, rounds })
	code, out, _ := run("logs", "swarm", "-f", "--socket", s.SocketPath)
	slot := regexp.QuoteMeta(bs[0].Slot)
	want := regexp.MustCompile(`^Following logs from session: swarm\nPress Ctrl\+C to stop\n\n` +
		`\[cc:` + slot + `\] \d\d:\d\d:\d\d line 3\n\[cc:` + slot + `\] \d\d:\d\d:\d\d line 4\n$`)
	if code != 0 || !want.MatchString(out) {
		t.Fatalf("logs -f = %d %q", code, out)
	}
	mu.Lock()
	reads = map[string]int{}
	mu.Unlock()
	code, out, _ = run("logs", "swarm", "--follow", "--filter", "4", "--json", "--socket", s.SocketPath)
	var line FollowedLine
	err := json.Unmarshal([]byte(out), &line)
	if _, terr := time.Parse(time.RFC3339, line.Timestamp); code != 0 || err != nil || terr != nil || line.Pane != bs[0].Slot ||
		line.AgentType != "claude" || line.Line != "line 4" {
		t.Fatalf("logs -f --json = %d %q %v", code, out, err)
	}
}

func TestShortAgentTypeIsNTMs(t *testing.T) {
	for in, want := range map[string]string{"claude": "cc", "codex": "cod", "antigravity": "agy", "oc": "oc",
		"omp": "omp", "pi": "pi", "grok": "gro", "": "unk"} {
		if got := shortAgentType(in); got != want {
			t.Errorf("shortAgentType(%q) = %q, want %q", in, got, want)
		}
	}
}
