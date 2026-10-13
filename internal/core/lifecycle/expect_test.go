package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

// recycled recycles IcyKnoll once, as another caller would between a
// review and its submission, and returns the binding as it was before.
func recycled(t *testing.T, w *world) bindings.Binding {
	t.Helper()
	b := w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusDone)
	if res, err := Recycle(context.Background(), w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "x"}); err != nil || res.Generation != b.Generation+1 {
		t.Fatalf("recycle = %+v %v", res, err)
	}
	return b
}

func TestRetireExpectedGeneration(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	old := recycled(t, w)
	w.facts.closed = []string{"acfs-1"}
	res, err := Retire(ctx, w.env, RetireRequest{Target: "IcyKnoll", ExpectedGeneration: old.Generation})
	if !errors.Is(err, ErrRefused) || len(res.Refusals) != 1 || !strings.Contains(res.Refusals[0], "expected generation 1") {
		t.Fatalf("stale = %+v %v", res, err)
	}
	if len(w.h.closed) != 0 || len(w.mail.retired) != 0 {
		t.Fatalf("effects: closed %v, retired %v", w.h.closed, w.mail.retired)
	}
	if b, _ := w.env.Bindings.Get(ctx, old.ID); b.Status != bindings.Active {
		t.Fatalf("binding = %s", b.Status)
	}
	// The generation now current is accepted.
	res, err = Retire(ctx, w.env, RetireRequest{Target: "IcyKnoll", ExpectedGeneration: old.Generation + 1})
	if err != nil || !res.Done {
		t.Fatalf("current = %+v %v", res, err)
	}
}

func TestRecycleExpectedGeneration(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	old := recycled(t, w)
	prompts, starts := len(w.h.prompts), len(w.h.starts)
	res, err := Recycle(ctx, w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "x", ExpectedGeneration: old.Generation})
	if err == nil || res.Stage != StageCheck || !strings.Contains(res.Error, "expected generation 1") {
		t.Fatalf("stale = %+v %v", res, err)
	}
	if len(w.h.prompts) != prompts || len(w.h.starts) != starts {
		t.Fatalf("effects: prompts %v, starts %v", w.h.prompts, w.h.starts)
	}
	if b, _ := w.env.Bindings.Get(ctx, old.ID); b.Generation != old.Generation+1 {
		t.Fatalf("generation = %d", b.Generation)
	}
}
