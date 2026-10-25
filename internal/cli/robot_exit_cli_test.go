package cli

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/robot"
	"github.com/arosl/nhm/internal/store"
)

// exitPanes is a fake herdr whose agents exit on keys: a pane's agent
// leaves after quit[pane] keys (0: never); the shell then holds the
// foreground. agent.start puts an agent back.
type exitPanes struct {
	mu     sync.Mutex
	agents map[string]string // pane: kind
	quit   map[string]int
	keys   map[string][]string
	starts []string
}

func exitServer(t *testing.T, quit map[string]int) (*herdrtest.Server, *exitPanes, store.Instance) {
	t.Helper()
	e := &exitPanes{agents: map[string]string{"w1:p1": "claude", "w1:p2": "codex", "w1:p4": "claude"}, quit: quit, keys: map[string][]string{}}
	s, _, _ := paneServer(t, func(string) string { return "" })
	agentInfo := func(pane, kind string) map[string]any {
		return map[string]any{"pane_id": pane, "tab_id": "w1:t1", "workspace_id": "w1", "terminal_id": "t-" + pane,
			"agent": kind, "agent_status": "idle", "focused": false, "revision": 1, "state_change_seq": 4}
	}
	target := func(r herdrtest.Request) string {
		var p struct {
			Target string `json:"target"`
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		return p.Target + p.PaneID
	}
	s.Handle("agent.get", func(r herdrtest.Request) (any, *herdrtest.Error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		pane := target(r)
		kind, ok := e.agents[pane]
		if !ok {
			return nil, &herdrtest.Error{Code: "agent_not_found", Message: "no agent in " + pane}
		}
		return map[string]any{"type": "agent_info", "agent": agentInfo(pane, kind)}, nil
	})
	s.Handle("agent.send_keys", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Target string   `json:"target"`
			Keys   []string `json:"keys"`
		}
		_ = json.Unmarshal(r.Params, &p)
		e.mu.Lock()
		defer e.mu.Unlock()
		e.keys[p.Target] = append(e.keys[p.Target], p.Keys...)
		if q := e.quit[p.Target]; q > 0 && len(e.keys[p.Target]) >= q {
			delete(e.agents, p.Target)
		}
		return map[string]any{"type": "ok"}, nil
	})
	s.Handle("pane.process_info", func(r herdrtest.Request) (any, *herdrtest.Error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		pane := target(r)
		info := map[string]any{"pane_id": pane, "shell_pid": 4100, "foreground_process_group_id": 4100}
		if kind, ok := e.agents[pane]; ok {
			info["foreground_process_group_id"] = 4200
			info["foreground_processes"] = []any{map[string]any{"pid": 4200, "name": kind}}
		}
		return map[string]any{"type": "pane_process_info", "process_info": info}, nil
	})
	s.Handle("agent.start", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Name   string `json:"name"`
			Kind   string `json:"kind"`
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		e.mu.Lock()
		defer e.mu.Unlock()
		e.starts = append(e.starts, p.Name+" "+p.Kind+" "+p.PaneID)
		e.agents[p.PaneID] = p.Kind
		return map[string]any{"type": "agent_started", "agent": agentInfo(p.PaneID, p.Kind)}, nil
	})
	s.Handle("agent.wait", func(r herdrtest.Request) (any, *herdrtest.Error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		pane := target(r)
		return map[string]any{"type": "agent_info", "agent": agentInfo(pane, e.agents[pane])}, nil
	})
	ie, err := openInstance(s.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	inst := ie.inst
	_ = ie.db.Close()
	ownNhm(t, inst)
	// No /proc and no real waits: the shells have no jobs, and the clock
	// moves a second per pause.
	clock := time.Now()
	var cmu sync.Mutex
	exitCLIEnv = func(env *lifecycle.Env) {
		env.Jobs = func(int) ([]lifecycle.Job, error) { return nil, nil }
		env.Now = func() time.Time { cmu.Lock(); defer cmu.Unlock(); return clock }
		env.Sleep = func(context.Context, time.Duration) { cmu.Lock(); clock = clock.Add(time.Second); cmu.Unlock() }
	}
	t.Cleanup(func() { exitCLIEnv = nil })
	return s, e, inst
}

func exitOut(t *testing.T, out string) robot.ExitCLIOutput {
	t.Helper()
	var got robot.ExitCLIOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %s", out)
	}
	return got
}

// Each agent pane gets ntm's Ctrl+C, Ctrl+C and is back at its shell; the
// shells are left out.
func TestRobotExitCLI(t *testing.T) {
	s, e, _ := exitServer(t, map[string]int{"w1:p1": 2, "w1:p2": 2, "w1:p4": 2})
	code, out, _ := run("--robot-exit-cli=swarm", "--socket", s.SocketPath)
	got := exitOut(t, out)
	if code != 0 || !got.Success || got.Session != "swarm" || len(got.Results) != 3 {
		t.Fatalf("--robot-exit-cli = %d %s", code, out)
	}
	for i, w := range []struct{ pane, target, typ string }{{"w1:p4", "w1:p4", "claude"}, {"0.1", "w1:p1", "claude"}, {"0.2", "w1:p2", "codex"}} {
		r := got.Results[i]
		if r.Pane != w.pane || r.Target != w.target || r.AgentType != w.typ || !r.Exited || !r.ShellPreserved || r.ShellPID != 4100 ||
			r.Relaunched || r.Detail != "" || strings.Join(e.keys[w.target], ",") != "ctrl+c,ctrl+c" {
			t.Errorf("result %d = %+v, keys %v", i, r, e.keys[w.target])
		}
	}
}

// A pane whose agent already exited gets no keys.
func TestRobotExitCLIAlreadyAtShell(t *testing.T) {
	s, e, _ := exitServer(t, map[string]int{"w1:p1": 2})
	delete(e.agents, "w1:p1")
	code, out, _ := run("--robot-exit-cli=swarm", "--panes=0.1", "--socket", s.SocketPath)
	got := exitOut(t, out)
	if code != 0 || len(got.Results) != 1 || !got.Results[0].Exited || got.Results[0].Detail != "agent CLI already exited; pane is at a bare shell" ||
		len(e.keys["w1:p1"]) != 0 {
		t.Errorf("already at shell = %d %s", code, out)
	}
}

// An agent still there after two rounds fails the call, with ntm's hint
// to escalate.
func TestRobotExitCLIFailureHintsKillAgent(t *testing.T) {
	s, e, _ := exitServer(t, map[string]int{"w1:p1": 2})
	code, out, _ := run("--robot-exit-cli=swarm", "--panes=0.1,0.2", "--socket", s.SocketPath)
	got := exitOut(t, out)
	if code != 1 || got.Success || got.ErrorCode != robot.ErrCodeInternalError ||
		got.Error != "one or more panes did not complete the requested lifecycle transition" ||
		got.Hint != "Inspect per-pane results; escalate stuck panes with --robot-kill-agent" || len(got.Results) != 2 {
		t.Fatalf("stuck = %d %s", code, out)
	}
	stuck := got.Results[1]
	if stuck.Exited || !stuck.ShellPreserved || len(e.keys["w1:p2"]) != 4 ||
		stuck.Detail != "agent CLI still in foreground (codex) after two graceful exit attempts; escalate with --robot-kill-agent" || !got.Results[0].Exited {
		t.Errorf("results = %+v, keys %v", got.Results, e.keys)
	}
}

// --relaunch starts each exited agent again in its pane, under its herdr
// name, and bumps its binding's generation. Every pane is checked first:
// an agent that nhm did not bind has no launch, and nothing exits.
func TestRobotExitCLIRelaunch(t *testing.T) {
	s, e, inst := exitServer(t, map[string]int{"w1:p1": 2, "w1:p4": 2})
	code, out, _ := run("--robot-exit-cli=swarm", "--panes=0.1", "--relaunch", "--socket", s.SocketPath)
	got := exitOut(t, out)
	if code != 0 || !got.Success || len(got.Results) != 1 || !got.Results[0].Exited || !got.Results[0].Relaunched ||
		strings.Join(e.starts, ",") != "icyknoll claude w1:p1" {
		t.Fatalf("relaunch = %d %s, starts %v", code, out, e.starts)
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bs, err := bindings.NewStore(db).List(context.Background(), bindings.Filter{WorkspaceID: "w1", Statuses: []bindings.Status{bindings.Active}})
	if err != nil || len(bs) != 2 || bs[0].PaneID != "w1:p1" || bs[0].Generation != 2 {
		t.Errorf("bindings = %+v %v", bs, err)
	}
	code, out, _ = run("--robot-exit-cli=swarm", "--panes=w1:p4,0.2", "--relaunch", "--socket", s.SocketPath)
	got = exitOut(t, out)
	if code != 1 || got.ErrorCode != robot.ErrCodeInternalError || !strings.Contains(got.Error, "pane w1:p4 holds an agent that nhm did not bind") ||
		!strings.HasSuffix(got.Hint, "no agent was exited") || len(e.keys["w1:p4"]) != 0 || len(e.keys["w1:p2"]) != 0 {
		t.Errorf("unbound relaunch = %d %s", code, out)
	}
}

// ntm's errors: no session, a pane without an agent; and nhm's NOT_OWNER
// while ACFS's Bash owns the instance.
func TestRobotExitCLIErrors(t *testing.T) {
	s, e, inst := exitServer(t, map[string]int{"w1:p1": 2})
	for _, c := range []struct {
		args      []string
		code, msg string
	}{
		{[]string{"--robot-exit-cli="}, robot.ErrCodeInvalidFlag, "--robot-exit-cli requires a session name"},
		{[]string{"--robot-exit-cli=swarm", "--panes=w1:p3"}, robot.ErrCodeInvalidFlag, "pane w1:p3 is not an agent pane (type user)"},
		{[]string{"--robot-exit-cli=nosuch"}, robot.ErrCodeSessionNotFound, "session 'nosuch' not found"},
	} {
		code, out, _ := run(append(c.args, "--socket", s.SocketPath)...)
		var got robot.ExitCLIOutput
		if code != 1 || json.Unmarshal([]byte(out), &got) != nil || got.ErrorCode != c.code || !strings.Contains(got.Error, c.msg) {
			t.Errorf("%v = %d %s", c.args, code, out)
		}
	}
	if _, err := core.SwitchOwner(context.Background(), inst.Dir, core.OwnerBash, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	code, out, _ := run("--robot-exit-cli=swarm", "--panes=0.1", "--socket", s.SocketPath)
	if got := exitOut(t, out); code != 1 || got.ErrorCode != robot.ErrCodeNotOwner || len(e.keys["w1:p1"]) != 0 {
		t.Errorf("not owner = %d %s", code, out)
	}
}
