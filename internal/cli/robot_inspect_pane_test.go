package cli

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/robot"
)

func inspectText(pane string) string {
	if pane == "w1:p1" {
		return "reading ./run and internal/cli/x.go\nError: boom\n```go\nfmt.Println(1)\n```\nsee x.go\n"
	}
	return "$ ls\n"
}

// --robot-inspect-pane reads the pane that --index names (pane M of the
// agent tab) and answers ntm's InspectPaneOutput: the agent, the output's
// last lines, errors, file references and, with --code, code blocks.
func TestRobotInspectPane(t *testing.T) {
	s, _, reads := paneServer(t, inspectText)
	s.Handle("pane.process_info", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		info := map[string]any{"pane_id": p.PaneID}
		if p.PaneID == "w1:p1" {
			info["foreground_processes"] = []any{map[string]any{"pid": 11, "name": "claude"}}
		}
		return map[string]any{"type": "pane_process_info", "process_info": info}, nil
	})
	code, out, _ := run("--robot-inspect-pane=swarm", "--index=1", "--code", "--socket", s.SocketPath)
	var got robot.InspectPaneOutput
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || !got.Success {
		t.Fatalf("--robot-inspect-pane = %d %s", code, out)
	}
	if got.Session != "swarm" || got.PaneIndex != 1 || got.PaneID != "w1:p1" {
		t.Errorf("pane = %s %d %s", got.Session, got.PaneIndex, got.PaneID)
	}
	if want := (robot.InspectPaneAgent{Type: "claude", Title: "IcyKnoll opus", State: "idle", StateConfidence: 1,
		Command: "claude", ProcessRunning: true}); got.Agent != want {
		t.Errorf("agent = %+v, want %+v", got.Agent, want)
	}
	o := got.Output
	if o.Lines != 6 || o.Characters != len(inspectText("w1:p1")) || len(o.LastLines) != 6 || o.LastLines[1] != "Error: boom" ||
		!slices.Equal(o.ErrorsFound, []string{"Error: boom"}) ||
		!slices.Equal(o.CodeBlocks, []robot.CodeBlockInfo{{Language: "go", LineStart: 2, LineEnd: 4}}) {
		t.Errorf("output = %+v", o)
	}
	if !slices.Equal(got.Context.RecentFiles, []string{"./run", "internal/cli/x.go", "x.go"}) {
		t.Errorf("recent files = %q", got.Context.RecentFiles)
	}
	h := got.AgentHints
	if h == nil || h.Summary != "claude agent in idle state, 6 lines of output" ||
		!slices.Equal(h.Warnings, []string{"1 error(s) detected in recent output"}) || len(h.SuggestedActions) != 0 {
		t.Errorf("hints = %+v", h)
	}
	if r := reads(); len(r) != 1 || r[0] != (paneRead{"w1:p1", 100}) {
		t.Errorf("reads = %v, want 100 lines of w1:p1", r)
	}
	// No --code: no code blocks. ntm's older names; --inspect-lines wins
	// over --lines, which is used alone.
	code, out, _ = run("--robot-inspect-pane=swarm", "--inspect-index=2", "--inspect-lines=7", "--lines=9", "--socket", s.SocketPath)
	got = robot.InspectPaneOutput{}
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || got.PaneID != "w1:p2" || got.Output.CodeBlocks != nil ||
		got.Agent.Command != "codex" {
		t.Errorf("--inspect-index=2 = %d %s", code, out)
	}
	run("--robot-inspect-pane=swarm", "--index=2", "--lines=9", "--socket", s.SocketPath)
	if r := reads(); len(r) != 2 || r[0].lines != 7 || r[1].lines != 9 {
		t.Errorf("reads = %v, want 7 then 9 lines", r)
	}
}

// Index 0, the default, names no pane, so it is the first pane, as in
// ntm: here a shell, which herdr finds no agent in.
func TestRobotInspectPaneFirstPane(t *testing.T) {
	s, _, _ := paneServer(t, inspectText)
	code, out, _ := run("--robot-inspect-pane=swarm", "--socket", s.SocketPath)
	var got robot.InspectPaneOutput
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || got.PaneID != "w1:p3" || got.PaneIndex != 0 {
		t.Fatalf("index 0 = %d %s", code, out)
	}
	if got.Agent.Type != "unknown" || got.Agent.StateConfidence != 0 || got.Agent.Title != "user@host:/p" ||
		got.Output.Lines != 1 || got.AgentHints.Summary != "unknown agent in idle state, 1 lines of output" {
		t.Errorf("shell = %+v %+v", got.Agent, got.AgentHints)
	}
}

// An index that names no pane is PANE_NOT_FOUND, with the valid indices;
// a session nhm cannot find is ntm's plain SESSION_NOT_FOUND; --pane is
// refused.
func TestRobotInspectPaneRefusals(t *testing.T) {
	s, _, _ := paneServer(t, inspectText)
	code, out, _ := run("--robot-inspect-pane=swarm", "--index=5", "--socket", s.SocketPath)
	var got robot.InspectPaneOutput
	if code != 1 || json.Unmarshal([]byte(out), &got) != nil || got.ErrorCode != robot.ErrCodePaneNotFound ||
		got.Error != "pane 5 not found in session 'swarm'" || got.Hint != "Valid pane indices: w1:p3, w1:p4, 1, 2" ||
		got.PaneIndex != 5 || got.Session != "swarm" || !strings.Contains(out, `"last_lines":[]`) {
		t.Errorf("--index=5 = %d %s", code, out)
	}
	code, out, _ = run("--robot-inspect-pane=nosuch", "--socket", s.SocketPath)
	if code != 1 || !strings.Contains(out, `"error_code":"SESSION_NOT_FOUND"`) || strings.Contains(out, `"pane_index"`) {
		t.Errorf("unknown session = %d %s", code, out)
	}
	code, out, _ = run("--robot-inspect-pane=swarm", "--pane=1", "--socket", s.SocketPath)
	if code != 1 || !strings.Contains(out, `"error_code":"INVALID_FLAG"`) {
		t.Errorf("--pane = %d %s", code, out)
	}
}
