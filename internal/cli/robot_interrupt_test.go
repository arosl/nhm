package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

// interruptKeys makes an interrupt stop the agent (it goes idle) and
// records the keys each pane got.
func interruptKeys(s *herdrtest.Server, h *sendHerdr) func() []string {
	var keys []string
	s.Handle("agent.send_keys", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Target string   `json:"target"`
			Keys   []string `json:"keys"`
		}
		_ = json.Unmarshal(r.Params, &p)
		h.mu.Lock()
		defer h.mu.Unlock()
		keys = append(keys, p.Target+"="+strings.Join(p.Keys, "+"))
		h.status[p.Target] = "idle"
		h.seq[p.Target]++
		return map[string]any{"type": "ok"}, nil
	})
	return func() []string {
		h.mu.Lock()
		defer h.mu.Unlock()
		return append([]string(nil), keys...)
	}
}

func strs(v any) []string {
	var out []string
	for _, s := range v.([]any) {
		out = append(out, s.(string))
	}
	return out
}

// ru's call (nhm-hir): `--robot-interrupt=S`. The working agent gets its
// key and is waited for; the idle one gets none and is ready at once.
func TestRobotInterruptRuForm(t *testing.T) {
	s, inst, h := newSendInstance(t)
	h.status["w1:p1"] = "working"
	keys := interruptKeys(s, h)
	t.Setenv("HERDR_PANE_ID", "")
	out, code := robotJSON(t, "--robot-interrupt=swarm", "--socket", s.SocketPath)
	if code != 1 || out["error_code"] != "NOT_OWNER" {
		t.Fatalf("under the Bash = %d %v", code, out)
	}
	ownNhm(t, inst)
	out, code = robotJSON(t, "--robot-interrupt=swarm", "--socket", s.SocketPath)
	prev := out["previous_states"].(map[string]any)
	if code != 0 || out["success"] != true || out["method"] != "ctrl_c" || out["timed_out"] != false || out["timeout_ms"] != 10000.0 ||
		strings.Join(strs(out["interrupted"]), ",") != "0.1" || strings.Join(strs(out["ready_for_input"]), ",") != "1.1,0.1" ||
		len(out["failed"].([]any)) != 0 || prev["0.1"].(map[string]any)["state"] != "active" || prev["1.1"].(map[string]any)["state"] != "idle" {
		t.Fatalf("interrupt = %d %v", code, out)
	}
	if got := keys(); len(got) != 1 || got[0] != "w1:p1=esc" {
		t.Fatalf("keys = %v", got)
	}
}

// --force sends every agent its key; --no-wait reports them ready at once;
// a dry run sends nothing and names every target.
func TestRobotInterruptForceNoWaitDryRun(t *testing.T) {
	s, inst, h := newSendInstance(t)
	ownNhm(t, inst)
	keys := interruptKeys(s, h)
	t.Setenv("HERDR_PANE_ID", "")
	out, code := robotJSON(t, "--robot-interrupt=swarm", "--dry-run", "--socket", s.SocketPath)
	if code != 0 || out["dry_run"] != true || strings.Join(strs(out["would_affect"]), ",") != "0.1,1.1" || len(keys()) != 0 {
		t.Fatalf("dry run = %d %v, keys %v", code, out, keys())
	}
	out, code = robotJSON(t, "--robot-interrupt=swarm", "--interrupt-force", "--no-wait", "--socket", s.SocketPath)
	if code != 0 || strings.Join(strs(out["interrupted"]), ",") != "0.1,1.1" || len(strs(out["ready_for_input"])) != 2 || len(keys()) != 2 {
		t.Fatalf("force = %d %v, keys %v", code, out, keys())
	}
}

// An agent that does not stop in time: TIMEOUT, exit 1, and it is not
// ready. --panes picks it; the caller's pane is left out without --panes.
func TestRobotInterruptTimesOut(t *testing.T) {
	s, inst, h := newSendInstance(t)
	ownNhm(t, inst)
	h.status["w1:p1"], h.status["w1:p2"] = "working", "working"
	s.Handle("agent.send_keys", func(herdrtest.Request) (any, *herdrtest.Error) { return map[string]any{"type": "ok"}, nil })
	t.Setenv("HERDR_PANE_ID", "w1:p2")
	out, code := robotJSON(t, "--robot-interrupt=swarm", "--timeout=200ms", "--socket", s.SocketPath)
	if code != 1 || out["error_code"] != "TIMEOUT" || out["timed_out"] != true || len(out["ready_for_input"].([]any)) != 0 ||
		strings.Join(strs(out["interrupted"]), ",") != "0.1" {
		t.Fatalf("timeout = %d %v", code, out)
	}
	if out, code = robotJSON(t, "--robot-interrupt=swarm", "--panes=9.9", "--socket", s.SocketPath); code != 1 || out["error_code"] != "PANE_NOT_FOUND" {
		t.Fatalf("no pane = %d %v", code, out)
	}
}

// --msg goes to the agents at their prompt through the send core; a
// retry of the same --op-id sends neither a key nor the message again.
func TestRobotInterruptMessageAndReplay(t *testing.T) {
	s, inst, h := newSendInstance(t)
	ownNhm(t, inst)
	h.status["w1:p1"] = "working"
	keys := interruptKeys(s, h)
	t.Setenv("HERDR_PANE_ID", "")
	args := []string{"--robot-interrupt=swarm", "--msg=Stop and fix the bug", "--op-id=int-1", "--socket", s.SocketPath}
	out, code := robotJSON(t, args...)
	if code != 0 || out["method"] != "ctrl_c_then_send" || out["message_sent"] != true || out["operation"].(map[string]any)["operation_id"] != "int-1" {
		t.Fatalf("interrupt and send = %d %v", code, out)
	}
	if sent := h.sent(); len(sent) != 2 || !strings.HasSuffix(sent[0], "=Stop and fix the bug") {
		t.Fatalf("prompts = %v", sent)
	}
	out, code = robotJSON(t, args...)
	if code != 0 || out["operation"].(map[string]any)["replayed"] != true || out["message_sent"] != true || len(keys()) != 1 || len(h.sent()) != 2 {
		t.Fatalf("replay = %d %v, keys %v, prompts %v", code, out, keys(), h.sent())
	}
}
