package forms

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/core/wake"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/actions"
)

// do runs a command as bubbletea would, in the test's goroutine.
func do(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command")
	}
	return cmd()
}

// fakeCore answers the workspace seams and records what they were asked.
type fakeCore struct {
	kills   []lifecycle.KillRequest
	killRes lifecycle.KillResult
	killErr error
	reaps   []lifecycle.ReapRequest
	reapOps []string
	reapRes [][]lifecycle.ReapItem
	reapErr error
	woken   [][]ui.Target
	wakeOps []string
	created []string
	newErr  error
	ops     []lifecycle.RecentOperation
}

func (f *fakeCore) core() actions.Core {
	return actions.Core{
		Kill: func(_ context.Context, r lifecycle.KillRequest) (lifecycle.KillResult, error) {
			f.kills = append(f.kills, r)
			return f.killRes, f.killErr
		},
		Reap: func(_ context.Context, r lifecycle.ReapRequest, op string) ([]lifecycle.ReapItem, error) {
			f.reaps, f.reapOps = append(f.reaps, r), append(f.reapOps, op)
			var items []lifecycle.ReapItem
			if len(f.reapRes) > 0 {
				items, f.reapRes = f.reapRes[0], f.reapRes[1:]
			}
			return items, f.reapErr
		},
		Wake: func(_ context.Context, ts []ui.Target, op string) (wake.Report, error) {
			f.woken, f.wakeOps = append(f.woken, ts), append(f.wakeOps, op)
			rep := wake.Report{}
			for _, t := range ts {
				rep.Decisions = append(rep.Decisions, wake.Decision{BindingID: t.BindingID, Action: wake.ActionNudged})
			}
			return rep, nil
		},
		New: func(_ context.Context, label, project, op string) (string, error) {
			f.created = append(f.created, label+"@"+project+"#"+op)
			return "w9", f.newErr
		},
	}
}

// newRunner is a form's runner for a row of the table, with a guard whose
// ids count up: op-1, op-2, …
func newRunner(t *testing.T, id string, core actions.Core) Runner {
	t.Helper()
	a, ok := actions.ByID(id)
	if !ok {
		t.Fatalf("no action %s", id)
	}
	n := 0
	return Runner{Action: a, Core: core, Guard: actions.NewGuard(func() (string, error) {
		n++
		return fmt.Sprintf("op-%d", n), nil
	})}
}

var ctxW = ui.Context{SocketPath: "/run/herdr.sock", WorkspaceID: "w1", WorkspaceLabel: "proj-a", Cwd: "/data/p"}

var reapReview = []lifecycle.ReapItem{
	{MailName: "IcyKnoll", BindingID: "b1", Generation: 3, Action: "would_retire"},
	{MailName: "BlueLake", BindingID: "b2", Generation: 1, Action: "kept", Reason: "the keep list holds it"},
	{MailName: "RedStone", BindingID: "b3", Generation: 7, Action: "would_retire"},
	{MailName: "GreenPike", BindingID: "b4", Generation: 2, Action: "refused", Reason: "it holds a reservation"},
}

// The confirm retires exactly the dry run's would_retire set, by binding
// and generation, once, under the set's own review id.
func TestReapConfirmsExactlyTheReviewedSet(t *testing.T) {
	fc := &fakeCore{reapRes: [][]lifecycle.ReapItem{reapReview, {{MailName: "IcyKnoll", BindingID: "b1", Action: "retired"},
		{MailName: "RedStone", BindingID: "b3", Action: "refused", Reason: "it changed since it was reviewed"}}}}
	f := NewReapForm(newRunner(t, "workspace.reap", fc.core()), ctxW, 30*time.Minute)
	if f.Confirm() != nil {
		t.Fatal("a confirm with no review submitted")
	}
	f.Update(do(t, f.StartReview()))
	if f.Phase() != Reviewed {
		t.Fatalf("phase = %v %v", f.Phase(), f.Err())
	}
	if r := fc.reaps[0]; !r.DryRun || r.Only != nil || r.WorkspaceID != "w1" || r.Idle != 30*time.Minute || fc.reapOps[0] != "op-1" {
		t.Fatalf("review asked %+v under %v", r, fc.reapOps)
	}
	want := []lifecycle.ReapTarget{{BindingID: "b1", Generation: 3}, {BindingID: "b3", Generation: 7}}
	if s := f.Set(); len(s) != 2 || s[0] != want[0] || s[1] != want[1] {
		t.Fatalf("set = %+v", s)
	}
	cmd := f.Confirm()
	if f.Confirm() != nil || f.Phase() != Running {
		t.Fatal("a second confirm submitted again")
	}
	f.Update(do(t, cmd))
	if r := fc.reaps[1]; r.DryRun || len(r.Only) != 2 || r.Only[0] != want[0] || r.Only[1] != want[1] || fc.reapOps[1] != "op-2" {
		t.Fatalf("reap asked %+v under %v", r, fc.reapOps)
	}
	if f.Phase() != Done || f.Result().Status != "partial" || f.Result().Targets[1].Reason != "it changed since it was reviewed" {
		t.Fatalf("result = %v %+v %v", f.Phase(), f.Result(), f.Err())
	}
	if f.Confirm() != nil || len(fc.reaps) != 2 {
		t.Fatal("a confirm after the end ran again")
	}
}

// An edit drops the review, and the old review's answer is dropped.
func TestReapEditDropsTheReview(t *testing.T) {
	fc := &fakeCore{reapRes: [][]lifecycle.ReapItem{reapReview, reapReview[:1]}}
	f := NewReapForm(newRunner(t, "workspace.reap", fc.core()), ctxW, 30*time.Minute)
	old := f.StartReview()
	f.SetIdle(time.Hour)
	if f.Phase() != Editing {
		t.Fatalf("phase after an edit = %v", f.Phase())
	}
	fresh := f.StartReview()
	f.Update(do(t, old))
	if f.Phase() != Reviewing || f.Set() != nil {
		t.Fatalf("a stale review was taken: %v %+v", f.Phase(), f.Set())
	}
	f.Update(do(t, fresh))
	if f.Phase() != Reviewed || len(f.Set()) != 1 || fc.reaps[1].Idle != time.Hour {
		t.Fatalf("fresh review: %v %+v", f.Phase(), f.Set())
	}
	f.SetIdle(2 * time.Hour)
	if f.Phase() != Editing || f.Confirm() != nil {
		t.Fatal("a confirm ran on a dropped review")
	}
}

func TestReapRefusalsAndFailures(t *testing.T) {
	fc := &fakeCore{reapRes: [][]lifecycle.ReapItem{reapReview[1:2]}}
	f := NewReapForm(newRunner(t, "workspace.reap", fc.core()), ctxW, time.Minute)
	f.Update(do(t, f.StartReview()))
	if f.Phase() != Refused || !errors.Is(f.Err(), ErrNothingToReap) || f.Confirm() != nil {
		t.Fatalf("nothing to reap: %v %v", f.Phase(), f.Err())
	}
	fc = &fakeCore{reapErr: errors.New("herdr: socket closed")}
	f = NewReapForm(newRunner(t, "workspace.reap", fc.core()), ctxW, time.Minute)
	f.Update(do(t, f.StartReview()))
	if f.Phase() != Failed || f.Confirm() != nil {
		t.Fatalf("a failed review: %v", f.Phase())
	}
	// The confirm's refusal (the owner, say) is the form's error.
	fc = &fakeCore{reapRes: [][]lifecycle.ReapItem{reapReview}}
	f = NewReapForm(newRunner(t, "workspace.reap", fc.core()), ctxW, time.Minute)
	f.Update(do(t, f.StartReview()))
	fc.reapErr = fmt.Errorf("%w: nhm does not own this server", lifecycle.ErrRefused)
	f.Update(do(t, f.Confirm()))
	if f.Phase() != Failed || !errors.Is(f.Err(), lifecycle.ErrRefused) {
		t.Fatalf("a refused reap: %v %v", f.Phase(), f.Err())
	}
}
