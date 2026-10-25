package cli

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

var ansiColour = regexp.MustCompile("\x1b\\[[0-9;]*m")

// plain drops ntm's colour codes.
func plain(s string) string { return ansiColour.ReplaceAllString(s, "") }

// ntmWaitInstance is waitInstance (IcyKnoll, claude, slot 0.1 in w1:p1;
// BlueLake, codex, slot 1.1 in w1:p2) with states that a test can change,
// and herdr's agent.wait that sleeps no longer than its poll.
func ntmWaitInstance(t *testing.T, icy, blue string) (*herdrtest.Server, func(icy, blue string)) {
	t.Helper()
	var mu sync.Mutex
	s, _, _ := waitInstance(t, func(pane string) map[string]any {
		mu.Lock()
		defer mu.Unlock()
		if pane == "w1:p1" {
			return agentMap(pane, "claude", icy, 4, 4)
		}
		return agentMap(pane, "codex", blue, 7, 0)
	})
	s.Handle("agent.wait", func(herdrtest.Request) (any, *herdrtest.Error) {
		return nil, &herdrtest.Error{Code: "timeout", Message: "timed out"}
	})
	t.Setenv("HERDR_SOCKET_PATH", s.SocketPath)
	return s, func(i, b string) {
		mu.Lock()
		icy, blue = i, b
		mu.Unlock()
	}
}

// TestWaitNTMText: ntm's lines, with its colour codes, a line per agent
// that met the condition (its herdr pane and ntm's state), and the text
// under --json too, which ntm's wait has no other form of.
func TestWaitNTMText(t *testing.T) {
	_, _ = ntmWaitInstance(t, "idle", "done")
	for _, args := range [][]string{{"swarm"}, {"swarm", "--json"}} {
		code, out, errOut := runVerb(verbRun("wait"), args...)
		lines := strings.Split(strings.TrimRight(plain(out), "\n"), "\n")
		if code != exitOK || errOut != "" || len(lines) != 4 || !strings.Contains(out, "\x1b[38;2;") ||
			lines[0] != "⏳ Waiting for 'swarm' until idle (timeout: 5m0s)..." ||
			!strings.HasPrefix(lines[1], "✓ Condition 'idle' met after ") ||
			lines[2] != "    w1:p1: WAITING" || lines[3] != "    w1:p2: WAITING" {
			t.Fatalf("%v = %d %q %q", args, code, plain(out), errOut)
		}
	}
}

// TestWaitCompleteMeetsIdle: ntm's complete is an agent at its prompt
// with no recent output; herdr's idle or done comes at the end of the
// turn, so an agent at its prompt meets it at once.
func TestWaitCompleteMeetsIdle(t *testing.T) {
	_, _ = ntmWaitInstance(t, "done", "idle")
	code, out, _ := runVerb(verbRun("wait"), "swarm", "--until", "complete", "--timeout", "2s")
	if code != exitOK || !strings.Contains(plain(out), "✓ Condition 'complete' met after ") {
		t.Fatalf("complete = %d %q", code, plain(out))
	}
}

// TestWaitComposedConditions: a comma list must all hold; a list no state
// meets times out; a bad name is ntm's error.
func TestWaitComposedConditions(t *testing.T) {
	_, _ = ntmWaitInstance(t, "idle", "working")
	code, out, _ := runVerb(verbRun("wait"), "swarm", "--name", "IcyKnoll", "--until", "idle,healthy", "--timeout", "2s")
	if code != exitOK || !strings.Contains(plain(out), "✓ Condition 'idle,healthy' met") {
		t.Fatalf("idle,healthy = %d %q", code, plain(out))
	}
	code, out, errOut := runVerb(verbRun("wait"), "swarm", "--until", "idle,generating", "--timeout", "100ms", "--poll", "10ms")
	if code != exitError || !strings.HasSuffix(plain(out), "✗ Timeout after 100ms\n") || errOut != "Error: wait timed out after 100ms\n" {
		t.Fatalf("idle,generating = %d %q %q", code, plain(out), errOut)
	}
	code, _, errOut = runVerb(verbRun("wait"), "swarm", "--until", "idle,finished")
	if code != exitError || errOut != "Error: invalid condition 'idle,finished': must be one of idle, complete, generating, healthy\n" {
		t.Fatalf("idle,finished = %d %q", code, errOut)
	}
	// generating: BlueLake works; healthy: neither is blocked.
	if code, out, _ := runVerb(verbRun("wait"), "swarm", "--until", "healthy", "--timeout", "2s"); code != exitOK || !strings.Contains(plain(out), "w1:p2: GENERATING") {
		t.Fatalf("healthy = %d %q", code, plain(out))
	}
}

// TestWaitExitOnError: herdr has no error state; an agent that can no
// longer meet the condition (here a pane that now holds another kind)
// ends the wait at once with --exit-on-error. Without it, the agent drops
// out, as a closed or respawned pane drops out of ntm's poll: the others
// decide, and with none left no pane matches.
func TestWaitExitOnError(t *testing.T) {
	s, _ := ntmWaitInstance(t, "idle", "working")
	s.Handle("agent.get", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal(r.Params, &p)
		if p.Target == "w1:p1" {
			return map[string]any{"type": "agent_info", "agent": agentMap("w1:p1", "claude", "idle", 4, 4)}, nil
		}
		return map[string]any{"type": "agent_info", "agent": agentMap("w1:p2", "pi", "working", 7, 0)}, nil
	})
	code, out, errOut := runVerb(verbRun("wait"), "swarm", "--exit-on-error", "--timeout", "5s")
	if code != exitError || !strings.HasSuffix(plain(out), "✗ Agent error detected in w1:p2\n") ||
		errOut != "Error: agent in pane 'w1:p2' entered ERROR state\n" {
		t.Fatalf("--exit-on-error = %d %q %q", code, plain(out), errOut)
	}
	code, out, errOut = runVerb(verbRun("wait"), "swarm", "--timeout", "5s")
	if code != exitOK || !strings.Contains(plain(out), "✓ Condition 'idle' met after ") ||
		!strings.HasSuffix(plain(out), "    w1:p1: WAITING\n") || strings.Contains(plain(out), "w1:p2") {
		t.Fatalf("without = %d %q %q", code, plain(out), errOut)
	}
	code, out, errOut = runVerb(verbRun("wait"), "swarm", "--name", "BlueLake", "--timeout", "5s")
	if code != exitError || !strings.HasSuffix(plain(out), "! No matching panes found\n") || errOut != "Error: no panes match the filter criteria\n" {
		t.Fatalf("none left = %d %q %q", code, plain(out), errOut)
	}
}

// TestWaitEveryAgentInOnePoll: an agent that met the condition at one poll
// and left it at the next does not count; ntm needs every agent at once.
func TestWaitEveryAgentInOnePoll(t *testing.T) {
	var mu sync.Mutex
	reads := 0
	icy, blue := "idle", "working"
	s, _, _ := waitInstance(t, func(pane string) map[string]any {
		mu.Lock()
		defer mu.Unlock()
		if pane == "w1:p1" {
			reads++
			// IcyKnoll starts its turn late: idle at the first poll, then
			// at work while BlueLake ends its turn, then idle.
			switch reads {
			case 2:
				icy, blue = "working", "idle"
			case 4:
				icy = "idle"
			}
			return agentMap(pane, "claude", icy, 4, 4)
		}
		return agentMap(pane, "codex", blue, 7, 0)
	})
	code, out, _ := runVerb(verbRun("wait"), "swarm", "--poll", "10ms", "--timeout", "5s", "--socket", s.SocketPath)
	mu.Lock()
	n := reads
	mu.Unlock()
	if code != exitOK || n < 4 || strings.Count(plain(out), "WAITING") != 2 {
		t.Fatalf("= %d %q after %d reads", code, plain(out), n)
	}
}

// TestWaitTypeAliases: --type takes ntm's names; user and unknown are
// ntm's error; a type no agent has is ntm's "No matching panes found".
func TestWaitTypeAliases(t *testing.T) {
	_, _ = ntmWaitInstance(t, "idle", "working")
	if code, out, _ := runVerb(verbRun("wait"), "swarm", "--type", "cc", "--timeout", "2s"); code != exitOK || !strings.Contains(plain(out), "w1:p1: WAITING") {
		t.Fatalf("--type cc = %d %q", code, plain(out))
	}
	if code, out, _ := runVerb(verbRun("wait"), "swarm", "--type", "cod", "--until", "generating", "--timeout", "2s"); code != exitOK ||
		!strings.Contains(plain(out), "w1:p2: GENERATING") || strings.Contains(out, "w1:p1") {
		t.Fatalf("--type cod = %d %q", code, plain(out))
	}
	if code, _, errOut := runVerb(verbRun("wait"), "swarm", "--type", "user"); code != exitError || errOut != "Error: invalid agent type 'user'\n" {
		t.Fatalf("--type user = %d %q", code, errOut)
	}
	code, out, errOut := runVerb(verbRun("wait"), "swarm", "--type", "gemini")
	if code != exitError || !strings.HasSuffix(plain(out), "! No matching panes found\n") || errOut != "Error: no panes match the filter criteria\n" {
		t.Fatalf("--type gemini = %d %q %q", code, plain(out), errOut)
	}
}

// TestWaitPaneIndex: --pane is ntm's index (in the active agent tab), or
// the slot N.M; anything else is pflag's int error, as ntm's.
func TestWaitPaneIndex(t *testing.T) {
	_, _ = ntmWaitInstance(t, "idle", "idle")
	if code, out, _ := runVerb(verbRun("wait"), "swarm", "--pane", "1", "--timeout", "2s"); code != exitOK ||
		!strings.Contains(plain(out), "w1:p1: WAITING") || strings.Contains(out, "w1:p2") {
		t.Fatalf("--pane 1 = %d %q", code, plain(out))
	}
	for _, slot := range []string{"1.1", "01.1"} {
		if code, out, _ := runVerb(verbRun("wait"), "swarm", "--pane", slot, "--timeout", "2s"); code != exitOK ||
			!strings.Contains(plain(out), "w1:p2: WAITING") || strings.Contains(out, "w1:p1") {
			t.Fatalf("--pane %s = %d %q", slot, code, plain(out))
		}
	}
	// An index that no agent pane has matches nothing, as ntm's.
	if code, out, errOut := runVerb(verbRun("wait"), "swarm", "--pane", "7", "--timeout", "2s"); code != exitError ||
		!strings.HasSuffix(plain(out), "! No matching panes found\n") || errOut != "Error: no panes match the filter criteria\n" {
		t.Fatalf("--pane 7 = %d %q %q", code, plain(out), errOut)
	}
	if code, _, errOut := runVerb(verbRun("wait"), "swarm", "--pane", "abc"); code != exitError ||
		errOut != "Error: invalid argument \"abc\" for \"--pane\" flag: strconv.ParseInt: parsing \"abc\": invalid syntax\n" {
		t.Fatalf("--pane abc = %d %q", code, errOut)
	}
}

// TestWaitAnyAndTheCaller: --any with --count; the calling pane, nhm's
// user pane, is left out unless named.
func TestWaitAnyAndTheCaller(t *testing.T) {
	_, _ = ntmWaitInstance(t, "idle", "working")
	if code, out, _ := runVerb(verbRun("wait"), "swarm", "--any", "--timeout", "2s"); code != exitOK || !strings.Contains(plain(out), "w1:p1: WAITING") {
		t.Fatalf("--any = %d %q", code, plain(out))
	}
	if code, out, _ := runVerb(verbRun("wait"), "swarm", "--any", "--count", "2", "--timeout", "100ms", "--poll", "10ms"); code != exitError ||
		!strings.Contains(plain(out), "✗ Timeout after 100ms") {
		t.Fatalf("--any --count 2 = %d %q", code, plain(out))
	}
	t.Setenv("HERDR_PANE_ID", "w1:p2")
	if code, out, _ := runVerb(verbRun("wait"), "swarm", "--timeout", "2s"); code != exitOK || strings.Contains(out, "w1:p2") {
		t.Fatalf("without the caller = %d %q", code, plain(out))
	}
	if code, out, _ := runVerb(verbRun("wait"), "swarm", "--name", "BlueLake", "--timeout", "100ms", "--poll", "10ms"); code != exitError ||
		!strings.Contains(plain(out), "Timeout") {
		t.Fatalf("the caller named = %d %q", code, plain(out))
	}
}

// TestWaitErrorsAreNTMs: at most one session; an unknown session and an
// unknown name are errors, exit 1.
func TestWaitErrorsAreNTMs(t *testing.T) {
	_, _ = ntmWaitInstance(t, "idle", "idle")
	if code, _, errOut := runVerb(verbRun("wait"), "a", "b"); code != exitError || errOut != "Error: accepts at most 1 arg(s), received 2\n" {
		t.Fatalf("two sessions = %d %q", code, errOut)
	}
	if code, _, errOut := runVerb(verbRun("wait"), "nowhere"); code != exitError || !strings.HasPrefix(errOut, "Error: ") {
		t.Fatalf("no such session = %d %q", code, errOut)
	}
	if code, _, errOut := runVerb(verbRun("wait"), "swarm", "--name", "Nobody"); code != exitError || !strings.Contains(errOut, "Nobody has no active binding") {
		t.Fatalf("an unknown name = %d %q", code, errOut)
	}
}
