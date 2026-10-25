package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/integrations/kinds/dialogs"
	"github.com/arosl/nhm/internal/robot"
)

// dialogScreens are the visible screens of paneServer's panes: a trust
// prompt on IcyKnoll (0.1), a staged paste in BlueLake's Codex (0.2), an
// idle prompt on the loose Claude (w1:p4).
func dialogScreens(pane string) string {
	switch pane {
	case "w1:p1":
		return "\x1b[1m Quick safety check: Is this a project you created or one you trust?\x1b[0m\n\n" +
			" ❯ 1. Yes, I trust this folder\n   2. No, exit\n\n Enter to confirm · Esc to cancel\n"
	case "w1:p2":
		return "transcript line\n› [Pasted text #1 +48 lines]\n  send a message\n"
	case "w1:p4":
		return " some transcript\n\n ❯ Try \"refactor <filepath>\"\n"
	}
	return "user@host:/p$ "
}

// --robot-dialogs classifies each agent pane's visible screen, the shells
// left out, in the session's order: the dialog's class, its options and
// how they are chosen.
func TestRobotDialogsClassifies(t *testing.T) {
	s, _, _ := paneServer(t, dialogScreens)
	code, out, _ := run("--robot-dialogs=swarm", "--socket", s.SocketPath)
	var got robot.DialogsOutput
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || !got.Success || got.Session != "swarm" || len(got.Panes) != 3 {
		t.Fatalf("--robot-dialogs = %d %s", code, out)
	}
	want := []struct{ pane, target, typ, class string }{
		{"w1:p4", "w1:p4", "claude", dialogs.None},
		{"0.1", "w1:p1", "claude", dialogs.TrustPrompt},
		{"0.2", "w1:p2", "codex", dialogs.PasteLimbo},
	}
	for i, w := range want {
		p := got.Panes[i]
		if p.Pane != w.pane || p.Target != w.target || p.AgentType != w.typ || p.Dialog.Class != w.class {
			t.Errorf("pane %d = %+v, want %+v", i, p, w)
		}
	}
	trust := got.Panes[1].Dialog
	if trust.InputMode != "numbered" || len(trust.Options) != 2 || !trust.Options[0].Selected || trust.Options[1].Label != "No, exit" ||
		!strings.HasPrefix(trust.Evidence, "Quick safety check") {
		t.Errorf("trust prompt = %+v", trust)
	}
	if got.Panes[2].Dialog.Evidence != "› [Pasted text #1 +48 lines]" {
		t.Errorf("paste limbo = %+v", got.Panes[2].Dialog)
	}
	// It reads the visible screens only, and types nothing.
	for _, r := range s.Requests() {
		var p struct {
			Source string `json:"source"`
		}
		_ = json.Unmarshal(r.Params, &p)
		switch {
		case r.Method == "pane.read" && p.Source != "visible":
			t.Errorf("pane.read source %q", p.Source)
		case strings.Contains(r.Method, "send") || strings.Contains(r.Method, "prompt"):
			t.Errorf("--robot-dialogs called %s", r.Method)
		}
	}
	// --panes narrows the panes; a selector that repeats a pane gives it
	// once, and in an empty session a pane without a slot keeps its id.
	code, out, _ = run("--robot-dialogs=swarm", "--panes=0.2,w1:p1,0.1", "--socket", s.SocketPath)
	got = robot.DialogsOutput{}
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || len(got.Panes) != 2 || got.Panes[0].Pane != "0.1" || got.Panes[1].Pane != "0.2" {
		t.Errorf("--panes=0.2,w1:p1,0.1 = %d %s", code, out)
	}
}

// The options of each menu form: a cursor menu with its footer, and a
// hotkey menu, which with an accept and a decline answer is an unknown
// dialog.
func TestRobotDialogsOptions(t *testing.T) {
	s, _, _ := paneServer(t, func(pane string) string {
		switch pane {
		case "w1:p1":
			return " Is this a project you trust?\n ❯ No, exit\n   Yes, I trust this folder\n Enter to confirm · Esc to cancel\n"
		case "w1:p2":
			return "Apply the change to this directory?\nYes, proceed                 y\nNo, quit                     n\n"
		}
		return ""
	})
	code, out, _ := run("--robot-dialogs=swarm", "--panes=0.1,0.2", "--socket", s.SocketPath)
	var got robot.DialogsOutput
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || len(got.Panes) != 2 {
		t.Fatalf("--robot-dialogs = %d %s", code, out)
	}
	cursor, hotkey := got.Panes[0].Dialog, got.Panes[1].Dialog
	if cursor.Class != dialogs.TrustPrompt || cursor.InputMode != "cursor" || len(cursor.Options) != 2 ||
		!cursor.Options[0].Selected || cursor.Options[1].Number != 2 || cursor.Options[1].Label != "Yes, I trust this folder" {
		t.Errorf("cursor menu = %+v", cursor)
	}
	if hotkey.Class != dialogs.Unknown || hotkey.InputMode != "hotkey" || len(hotkey.Options) != 2 ||
		hotkey.Options[0].Key != "y" || hotkey.Options[1].Key != "n" || hotkey.Options[1].Label != "No, quit" {
		t.Errorf("hotkey menu = %+v", hotkey)
	}
	// ntm's JSON: an option's key only when the menu shows one.
	if !strings.Contains(out, `"key": "y"`) || strings.Count(out, `"key"`) != 2 || !strings.Contains(out, `"input_mode": "cursor"`) {
		t.Errorf("JSON = %s", out)
	}
}

// A pane whose screen herdr does not give is an unknown dialog with the
// error as its evidence; the other panes are still classified.
func TestRobotDialogsCaptureFails(t *testing.T) {
	s, _, _ := paneServer(t, dialogScreens)
	s.Handle("pane.read", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		if p.PaneID == "w1:p2" {
			return nil, &herdrtest.Error{Code: "pane_not_found", Message: "no pane"}
		}
		return map[string]any{"type": "pane_read", "read": map[string]any{"pane_id": p.PaneID, "tab_id": "w1:t1",
			"workspace_id": "w1", "source": "visible", "format": "text", "revision": 3, "text": dialogScreens(p.PaneID), "truncated": false}}, nil
	})
	code, out, _ := run("--robot-dialogs=swarm", "--socket", s.SocketPath)
	var got robot.DialogsOutput
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || !got.Success || len(got.Panes) != 3 {
		t.Fatalf("--robot-dialogs = %d %s", code, out)
	}
	if d := got.Panes[2].Dialog; d.Class != dialogs.Unknown || !strings.HasPrefix(d.Evidence, "capture failed: ") || !strings.Contains(d.Evidence, "pane_not_found") {
		t.Errorf("failed capture = %+v", d)
	}
	if got.Panes[1].Dialog.Class != dialogs.TrustPrompt {
		t.Errorf("the other panes = %+v", got.Panes)
	}
}

// ntm's errors, each with exit 1: no session, a session herdr does not
// have, a selector that names no pane or a bad one, and a pane without an
// agent.
func TestRobotDialogsErrors(t *testing.T) {
	s, _, _ := paneServer(t, dialogScreens)
	for _, c := range []struct {
		args      []string
		code, msg string
	}{
		{[]string{"--robot-dialogs="}, robot.ErrCodeInvalidFlag, "--robot-dialogs requires a session name"},
		{[]string{"--robot-dialogs=nosuch"}, robot.ErrCodeSessionNotFound, "session 'nosuch' not found"},
		{[]string{"--robot-dialogs=swarm", "--panes=9"}, robot.ErrCodeInvalidFlag, `pane selector "9" not found; available: w1:p3 (w1:p3), w1:p4 (w1:p4), 0.1 (w1:p1), 0.2 (w1:p2)`},
		{[]string{"--robot-dialogs=swarm", "--panes=0.1,"}, robot.ErrCodeInvalidFlag, `invalid pane selector "": expected N, W.P, or a herdr pane id`},
		{[]string{"--robot-dialogs=swarm", "--panes=%3"}, robot.ErrCodeInvalidFlag, "herdr has no tmux pane ids"},
		{[]string{"--robot-dialogs=swarm", "--panes=0.1,w1:p3"}, robot.ErrCodeInvalidFlag, "pane w1:p3 is not an agent pane (type user)"},
	} {
		code, out, _ := run(append(c.args, "--socket", s.SocketPath)...)
		var got robot.DialogsOutput
		if code != 1 || json.Unmarshal([]byte(out), &got) != nil || got.Success || got.ErrorCode != c.code ||
			!strings.Contains(got.Error, c.msg) || got.OutputFormat != "json" {
			t.Errorf("%v = %d %s", c.args, code, out)
		}
	}
	// A session with no agent pane has nothing to classify.
	s2, _ := cliInstance(t)
	s2.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		snap := paneSnapshot()
		snap["panes"] = []any{testPane("w1:p3", "w1:t2", "", "", "user@host:/p")}
		return map[string]any{"type": "session_snapshot", "snapshot": snap}, nil
	})
	code, out, _ := run("--robot-dialogs=swarm", "--socket", s2.SocketPath)
	var got robot.DialogsOutput
	if code != 1 || json.Unmarshal([]byte(out), &got) != nil || got.Error != "no agent panes matched" || got.ErrorCode != robot.ErrCodeInvalidFlag ||
		got.Session != "swarm" || got.Panes == nil {
		t.Errorf("no agent pane = %d %s", code, out)
	}
}
