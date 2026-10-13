package forms

import (
	"errors"
	"slices"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/actions"
)

// ErrNoRecipients refuses a wake with no recipient: the menu never reads
// an empty pick as "every agent".
var ErrNoRecipients = errors.New("pick at least one agent: a wake from the menu never means every agent")

// WakeForm is the menu's wake (workspace.wake): the picked agents, one
// pass, a decision per agent.
type WakeForm struct {
	run        Runner
	ctx        ui.Context
	recipients []ui.Target

	phase   Phase
	request uint64
	result  actions.Result
	err     error
}

// NewWakeForm is a wake in c with no recipient yet.
func NewWakeForm(run Runner, c ui.Context) *WakeForm { return &WakeForm{run: run, ctx: c} }

// Phase is where the form is.
func (f *WakeForm) Phase() Phase { return f.phase }

// Recipients are the picked agents.
func (f *WakeForm) Recipients() []ui.Target { return f.recipients }

// Result is the pass's answer: each agent's decision, with its reason.
func (f *WakeForm) Result() actions.Result { return f.result }

// Err is the error the form ended with, or why it refused a confirm.
func (f *WakeForm) Err() error { return f.err }

// SetRecipients takes the picker's agents.
func (f *WakeForm) SetRecipients(ts []ui.Target) {
	if f.phase < Running {
		f.recipients, f.err = slices.Clone(ts), nil
	}
}

// Confirm runs the pass once. With no recipient it refuses.
func (f *WakeForm) Confirm() tea.Cmd {
	if f.phase >= Running {
		return nil
	}
	if len(f.recipients) == 0 {
		f.err = ErrNoRecipients
		return nil
	}
	req := actions.Request{Context: f.ctx, Targets: slices.Clone(f.recipients)}
	key, _, err := f.run.review(req)
	if err == nil {
		req.OperationID, err = f.run.Guard.Submit(key)
	}
	if err != nil {
		f.err = err
		return nil
	}
	f.phase, f.request, f.err = Running, nextRequest(), nil
	return f.run.submit(f.request, key, req)
}

// Update takes the action's answer.
func (f *WakeForm) Update(msg tea.Msg) tea.Cmd {
	if m, ok := msg.(done); ok && m.request == f.request && f.phase == Running {
		f.result, f.err, f.phase = m.res, m.err, Done
		if m.err != nil {
			f.phase = Failed
		}
	}
	return nil
}
