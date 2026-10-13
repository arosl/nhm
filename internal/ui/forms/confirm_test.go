package forms

import (
	"context"
	"testing"

	"github.com/arosl/nhm/internal/core/ensemble"
	"github.com/arosl/nhm/internal/ui/actions"
)

// The review is the dry run; the confirm submits once, under the review's
// id; a stopped run has no confirm.
func TestConfirmFormStopsARunOnce(t *testing.T) {
	var asked []string
	status := ensemble.RunRunning
	core := actions.Core{EnsembleStop: func(_ context.Context, run string, dryRun bool, opID string) (ensemble.Run, error) {
		if dryRun {
			asked = append(asked, "review "+run+" "+opID)
			return ensemble.Run{ID: "run-1", Status: status}, nil
		}
		asked = append(asked, "stop "+run+" "+opID)
		return ensemble.Run{ID: "run-1", Status: ensemble.RunStopped}, nil
	}}
	req := actions.Request{Context: ctxW, Params: map[string]string{actions.ParamRun: "run-1"}}
	f := NewConfirmForm(newRunner(t, "ensemble.stop", core), req)
	if f.Confirm() != nil {
		t.Fatal("a confirm with no review")
	}
	f.Update(do(t, f.StartReview()))
	if f.Phase() != Reviewed || len(asked) != 1 || asked[0] != "review run-1 op-1" {
		t.Fatalf("review: %v %v %v", f.Phase(), asked, f.Err())
	}
	cmd := f.Confirm()
	if f.Confirm() != nil {
		t.Fatal("a second confirm submitted")
	}
	f.Update(do(t, cmd))
	if f.Phase() != Done || asked[1] != "stop run-1 op-1" || f.Result().Status != ensemble.RunStopped {
		t.Fatalf("stop: %v %v %+v", f.Phase(), asked, f.Result())
	}
	if f.CanReview() || f.StartReview() != nil {
		t.Fatal("the form reviewed after its confirm went out")
	}
	status = ensemble.RunStopped
	g := NewConfirmForm(newRunner(t, "ensemble.stop", core), req)
	g.Update(do(t, g.StartReview()))
	if g.Phase() != Refused || g.Confirm() != nil {
		t.Fatalf("a stopped run: %v %v", g.Phase(), g.Err())
	}
	// A refused review runs again: nothing was submitted (the review's L4).
	status = ensemble.RunRunning
	if !g.CanReview() {
		t.Fatal("a refused review cannot run again")
	}
	g.Update(do(t, g.StartReview()))
	if g.Phase() != Reviewed {
		t.Fatalf("the review again: %v %v", g.Phase(), g.Err())
	}
}
