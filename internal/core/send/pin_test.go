package send

import (
	"context"
	"errors"
	"testing"

	"github.com/arosl/nhm/internal/herdr"
)

// TestSelectorBindings: pinned bindings (send --if-generation) select
// exactly themselves, a union with names; a binding that is not active in
// the workspace selects nothing, never another agent.
func TestSelectorBindings(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	a := w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	w.bind(t, "BlueLake", "w1:t2", "w1:p2", "codex", herdr.StatusIdle)
	c := w.bind(t, "RedStone", "w1:t3", "w1:p3", "claude", herdr.StatusIdle)
	agents, _ := w.fake.AgentList(ctx)
	sel := func(s Selector) ([]Target, error) {
		ts, _, err := Select(ctx, w.env.Bindings, agents, "w1", "", "", s)
		return ts, err
	}
	ts, err := sel(Selector{Bindings: []string{a.ID}})
	if err != nil || len(ts) != 1 || ts[0].Binding.ID != a.ID {
		t.Fatalf("one pin = %+v %v", ts, err)
	}
	ts, err = sel(Selector{Bindings: []string{a.ID, "bnd-gone"}, Names: []string{"RedStone"}})
	if err != nil || len(ts) != 2 || ts[0].Binding.ID != a.ID || ts[1].Binding.ID != c.ID {
		t.Fatalf("pins and a name = %+v %v", ts, err)
	}
	// Only gone pins: nothing is selected, and the selection is not "all".
	if ts, err := sel(Selector{Bindings: []string{"bnd-gone"}}); !errors.Is(err, ErrNoTargets) || len(ts) != 0 {
		t.Fatalf("gone pin = %+v %v", ts, err)
	}
}

// TestPinnedSendAfterARecycle is nhm-47w's case: a caller recorded a
// binding at one generation; after a recycle in the same pane, the same
// pinned send is refused with generation_changed, and nothing is typed.
func TestPinnedSendAfterARecycle(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	a := w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	w.bind(t, "BlueLake", "w1:t2", "w1:p2", "codex", herdr.StatusIdle)
	pin := func(op string) Request {
		return base(Request{Message: "packet", OperationID: op, Selector: Selector{Bindings: []string{a.ID}},
			Expect: map[string]int64{a.ID: a.Generation}})
	}
	res, err := Send(ctx, w.env, pin("op-p1"))
	if err != nil || len(res.Targets) != 1 || receiptOf(t, res, a.ID).Status != StatusSubmitted || w.fake.promptCount() != 1 {
		t.Fatalf("matching pin = %+v %v", res.Targets, err)
	}
	recycleBinding(t, w, a)
	res, err = Send(ctx, w.env, pin("op-p2"))
	if err != nil {
		t.Fatal(err)
	}
	if rc := receiptOf(t, res, a.ID); len(res.Targets) != 1 || rc.Status != StatusRefused || rc.ErrorCode != CodeGenerationChanged {
		t.Fatalf("after the recycle = %+v", res.Targets)
	}
	if w.fake.promptCount() != 1 {
		t.Fatalf("a prompt was typed: %+v", w.fake.prompts)
	}
}
