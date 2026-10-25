package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/runner"
)

// nhm send refuses a prompt that Claude Code or Codex would run as a
// shell command, before anything is claimed or typed, a dry run too, and
// says why (nhm-ubi): ntm's error for a send that its guard refused, exit
// 1; under --json ntm's SendResult with the error. A "!" on a later line
// goes out as typed.
func TestSendRefusesAShellEscape(t *testing.T) {
	s, inst, h := newSendInstance(t)
	ownNhm(t, inst)
	sock := s.SocketPath
	for _, args := range [][]string{{"!ls"}, {"  !ls"}, {"!ls", "--dry-run"}, {"--msg", "hi\x1b[201~\r!ls"}} {
		code, out, errOut := run(append([]string{"send", "swarm", "--socket", sock}, args...)...)
		if code != exitError || !strings.Contains(errOut, "refused before any prompt") || !strings.Contains(errOut, "IcyKnoll (claude), BlueLake (codex)") {
			t.Fatalf("%q = %d %q %q", args, code, out, errOut)
		}
	}
	code, out, _ := run("send", "swarm", "!ls", "--socket", sock, "--json")
	if r := decodeSendResult(t, out); code != exitError || r.Success || !strings.Contains(r.Error, "refused before any prompt") {
		t.Fatalf("--json = %d %s", code, out)
	}
	if _, _, errOut := run("send", "swarm", "!ls", "--socket", sock); !strings.Contains(errOut, `starts with "!"`) ||
		!strings.Contains(errOut, "no guard such as dcg screens it") {
		t.Fatalf("the refusal says no why: %s", errOut)
	}
	if len(h.sent()) != 0 {
		t.Fatalf("prompts = %v", h.sent())
	}
	if code, out, errOut := run("send", "swarm", "hello\n!ls", "--cc", "--socket", sock); code != 0 || len(h.sent()) != 1 {
		t.Fatalf("a later line = %d %s %s", code, out, errOut)
	}
	// --msg-file is the prompt as the file holds it.
	file := t.TempDir() + "/prompt.md"
	if err := os.WriteFile(file, []byte("!make deploy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := run("send", "swarm", "--msg-file", file, "--socket", sock); code != exitError || !strings.Contains(errOut, `starts with "!"`) ||
		len(h.sent()) != 1 {
		t.Fatalf("--msg-file = %d %s", code, errOut)
	}
}

// --robot-send answers ntm's shape for a send that its guard refused:
// blocked, DESTRUCTIVE_COMMAND_BLOCKED, a "guard" failure, the selected
// targets and the preview, exit 1.
func TestRobotSendRefusesAShellEscape(t *testing.T) {
	s, inst, h := newSendInstance(t)
	ownNhm(t, inst)
	sock := "--socket=" + s.SocketPath
	cases := []struct {
		args    []string
		targets string
	}{
		{[]string{"--msg=!ls", "--op-id=esc-1"}, `["0.1","1.1"]`},
		{[]string{"--msg=!ls", "--dry-run"}, `["0.1","1.1"]`},
		{[]string{"--msg=\t!ls", "--type=codex"}, `["1.1"]`},
	}
	for _, c := range cases {
		out, code := robotJSON(t, append([]string{"--robot-send=swarm", sock}, c.args...)...)
		failed, _ := out["failed"].([]any)
		first, _ := failed[0].(map[string]any)
		targets, _ := json.Marshal(out["targets"])
		if code != 1 || out["success"] != false || out["blocked"] != true || out["error_code"] != "DESTRUCTIVE_COMMAND_BLOCKED" ||
			len(failed) != 1 || first["pane"] != "guard" || !strings.Contains(out["error"].(string), "BlueLake (codex)") ||
			out["hint"] == "" || string(targets) != c.targets || !strings.Contains(out["message_preview"].(string), "!ls") {
			t.Fatalf("%q = %d %v", c.args, code, out)
		}
	}
	if len(h.sent()) != 0 {
		t.Fatalf("prompts = %v", h.sent())
	}
	if receipt, code := robotJSON(t, "--robot-send-receipt=esc-1", sock); code != 1 || receipt["error_code"] != "NOT_FOUND" {
		t.Fatalf("the refused send claimed its id: %d %v", code, receipt)
	}
}

// --robot-interrupt refuses such a --msg before any agent gets its key,
// in ntm's shape: its interrupt answer with a "guard" failure.
func TestRobotInterruptRefusesAShellEscape(t *testing.T) {
	s, inst, h := newSendInstance(t)
	ownNhm(t, inst)
	h.status["w1:p1"] = "working"
	keys := interruptKeys(s, h)
	t.Setenv("HERDR_PANE_ID", "")
	for _, msg := range []string{"--msg=!ls", "--msg=stop\x1b[201~\r!ls"} {
		out, code := robotJSON(t, "--robot-interrupt=swarm", msg, "--op-id=int-esc", "--socket", s.SocketPath)
		failed, _ := out["failed"].([]any)
		first, _ := failed[0].(map[string]any)
		if code != 1 || out["success"] != false || out["error_code"] != "DESTRUCTIVE_COMMAND_BLOCKED" || len(failed) != 1 ||
			first["pane"] != "guard" || !strings.Contains(first["reason"].(string), "IcyKnoll (claude)") ||
			out["method"] != "ctrl_c_then_send" || len(out["interrupted"].([]any)) != 0 {
			t.Fatalf("%q = %d %v", msg, code, out)
		}
	}
	if len(keys()) != 0 || len(h.sent()) != 0 {
		t.Fatalf("keys %v, prompts %v", keys(), h.sent())
	}
}

// The palette's and the menu's send: a dry run in the surface and the
// job of a real send both reach core send, which refuses the prompt and
// claims nothing.
func TestUISendRefusesAShellEscape(t *testing.T) {
	s, inst, h := newSendInstance(t)
	ownNhm(t, inst)
	ie, err := openInstance(s.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ie.db.Close()
	ctx := context.Background()
	req, err := uiSendBase(ctx, ie, ui.Context{SocketPath: s.SocketPath, WorkspaceID: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	req.Message, req.Selector, req.WhenAny, req.DryRun = "!ls", send.Selector{Panes: []string{"w1:p1"}}, true, true
	if _, err := uiCore(ie, ui.Context{SocketPath: s.SocketPath, WorkspaceID: "w1"}, runner.Client{}).Send(ctx, req); !errors.Is(err, send.ErrShellEscape) {
		t.Fatalf("dry run = %v", err)
	}
	req.DryRun = false
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	_, code, err := runSendJob(ctx, ie, runner.Job{ID: "op-ui-esc", Verb: runner.VerbSend, Request: raw})
	if code != exitRefused || !errors.Is(err, send.ErrShellEscape) || !strings.Contains(err.Error(), "IcyKnoll (claude)") {
		t.Fatalf("job = %d %v", code, err)
	}
	if _, err := ie.db.Operation(ctx, "op-ui-esc"); err == nil || len(h.sent()) != 0 {
		t.Fatalf("the job claimed or sent: %v, %v", err, h.sent())
	}
}
