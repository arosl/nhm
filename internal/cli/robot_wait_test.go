package cli

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

// reads plays each pane's agent reads in order; the last one repeats.
// A nil read is a gone agent.
type reads struct {
	mu   sync.Mutex
	seen map[string]int
	by   map[string][]map[string]any
}

func (r *reads) agent(pane string) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	steps := r.by[pane]
	i := min(r.seen[pane], len(steps)-1)
	r.seen[pane]++
	return steps[i]
}

func robotWaitRun(t *testing.T, args ...string) (int, map[string]any) {
	t.Helper()
	code, out, errOut := run(args...)
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%v: %d %q %q", args, code, out, errOut)
	}
	return code, v
}

// ru's call (nhm-hir): `--robot-wait=S --condition=idle --wait-timeout
// --exit-on-error --transition`. IcyKnoll is idle at the start, so it
// must work and come back; BlueLake works at the start and only has to
// stop. Done counts as idle, as ntm's WAITING.
func TestRobotWaitRuForm(t *testing.T) {
	r := &reads{seen: map[string]int{}, by: map[string][]map[string]any{
		"w1:p1": {agentMap("w1:p1", "claude", "idle", 4, 4), agentMap("w1:p1", "claude", "idle", 4, 4),
			agentMap("w1:p1", "claude", "working", 5, 4), agentMap("w1:p1", "claude", "idle", 6, 6)},
		"w1:p2": {agentMap("w1:p2", "codex", "working", 7, 0), agentMap("w1:p2", "codex", "done", 8, 8)},
	}}
	s, _, _ := waitInstance(t, r.agent)
	code, v := robotWaitRun(t, "--robot-wait=swarm", "--condition=idle", "--wait-timeout=5s", "--exit-on-error", "--transition",
		"--poll=5ms", "--socket", s.SocketPath)
	agents, _ := v["agents"].([]any)
	if code != 0 || v["success"] != true || v["session"] != "swarm" || v["condition"] != "idle" || len(agents) != 2 {
		t.Fatalf("ru's wait = %d %v", code, v)
	}
	icy := agents[0].(map[string]any)
	if icy["pane"] != "0.1" || icy["state"] != "WAITING" || icy["agent_type"] != "claude" || icy["met_at"] == "" {
		t.Fatalf("agent = %v", icy)
	}
	if _, ok := v["_nhm"].(map[string]any)["targets"]; !ok {
		t.Fatalf("no _nhm targets: %v", v)
	}
}

// An agent that never leaves the target state times out: TIMEOUT, exit 1,
// the pending pane named and agents an empty array.
func TestRobotWaitTransitionTimesOut(t *testing.T) {
	s, _, _ := waitInstance(t, func(pane string) map[string]any { return agentMap(pane, "claude", "idle", 4, 4) })
	code, v := robotWaitRun(t, "--robot-wait=swarm", "--panes=0.1", "--transition", "--timeout=100ms", "--poll=5ms", "--socket", s.SocketPath)
	agents, isArray := v["agents"].([]any)
	pending, _ := v["agents_pending"].([]any)
	if code != 1 || v["error_code"] != "TIMEOUT" || !isArray || len(agents) != 0 || len(pending) != 1 || pending[0] != "0.1" ||
		v["_meta"].(map[string]any)["exit_code"] != 1.0 {
		t.Fatalf("timeout = %d %v", code, v)
	}
}

// --exit-on-error: a gone agent ends the wait at once with AGENT_ERROR;
// the other agent, still working, does not hold it to the deadline.
func TestRobotWaitExitOnError(t *testing.T) {
	s, _, _ := waitInstance(t, func(pane string) map[string]any {
		if pane == "w1:p2" {
			return nil
		}
		return agentMap(pane, "claude", "working", 4, 0)
	})
	start := time.Now()
	code, v := robotWaitRun(t, "--robot-wait=swarm", "--exit-on-error", "--timeout=10s", "--poll=5ms", "--socket", s.SocketPath)
	agents, _ := v["agents"].([]any)
	if code != 1 || v["error_code"] != "AGENT_ERROR" || len(agents) != 1 || agents[0].(map[string]any)["pane"] != "1.1" ||
		!strings.Contains(v["error"].(string), "continuity_lost") {
		t.Fatalf("exit on error = %d %v", code, v)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("exit on error waited %s", d)
	}
}

// What nhm does not answer is NOT_IMPLEMENTED with exit 2; a bad flag,
// session or pane is exit 1 with ntm's code.
func TestRobotWaitRefusals(t *testing.T) {
	s, _, _ := waitInstance(t, func(pane string) map[string]any { return agentMap(pane, "claude", "idle", 4, 4) })
	s.Handle("agent.wait", func(herdrtest.Request) (any, *herdrtest.Error) {
		return nil, &herdrtest.Error{Code: "timeout", Message: "timed out"}
	})
	for _, c := range []struct {
		args []string
		code int
		ec   string
	}{
		{[]string{"--condition=complete"}, 2, "NOT_IMPLEMENTED"},
		{[]string{"--wait-until=idle,healthy"}, 2, "NOT_IMPLEMENTED"},
		{[]string{"--condition=finished"}, 1, "INVALID_FLAG"},
		{[]string{"--wait-timeout=0s"}, 1, "INVALID_FLAG"},
		{[]string{"--poll=soon"}, 1, "INVALID_FLAG"},
		{[]string{"--panes=9.9"}, 1, "PANE_NOT_FOUND"},
		{[]string{"--type=grok"}, 1, "PANE_NOT_FOUND"},
	} {
		args := append([]string{"--robot-wait=swarm", "--socket", s.SocketPath}, c.args...)
		if code, v := robotWaitRun(t, args...); code != c.code || v["error_code"] != c.ec {
			t.Errorf("%v = %d %v, want %d %s", c.args, code, v, c.code, c.ec)
		}
	}
	if code, v := robotWaitRun(t, "--robot-wait=nowhere", "--socket", s.SocketPath); code != 1 || v["error_code"] != "SESSION_NOT_FOUND" {
		t.Errorf("nowhere = %d %v", code, v)
	}
	if code, v := robotWaitRun(t, "--robot-wait=", "--socket", s.SocketPath); code != 1 || v["error_code"] != "INVALID_FLAG" {
		t.Errorf("no session = %d %v", code, v)
	}
	// Without --transition an idle agent meets idle at once.
	if code, v := robotWaitRun(t, "--robot-wait=swarm", "--wait-panes=w1:p1", "--socket", s.SocketPath); code != 0 || v["success"] != true {
		t.Errorf("idle = %d %v", code, v)
	}
}
