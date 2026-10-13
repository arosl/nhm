package lifecycle

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

// nhm-hae: recycle gives the successor's pane the marks it lacks, the
// label and the sidebar tokens. ScarletOrchid's spawn stopped at the
// trust dialog and never set them; its recycle did not set them either.
func TestRecycleMarksTheSuccessorsPane(t *testing.T) {
	w := newWorld(t)
	b := w.bind(t, "ScarletOrchid", "w1:p1", herdr.StatusDone)
	// No label, and a token from somewhere else.
	w.h.tokens = map[string]map[string]string{"w1:p1": {"mail": "ScarletOrchid"}}
	w.env.Handoff = func(context.Context, bindings.Binding, *Bead, string) Handoff {
		return Handoff{Text: "Handoff", Sources: []string{"excerpt"}, Degraded: []string{}}
	}
	res, err := Recycle(context.Background(), w.env, RecycleRequest{Target: "ScarletOrchid", Prompt: "Carry on."})
	if err != nil || res.Stage != StageDone || res.Generation != b.Generation+1 {
		t.Fatalf("recycle = %+v %v", res, err)
	}
	if w.h.labels["w1:p1"] != "ScarletOrchid opus" ||
		!maps.Equal(w.h.tokens["w1:p1"], map[string]string{"mail": "ScarletOrchid", "model": "opus"}) {
		t.Fatalf("marks: labels %v, tokens %v", w.h.labels, w.h.tokens)
	}
	if strings.Contains(strings.Join(res.Warnings, "; "), "pane label") {
		t.Fatalf("warnings = %v", res.Warnings)
	}
}

// After a herdr restart, restore applies the marks the pane lacks (it
// applied only the tokens before nhm-hae), and a label the user set
// stays. A resume marks the pane it starts the agent in.
func TestRestoreAndResumeMarkThePane(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	icy := w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
	items, err := Restore(ctx, w.env)
	if err != nil || len(items) != 1 || items[0].Action != "verified" ||
		!slices.Contains(items[0].Redone, "pane label") || !slices.Contains(items[0].Redone, "sidebar tokens") {
		t.Fatalf("restore = %+v %v", items, err)
	}
	if w.h.labels["w1:p1"] != "IcyKnoll opus" || w.h.tokens["w1:p1"]["mail"] != "IcyKnoll" {
		t.Fatalf("restore marks: labels %v, tokens %v", w.h.labels, w.h.tokens)
	}
	// herdr restarts again: the tokens are gone, the user's label is not.
	w.h.labels["w1:p1"], w.h.tokens = "reviewer", nil
	if items, err = Restore(ctx, w.env); err != nil || len(items) != 1 || !slices.Equal(items[0].Redone, []string{"sidebar tokens", "Agent Mail identity file"}) ||
		w.h.labels["w1:p1"] != "reviewer" || w.h.tokens["w1:p1"]["mail"] != "IcyKnoll" {
		t.Fatalf("second restore = %+v %v, labels %v", items, err, w.h.labels)
	}
	// A resume after the tokens were lost: they come back, and the user's
	// label stays.
	delete(w.h.agents, "w1:p1")
	w.h.tokens = nil
	if p, err := Resume(ctx, w.env, icy.ID, true); err != nil || !p.Started {
		t.Fatalf("resume = %+v %v", p, err)
	}
	if w.h.labels["w1:p1"] != "reviewer" || !maps.Equal(w.h.tokens["w1:p1"], map[string]string{"mail": "IcyKnoll", "model": "opus"}) {
		t.Fatalf("resume marks: labels %v, tokens %v", w.h.labels, w.h.tokens)
	}
	// With no label at all, the resume gives the pane nhm's.
	delete(w.h.agents, "w1:p1")
	w.h.labels["w1:p1"] = ""
	icy, _ = w.env.Bindings.Get(ctx, icy.ID)
	if p, err := Resume(ctx, w.env, icy.ID, true); err != nil || !p.Started || w.h.labels["w1:p1"] != "IcyKnoll opus" {
		t.Fatalf("resume = %+v %v, labels %v", p, err, w.h.labels)
	}
}
