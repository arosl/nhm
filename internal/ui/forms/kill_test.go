package forms

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/ui/actions"
)

var errYes = fmt.Errorf("%w: kill closes the workspace and retires its agents: give --yes", lifecycle.ErrRefused)

func reviewedKill(t *testing.T, core actions.Core) *KillForm {
	t.Helper()
	f := NewKillForm(newRunner(t, "workspace.kill", core), ctxW)
	f.Update(do(t, f.StartReview()))
	return f
}

// The review fixes the kill's id; a wrong label starts nothing; the right
// one submits once, under the review's id.
func TestKillConfirmsByTypedLabel(t *testing.T) {
	fc := &fakeCore{killRes: lifecycle.KillResult{WorkspaceID: "w1", Refusals: []string{},
		Agents: []lifecycle.RetireResult{{MailName: "IcyKnoll", BindingID: "b1", DryRun: true}}}, killErr: errYes}
	f := reviewedKill(t, fc.core())
	if f.Phase() != Reviewed || f.OperationID() != "op-1" || f.Review().Targets[0].Name != "IcyKnoll" {
		t.Fatalf("review: %v %q %v", f.Phase(), f.OperationID(), f.Err())
	}
	if k := fc.kills[0]; k.Yes || k.OperationID != "" {
		t.Fatalf("the review asked %+v", k)
	}
	f.Type("proj-b")
	if f.Confirm() != nil || !f.Mismatch() || len(fc.kills) != 1 {
		t.Fatal("a wrong label started the kill")
	}
	f.Type(" proj-a ")
	fc.killRes, fc.killErr = lifecycle.KillResult{OperationID: "op-1", Closed: true, Refusals: []string{}}, nil
	cmd := f.Confirm()
	if cmd == nil || f.Confirm() != nil || f.Phase() != Running {
		t.Fatalf("confirm: %v", f.Phase())
	}
	f.Update(do(t, cmd))
	if k := fc.kills[1]; !k.Yes || k.OperationID != "op-1" || k.WorkspaceID != "w1" {
		t.Fatalf("the kill asked %+v", k)
	}
	if f.Phase() != Done || f.Result().Status != "closed" {
		t.Fatalf("end: %v %+v %v", f.Phase(), f.Result(), f.Err())
	}
}

// While the kill runs, the form reads its record.
func TestKillShowsProgress(t *testing.T) {
	fc := &fakeCore{killErr: errYes}
	core := fc.core()
	reads := 0
	core.Operation = func(_ context.Context, id string) (lifecycle.RecentOperation, error) {
		reads++
		if reads == 1 {
			return lifecycle.RecentOperation{}, lifecycle.ErrNoOperation
		}
		return lifecycle.RecentOperation{OperationID: id, Status: lifecycle.KillClosing,
			Targets: []lifecycle.OperationTarget{{Status: lifecycle.KillRetired}}}, nil
	}
	f := reviewedKill(t, core)
	f.Type("proj-a")
	if f.Confirm() == nil {
		t.Fatal("no confirm")
	}
	for i := 0; i < 2; i++ {
		if next := f.Update(do(t, f.Update(killTick{request: f.request}))); next == nil {
			t.Fatalf("read %d: no next read", i)
		}
	}
	if p := f.Progress(); p == nil || p.Status != lifecycle.KillClosing || len(p.Targets) != 1 {
		t.Fatalf("progress = %+v", p)
	}
	// The kill's answer ends the reads.
	f.Update(done{request: f.request, res: actions.Result{Status: "closed"}})
	if f.Phase() != Done || f.Update(killTick{request: f.request}) != nil {
		t.Fatalf("after the end: %v", f.Phase())
	}
}

func TestKillReviewRefusesOrFails(t *testing.T) {
	fc := &fakeCore{killRes: lifecycle.KillResult{Refusals: []string{"IcyKnoll: it is working"}},
		killErr: fmt.Errorf("%w: IcyKnoll: it is working", lifecycle.ErrRefused)}
	f := reviewedKill(t, fc.core())
	f.Type("proj-a")
	if f.Phase() != Refused || f.Confirm() != nil || f.Review().Lines[0] != "IcyKnoll: it is working" {
		t.Fatalf("refused review: %v %+v", f.Phase(), f.Review())
	}
	fc = &fakeCore{killErr: errors.New("herdr: socket closed")}
	if f := reviewedKill(t, fc.core()); f.Phase() != Failed {
		t.Fatalf("failed review: %v", f.Phase())
	}
	// A kill that stops partway is no success.
	fc = &fakeCore{killErr: errYes}
	f = reviewedKill(t, fc.core())
	f.Type("proj-a")
	fc.killRes, fc.killErr = lifecycle.KillResult{Refusals: []string{"BlueLake: it is working"}}, fmt.Errorf("%w: BlueLake", lifecycle.ErrRefused)
	f.Update(do(t, f.Confirm()))
	if f.Phase() != Failed || f.Result().Status != "refused" {
		t.Fatalf("a refused kill: %v %+v", f.Phase(), f.Result())
	}
}

// --no-beads changes the checks: it drops the review, its answer and its
// id.
func TestKillEditDropsTheReview(t *testing.T) {
	fc := &fakeCore{killErr: errYes}
	f := NewKillForm(newRunner(t, "workspace.kill", fc.core()), ctxW)
	old := f.StartReview()
	f.SetNoBeads(true)
	f.Update(do(t, old))
	if f.Phase() != Editing {
		t.Fatalf("a stale review was taken: %v", f.Phase())
	}
	f.Update(do(t, f.StartReview()))
	if f.OperationID() != "op-2" {
		t.Fatalf("an edited review kept id %q", f.OperationID())
	}
	f.Type("proj-a")
	f.Update(do(t, f.Confirm()))
	if k := fc.kills[len(fc.kills)-1]; !k.Yes || !k.NoBeads || k.OperationID != "op-2" {
		t.Fatalf("the kill asked %+v", k)
	}
}
