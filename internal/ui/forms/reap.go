package forms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/ui/runner"
)

// ReapReviewer runs reap's dry run (lifecycle.Reap with DryRun) in the
// menu's context, with nhm reap's keep list and mail check. It retires
// nobody.
type ReapReviewer interface {
	ReviewReap(ctx context.Context, req lifecycle.ReapRequest) ([]lifecycle.ReapItem, error)
}

// ReapForm is the menu's reap: a dry run, then a confirm that retires
// exactly the agents the dry run would retire, by binding and
// generation, one at a time through the core's checks (plan-v4 section
// 4). An edit drops the review.
type ReapForm struct {
	review      ReapReviewer
	job         tracker
	workspaceID string
	idle        time.Duration

	phase   Phase
	request uint64
	items   []lifecycle.ReapItem
	set     []lifecycle.ReapTarget
	result  []lifecycle.ReapItem
	err     error
}

type reapReviewed struct {
	request uint64
	items   []lifecycle.ReapItem
	err     error
}

// ErrNothingToReap is the review's answer when no agent would be retired.
var ErrNothingToReap = errors.New("no agent would be retired: nothing to confirm")

// NewReapForm is a reap of a workspace with an idle limit (nhm reap's
// --idle, in minutes there).
func NewReapForm(review ReapReviewer, jobs Jobs, workspaceID string, idle time.Duration) *ReapForm {
	return &ReapForm{review: review, job: tracker{jobs: jobs}, workspaceID: workspaceID, idle: idle}
}

// Phase is where the form is.
func (f *ReapForm) Phase() Phase { return f.phase }

// Idle is the idle limit.
func (f *ReapForm) Idle() time.Duration { return f.idle }

// Review is the dry run's items, as reap reported them.
func (f *ReapForm) Review() []lifecycle.ReapItem { return f.items }

// Set is the reviewed set a confirm retires.
func (f *ReapForm) Set() []lifecycle.ReapTarget { return f.set }

// JobID is the confirm's job, once it started.
func (f *ReapForm) JobID() string { return f.job.id }

// Result is the confirmed sweep's items: retired, refused or kept, each
// with the core's reason.
func (f *ReapForm) Result() []lifecycle.ReapItem { return f.result }

// Err is the error the form ended with.
func (f *ReapForm) Err() error { return f.err }

// SetIdle changes the idle limit. It drops the review: run it again.
func (f *ReapForm) SetIdle(d time.Duration) {
	if f.phase >= Running || d == f.idle {
		return
	}
	f.idle = d
	f.drop()
}

func (f *ReapForm) drop() {
	f.request, f.items, f.set, f.err = 0, nil, nil, nil
	f.phase = Editing
}

// StartReview starts the dry run.
func (f *ReapForm) StartReview() tea.Cmd {
	if f.phase >= Running {
		return nil
	}
	f.drop()
	f.phase, f.request = Reviewing, nextRequest()
	review, id := f.review, f.request
	req := lifecycle.ReapRequest{Idle: f.idle, WorkspaceID: f.workspaceID, DryRun: true}
	return func() tea.Msg {
		items, err := review.ReviewReap(context.Background(), req)
		return reapReviewed{request: id, items: items, err: err}
	}
}

// Confirm starts the job that retires the reviewed set. It submits once;
// a confirm with no passed review does nothing.
func (f *ReapForm) Confirm() tea.Cmd {
	if f.phase != Reviewed || len(f.set) == 0 {
		return nil
	}
	f.phase = Running
	return f.job.start(runner.VerbReap, runner.ReapRequest{WorkspaceID: f.workspaceID, Idle: f.idle, Only: slices.Clone(f.set)})
}

// Update takes the review's answer and the job's messages.
func (f *ReapForm) Update(msg tea.Msg) tea.Cmd {
	if m, ok := msg.(reapReviewed); ok {
		if m.request != f.request || f.phase != Reviewing {
			return nil
		}
		f.items = m.items
		if m.err != nil {
			f.phase, f.err = Failed, m.err
			return nil
		}
		for _, it := range m.items {
			if it.Action == "would_retire" {
				f.set = append(f.set, lifecycle.ReapTarget{BindingID: it.BindingID, Generation: it.Generation})
			}
		}
		f.phase = Reviewed
		if len(f.set) == 0 {
			f.phase, f.err = Refused, ErrNothingToReap
		}
		return nil
	}
	if f.phase != Running {
		return nil
	}
	cmd, done := f.job.handle(msg)
	if done {
		f.finish()
	}
	return cmd
}

func (f *ReapForm) finish() {
	f.phase = Failed
	if f.job.startErr != nil {
		f.err = f.job.startErr
		return
	}
	r := *f.job.ended
	var out struct {
		Agents []lifecycle.ReapItem `json:"agents"`
	}
	if len(r.Result) > 0 {
		if err := json.Unmarshal(r.Result, &out); err != nil {
			f.err = fmt.Errorf("the reap's result cannot be read: %w", err)
			return
		}
	}
	f.result = out.Agents
	if f.err = jobError(r); f.err == nil {
		f.phase = Done
	}
}
