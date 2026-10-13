package hooks

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
)

// setMarks gives a pane of the stub the marks a user or herdr left.
func (w *world) setMarks(id, label string, tokens map[string]string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.panes[id].label, w.panes[id].tokens = label, maps.Clone(tokens)
}

// marks are a pane's label and tokens as the stub holds them.
func (w *world) marks(id string) (string, map[string]string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.panes[id].label, maps.Clone(w.panes[id].tokens)
}

// nhm-hae: a bound agent's pane without its marks gets them back on the
// next reconcile. ScarletOrchid's spawn stopped at the trust dialog, so
// spawn never set them, and the reconcile did not either. A label that
// someone set stays; a missing token comes back alone, and a token of
// spawn's (group) stays.
func TestReconcileGivesBackMissingMarks(t *testing.T) {
	h := newHarness(t, core.OwnerNhm)
	ctx := context.Background()
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500, name: "scarletorchid"})
	if _, err := h.env.Bindings.Create(ctx, bindings.New{MailName: "ScarletOrchid", MailProjectKey: "/p", ProjectID: "proj-p",
		Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p1",
			HerdrAlias: "scarletorchid", Kind: "claude", Model: "opus", NativeSession: &bindings.NativeSession{Value: "sess-a"},
			ForegroundPID: 500, ForegroundStart: 5000}}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"mail": "ScarletOrchid", "model": "opus"}
	r, err := ReconcilePane(ctx, h.env, "w1:p1")
	if err != nil {
		t.Fatal(err)
	}
	if label, tokens := h.world.marks("w1:p1"); label != "ScarletOrchid opus" || !maps.Equal(tokens, want) || len(r.Warnings) != 0 ||
		!slices.Contains(r.Actions, "marked w1:p1 for ScarletOrchid: pane label, sidebar tokens") {
		t.Fatalf("marks %q %v, report %+v, calls %v", label, tokens, r, h.world.calls)
	}
	// Both marks are there: the next reconcile changes nothing.
	if r, err = ReconcilePane(ctx, h.env, "w1:p1"); err != nil || len(r.Actions) != 0 || len(r.Warnings) != 0 ||
		h.world.count("pane.rename") != 1 || h.world.count("pane.report_metadata") != 1 {
		t.Fatalf("second reconcile: %+v %v, calls %v", r, err, h.world.calls)
	}
	// A user clears both by hand.
	h.world.setMarks("w1:p1", "", nil)
	if r, err = ReconcilePane(ctx, h.env, "w1:p1"); err != nil || h.world.count("pane.rename") != 2 || h.world.count("pane.report_metadata") != 2 {
		t.Fatalf("cleared marks: %+v %v, calls %v", r, err, h.world.calls)
	}
	// A label the user chose stays; the missing model comes back, and
	// spawn's group token stays.
	h.world.setMarks("w1:p1", "reviewer", map[string]string{"mail": "ScarletOrchid", "group": "g1"})
	if r, err = ReconcilePane(ctx, h.env, "w1:p1"); err != nil || h.world.count("pane.rename") != 2 || h.world.count("pane.report_metadata") != 3 ||
		!slices.Equal(r.Actions, []string{"marked w1:p1 for ScarletOrchid: sidebar tokens"}) {
		t.Fatalf("user's label: %+v %v, calls %v", r, err, h.world.calls)
	}
	if label, tokens := h.world.marks("w1:p1"); label != "reviewer" || !maps.Equal(tokens, map[string]string{"mail": "ScarletOrchid", "model": "opus", "group": "g1"}) {
		t.Fatalf("marks after the user's label: %q %v", label, tokens)
	}
}

// A pane that herdr moved has a new id; the reconcile that follows the
// move gives it the marks it lacks there (nhm-hae).
func TestMovedPaneGetsItsMarks(t *testing.T) {
	h := newHarness(t, core.OwnerNhm)
	ctx := context.Background()
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500})
	h.hook(registration("sess-a", "IcyKnoll", t.TempDir()), "e1")
	h.world.move("w1:p1", "w2:p1", "w2:t1")
	h.world.addTab("w2:t1", "3", 1)
	h.world.setMarks("w2:p1", "", nil)
	r, err := ReconcilePane(ctx, h.env, "w1:p1")
	if err != nil {
		t.Fatal(err)
	}
	if label, tokens := h.world.marks("w2:p1"); label != "IcyKnoll" || tokens["mail"] != "IcyKnoll" || len(r.Warnings) != 0 ||
		!slices.Contains(r.Actions, "marked w2:p1 for IcyKnoll: pane label, sidebar tokens") {
		t.Fatalf("marks %q %v, report %+v", label, tokens, r)
	}
}

// The hooks' bind of a hand-started agent gives its pane both marks, as
// spawn does: the label and the sidebar tokens (nhm-hae).
func TestRegistrationMarksThePane(t *testing.T) {
	h := newHarness(t, core.OwnerNhm)
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500})
	h.hook(registration("sess-a", "IcyKnoll", t.TempDir()), "e1")
	if label, tokens := h.world.marks("w1:p1"); label != "IcyKnoll" || !maps.Equal(tokens, map[string]string{"mail": "IcyKnoll"}) {
		t.Fatalf("marks %q %v, calls %v", label, tokens, h.world.calls)
	}
	if !h.world.called("pane.report_metadata w1:p1 nhm mail=IcyKnoll") {
		t.Fatalf("calls %v", h.world.calls)
	}
}
