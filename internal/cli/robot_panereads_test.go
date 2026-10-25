package cli

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/robot"
)

// --robot-logs prints ntm's LogsOutput: every agent pane but the shells,
// its pane the slot's M, its last --logs-limit lines of at least 200 read.
func TestRobotLogs(t *testing.T) {
	s, _, reads := paneServer(t, copyText)
	code, out, _ := run("--robot-logs=swarm", "--socket", s.SocketPath)
	var got robot.LogsOutput
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || !got.Success || got.Session != "swarm" {
		t.Fatalf("--robot-logs = %d %s", code, out)
	}
	type row struct {
		pane      int
		typ, last string
		n         int
	}
	want := []row{{0, "claude", "loose claude", 1}, {1, "claude", "```", 5}, {2, "codex", "ERROR blue", 2}}
	if len(got.Panes) != len(want) || got.Summary.TotalPanes != 3 || got.Summary.TotalLines != 8 {
		t.Fatalf("panes = %+v, summary %+v", got.Panes, got.Summary)
	}
	for i, w := range want {
		p := got.Panes[i]
		if p.Pane != w.pane || p.AgentType != w.typ || p.LineCount != w.n || p.Lines[len(p.Lines)-1] != w.last || p.Truncated {
			t.Errorf("pane %d = %+v, want %+v", i, p, w)
		}
	}
	if r := reads(); len(r) != 3 || r[0].lines != 200 {
		t.Errorf("reads = %v", r)
	}
	// --logs-panes takes pane indexes (pane M of the agent tab);
	// --logs-limit keeps the last lines.
	code, out, _ = run("--robot-logs=swarm", "--logs-panes=2", "--logs-limit=1", "--socket", s.SocketPath)
	got = robot.LogsOutput{}
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || len(got.Panes) != 1 || got.Panes[0].Pane != 2 ||
		len(got.Panes[0].Lines) != 1 || got.Panes[0].Lines[0] != "ERROR blue" || !got.Panes[0].Truncated || got.Summary.TruncatedPanes != 1 {
		t.Errorf("--logs-panes=2 --logs-limit=1 = %d %s", code, out)
	}
}

// --robot-logs refuses a bad pane list and a pane selector it does not
// read; a session herdr does not have is SESSION_NOT_FOUND.
func TestRobotLogsRefusesPanes(t *testing.T) {
	s, _, _ := paneServer(t, copyText)
	for _, c := range []struct {
		args []string
		code string
	}{
		{[]string{"--robot-logs=swarm", "--logs-panes=x"}, robot.ErrCodeInvalidFlag},
		{[]string{"--robot-logs=swarm", "--panes=1"}, robot.ErrCodeInvalidFlag},
		{[]string{"--robot-logs=nosuch"}, robot.ErrCodeSessionNotFound},
	} {
		code, out, _ := run(append(c.args, "--socket", s.SocketPath)...)
		var got robot.ErrorOutput
		if code != 1 || json.Unmarshal([]byte(out), &got) != nil || got.Success || got.ErrorCode != c.code {
			t.Errorf("%v = %d %s", c.args, code, out)
		}
	}
}

// --robot-pane-address lists every pane with nhm's selector (the slot on
// a session with several tabs, the pane id for a pane without a slot).
func TestRobotPaneAddressMultiTab(t *testing.T) {
	s, _, _ := paneServer(t, copyText)
	code, out, _ := run("--robot-pane-address=swarm", "--socket", s.SocketPath)
	var got robot.PaneAddressOutput
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || !got.Success || !got.MultiWindow || len(got.Panes) != 4 {
		t.Fatalf("--robot-pane-address = %d %s", code, out)
	}
	want := []robot.PaneAddressEntry{
		{Selector: "w1:p3", PaneID: "w1:p3", Title: "user@host:/p", Type: "user", TmuxTarget: "w1:p3"},
		{Selector: "w1:p4", PaneID: "w1:p4", Title: "loose", Type: "claude", TmuxTarget: "w1:p4"},
		{Selector: "0.1", PaneID: "w1:p1", PaneIndex: 1, Title: "IcyKnoll opus", Type: "claude", TmuxTarget: "w1:p1"},
		{Selector: "0.2", PaneID: "w1:p2", PaneIndex: 2, Title: "BlueLake gpt", Type: "codex", TmuxTarget: "w1:p2"},
	}
	for i, w := range want {
		if got.Panes[i] != w {
			t.Errorf("pane %d = %+v, want %+v", i, got.Panes[i], w)
		}
	}
	code, out, _ = run("--robot-pane-address=nosuch", "--socket", s.SocketPath)
	got = robot.PaneAddressOutput{}
	if code != 1 || json.Unmarshal([]byte(out), &got) != nil || got.ErrorCode != robot.ErrCodeSessionNotFound || got.Panes == nil {
		t.Errorf("unknown session = %d %s", code, out)
	}
}

// With one tab, a pane's selector is its pane index alone, as ntm's is on
// a session with one window.
func TestRobotPaneAddressSingleTab(t *testing.T) {
	s, _, _ := paneServer(t, copyText)
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		snap := paneSnapshot()
		snap["panes"] = snap["panes"].([]any)[:2] // IcyKnoll and BlueLake, tab w1:t1
		return map[string]any{"type": "session_snapshot", "snapshot": snap}, nil
	})
	code, out, _ := run("--robot-pane-address=swarm", "--socket", s.SocketPath)
	var got robot.PaneAddressOutput
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || got.MultiWindow || len(got.Panes) != 2 ||
		got.Panes[0].Selector != "1" || got.Panes[1].Selector != "2" {
		t.Errorf("one tab = %d %s", code, out)
	}
}

// --robot-watch-bead finds the bead's mentions in each agent pane, once,
// with ntm's fields, and reads its status with br show.
func TestRobotWatchBead(t *testing.T) {
	s, _, reads := paneServer(t, func(pane string) string {
		switch pane {
		case "w1:p1":
			return "working on NHM-7 now\nother\n  done with nhm-7.\n"
		case "w1:p3":
			return "nhm-7 in the shell\n"
		}
		return "nothing\n"
	})
	old := watchBeadStatus
	t.Cleanup(func() { watchBeadStatus = old })
	watchBeadStatus = func(_ context.Context, root, id string) (string, error) {
		if root != "" || id != "nhm-7" {
			t.Errorf("status of %q in %q", id, root)
		}
		return "in_progress", nil
	}
	code, out, _ := run("--robot-watch-bead=swarm", "--bead=nhm-7", "--socket", s.SocketPath)
	var got robot.WatchBeadOutput
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || !got.Success {
		t.Fatalf("--robot-watch-bead = %d %s", code, out)
	}
	if got.BeadID != "nhm-7" || got.Interval != "30s" || got.PanesScanned != 3 || got.BeadStatus != "in_progress" ||
		len(got.Mentions) != 2 {
		t.Fatalf("output = %+v", got)
	}
	m := got.Mentions[1]
	if m.Pane != 1 || m.PaneRef != "0.1" || m.PaneID != "w1:p1" || m.AgentType != "claude" || m.Line != "done with nhm-7." ||
		m.LineNum != 3 || !m.Timestamp.Equal(got.CheckedAt) {
		t.Errorf("mention = %+v", m)
	}
	if r := reads(); len(r) != 3 || r[0].lines != 200 {
		t.Errorf("reads = %v, want 200 lines of each agent pane", r)
	}
	// --panes keeps the panes named; --lines and --interval are ntm's.
	code, out, _ = run("--robot-watch-bead=swarm", "--bead=nhm-7", "--panes=2", "--lines=50", "--interval=1500",
		"--socket", s.SocketPath)
	got = robot.WatchBeadOutput{}
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || got.PanesScanned != 1 || len(got.Mentions) != 0 || got.Interval != "1.5s" {
		t.Errorf("--panes=2 = %d %s", code, out)
	}
	if r := reads(); len(r) != 1 || r[0] != (paneRead{"w1:p2", 50}) {
		t.Errorf("reads = %v", r)
	}
}

// --bead is required; a selector that names no pane is PANE_NOT_FOUND; a
// status that fails stays "unknown", with the error.
func TestRobotWatchBeadNeedsBead(t *testing.T) {
	s, _, _ := paneServer(t, copyText)
	old := watchBeadStatus
	t.Cleanup(func() { watchBeadStatus = old })
	watchBeadStatus = func(context.Context, string, string) (string, error) { return "", errors.New("br: no such bead") }
	for _, c := range []struct {
		args []string
		code string
	}{
		{[]string{"--robot-watch-bead=swarm"}, robot.ErrCodeInvalidFlag},
		{[]string{"--robot-watch-bead=swarm", "--bead=x", "--panes=7"}, robot.ErrCodePaneNotFound},
		{[]string{"--robot-watch-bead=swarm", "--bead=x", "--panes=%3"}, robot.ErrCodeInvalidFlag},
		{[]string{"--robot-watch-bead=swarm", "--bead=x", "--interval=soon"}, robot.ErrCodeInvalidFlag},
	} {
		code, out, _ := run(append(c.args, "--socket", s.SocketPath)...)
		var got robot.WatchBeadOutput
		if code != 1 || json.Unmarshal([]byte(out), &got) != nil || got.ErrorCode != c.code {
			t.Errorf("%v = %d %s", c.args, code, out)
		}
	}
	code, out, _ := run("--robot-watch-bead=swarm", "--bead=x", "--socket", s.SocketPath)
	var got robot.WatchBeadOutput
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || got.BeadStatus != "unknown" || got.StatusError != "br: no such bead" {
		t.Errorf("status unknown = %d %s", code, out)
	}
}
