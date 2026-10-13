package lifecycle

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

// paneGone is the fake herdr where the panes in gone no longer exist:
// pane.process_info answers pane_not_found.
type paneGone struct {
	*fakeHerdr
	gone map[string]bool
}

func (f *paneGone) PaneProcessInfo(ctx context.Context, pane string) (herdr.ProcessInfo, error) {
	if f.gone[pane] {
		return herdr.ProcessInfo{}, &herdr.Error{Code: herdr.CodePaneNotFound}
	}
	return f.fakeHerdr.PaneProcessInfo(ctx, pane)
}

// The agent exited on its own and its pane is back at the shell: herdr
// lists no agent there. Recycle types no exit command, starts the
// successor in the same pane with the handoff, and bumps the generation
// (nhm-lsa). A wait for its turn does not time out on the missing agent.
func TestRecycleStartsTheSuccessorOfAnExitedAgent(t *testing.T) {
	ctx := context.Background()
	for name, wait := range map[string]time.Duration{"no wait": 0, "a wait for its turn": time.Minute} {
		w := newWorld(t)
		b := bindIn(t, w, "IcyKnoll", "w1:p1")
		delete(w.h.agents, "w1:p1")
		handoffs := 0
		w.env.Handoff = func(context.Context, bindings.Binding, *Bead, string) Handoff {
			handoffs++
			return Handoff{Text: "Handoff: the work so far", Sources: []string{"bead"}, Degraded: []string{}}
		}
		res, err := Recycle(ctx, w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "Carry on.", WaitTurn: wait})
		if err != nil || res.Stage != StageDone || res.Generation != b.Generation+1 {
			t.Fatalf("%s: recycle = %+v %v", name, res, err)
		}
		if len(w.h.prompts) != 0 {
			t.Errorf("%s: an exit command was typed to no agent: %v", name, w.h.prompts)
		}
		if len(w.h.starts) != 1 || !strings.Contains(w.h.starts[0], " w1:p1 ") {
			t.Errorf("%s: starts = %v, want one in w1:p1", name, w.h.starts)
		}
		if handoffs != 1 || len(w.sent) != 1 || !strings.Contains(w.sent[0].Message, "Handoff: the work so far") {
			t.Errorf("%s: handoffs %d, kickoff %+v", name, handoffs, w.sent)
		}
		if !strings.Contains(strings.Join(res.Warnings, "; "), "IcyKnoll had already exited") {
			t.Errorf("%s: warnings = %v", name, res.Warnings)
		}
	}
}

// With no agent in the pane, recycle starts a successor only at the
// pane's shell. A pane that is gone, or one where another program holds
// the foreground, is refused at the check: nothing is claimed, typed or
// started.
func TestRecycleRefusesAnExitedAgentAwayFromItsShell(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		setup func(w *world)
		want  string
	}{
		"the pane is gone": {func(w *world) {
			w.env.Herdr = &paneGone{fakeHerdr: w.h, gone: map[string]bool{"w1:p1": true}}
		}, "pane w1:p1 is gone"},
		"another program": {func(w *world) { w.h.jobs = map[string]bool{"w1:p1": true} }, "another program holds the foreground of pane w1:p1"},
	} {
		w := newWorld(t)
		b := bindIn(t, w, "IcyKnoll", "w1:p1")
		delete(w.h.agents, "w1:p1")
		c.setup(w)
		res, err := Recycle(ctx, w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "Carry on."})
		if err == nil || res.Stage != StageCheck || !strings.Contains(err.Error(), c.want) ||
			!strings.Contains(err.Error(), "agent_not_found") {
			t.Errorf("%s: recycle = %+v %v", name, res, err)
		}
		if res.OperationID != "" || len(w.h.prompts) != 0 || len(w.h.starts) != 0 || len(w.sent) != 0 {
			t.Errorf("%s: a refused recycle acted: op %q, prompts %v, starts %v, sent %d", name, res.OperationID, w.h.prompts,
				w.h.starts, len(w.sent))
		}
		if nb, err := w.env.Bindings.Get(ctx, b.ID); err != nil || nb.Generation != b.Generation {
			t.Errorf("%s: binding = %+v %v", name, nb, err)
		}
	}
}
