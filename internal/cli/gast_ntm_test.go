package cli

import (
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

func gastText(pane string) string {
	switch pane {
	case "w1:p3":
		return "user@host:~$ \n"
	case "w1:p4":
		return "Rate limit exceeded\n"
	case "w1:p1":
		return "working...\n· thinking\n"
	case "w1:p2":
		return "› Write tests\nerror: something went badly wrong here\n"
	}
	return ""
}

// The full table: the first shell is the controller, each other pane a
// worker with its slot's M, short type, ntm's state word, last line and
// issues; then ntm's summary.
func TestGASTFullTable(t *testing.T) {
	s, _, reads := paneServer(t, gastText)
	code, out, errOut := run("get-all-session-text", "--socket", s.SocketPath)
	want := "## NHM Swarm Status\n\n" +
		"| Session | Pane | Type | State | Last Message | Issues |\n" +
		"|---------|------|------|-------|--------------|--------|\n" +
		"| swarm | 1 (ctrl) | user | WAITING | user@host:~$ | - |\n" +
		"| swarm | 0 | cc | ERROR | Rate limit exceeded | RATE_LIMIT |\n" +
		"| swarm | 1 | cc | THINKING | · thinking | - |\n" +
		"| swarm | 2 | cod | ERROR | error: something went badly wrong here | error: something went badly wrong here |\n" +
		"\n**Summary:** 1 sessions, 2 errors, 1 rate-limited\n"
	if code != 0 || errOut != "" || out != want {
		t.Fatalf("gast = %d %q\n%s\nwant\n%s", code, errOut, out, want)
	}
	got := reads()
	if len(got) != 4 || got[0] != (paneRead{"w1:p3", 10}) {
		t.Errorf("reads = %v, want 10 lines of every pane", got)
	}
	// --lines sets the lines read; --json changes nothing.
	code, out2, _ := run("get-all-session-text", "--lines", "5", "--json", "--socket", s.SocketPath)
	if code != 0 || out2 != want {
		t.Errorf("--lines 5 --json = %d\n%s", code, out2)
	}
	if got := reads(); len(got) != 4 || got[0].lines != 5 {
		t.Errorf("--lines 5 reads = %v", got)
	}
}

// --compact: one row per session, with ntm's short states.
func TestGASTCompact(t *testing.T) {
	s, _, _ := paneServer(t, gastText)
	code, out, _ := run("gast", "--compact", "--socket", s.SocketPath)
	want := "## NHM Swarm Status (Compact)\n\n" +
		"| Session | Ctrl | Workers | Errors |\n" +
		"|---------|------|---------|--------|\n" +
		"| swarm | WAIT | cc:RATE cc:THINK cod:ERR | 2 (1 rate) |\n"
	if code != 0 || out != want {
		t.Fatalf("gast --compact = %d\n%s\nwant\n%s", code, out, want)
	}
}

// No workspace: ntm's line, exit 0.
func TestGASTNoSessions(t *testing.T) {
	s, _, _ := paneServer(t, gastText)
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		snap := paneSnapshot()
		snap["workspaces"], snap["tabs"], snap["panes"], snap["layouts"] = []any{}, []any{}, []any{}, []any{}
		return map[string]any{"type": "session_snapshot", "snapshot": snap}, nil
	})
	code, out, _ := run("get-all-session-text", "--socket", s.SocketPath)
	if code != 0 || out != "No active herdr sessions found.\n" {
		t.Errorf("gast = %d %q", code, out)
	}
}

// gast and swarm-status are get-all-session-text; a long last line is cut
// to 60 bytes and a pipe escaped; it takes no argument.
func TestGASTAliases(t *testing.T) {
	long := strings.Repeat("x", 70) + "\n"
	s, _, _ := paneServer(t, func(pane string) string {
		if pane == "w1:p1" {
			return long
		}
		return "a | b\n"
	})
	var outs []string
	for _, verb := range []string{"get-all-session-text", "gast", "swarm-status"} {
		code, out, _ := run(verb, "--socket", s.SocketPath)
		if code != 0 {
			t.Fatalf("%s = %d", verb, code)
		}
		outs = append(outs, out)
	}
	if outs[0] != outs[1] || outs[1] != outs[2] {
		t.Errorf("the aliases differ:\n%s\n%s\n%s", outs[0], outs[1], outs[2])
	}
	if !strings.Contains(outs[0], "| "+strings.Repeat("x", 57)+"... |") || !strings.Contains(outs[0], "| a \\| b |") {
		t.Errorf("truncation or escape:\n%s", outs[0])
	}
	code, _, errOut := run("gast", "extra", "--socket", s.SocketPath)
	if code != 1 || errOut != "Error: unknown command \"extra\" for \"nhm get-all-session-text\"\n" {
		t.Errorf("gast extra = %d %q", code, errOut)
	}
}
