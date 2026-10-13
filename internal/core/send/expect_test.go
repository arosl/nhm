package send

import (
	"context"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

func recycleBinding(t *testing.T, w *world, b bindings.Binding) bindings.Binding {
	t.Helper()
	nb, err := w.env.Bindings.Occupy(context.Background(), b.ID, b.Generation, bindings.Occupant{WorkspaceID: "w1",
		TabID: b.TabID, PaneID: b.PaneID, HerdrAlias: b.HerdrAlias, Kind: b.Kind})
	if err != nil {
		t.Fatal(err)
	}
	return nb
}

func receiptOf(t *testing.T, res Result, bindingID string) Receipt {
	t.Helper()
	for _, rc := range res.Targets {
		if rc.BindingID == bindingID {
			return rc
		}
	}
	t.Fatalf("no receipt for %s in %+v", bindingID, res.Targets)
	return Receipt{}
}

func TestExpectRefusesAStaleGeneration(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	a := w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	b := w.bind(t, "BlueLake", "w1:t2", "w1:p2", "codex", herdr.StatusIdle)
	// The caller reviewed both; IcyKnoll is recycled before submission.
	expect := map[string]int64{a.ID: a.Generation, b.ID: b.Generation}
	recycleBinding(t, w, a)
	res, err := Send(ctx, w.env, base(Request{Message: "hi", OperationID: "op-e1", Expect: expect, Selector: Selector{All: true}}))
	if err != nil {
		t.Fatal(err)
	}
	if rc := receiptOf(t, res, a.ID); rc.Status != StatusRefused || rc.ErrorCode != CodeGenerationChanged {
		t.Fatalf("stale target = %+v", rc)
	}
	if rc := receiptOf(t, res, b.ID); rc.Status != StatusSubmitted {
		t.Fatalf("current target = %+v", rc)
	}
	if w.fake.promptCount() != 1 || w.fake.prompts[0].pane != "w1:p2" {
		t.Fatalf("prompts = %+v", w.fake.prompts)
	}
	// Unset Expect keeps today's selection: both get it.
	res, err = Send(ctx, w.env, base(Request{Message: "hi", OperationID: "op-e2", Selector: Selector{All: true}}))
	if err != nil {
		t.Fatal(err)
	}
	if rc := receiptOf(t, res, a.ID); rc.Status != StatusSubmitted {
		t.Fatalf("without Expect = %+v", rc)
	}
}

func TestExpectRefusesUnreviewedAndReportsMissing(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	a := w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	b := w.bind(t, "BlueLake", "w1:t2", "w1:p2", "codex", herdr.StatusIdle)
	expect := map[string]int64{a.ID: a.Generation, "gone-binding": 3}
	res, err := Send(ctx, w.env, base(Request{Message: "hi", OperationID: "op-e3", Expect: expect, Selector: Selector{All: true}}))
	if err != nil {
		t.Fatal(err)
	}
	if rc := receiptOf(t, res, b.ID); rc.Status != StatusRefused || rc.ErrorCode != CodeNotReviewed {
		t.Fatalf("unreviewed = %+v", rc)
	}
	if rc := receiptOf(t, res, "gone-binding"); rc.Status != StatusRefused || rc.ErrorCode != CodeNotSelected {
		t.Fatalf("missing = %+v", rc)
	}
	if rc := receiptOf(t, res, a.ID); rc.Status != StatusSubmitted {
		t.Fatalf("reviewed = %+v", rc)
	}
	if w.fake.promptCount() != 1 {
		t.Fatalf("prompts = %d", w.fake.promptCount())
	}
}

// A recycle between the core's selection and the target's lock: the
// lock-time check refuses it before any prompt.
func TestExpectRecycleBetweenSelectionAndSubmission(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	a := w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	unlock, err := lockTarget(ctx, w.env.InstanceDir, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan Result, 1)
	errs := make(chan error, 1)
	go func() {
		res, err := Send(ctx, w.env, base(Request{Message: "hi", OperationID: "op-e4",
			Expect: map[string]int64{a.ID: a.Generation}, Selector: Selector{Names: []string{"IcyKnoll"}}}))
		errs <- err
		done <- res
	}()
	// Wait until the receipts exist: selection is over, dispatch waits
	// for the lock.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if st, err := Stored(ctx, w.db, "op-e4"); err == nil && len(st.Targets) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("send never reached dispatch")
		}
		time.Sleep(10 * time.Millisecond)
	}
	recycleBinding(t, w, a)
	unlock()
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	res := <-done
	if rc := receiptOf(t, res, a.ID); rc.Status != StatusRefused || rc.ErrorCode != CodeGenerationChanged {
		t.Fatalf("= %+v", rc)
	}
	if w.fake.promptCount() != 0 {
		t.Fatalf("prompts = %d", w.fake.promptCount())
	}
}

func TestExpectDryRunShowsTheRefusal(t *testing.T) {
	w := newWorld(t)
	a := w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	res, err := Send(context.Background(), w.env, base(Request{Message: "hi", DryRun: true,
		Expect: map[string]int64{a.ID: a.Generation + 1}, Selector: Selector{All: true}}))
	if err != nil {
		t.Fatal(err)
	}
	if rc := receiptOf(t, res, a.ID); rc.Status != StatusRefused {
		t.Fatalf("= %+v", rc)
	}
}
