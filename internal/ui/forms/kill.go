package forms

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/actions"
)

// KillPoll is how often a running kill's record is read.
const KillPoll = time.Second

// KillForm is the menu's kill (workspace.kill): the checks, then the
// workspace's label typed as the confirmation, then the kill under the
// review's operation id. The kill runs as a job of its own, so it
// outlives the popup, also when it closes the popup's own workspace
// (plan-v4 section 4). While it runs, the form reads its record: each
// agent's receipt as it ends.
type KillForm struct {
	run     Runner
	ctx     ui.Context
	noBeads bool

	phase    Phase
	request  uint64
	review   actions.Result
	key      string
	opID     string
	typed    string
	mismatch bool
	progress *lifecycle.RecentOperation
	result   actions.Result
	err      error
	// submitted is true once the confirm went out: the form never reviews
	// again after it.
	submitted bool
}

type killTick struct{ request uint64 }

type killProgress struct {
	request uint64
	op      lifecycle.RecentOperation
	err     error
}

// NewKillForm is a kill of c's workspace.
func NewKillForm(run Runner, c ui.Context) *KillForm { return &KillForm{run: run, ctx: c} }

// Phase is where the form is.
func (f *KillForm) Phase() Phase { return f.phase }

// NoBeads reports whether every agent's retire waives the bead check.
func (f *KillForm) NoBeads() bool { return f.noBeads }

// Label is what the operator types: the workspace's label, else its id.
func (f *KillForm) Label() string { return actions.KillLabel(f.ctx) }

// Review is the checks' answer: each agent's retire checks, and the
// refusals (Lines).
func (f *KillForm) Review() actions.Result { return f.review }

// Mismatch is true after a confirm with a typed label that does not match.
func (f *KillForm) Mismatch() bool { return f.mismatch }

// OperationID is the kill's record id, once the review fixed it.
func (f *KillForm) OperationID() string { return f.opID }

// Progress is the running kill's record, as last read.
func (f *KillForm) Progress() *lifecycle.RecentOperation { return f.progress }

// Result is the kill's answer.
func (f *KillForm) Result() actions.Result { return f.result }

// Err is the error the form ended with.
func (f *KillForm) Err() error { return f.err }

// CanReview reports whether the checks may run again: until the confirm
// goes out, also after a review that failed or refused.
func (f *KillForm) CanReview() bool { return !f.submitted && f.phase != Running }

// SetNoBeads passes retire's --no-beads to every agent. It drops the
// review: run it again.
func (f *KillForm) SetNoBeads(v bool) {
	if !f.CanReview() || v == f.noBeads {
		return
	}
	f.noBeads = v
	f.request, f.review, f.key, f.opID, f.err, f.typed, f.mismatch = 0, actions.Result{}, "", "", nil, "", false
	f.phase = Editing
}

// Type sets the typed label.
func (f *KillForm) Type(s string) { f.typed, f.mismatch = s, false }

func (f *KillForm) request0() actions.Request {
	p := map[string]string{}
	if f.noBeads {
		p[actions.ParamNoBeads] = "1"
	}
	return actions.Request{Context: f.ctx, Params: p}
}

// StartReview runs the checks. The review is of the kill as it will be
// submitted, so its operation id is the kill's.
func (f *KillForm) StartReview() tea.Cmd {
	if !f.CanReview() {
		return nil
	}
	req := f.request0()
	key, opID, err := f.run.review(req)
	f.review, f.err, f.typed, f.mismatch = actions.Result{}, nil, "", false
	if err != nil {
		f.phase, f.err = Failed, err
		return nil
	}
	req.OperationID = opID
	f.key, f.opID, f.phase, f.request = key, opID, Reviewing, nextRequest()
	return f.run.do(f.request, withParam(req, actions.ParamDryRun, "1"))
}

// Confirm starts the kill when the typed label matches. It submits once.
func (f *KillForm) Confirm() tea.Cmd {
	if f.phase != Reviewed {
		return nil
	}
	if strings.TrimSpace(f.typed) != f.Label() {
		f.mismatch = true
		return nil
	}
	opID, err := f.run.Guard.Submit(f.key)
	if err != nil {
		f.err = err
		return nil
	}
	req := withParam(f.request0(), actions.ParamConfirm, strings.TrimSpace(f.typed))
	req.OperationID = opID
	f.opID, f.phase, f.request, f.submitted = opID, Running, nextRequest(), true
	return tea.Batch(f.run.do(f.request, req), f.tick())
}

func (f *KillForm) tick() tea.Cmd {
	if f.run.Core.Operation == nil {
		return nil
	}
	id := f.request
	return tea.Tick(KillPoll, func(time.Time) tea.Msg { return killTick{request: id} })
}

// Update takes the action's answers and the record's progress.
func (f *KillForm) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case done:
		if m.request != f.request {
			return nil
		}
		switch f.phase {
		case Reviewing:
			f.review = m.res
			switch {
			case m.err == nil && m.res.Status == "checked":
				f.phase = Reviewed
			case m.res.Status == "refused":
				f.phase, f.err = Refused, m.err
			default:
				f.phase, f.err = Failed, m.err
			}
		case Running:
			f.result, f.err, f.phase = m.res, m.err, Done
			if m.err != nil || m.res.Status != "closed" {
				f.phase = Failed
			}
		}
	case killTick:
		if m.request != f.request || f.phase != Running {
			return nil
		}
		read, rid, id := f.run.Core.Operation, f.request, f.opID
		return func() tea.Msg {
			op, err := read(context.Background(), id)
			return killProgress{request: rid, op: op, err: err}
		}
	case killProgress:
		if m.request != f.request || f.phase != Running {
			return nil
		}
		if m.err == nil {
			op := m.op
			f.progress = &op
		}
		return f.tick()
	}
	return nil
}
