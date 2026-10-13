package forms

import (
	"errors"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/actions"
)

// ErrNothingToReap is the review's answer when no agent would be retired.
var ErrNothingToReap = errors.New("no agent would be retired: nothing to confirm")

// ReapForm is the menu's reap (workspace.reap): a dry run, then a confirm
// that retires exactly the agents the dry run would retire, by binding
// and generation, one at a time through the core's checks (plan-v4
// section 4). An edit drops the review.
type ReapForm struct {
	run  Runner
	ctx  ui.Context
	idle time.Duration

	phase   Phase
	request uint64
	review  actions.Result
	set     []lifecycle.ReapTarget
	key     string
	result  actions.Result
	err     error
	// submitted is true once the confirm went out: the form never reviews
	// again after it.
	submitted bool
}

// NewReapForm is a reap of c's workspace with an idle limit (nhm reap's
// --idle).
func NewReapForm(run Runner, c ui.Context, idle time.Duration) *ReapForm {
	return &ReapForm{run: run, ctx: c, idle: idle}
}

// Phase is where the form is.
func (f *ReapForm) Phase() Phase { return f.phase }

// Idle is the idle limit.
func (f *ReapForm) Idle() time.Duration { return f.idle }

// Review is the dry run's answer: each agent with reap's action.
func (f *ReapForm) Review() actions.Result { return f.review }

// Set is the reviewed set a confirm retires.
func (f *ReapForm) Set() []lifecycle.ReapTarget { return f.set }

// Result is the reap's answer: each agent retired, refused or kept, with
// the core's reason.
func (f *ReapForm) Result() actions.Result { return f.result }

// Err is the error the form ended with.
func (f *ReapForm) Err() error { return f.err }

// CanReview reports whether the dry run may run again: until the confirm
// goes out, also after a review that failed or refused.
func (f *ReapForm) CanReview() bool { return !f.submitted && f.phase != Running }

// SetIdle changes the idle limit. It drops the review: run it again.
func (f *ReapForm) SetIdle(d time.Duration) {
	if !f.CanReview() || d == f.idle {
		return
	}
	f.idle = d
	f.drop()
}

func (f *ReapForm) drop() {
	f.request, f.review, f.set, f.key, f.err = 0, actions.Result{}, nil, "", nil
	f.phase = Editing
}

func (f *ReapForm) request0(set []lifecycle.ReapTarget) actions.Request {
	p := map[string]string{actions.ParamIdle: strconv.Itoa(int(f.idle / time.Minute))}
	if set != nil {
		p[actions.ParamOnly] = actions.OnlyParam(set)
	}
	return actions.Request{Context: f.ctx, Params: p}
}

// StartReview starts the dry run.
func (f *ReapForm) StartReview() tea.Cmd {
	if !f.CanReview() {
		return nil
	}
	f.drop()
	req := f.request0(nil)
	_, opID, err := f.run.review(req)
	if err != nil {
		f.phase, f.err = Failed, err
		return nil
	}
	req.OperationID = opID
	f.phase, f.request = Reviewing, nextRequest()
	return f.run.do(f.request, withParam(req, actions.ParamDryRun, "1"))
}

// Confirm retires the reviewed set. It submits once; a confirm with no
// passed review does nothing.
func (f *ReapForm) Confirm() tea.Cmd {
	if f.phase != Reviewed {
		return nil
	}
	opID, err := f.run.Guard.Submit(f.key)
	if err != nil {
		f.err = err
		return nil
	}
	req := f.request0(f.set)
	req.OperationID = opID
	f.phase, f.request, f.submitted = Running, nextRequest(), true
	return f.run.do(f.request, req)
}

// Update takes the action's answers.
func (f *ReapForm) Update(msg tea.Msg) tea.Cmd {
	m, ok := msg.(done)
	if !ok || m.request != f.request {
		return nil
	}
	switch f.phase {
	case Reviewing:
		f.review = m.res
		if m.err != nil {
			f.phase, f.err = Failed, m.err
			return nil
		}
		for _, t := range m.res.Targets {
			if t.Status == "would_retire" {
				f.set = append(f.set, lifecycle.ReapTarget{BindingID: t.Target.BindingID, Generation: t.Target.Generation})
			}
		}
		if len(f.set) == 0 {
			f.phase, f.err = Refused, ErrNothingToReap
			return nil
		}
		// The set is the request: its own review, its own operation id.
		key, _, err := f.run.review(f.request0(f.set))
		if err != nil {
			f.phase, f.err = Failed, err
			return nil
		}
		f.key, f.phase = key, Reviewed
	case Running:
		f.result, f.err, f.phase = m.res, m.err, Done
		if m.err != nil {
			f.phase = Failed
		}
	}
	return nil
}
