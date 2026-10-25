package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/robot"
)

// --robot-summary reads 500 lines of each pane, shells too, and answers
// ntm's SessionSummaryResponse: ntm's short agent types, herdr's state,
// the counts from ntm's extractors and git's changed files as conflicts.
func TestRobotSummary(t *testing.T) {
	s, _, reads := paneServer(t, copyText)
	t.Chdir(t.TempDir())
	fakeGitStatus(t, " M a.go\n", nil)
	code, out, _ := run("--robot-summary=swarm", "--socket", s.SocketPath)
	var got robot.SessionSummaryResponse
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || !got.Success || got.Summary == nil {
		t.Fatalf("--robot-summary = %d %s", code, out)
	}
	sum := got.Summary
	tr := sum.TimeRange
	if sum.Session != "swarm" || tr.Duration != 30*time.Minute || tr.End.Sub(tr.Start) != 30*time.Minute ||
		tr.End.Location() != time.UTC || !strings.HasSuffix(sum.Timestamp, "Z") {
		t.Errorf("summary = %+v", sum)
	}
	if len(sum.Agents) != 4 || sum.TotalOutput != 0+2+7+3 || sum.TotalFiles != 0 {
		t.Fatalf("agents = %+v, output %d", sum.Agents, sum.TotalOutput)
	}
	want := []struct {
		pane, title, typ string
		lines            int
	}{{"w1:p3", "user@host:/p", "user", 0}, {"w1:p4", "loose", "cc", 2}, {"w1:p1", "IcyKnoll opus", "cc", 7}, {"w1:p2", "BlueLake gpt", "cod", 3}}
	for i, w := range want {
		a := sum.Agents[i]
		if a.PaneID != w.pane || a.PaneTitle != w.title || a.AgentType != w.typ || a.OutputLines != w.lines || a.State != "idle" {
			t.Errorf("agent %d = %+v, want %+v", i, a, w)
		}
	}
	if a := sum.Agents[2]; a.CodeBlocks != 1 || a.OutputChars != len(copyText("w1:p1")) || a.Errors != 0 {
		t.Errorf("IcyKnoll = %+v", a)
	}
	if len(sum.Conflicts) != 1 || sum.Conflicts[0].Path != "a.go" || !strings.Contains(out, `"agents": []`) {
		t.Errorf("conflicts = %+v in %s", sum.Conflicts, out)
	}
	if r := reads(); len(r) != 4 || r[0].lines != 500 {
		t.Errorf("reads = %v, want 500 lines of each pane", r)
	}
	// --summary-since wins over --since; a bare number is minutes.
	code, out, _ = run("--robot-summary=swarm", "--summary-since=10", "--since=1h", "--socket", s.SocketPath)
	got = robot.SessionSummaryResponse{}
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || got.Summary.TimeRange.Duration != 10*time.Minute {
		t.Errorf("--summary-since=10 = %d %s", code, out)
	}
}

// Outside a git repo there are no conflicts; a bad window is
// INVALID_FLAG, and a session nhm cannot find SESSION_NOT_FOUND.
func TestRobotSummaryNoGitAndRefusals(t *testing.T) {
	s, _, _ := paneServer(t, copyText)
	fakeGitStatus(t, "", errFakeGit)
	code, out, _ := run("--robot-summary=swarm", "--since=1h", "--socket", s.SocketPath)
	var got robot.SessionSummaryResponse
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || got.Summary.Conflicts != nil ||
		got.Summary.TimeRange.Duration != time.Hour || strings.Contains(out, `"conflicts"`) {
		t.Errorf("no git = %d %s", code, out)
	}
	for _, tc := range []struct {
		args []string
		code string
	}{
		{[]string{"--robot-summary=swarm", "--since=later"}, robot.ErrCodeInvalidFlag},
		{[]string{"--robot-summary=nosuch"}, robot.ErrCodeSessionNotFound},
	} {
		code, out, _ := run(append(tc.args, "--socket", s.SocketPath)...)
		if code != 1 || !strings.Contains(out, `"error_code":"`+tc.code+`"`) {
			t.Errorf("%v = %d %s", tc.args, code, out)
		}
	}
}
