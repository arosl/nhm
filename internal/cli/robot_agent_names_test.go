package cli

import (
	"encoding/json"
	"testing"

	"github.com/arosl/nhm/internal/robot"
)

func agentNames(t *testing.T, args ...string) (int, robot.AgentNamesOutput, string) {
	t.Helper()
	code, out, _ := run(args...)
	var got robot.AgentNamesOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: not JSON: %v\n%s", args, err, out)
	}
	return code, got, out
}

func namesOf(es []robot.AgentNameEntry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Name+"@"+e.Pane+"/"+e.AgentType)
	}
	return out
}

// One NATO count over every pane of the workspace, in ntm's pane order:
// the shell is user-alpha, then the agents; the list is sorted by pane
// reference (the slot, or the pane id of a pane without one).
func TestRobotAgentNamesNato(t *testing.T) {
	s, _, _ := paneServer(t, copyText)
	code, got, raw := agentNames(t, "--robot-agent-names=swarm", "--socket", s.SocketPath)
	want := []string{"claude-charlie@0.1/claude", "codex-delta@0.2/codex", "user-alpha@w1:p3/user", "claude-bravo@w1:p4/claude"}
	if code != 0 || !got.Success || got.Session != "swarm" || got.Count != 4 || fmtList(namesOf(got.Agents)) != fmtList(want) {
		t.Fatalf("agent names = %d %+v\n%s", code, got, raw)
	}
	// The session by its workspace id, as given.
	if code, got, _ := agentNames(t, "--robot-agent-names=w1", "--socket", s.SocketPath); code != 0 || got.Session != "w1" || got.Count != 4 {
		t.Errorf("by id = %d %+v", code, got)
	}
}

// --spawn-names names the agents first, in pane order; the user pane and
// the agents past the list take NATO names.
func TestRobotAgentNamesCustomNames(t *testing.T) {
	s, _, _ := paneServer(t, copyText)
	code, got, _ := agentNames(t, "--robot-agent-names=swarm", "--spawn-names=alice, bob", "--socket", s.SocketPath)
	want := []string{"bob@0.1/claude", "codex-bravo@0.2/codex", "user-alpha@w1:p3/user", "alice@w1:p4/claude"}
	if code != 0 || fmtList(namesOf(got.Agents)) != fmtList(want) {
		t.Errorf("custom names = %d %v", code, namesOf(got.Agents))
	}
}

// A session herdr does not have, by its exact name: ntm's
// SESSION_NOT_FOUND, exit 1, with an empty agents list.
func TestRobotAgentNamesSessionNotFound(t *testing.T) {
	s, _, _ := paneServer(t, copyText)
	for _, name := range []string{"nosuch", "swar"} {
		code, got, raw := agentNames(t, "--robot-agent-names="+name, "--socket", s.SocketPath)
		if code != 1 || got.Success || got.ErrorCode != robot.ErrCodeSessionNotFound || got.Error != "session '"+name+"' not found" ||
			got.Session != name || got.Agents == nil || got.Count != 0 {
			t.Errorf("%s = %d %s", name, code, raw)
		}
	}
}

func fmtList(xs []string) string {
	b, _ := json.Marshal(xs)
	return string(b)
}
