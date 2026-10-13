package forms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/ui/runner"
)

// do runs a command as bubbletea would, in the test's goroutine.
func do(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command")
	}
	return cmd()
}

type startedJob struct {
	id   string
	verb string
	req  any
}

// fakeJobs records the jobs a form starts and answers their ends.
type fakeJobs struct {
	started  []startedJob
	startErr error
	results  map[string]runner.Result
}

func (f *fakeJobs) Start(verb string, req any) (string, error) {
	id := fmt.Sprintf("op-%d", len(f.started)+1)
	f.started = append(f.started, startedJob{id: id, verb: verb, req: req})
	return id, f.startErr
}

func (f *fakeJobs) Result(id string) (runner.Result, bool, error) {
	r, ok := f.results[id]
	return r, ok, nil
}

func (f *fakeJobs) end(t *testing.T, id string, code int, msg string, result any) {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if f.results == nil {
		f.results = map[string]runner.Result{}
	}
	f.results[id] = runner.Result{ID: id, Code: code, Error: msg, Result: data, EndedAt: time.Now()}
}

// poll delivers one job tick to a form and the read it starts.
func poll(t *testing.T, update func(tea.Msg) tea.Cmd, request uint64) tea.Cmd {
	t.Helper()
	return update(do(t, update(jobTick{request: request})))
}

type fakeReviewer struct {
	reqs    []lifecycle.ReapRequest
	answers [][]lifecycle.ReapItem
	err     error
}

func (f *fakeReviewer) ReviewReap(_ context.Context, req lifecycle.ReapRequest) ([]lifecycle.ReapItem, error) {
	f.reqs = append(f.reqs, req)
	var items []lifecycle.ReapItem
	if len(f.answers) > 0 {
		items, f.answers = f.answers[0], f.answers[1:]
	}
	return items, f.err
}

var reapReview = []lifecycle.ReapItem{
	{MailName: "IcyKnoll", BindingID: "b1", Generation: 3, Action: "would_retire"},
	{MailName: "BlueLake", BindingID: "b2", Generation: 1, Action: "kept", Reason: "the keep list holds it"},
	{MailName: "RedStone", BindingID: "b3", Generation: 7, Action: "would_retire"},
	{MailName: "GreenPike", BindingID: "b4", Generation: 2, Action: "refused", Reason: "it holds a reservation"},
}

// The confirm starts one job with exactly the dry run's would_retire set,
// by binding and generation, and shows how it ended.
func TestReapConfirmsExactlyTheReviewedSet(t *testing.T) {
	review, jobs := &fakeReviewer{answers: [][]lifecycle.ReapItem{reapReview}}, &fakeJobs{}
	f := NewReapForm(review, jobs, "w1", 30*time.Minute)
	if f.Confirm() != nil {
		t.Fatal("a confirm with no review submitted")
	}
	f.Update(do(t, f.StartReview()))
	if f.Phase() != Reviewed {
		t.Fatalf("phase = %v %v", f.Phase(), f.Err())
	}
	if r := review.reqs[0]; !r.DryRun || r.Only != nil || r.WorkspaceID != "w1" || r.Idle != 30*time.Minute {
		t.Fatalf("review request = %+v", r)
	}
	want := []lifecycle.ReapTarget{{BindingID: "b1", Generation: 3}, {BindingID: "b3", Generation: 7}}
	cmd := f.Confirm()
	if f.Confirm() != nil || f.Phase() != Running {
		t.Fatal("a second confirm submitted again")
	}
	if f.Update(do(t, cmd)) == nil || f.JobID() != "op-1" {
		t.Fatalf("no poll after the start: %q", f.JobID())
	}
	req := jobs.started[0].req.(runner.ReapRequest)
	if jobs.started[0].verb != runner.VerbReap || len(jobs.started) != 1 || req.WorkspaceID != "w1" || len(req.Only) != 2 ||
		req.Only[0] != want[0] || req.Only[1] != want[1] {
		t.Fatalf("job = %+v", jobs.started)
	}
	if poll(t, f.Update, f.job.request) == nil || f.Phase() != Running {
		t.Fatal("a running job did not poll again")
	}
	jobs.end(t, "op-1", 0, "", map[string]any{"agents": []lifecycle.ReapItem{{MailName: "IcyKnoll", Action: "retired"},
		{MailName: "RedStone", Action: "refused", Reason: "it changed since it was reviewed"}}})
	if poll(t, f.Update, f.job.request) != nil {
		t.Fatal("an ended job polled again")
	}
	if f.Phase() != Done || len(f.Result()) != 2 || f.Result()[1].Action != "refused" {
		t.Fatalf("after the job: %v %+v %v", f.Phase(), f.Result(), f.Err())
	}
}

// An edit drops the review, and the old review's answer is dropped.
func TestReapEditDropsTheReview(t *testing.T) {
	review := &fakeReviewer{answers: [][]lifecycle.ReapItem{reapReview, reapReview[:1]}}
	f := NewReapForm(review, &fakeJobs{}, "w1", 30*time.Minute)
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
	if f.Phase() != Reviewed || len(f.Set()) != 1 || review.reqs[1].Idle != time.Hour {
		t.Fatalf("fresh review: %v %+v", f.Phase(), f.Set())
	}
	f.SetIdle(2 * time.Hour)
	if f.Phase() != Editing || f.Confirm() != nil {
		t.Fatal("a confirm ran on a dropped review")
	}
}

func TestReapRefusalsAndFailures(t *testing.T) {
	f := NewReapForm(&fakeReviewer{answers: [][]lifecycle.ReapItem{reapReview[1:2]}}, &fakeJobs{}, "w1", time.Minute)
	f.Update(do(t, f.StartReview()))
	if f.Phase() != Refused || !errors.Is(f.Err(), ErrNothingToReap) || f.Confirm() != nil {
		t.Fatalf("nothing to reap: %v %v", f.Phase(), f.Err())
	}
	f = NewReapForm(&fakeReviewer{err: errors.New("herdr: socket closed")}, &fakeJobs{}, "w1", time.Minute)
	f.Update(do(t, f.StartReview()))
	if f.Phase() != Failed || f.Confirm() != nil {
		t.Fatalf("a failed review: %v", f.Phase())
	}
	// A job that does not start, and one the owner check refuses.
	for _, c := range []struct {
		startErr error
		code     int
	}{{errors.New("runner: kill did not start"), 0}, {nil, runner.CodeRefused}} {
		jobs := &fakeJobs{startErr: c.startErr}
		f = NewReapForm(&fakeReviewer{answers: [][]lifecycle.ReapItem{reapReview}}, jobs, "w1", time.Minute)
		f.Update(do(t, f.StartReview()))
		f.Update(do(t, f.Confirm()))
		if c.startErr == nil {
			jobs.end(t, "op-1", c.code, "nhm does not own this server", nil)
			poll(t, f.Update, f.job.request)
		}
		var je *JobError
		if f.Phase() != Failed || f.Err() == nil || c.code != 0 && (!errors.As(f.Err(), &je) || !je.Refused()) {
			t.Fatalf("%+v: %v %v", c, f.Phase(), f.Err())
		}
	}
}
