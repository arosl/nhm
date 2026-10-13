package forms

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/core/wake"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/runner"
)

// ErrNoRecipients refuses a wake with no recipient: the menu never reads
// an empty pick as "every agent".
var ErrNoRecipients = errors.New("pick at least one agent: a wake from the menu never means every agent")

// WakeForm is the menu's wake: pick the recipients, run one pass as a
// job, read the decision per agent.
type WakeForm struct {
	job        tracker
	recipients []ui.Target

	phase  Phase
	report wake.Report
	err    error
}

// NewWakeForm is a wake with no recipient yet.
func NewWakeForm(jobs Jobs) *WakeForm { return &WakeForm{job: tracker{jobs: jobs}} }

// Phase is where the form is.
func (f *WakeForm) Phase() Phase { return f.phase }

// Recipients are the picked agents.
func (f *WakeForm) Recipients() []ui.Target { return f.recipients }

// JobID is the job, once it started.
func (f *WakeForm) JobID() string { return f.job.id }

// Report is the pass's report: a decision per recipient, with its reason.
func (f *WakeForm) Report() wake.Report { return f.report }

// Err is the error the form ended with, or why it refused a confirm.
func (f *WakeForm) Err() error { return f.err }

// SetRecipients takes the picker's agents.
func (f *WakeForm) SetRecipients(ts []ui.Target) {
	if f.phase >= Running {
		return
	}
	f.recipients, f.err = slices.Clone(ts), nil
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
	f.phase, f.err = Running, nil
	return f.job.start(runner.VerbWake, runner.WakeRequest{Recipients: slices.Clone(f.recipients)})
}

// Update takes the job's messages.
func (f *WakeForm) Update(msg tea.Msg) tea.Cmd {
	if f.phase != Running {
		return nil
	}
	cmd, done := f.job.handle(msg)
	if !done {
		return cmd
	}
	f.phase = Failed
	if f.err = f.job.startErr; f.err != nil {
		return nil
	}
	r := *f.job.ended
	if len(r.Result) > 0 {
		if err := json.Unmarshal(r.Result, &f.report); err != nil {
			f.err = fmt.Errorf("wake's result cannot be read: %w", err)
			return nil
		}
	}
	if f.err = jobError(r); f.err == nil {
		f.phase = Done
	}
	return nil
}
