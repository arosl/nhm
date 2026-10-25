package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/robot"
)

func robotErrorsText(pane string) string {
	switch pane {
	case "w1:p1":
		return "Error: config invalid\nnext\nlast\n"
	case "w1:p2":
		return "a\nb\nc\npanic: boom\nd\n"
	case "w1:p3":
		return "ERROR in the shell\n"
	case "w1:p4":
		return "rate limit exceeded\n"
	}
	return ""
}

func robotErrors(t *testing.T, args ...string) (int, robot.ErrorsOutput, string) {
	t.Helper()
	code, out, _ := run(args...)
	var got robot.ErrorsOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return code, got, out
}

// --robot-errors scans every agent pane (not the shells) with ntm's robot
// pattern table, and counts the errors by type and by agent.
func TestRobotErrorsMatchesPatterns(t *testing.T) {
	s, _, reads := paneServer(t, robotErrorsText)
	code, got, raw := robotErrors(t, "--robot-errors=swarm", "--socket", s.SocketPath)
	if code != 0 || !got.Success || got.Session != "swarm" || got.Summary.TotalErrors != 3 || got.Summary.PanesSearched != 3 ||
		got.Summary.TotalLines != 9 {
		t.Fatalf("--robot-errors = %d\n%s", code, raw)
	}
	types := map[string]int{"rate_limit": 1, "error": 1, "panic": 1}
	agents := map[string]int{"claude": 2, "codex": 1}
	for k, v := range types {
		if got.Summary.ByType[k] != v {
			t.Errorf("by_type = %v", got.Summary.ByType)
		}
	}
	for k, v := range agents {
		if got.Summary.ByAgent[k] != v {
			t.Errorf("by_agent = %v", got.Summary.ByAgent)
		}
	}
	e := got.Errors[1]
	if e.PaneID != "w1:p1" || e.PaneName != "IcyKnoll opus" || e.PaneIndex != 1 || e.AgentType != "claude" || e.LineNumber != 1 ||
		e.Content != "Error: config invalid" || e.MatchType != "error" || e.Timestamp.IsZero() {
		t.Errorf("entry = %+v", e)
	}
	// Every pane is read, the shell too, as ntm captures before it checks
	// the type; 1000 lines when --lines is not given.
	if r := reads(); len(r) != 4 || r[0].lines != 1000 {
		t.Errorf("reads = %v", r)
	}
}

// Two lines of context on each side, clamped at the top of the output.
func TestRobotErrorsContextLines(t *testing.T) {
	s, _, _ := paneServer(t, robotErrorsText)
	_, got, _ := robotErrors(t, "--robot-errors=swarm", "--socket", s.SocketPath)
	for _, e := range got.Errors {
		switch e.Content {
		case "panic: boom":
			if strings.Join(e.Context, "|") != "b|c|d" {
				t.Errorf("panic context = %q", e.Context)
			}
		case "Error: config invalid":
			if strings.Join(e.Context, "|") != "next|last" {
				t.Errorf("top context = %q", e.Context)
			}
		}
	}
}

// --panes keeps the panes named (nhm's pane forms), --type the agent
// type, by ntm's names or short names; --lines sets the lines read.
func TestRobotErrorsPanesAndType(t *testing.T) {
	s, _, reads := paneServer(t, robotErrorsText)
	code, got, raw := robotErrors(t, "--robot-errors=swarm", "--panes=2", "--lines=50", "--socket", s.SocketPath)
	if code != 0 || got.Summary.PanesSearched != 1 || len(got.Errors) != 1 || got.Errors[0].PaneID != "w1:p2" {
		t.Errorf("--panes=2 = %d\n%s", code, raw)
	}
	if r := reads(); len(r) != 1 || r[0] != (paneRead{"w1:p2", 50}) {
		t.Errorf("reads = %v", r)
	}
	for _, typ := range []string{"cc", "claude"} {
		code, got, raw = robotErrors(t, "--robot-errors=swarm", "--type="+typ, "--socket", s.SocketPath)
		if code != 0 || got.Summary.PanesSearched != 2 || got.Summary.ByAgent["codex"] != 0 || got.Summary.TotalErrors != 2 {
			t.Errorf("--type=%s = %d\n%s", typ, code, raw)
		}
	}
}

// A selector that does not parse is INVALID_FLAG, one that names no pane
// PANE_NOT_FOUND; a session herdr does not have is SESSION_NOT_FOUND.
func TestRobotErrorsBadSelector(t *testing.T) {
	s, _, _ := paneServer(t, robotErrorsText)
	for _, c := range []struct {
		args []string
		code string
	}{
		{[]string{"--robot-errors=swarm", "--panes=%4"}, robot.ErrCodeInvalidFlag},
		{[]string{"--robot-errors=swarm", "--panes=9"}, robot.ErrCodePaneNotFound},
		{[]string{"--robot-errors=nosuch"}, robot.ErrCodeSessionNotFound},
	} {
		code, got, raw := robotErrors(t, append(c.args, "--socket", s.SocketPath)...)
		if code != 1 || got.Success || got.ErrorCode != c.code {
			t.Errorf("%v = %d\n%s", c.args, code, raw)
		}
	}
}
