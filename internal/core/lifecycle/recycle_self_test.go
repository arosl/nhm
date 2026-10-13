package lifecycle

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/herdr"
)

// turnEnds is the fake herdr where the agent ends its turn while recycle
// waits for it (agent.wait), or does not when ends is false.
type turnEnds struct {
	*fakeHerdr
	ends  bool
	waits []herdr.AgentWaitParams
}

func (f *turnEnds) AgentWait(ctx context.Context, p herdr.AgentWaitParams) (herdr.AgentInfo, error) {
	f.mu.Lock()
	f.waits = append(f.waits, p)
	if a, ok := f.agents[p.Target]; ok && f.ends && a.AgentStatus == herdr.StatusWorking {
		a.AgentStatus = herdr.StatusDone
		f.agents[p.Target] = a
	}
	f.mu.Unlock()
	return f.fakeHerdr.AgentWait(ctx, p)
}

// nhm recycle --self's job (nhm-abn): the agent is working when it asks,
// recycle waits for its turn to end, then recycles it; a turn that does
// not end in time is refused at the check, and nothing stops.
func TestRecycleWaitsForTheTurn(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	b := w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusWorking)
	h := &turnEnds{fakeHerdr: w.h, ends: true}
	w.env.Herdr = h
	res, err := Recycle(ctx, w.env, RecycleRequest{Target: b.ID, Prompt: "Next bead.", WaitTurn: 2 * time.Minute,
		ExpectedGeneration: b.Generation})
	if err != nil || res.Stage != StageDone || res.Generation != b.Generation+1 {
		t.Fatalf("recycle = %+v %v", res, err)
	}
	if len(h.waits) < 1 || h.waits[0].Target != "w1:p1" || h.waits[0].TimeoutMS == nil || *h.waits[0].TimeoutMS != 120000 {
		t.Fatalf("waits = %+v", h.waits)
	}

	w = newWorld(t)
	b = w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusWorking)
	w.env.Herdr = &turnEnds{fakeHerdr: w.h}
	res, err = Recycle(ctx, w.env, RecycleRequest{Target: b.ID, Prompt: "Next bead.", WaitTurn: time.Second})
	if err == nil || res.Stage != StageCheck || !strings.Contains(err.Error(), "did not end its turn within 1s") ||
		len(w.h.prompts) != 0 || len(w.h.starts) != 0 {
		t.Fatalf("a turn that did not end = %+v %v", res, err)
	}
}

// Recycle in the agent's own pane is refused and names --self.
func TestRecycleInTheAgentsPaneNamesSelf(t *testing.T) {
	w := newWorld(t)
	w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
	w.env.Caller = "w1:p1"
	res, err := Recycle(context.Background(), w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "x"})
	if err == nil || res.Stage != StageCheck || !strings.Contains(err.Error(), "nhm recycle --self") {
		t.Fatalf("recycle in its own pane = %+v %v", res, err)
	}
}

// CheckRecycle runs the checks that need neither the turn nor the lock,
// and changes nothing.
func TestCheckRecycle(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	b := w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusWorking)
	if got, err := CheckRecycle(ctx, w.env, RecycleRequest{Target: b.ID, Prompt: "x"}); err != nil || got.ID != b.ID {
		t.Fatalf("a working agent's check = %+v %v", got, err)
	}
	if _, err := CheckRecycle(ctx, w.env, RecycleRequest{Target: b.ID, Prompt: "x", Template: "y"}); err == nil {
		t.Fatal("-t with --prompt passed the check")
	}
	if _, err := w.env.Bindings.Retire(ctx, b.ID, b.Generation, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckRecycle(ctx, w.env, RecycleRequest{Target: b.ID, Prompt: "x"}); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("a retired binding's check = %v", err)
	}
	if len(w.h.prompts) != 0 || len(w.h.starts) != 0 {
		t.Fatalf("a check acted: prompts %v, starts %v", w.h.prompts, w.h.starts)
	}
}
