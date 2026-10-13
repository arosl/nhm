package actions

import (
	"context"
	"errors"
	"testing"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/ui"
)

// lostErr is a lost answer as the CLI's runner reports it: an error of
// its own that matches ErrNoAnswer.
type lostErr struct{ id string }

func (e lostErr) Error() string        { return "operation " + e.id + ": no answer" }
func (e lostErr) Is(target error) bool { return target == ErrNoAnswer }

// TestNoAnswerIsUnknown: a lost answer is unknown, for the operation and
// each target, never failed; the error stays, and so does the id.
func TestNoAnswerIsUnknown(t *testing.T) {
	f := &fakeCore{err: lostErr{id: "op-9"}, retireRes: lifecycle.RetireResult{OperationID: "op-9"}}
	res, err := mustAction(t, "agent.retire").Do(context.Background(), f.core(),
		Request{Context: ctxWS, Targets: []ui.Target{bound1}, OperationID: "op-9"})
	if !errors.Is(err, ErrNoAnswer) || !NoAnswer(err) || res.Status != StatusUnknown || res.OperationID != "op-9" ||
		len(res.Targets) != 1 || res.Targets[0].Status != StatusUnknown || res.Targets[0].Target != bound1 {
		t.Fatalf("retire, lost = %+v %v", res, err)
	}
	// The wait's own deadline counts too; the request's id names it.
	f = &fakeCore{err: context.DeadlineExceeded, interrupt: wait.InterruptResult{}}
	res, err = mustAction(t, "agent.interrupt").Do(context.Background(), f.core(),
		Request{Context: ctxWS, Targets: []ui.Target{bound1, bound2}, OperationID: "op-10"})
	if !NoAnswer(err) || res.Status != StatusUnknown || res.OperationID != "op-10" || len(res.Targets) != 2 {
		t.Fatalf("interrupt, deadline = %+v %v", res, err)
	}
	// A refusal is not a lost answer.
	if NoAnswer(lifecycle.ErrRefused) || NoAnswer(nil) {
		t.Fatal("NoAnswer of a refusal")
	}
}
