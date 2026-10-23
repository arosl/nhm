package cli

import (
	"testing"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

// ru's `--robot-health=S` (nhm-hir) gives ntm's SessionHealthOutput: the
// bound idle agent is healthy, the blocked codex is blocked. A limit line
// on the screen now makes the idle agent rate_limited, as ntm's screen
// check does (nhm-8l6), also above a blank gap (nhm-1q7). ntm's bare form
// (project health) is NOT_IMPLEMENTED, exit 2; an empty session is ntm's
// INVALID_FLAG.
func TestRobotHealth(t *testing.T) {
	s := swarmInstance(t)
	env, code := robotRun(t, "--robot-health=swarm", "--socket", s.SocketPath)
	agents, _ := env.Raw["agents"].([]any)
	summary, _ := env.Raw["summary"].(map[string]any)
	if code != 0 || env.Raw["session"] != "swarm" || len(agents) != 2 || summary["healthy"] != 1.0 || summary["blocked"] != 1.0 {
		t.Fatalf("health = %d %v", code, env.Raw)
	}
	s.Handle("agent.read", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "pane_read", "read": map[string]any{"pane_id": "w1:p1", "tab_id": "w1:t1",
			"workspace_id": "w1", "source": "visible", "format": "text", "revision": 4,
			"text": "  ⎿  You've hit your session limit · resets 3pm (UTC)\n❯ \n", "truncated": false}}, nil
	})
	env, code = robotRun(t, "--robot-health=swarm", "--socket", s.SocketPath)
	agents, _ = env.Raw["agents"].([]any)
	if code != 0 || len(agents) != 2 || agents[0].(map[string]any)["health"] != "rate_limited" || agents[1].(map[string]any)["health"] != "blocked" {
		t.Fatalf("a limit on the screen = %d %v", code, env.Raw)
	}
	// A limit above a blank gap and the prompt box, out of the screen's
	// last 15 lines, is found too (nhm-1q7).
	s.Handle("agent.read", func(r herdrtest.Request) (any, *herdrtest.Error) {
		return visibleRead(r, shortScreen()), nil
	})
	env, code = robotRun(t, "--robot-health=swarm", "--socket", s.SocketPath)
	agents, _ = env.Raw["agents"].([]any)
	if code != 0 || len(agents) != 2 || agents[0].(map[string]any)["health"] != "rate_limited" {
		t.Fatalf("a limit above a blank gap = %d %v", code, env.Raw)
	}
	if env, code = robotRun(t, "--robot-health", "--socket", s.SocketPath); code != 2 || env.ErrorCode != "NOT_IMPLEMENTED" {
		t.Fatalf("bare = %d %+v", code, env)
	}
	if env, code = robotRun(t, "--robot-health=", "--socket", s.SocketPath); code != 1 || env.ErrorCode != "INVALID_FLAG" ||
		env.Error != "--robot-health requires a session name or no value for project health" {
		t.Fatalf("empty = %d %+v", code, env)
	}
	if env, code = robotRun(t, "--robot-health=nowhere", "--socket", s.SocketPath); code != 1 || env.ErrorCode != "SESSION_NOT_FOUND" {
		t.Fatalf("nowhere = %d %+v", code, env)
	}
}
