package forms

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/ui/actions"
)

// ConfirmForm is a form whose review is its action's dry run and whose
// confirm submits the reviewed request once, under the review's
// operation id: ensemble stop.
type ConfirmForm struct {
	run Runner
	req actions.Request

	phase   Phase
	request uint64
	key     string
	review  actions.Result
	result  actions.Result
	err     error
	// submitted is true once the confirm went out: the form never reviews
	// again after it.
	submitted bool
}

// NewConfirmForm is a form for req, the request as it will be submitted.
func NewConfirmForm(run Runner, req actions.Request) *ConfirmForm {
	return &ConfirmForm{run: run, req: req}
}

// Phase is where the form is.
func (f *ConfirmForm) Phase() Phase { return f.phase }

// Action is the form's action.
func (f *ConfirmForm) Action() actions.Action { return f.run.Action }

// Review is the dry run's answer; Result the submission's.
func (f *ConfirmForm) Review() actions.Result { return f.review }
func (f *ConfirmForm) Result() actions.Result { return f.result }

// Err is the error the form ended with.
func (f *ConfirmForm) Err() error { return f.err }

// CanReview reports whether the dry run may run again: until the confirm
// goes out, also after a review that failed or refused.
func (f *ConfirmForm) CanReview() bool { return !f.submitted && f.phase != Running }

// StartReview runs the dry run under the review's operation id.
func (f *ConfirmForm) StartReview() tea.Cmd {
	if !f.CanReview() {
		return nil
	}
	key, opID, err := f.run.review(f.req)
	if err != nil {
		f.phase, f.err = Failed, err
		return nil
	}
	req := f.req
	req.OperationID = opID
	f.key, f.phase, f.request, f.err = key, Reviewing, nextRequest(), nil
	return f.run.do(f.request, withParam(req, actions.ParamDryRun, "1"))
}

// Confirm submits the reviewed request, once.
func (f *ConfirmForm) Confirm() tea.Cmd {
	if f.phase != Reviewed {
		return nil
	}
	opID, err := f.run.Guard.Submit(f.key)
	if err != nil {
		f.err = err
		return nil
	}
	req := f.req
	req.OperationID = opID
	f.phase, f.request, f.submitted = Running, nextRequest(), true
	return f.run.do(f.request, req)
}

// Update takes the action's answers.
func (f *ConfirmForm) Update(msg tea.Msg) tea.Cmd {
	m, ok := msg.(done)
	if !ok || m.request != f.request {
		return nil
	}
	switch f.phase {
	case Reviewing:
		f.review, f.err = m.res, m.err
		switch {
		case m.err == nil:
			f.phase = Reviewed
		case m.res.Status == "refused":
			f.phase = Refused
		default:
			f.phase = Failed
		}
	case Running:
		f.result, f.err, f.phase = m.res, m.err, Done
		if m.err != nil {
			f.phase = Failed
		}
	}
	return nil
}
