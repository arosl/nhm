package forms

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/ui/runner"
)

type fakeKillReader struct {
	review    lifecycle.KillResult
	reviewErr error
	ops       []*lifecycle.RecentOperation
	read      []string
}

func (f *fakeKillReader) ReviewKill(context.Context, lifecycle.KillRequest) (lifecycle.KillResult, error) {
	return f.review, f.reviewErr
}

func (f *fakeKillReader) ReadOperation(_ context.Context, id string) (lifecycle.RecentOperation, error) {
	f.read = append(f.read, id)
	var op *lifecycle.RecentOperation
	if len(f.ops) > 0 {
		op = f.ops[0]
		if len(f.ops) > 1 {
			f.ops = f.ops[1:]
		}
	}
	if op == nil {
		return lifecycle.RecentOperation{}, lifecycle.ErrNoOperation
	}
	return *op, nil
}

var errYes = fmt.Errorf("%w: kill closes the workspace and retires its agents: give --yes", lifecycle.ErrRefused)

func reviewedKill(t *testing.T, read *fakeKillReader, jobs *fakeJobs) *KillForm {
	t.Helper()
	f := NewKillForm(read, jobs, "w1", "proj-a")
	f.Update(do(t, f.StartReview()))
	return f
}

func pollKill(t *testing.T, f *KillForm) {
	t.Helper()
	f.Update(do(t, f.Update(killTick{request: f.request})))
}

// A wrong label starts nothing; the right one starts one kill job, whose
// id is the kill's operation id.
func TestKillConfirmsByTypedLabel(t *testing.T) {
	read, jobs := &fakeKillReader{review: lifecycle.KillResult{WorkspaceID: "w1", Refusals: []string{}}, reviewErr: errYes}, &fakeJobs{}
	f := reviewedKill(t, read, jobs)
	if f.Phase() != Reviewed {
		t.Fatalf("phase = %v %v", f.Phase(), f.Err())
	}
	f.Type("proj-b")
	if f.Confirm() != nil || !f.Mismatch() || len(jobs.started) != 0 {
		t.Fatal("a wrong label started the kill")
	}
	f.Type(" proj-a ")
	cmd := f.Confirm()
	if cmd == nil || f.Confirm() != nil {
		t.Fatalf("confirm: %v", f.Phase())
	}
	if f.Update(do(t, cmd)) == nil || f.OperationID() != "op-1" {
		t.Fatalf("no poll after the start: %q", f.OperationID())
	}
	if len(jobs.started) != 1 || jobs.started[0].verb != runner.VerbKill ||
		jobs.started[0].req != (runner.KillRequest{WorkspaceID: "w1"}) {
		t.Fatalf("started = %+v", jobs.started)
	}
}

// The form follows the record until it is final.
func TestKillFollowsItsRecord(t *testing.T) {
	now := time.Now()
	closing := &lifecycle.RecentOperation{OperationID: "op-1", Status: lifecycle.KillClosing}
	closed := &lifecycle.RecentOperation{OperationID: "op-1", Status: lifecycle.KillClosed, Completed: &now}
	read, jobs := &fakeKillReader{reviewErr: errYes, ops: []*lifecycle.RecentOperation{nil, closing, closed}}, &fakeJobs{}
	f := reviewedKill(t, read, jobs)
	f.Type("proj-a")
	f.Update(do(t, f.Confirm()))
	for i := 0; i < 3; i++ {
		if f.Phase() != Running {
			t.Fatalf("poll %d: phase %v", i, f.Phase())
		}
		pollKill(t, f)
	}
	if f.Phase() != Done || f.Err() != nil || f.Status().Operation.Status != lifecycle.KillClosed {
		t.Fatalf("end: %v %v", f.Phase(), f.Err())
	}
	if strings.Join(read.read, ",") != "op-1,op-1,op-1" {
		t.Fatalf("read %v", read.read)
	}
	// A late tick of the finished kill does nothing.
	if f.Update(killTick{request: f.request}) != nil {
		t.Fatal("a tick after the end polled again")
	}
}

func TestKillEndsWithoutAFinalRecord(t *testing.T) {
	stopped := time.Now()
	cases := []struct {
		name string
		op   *lifecycle.RecentOperation
		code int
		msg  string
		want string
	}{
		{"refused at its own checks", nil, runner.CodeRefused, "refused: IcyKnoll: it is working", "no record"},
		{"stopped partway", &lifecycle.RecentOperation{Status: lifecycle.KillStopped, Completed: &stopped}, runner.CodeRefused,
			"BlueLake: refused", "stopped"},
		{"died partway", &lifecycle.RecentOperation{Status: lifecycle.KillDispatching}, runner.CodeError, "", "no final outcome"},
	}
	for _, c := range cases {
		read, jobs := &fakeKillReader{reviewErr: errYes, ops: []*lifecycle.RecentOperation{c.op}}, &fakeJobs{}
		f := reviewedKill(t, read, jobs)
		f.Type("proj-a")
		f.Update(do(t, f.Confirm()))
		jobs.end(t, "op-1", c.code, c.msg, nil)
		pollKill(t, f)
		if f.Phase() != Failed || f.Err() == nil || !strings.Contains(f.Err().Error(), c.want) {
			t.Errorf("%s: %v %v", c.name, f.Phase(), f.Err())
		}
	}
}

func TestKillReviewRefusesOrFails(t *testing.T) {
	refused := &fakeKillReader{review: lifecycle.KillResult{Refusals: []string{"IcyKnoll: it is working"}},
		reviewErr: fmt.Errorf("%w: IcyKnoll: it is working", lifecycle.ErrRefused)}
	f := reviewedKill(t, refused, &fakeJobs{})
	f.Type("proj-a")
	if f.Phase() != Refused || f.Confirm() != nil {
		t.Fatalf("refused review: %v", f.Phase())
	}
	if f := reviewedKill(t, &fakeKillReader{reviewErr: errors.New("herdr: socket closed")}, &fakeJobs{}); f.Phase() != Failed {
		t.Fatalf("failed review: %v", f.Phase())
	}
	f = reviewedKill(t, &fakeKillReader{reviewErr: errYes}, &fakeJobs{startErr: errors.New("runner: kill did not start")})
	f.Type("proj-a")
	f.Update(do(t, f.Confirm()))
	if f.Phase() != Failed || !strings.Contains(f.Err().Error(), "did not start") {
		t.Fatalf("failed start: %v %v", f.Phase(), f.Err())
	}
}

// --no-beads changes the checks: it drops the review and its answer.
func TestKillEditDropsTheReview(t *testing.T) {
	jobs := &fakeJobs{}
	f := NewKillForm(&fakeKillReader{reviewErr: errYes}, jobs, "w1", "  ")
	if f.Label() != "w1" {
		t.Fatalf("a blank label = %q, not the workspace id", f.Label())
	}
	old := f.StartReview()
	f.SetNoBeads(true)
	f.Update(do(t, old))
	if f.Phase() != Editing {
		t.Fatalf("a stale review was taken: %v", f.Phase())
	}
	f.Update(do(t, f.StartReview()))
	f.Type("w1")
	f.Update(do(t, f.Confirm()))
	if len(jobs.started) != 1 || !jobs.started[0].req.(runner.KillRequest).NoBeads {
		t.Fatalf("started = %+v", jobs.started)
	}
}
