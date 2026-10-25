package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/integrations/kinds/dialogs"
	"github.com/arosl/nhm/internal/robot"
)

// dialogPane is a fake agent screen that keys change: onKey gets each
// key as herdr names it and returns the next screen.
type dialogPane struct {
	mu     sync.Mutex
	screen map[string]string
	keys   []string
	onKey  func(pane, key, screen string) string
}

// answerServer is paneServer with screens that answer keys: the panes'
// agents (agent.get) and agent.send_keys, one key per call.
func answerServer(t *testing.T, screens map[string]string, onKey func(pane, key, screen string) string) (*herdrtest.Server, *dialogPane) {
	t.Helper()
	d := &dialogPane{screen: screens, onKey: onKey}
	s, _, _ := paneServer(t, func(pane string) string {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.screen[pane]
	})
	kinds := map[string]string{"w1:p1": "claude", "w1:p2": "codex", "w1:p4": "claude"}
	s.Handle("agent.get", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal(r.Params, &p)
		return map[string]any{"type": "agent_info", "agent": map[string]any{"pane_id": p.Target, "tab_id": "w1:t1", "workspace_id": "w1",
			"terminal_id": "t-" + p.Target, "agent": kinds[p.Target], "agent_status": "blocked", "focused": false, "revision": 1, "state_change_seq": 4}}, nil
	})
	s.Handle("agent.send_keys", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Target string   `json:"target"`
			Keys   []string `json:"keys"`
		}
		_ = json.Unmarshal(r.Params, &p)
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, k := range p.Keys {
			d.keys = append(d.keys, p.Target+"="+k)
			if d.onKey != nil {
				d.screen[p.Target] = d.onKey(p.Target, k, d.screen[p.Target])
			}
		}
		return map[string]any{"type": "ok"}, nil
	})
	return s, d
}

func (d *dialogPane) sent() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.keys...)
}

const (
	answerTrust = " Quick safety check: Is this a project you created or one you trust?\n\n" +
		" ❯ 1. Yes, I trust this folder\n   2. No, exit\n\n Enter to confirm · Esc to cancel\n"
	answerCursor = " Quick safety check: Is this a project you created or one you trust?\n" +
		" ❯ No, exit\n   Yes, I trust this folder\n Enter to confirm · Esc to cancel\n"
	answerCursorOnYes = " Quick safety check: Is this a project you created or one you trust?\n" +
		"   No, exit\n ❯ Yes, I trust this folder\n Enter to confirm · Esc to cancel\n"
	answerDestructive = " Bash command\n\n rm -rf /tmp/scratch\n\n Do you want to proceed?\n" +
		" ❯ 1. Yes\n   2. Yes, and always allow access to /tmp from this project\n   3. No\n\n Esc to cancel · Tab to amend\n"
	answerIdle = " some transcript\n\n ❯ Try \"refactor <filepath>\"\n"
)

// Enter clears the dialog.
func clearOnEnter(_, key, screen string) string {
	if key == "enter" {
		return answerIdle
	}
	return screen
}

func answerOut(t *testing.T, out string) robot.AnswerDialogOutput {
	t.Helper()
	var got robot.AnswerDialogOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %s", out)
	}
	return got
}

// A numbered trust prompt: decline types 2 and Enter, one key per call
// under herdr's names, and the screen read after it shows no dialog.
func TestRobotAnswerDialogTrust(t *testing.T) {
	s, d := answerServer(t, map[string]string{"w1:p1": answerTrust}, clearOnEnter)
	code, out, _ := run("--robot-answer-dialog=swarm", "--panes=0.1", "--choice=decline", "--socket", s.SocketPath)
	got := answerOut(t, out)
	if code != 0 || !got.Success || !got.Resolved || got.Session != "swarm" || got.Pane != "0.1" || got.Target != "w1:p1" ||
		got.Choice != "decline" || got.Before.Class != dialogs.TrustPrompt || got.After.Class != dialogs.None ||
		!reflect.DeepEqual(got.KeysSent, []string{"2", "Enter"}) || got.FollowUpDialog != "" {
		t.Fatalf("--robot-answer-dialog = %d %s", code, out)
	}
	if k := d.sent(); !reflect.DeepEqual(k, []string{"w1:p1=2", "w1:p1=enter"}) {
		t.Errorf("herdr got %v", k)
	}
	// An agent that nhm did not bind (the loose Claude, w1:p4) is
	// answered too; dismiss is Escape.
	s, d = answerServer(t, map[string]string{"w1:p4": answerTrust}, func(_, key, screen string) string {
		if key == "esc" {
			return answerIdle
		}
		return screen
	})
	code, out, _ = run("--robot-answer-dialog=swarm", "--panes=w1:p4", "--choice=dismiss", "--socket", s.SocketPath)
	if got := answerOut(t, out); code != 0 || !got.Resolved || got.Pane != "w1:p4" || !reflect.DeepEqual(got.KeysSent, []string{"Escape"}) ||
		!reflect.DeepEqual(d.sent(), []string{"w1:p4=esc"}) {
		t.Errorf("dismiss on w1:p4 = %d %s", code, out)
	}
}

// A cursor menu: the keys move the cursor, and Enter goes only when the
// screen shows it on the chosen option. A dropped Down leaves Enter
// unsent.
func TestRobotAnswerDialogCursorVerifies(t *testing.T) {
	s, d := answerServer(t, map[string]string{"w1:p1": answerCursor}, func(_, key, screen string) string {
		switch key {
		case "down":
			return answerCursorOnYes
		case "enter":
			return answerIdle
		}
		return screen
	})
	code, out, _ := run("--robot-answer-dialog=swarm", "--panes=0.1", "--choice=option-2", "--socket", s.SocketPath)
	if got := answerOut(t, out); code != 0 || !got.Resolved || got.Before.InputMode != "cursor" ||
		!reflect.DeepEqual(got.KeysSent, []string{"Down", "Enter"}) || !reflect.DeepEqual(d.sent(), []string{"w1:p1=down", "w1:p1=enter"}) {
		t.Fatalf("option-2 = %d %s", code, out)
	}
	s, d = answerServer(t, map[string]string{"w1:p1": answerCursor}, nil)
	code, out, _ = run("--robot-answer-dialog=swarm", "--panes=0.1", "--choice=option-2", "--socket", s.SocketPath)
	got := answerOut(t, out)
	if code != 1 || got.Success || got.ErrorCode != robot.ErrCodeInternalError || !strings.Contains(got.Error, "requested option is not selected") ||
		got.Hint != "Enter was not sent; re-run --robot-dialogs before retrying" || !reflect.DeepEqual(got.KeysSent, []string{"Down"}) ||
		!reflect.DeepEqual(d.sent(), []string{"w1:p1=down"}) || got.After.Class != dialogs.TrustPrompt {
		t.Errorf("dropped Down = %d %s", code, out)
	}
}

// An accept on a destructive confirm is refused before any key; decline
// picks its "No".
func TestRobotAnswerDialogRefusesDestructiveAccept(t *testing.T) {
	s, d := answerServer(t, map[string]string{"w1:p1": answerDestructive}, clearOnEnter)
	for _, choice := range []string{"option-1", "option-2"} {
		code, out, _ := run("--robot-answer-dialog=swarm", "--panes=0.1", "--choice="+choice, "--socket", s.SocketPath)
		got := answerOut(t, out)
		if code != 1 || got.ErrorCode != robot.ErrCodeInvalidFlag || !strings.HasPrefix(got.Error, "POLICY_REFUSED: ") ||
			got.Before.Class != dialogs.DestructiveConfirm || len(got.KeysSent) != 0 {
			t.Errorf("%s = %d %s", choice, code, out)
		}
	}
	if k := d.sent(); len(k) != 0 {
		t.Fatalf("keys sent: %v", k)
	}
	code, out, _ := run("--robot-answer-dialog=swarm", "--panes=0.1", "--choice=decline", "--socket", s.SocketPath)
	if got := answerOut(t, out); code != 0 || !got.Resolved || !reflect.DeepEqual(got.KeysSent, []string{"3", "Enter"}) {
		t.Errorf("decline = %d %s", code, out)
	}
}

// --panes must name exactly one agent pane, and the pane must show a
// dialog the choice fits.
func TestRobotAnswerDialogNeedsOnePane(t *testing.T) {
	s, d := answerServer(t, map[string]string{"w1:p1": answerTrust, "w1:p2": answerIdle}, nil)
	for _, c := range []struct {
		args      []string
		code, msg string
	}{
		{[]string{"--robot-answer-dialog=", "--panes=0.1", "--choice=decline"}, robot.ErrCodeInvalidFlag, "--robot-answer-dialog requires a session name"},
		{[]string{"--robot-answer-dialog=swarm", "--choice=decline"}, robot.ErrCodeInvalidFlag, "--panes is required"},
		{[]string{"--robot-answer-dialog=swarm", "--panes=0.1,0.2", "--choice=decline"}, robot.ErrCodeInvalidFlag, "requires exactly one target pane, resolved 2"},
		{[]string{"--robot-answer-dialog=swarm", "--panes=w1:p3", "--choice=decline"}, robot.ErrCodeInvalidFlag, "pane w1:p3 is not an agent pane (type user)"},
		{[]string{"--robot-answer-dialog=nosuch", "--panes=0.1", "--choice=decline"}, robot.ErrCodeSessionNotFound, "session 'nosuch' not found"},
		{[]string{"--robot-answer-dialog=swarm", "--panes=0.2", "--choice=decline"}, robot.ErrCodeInvalidFlag, "no dialog detected on the pane"},
		{[]string{"--robot-answer-dialog=swarm", "--panes=0.1", "--choice=approve"}, robot.ErrCodeInvalidFlag, `unknown choice "approve"`},
		{[]string{"--robot-answer-dialog=swarm", "--panes=0.1", "--choice=extra-usage"}, robot.ErrCodeInvalidFlag, "extra-usage only applies to rate_limit_options dialogs (found trust_prompt)"},
		{[]string{"--robot-answer-dialog=swarm", "--panes=0.1", "--choice=option-7"}, robot.ErrCodeInvalidFlag, "dialog has no option 7"},
	} {
		code, out, _ := run(append(c.args, "--socket", s.SocketPath)...)
		var got robot.AnswerDialogOutput
		if code != 1 || json.Unmarshal([]byte(out), &got) != nil || got.Success || got.ErrorCode != c.code || !strings.Contains(got.Error, c.msg) {
			t.Errorf("%v = %d %s", c.args, code, out)
		}
	}
	if k := d.sent(); len(k) != 0 {
		t.Fatalf("keys sent: %v", k)
	}
}

// An answer that leaves the dialog, or brings another, is not resolved.
func TestRobotAnswerDialogNotResolved(t *testing.T) {
	s, _ := answerServer(t, map[string]string{"w1:p1": answerTrust}, nil)
	code, out, _ := run("--robot-answer-dialog=swarm", "--panes=0.1", "--choice=decline", "--socket", s.SocketPath)
	if got := answerOut(t, out); code != 1 || got.Resolved || got.Error != "dialog still present after the answer" ||
		got.ErrorCode != robot.ErrCodeInternalError || got.FollowUpDialog != "" || len(got.KeysSent) != 2 {
		t.Errorf("still present = %d %s", code, out)
	}
	s, _ = answerServer(t, map[string]string{"w1:p1": answerTrust}, func(_, key, screen string) string {
		if key == "enter" {
			return answerDestructive
		}
		return screen
	})
	code, out, _ = run("--robot-answer-dialog=swarm", "--panes=0.1", "--choice=decline", "--socket", s.SocketPath)
	if got := answerOut(t, out); code != 1 || got.Resolved || got.FollowUpDialog != dialogs.DestructiveConfirm ||
		!strings.Contains(got.Error, "follow-up dialog (destructive_confirm)") {
		t.Errorf("follow-up = %d %s", code, out)
	}
}
