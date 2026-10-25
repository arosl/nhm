package lifecycle

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

// exitHerdr is the fake herdr whose agents take keys: an agent exits
// after its quitAfter-th key (0: never), and the panes in gone answer
// pane_not_found once the keys went. After the exit, herdr keeps the
// agent's identity for the next retain reads, as herdr does for about a
// second.
type exitHerdr struct {
	*fakeHerdr
	keys      []string
	quitAfter int
	gone      bool
	retain    int
}

func (f *exitHerdr) AgentGet(ctx context.Context, pane string) (herdr.AgentInfo, error) {
	f.mu.Lock()
	if _, ok := f.agents[pane]; !ok && f.retain > 0 {
		f.retain--
		f.mu.Unlock()
		return herdr.AgentInfo{PaneID: pane, Name: "icyknoll", AgentStatus: herdr.StatusIdle}, nil
	}
	f.mu.Unlock()
	return f.fakeHerdr.AgentGet(ctx, pane)
}

func (f *exitHerdr) AgentSendKeys(_ context.Context, target string, keys ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, keys...)
	if f.quitAfter > 0 && len(f.keys) >= f.quitAfter {
		delete(f.agents, target)
	}
	return nil
}

func (f *exitHerdr) PaneProcessInfo(ctx context.Context, pane string) (herdr.ProcessInfo, error) {
	f.mu.Lock()
	gone := f.gone && len(f.keys) > 0
	f.mu.Unlock()
	if gone {
		return herdr.ProcessInfo{}, &herdr.Error{Code: herdr.CodePaneNotFound}
	}
	info, err := f.fakeHerdr.PaneProcessInfo(ctx, pane)
	if len(info.ForegroundProcesses) > 0 {
		info.ForegroundProcesses[0].Name = "claude"
	}
	return info, err
}

func exitWorld(t *testing.T, quitAfter int) (*world, *exitHerdr, ExitRequest) {
	t.Helper()
	w := newWorld(t)
	h := &exitHerdr{fakeHerdr: w.h, quitAfter: quitAfter}
	w.env.Herdr = h
	w.env.Jobs = func(int) ([]Job, error) { return nil, nil }
	b := w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
	return w, h, ExitRequest{PaneID: "w1:p1", Binding: &b, Kind: "claude", NTMType: "claude"}
}

// ntm's exit: Ctrl+C, then Ctrl+C, and the pane is back at its shell.
func TestExitAgentDoubleCtrlC(t *testing.T) {
	w, h, req := exitWorld(t, 2)
	out := ExitAgent(context.Background(), w.env, req)
	if !out.Exited || !out.ShellPreserved || out.ShellPID != 100 || out.Detail != "" || out.VerificationFailed ||
		!reflect.DeepEqual(h.keys, []string{"ctrl+c", "ctrl+c"}) {
		t.Fatalf("ExitAgent = %+v, keys %v", out, h.keys)
	}
	// The second round goes only when the first did not end the agent.
	w, h, req = exitWorld(t, 4)
	if out := ExitAgent(context.Background(), w.env, req); !out.Exited || len(h.keys) != 4 {
		t.Errorf("second round = %+v, keys %v", out, h.keys)
	}
}

// A pane already at its shell gets no keys.
func TestExitAgentAlreadyAtShell(t *testing.T) {
	w, h, req := exitWorld(t, 2)
	delete(w.h.agents, "w1:p1")
	out := ExitAgent(context.Background(), w.env, req)
	if !out.Exited || !out.ShellPreserved || out.Detail != "agent CLI already exited; pane is at a bare shell" || len(h.keys) != 0 {
		t.Errorf("ExitAgent = %+v, keys %v", out, h.keys)
	}
	// A stopped job of the shell is the agent, not a bare shell: nhm
	// types nothing into the shell, and reports it.
	w, h, req = exitWorld(t, 2)
	delete(w.h.agents, "w1:p1")
	w.env.Jobs = func(int) ([]Job, error) { return []Job{{PID: 300, State: 'T', Argv: []string{"claude"}}}, nil }
	out = ExitAgent(context.Background(), w.env, req)
	if out.Exited || !out.ShellPreserved || !strings.HasPrefix(out.Detail, "exit keystrokes failed: the pane holds no agent: w1:p1; agent CLI still in foreground") ||
		len(h.keys) != 0 {
		t.Errorf("stopped job = %+v, keys %v", out, h.keys)
	}
}

// An agent that does not exit after two rounds stays, with ntm's detail;
// a pane that went away is not preserved.
func TestExitAgentFailures(t *testing.T) {
	w, h, req := exitWorld(t, 0)
	out := ExitAgent(context.Background(), w.env, req)
	if out.Exited || !out.ShellPreserved || len(h.keys) != 4 ||
		out.Detail != "agent CLI still in foreground (claude) after two graceful exit attempts; escalate with --robot-kill-agent" {
		t.Errorf("stays = %+v, keys %v", out, h.keys)
	}
	w, h, req = exitWorld(t, 0)
	h.gone = true
	if out := ExitAgent(context.Background(), w.env, req); out.Exited || out.ShellPreserved || out.VerificationFailed {
		t.Errorf("gone = %+v", out)
	}
}

// omp gets its own sequence: Escape, Ctrl+C, Ctrl+D.
func TestExitKeys(t *testing.T) {
	if k, p := ExitKeys("omp"); !reflect.DeepEqual(k, []string{"esc", "ctrl+c", "ctrl+d"}) || p[1] != ompExitInterruptSettle || p[2] != ompClearVerifyWait {
		t.Errorf("omp = %v %v", k, p)
	}
	for _, typ := range []string{"claude", "codex", "pi", "grok"} {
		if k, p := ExitKeys(typ); !reflect.DeepEqual(k, []string{"ctrl+c", "ctrl+c"}) || p[1] != exitDoubleTapGap {
			t.Errorf("%s = %v %v", typ, k, p)
		}
	}
}

// --relaunch starts the binding's agent again in its pane, at the same
// shell, and bumps the generation; a pane whose shell changed is not
// relaunched. Every pane needs a binding before any agent exits.
func TestRelaunch(t *testing.T) {
	w, h, req := exitWorld(t, 2)
	ctx := context.Background()
	out := ExitAgent(ctx, w.env, req)
	ok, detail := Relaunch(ctx, w.env, req, out.ShellPID)
	nb, _ := w.env.Bindings.Get(ctx, req.Binding.ID)
	if !ok || len(h.starts) != 1 || !strings.HasPrefix(h.starts[0], "icyknoll claude w1:p1") || nb.Generation != req.Binding.Generation+1 ||
		nb.Evidence[0].Kind != "relaunch" {
		t.Fatalf("Relaunch = %v %q, starts %v, binding %+v", ok, detail, h.starts, nb)
	}
	// A stale binding (the generation moved) is not relaunched again.
	if ok, detail := Relaunch(ctx, w.env, req, out.ShellPID); ok || detail != "relaunch withheld: original agent pane identity could not be verified" {
		t.Errorf("stale binding = %v %q", ok, detail)
	}
	w, h, req = exitWorld(t, 2)
	out = ExitAgent(ctx, w.env, req)
	if ok, detail := Relaunch(ctx, w.env, req, out.ShellPID+1); ok || detail != "relaunch withheld: original agent pane identity could not be verified" || len(h.starts) != 0 {
		t.Errorf("another shell = %v %q", ok, detail)
	}
	// The start waits until herdr releases the old agent's identity, and
	// withholds the relaunch while herdr keeps it past 10 s.
	h.retain = 3
	if ok, _ := Relaunch(ctx, w.env, req, out.ShellPID); !ok || h.retain != 0 || len(h.starts) != 1 {
		t.Errorf("retained 3 reads = %v, retain %d, starts %v", ok, h.retain, h.starts)
	}
	w, h, req = exitWorld(t, 2)
	out = ExitAgent(ctx, w.env, req)
	h.retain = 1000
	if ok, detail := Relaunch(ctx, w.env, req, out.ShellPID); ok || detail != "relaunch withheld: herdr still holds the agent of pane w1:p1 after 10s" || len(h.starts) != 0 {
		t.Errorf("retained = %v %q", ok, detail)
	}
	if err := RelaunchCheck(w.env, []ExitRequest{req, {PaneID: "w1:p4", Kind: "claude"}}); err == nil ||
		!strings.Contains(err.Error(), "pane w1:p4 holds an agent that nhm did not bind") {
		t.Errorf("unbound = %v", err)
	}
	pi := bindings.Binding{MailName: "BlueLake", Kind: "pi"}
	if err := RelaunchCheck(w.env, []ExitRequest{{PaneID: "w1:p2", Binding: &pi, Kind: "pi"}}); err == nil || !strings.Contains(err.Error(), "nhm's pi guard") {
		t.Errorf("pi without its guard = %v", err)
	}
	if err := RelaunchCheck(w.env, []ExitRequest{req}); err != nil {
		t.Errorf("bound claude = %v", err)
	}
}
