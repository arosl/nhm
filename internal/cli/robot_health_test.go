package cli

import (
	"testing"
)

// ru's `--robot-health=S` (nhm-hir) gives ntm's SessionHealthOutput: the
// bound idle agent is healthy, the blocked codex is blocked. ntm's bare
// form (project health) is NOT_IMPLEMENTED, exit 2.
func TestRobotHealth(t *testing.T) {
	s := swarmInstance(t)
	env, code := robotRun(t, "--robot-health=swarm", "--socket", s.SocketPath)
	agents, _ := env.Raw["agents"].([]any)
	summary, _ := env.Raw["summary"].(map[string]any)
	if code != 0 || env.Raw["session"] != "swarm" || len(agents) != 2 || summary["healthy"] != 1.0 || summary["blocked"] != 1.0 {
		t.Fatalf("health = %d %v", code, env.Raw)
	}
	if env, code = robotRun(t, "--robot-health", "--socket", s.SocketPath); code != 2 || env.ErrorCode != "NOT_IMPLEMENTED" {
		t.Fatalf("bare = %d %+v", code, env)
	}
	if env, code = robotRun(t, "--robot-health=nowhere", "--socket", s.SocketPath); code != 1 || env.ErrorCode != "SESSION_NOT_FOUND" {
		t.Fatalf("nowhere = %d %+v", code, env)
	}
}
