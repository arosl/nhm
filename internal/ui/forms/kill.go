package forms

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/ui/runner"
)

// KillReader runs kill's checks and reads the kill's record, in the
// menu's context. Neither changes anything.
type KillReader interface {
	// ReviewKill is lifecycle.KillWith without Yes.
	ReviewKill(ctx context.Context, req lifecycle.KillRequest) (lifecycle.KillResult, error)
	// ReadOperation is lifecycle.ReadOperation: lifecycle.ErrNoOperation
	// until the kill claims its record.
	ReadOperation(ctx context.Context, id string) (lifecycle.RecentOperation, error)
}

// KillStatus is what is known of a started kill.
type KillStatus struct {
	// Operation is the kill's record; nil until the kill claims it, after
	// its checks pass again.
	Operation *lifecycle.RecentOperation
	// Ended is set once the kill's job ended.
	Ended *runner.Result
}

// KillForm is the menu's kill: the checks, then the workspace's label
// typed as the confirmation, then the kill as a job of its own, so it
// outlives the popup, also when it closes the popup's own workspace
// (plan-v4 section 4). The job's id is the kill's operation id: the form,
// and recent operations after the popup closed, follow its record.
type KillForm struct {
	read        KillReader
	jobs        Jobs
	workspaceID string
	label       string
	noBeads     bool

	phase    Phase
	request  uint64
	review   lifecycle.KillResult
	typed    string
	mismatch bool
	opID     string
	status   KillStatus
	err      error
}

type killReviewed struct {
	request uint64
	res     lifecycle.KillResult
	err     error
}

type killStarted struct {
	request uint64
	id      string
	err     error
}

type killTick struct{ request uint64 }

type killPolled struct {
	request uint64
	status  KillStatus
}

// NewKillForm is a kill of a workspace; label is the label the operator
// types (the workspace id when it has none).
func NewKillForm(read KillReader, jobs Jobs, workspaceID, label string) *KillForm {
	label = strings.TrimSpace(label)
	if label == "" {
		label = workspaceID
	}
	return &KillForm{read: read, jobs: jobs, workspaceID: workspaceID, label: label}
}

// Phase is where the form is.
func (f *KillForm) Phase() Phase { return f.phase }

// Label is what the operator must type.
func (f *KillForm) Label() string { return f.label }

// Review is the checks' result: each agent's retire dry run and the
// refusals.
func (f *KillForm) Review() lifecycle.KillResult { return f.review }

// Mismatch is true after a confirm with a typed label that does not match.
func (f *KillForm) Mismatch() bool { return f.mismatch }

// OperationID is the kill's record id (its job's id), once it started.
func (f *KillForm) OperationID() string { return f.opID }

// Status is what is known of the started kill.
func (f *KillForm) Status() KillStatus { return f.status }

// Err is the error the form ended with.
func (f *KillForm) Err() error { return f.err }

// SetNoBeads passes retire's --no-beads to every agent. It drops the
// review: run it again.
func (f *KillForm) SetNoBeads(v bool) {
	if f.phase >= Running || v == f.noBeads {
		return
	}
	f.noBeads = v
	f.request, f.review, f.err, f.typed, f.mismatch = 0, lifecycle.KillResult{}, nil, "", false
	f.phase = Editing
}

// Type sets the typed label.
func (f *KillForm) Type(s string) {
	f.typed, f.mismatch = s, false
}

// StartReview runs the checks.
func (f *KillForm) StartReview() tea.Cmd {
	if f.phase >= Running {
		return nil
	}
	f.phase, f.request, f.review, f.err, f.typed, f.mismatch = Reviewing, nextRequest(), lifecycle.KillResult{}, nil, "", false
	read, id := f.read, f.request
	req := lifecycle.KillRequest{WorkspaceID: f.workspaceID, NoBeads: f.noBeads}
	return func() tea.Msg {
		res, err := read.ReviewKill(context.Background(), req)
		return killReviewed{request: id, res: res, err: err}
	}
}

// Confirm starts the kill when the typed label matches. It submits once.
func (f *KillForm) Confirm() tea.Cmd {
	if f.phase != Reviewed {
		return nil
	}
	if strings.TrimSpace(f.typed) != f.label {
		f.mismatch = true
		return nil
	}
	f.phase, f.request = Running, nextRequest()
	jobs, rid := f.jobs, f.request
	req := runner.KillRequest{WorkspaceID: f.workspaceID, NoBeads: f.noBeads}
	return func() tea.Msg {
		id, err := jobs.Start(runner.VerbKill, req)
		return killStarted{request: rid, id: id, err: err}
	}
}

// Update takes the review, the job's start and the poll ticks.
func (f *KillForm) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case killReviewed:
		if m.request != f.request || f.phase != Reviewing {
			return nil
		}
		f.review = m.res
		switch {
		case len(m.res.Refusals) > 0:
			f.phase, f.err = Refused, m.err
		case m.err == nil, errors.Is(m.err, lifecycle.ErrRefused):
			// The checks pass: kill without Yes refuses only for the yes.
			f.phase = Reviewed
		default:
			f.phase, f.err = Failed, m.err
		}
	case killStarted:
		if m.request != f.request || f.phase != Running {
			return nil
		}
		f.opID = m.id
		if m.err != nil {
			f.phase, f.err = Failed, m.err
			return nil
		}
		return f.tick()
	case killTick:
		if m.request != f.request || f.phase != Running {
			return nil
		}
		jobs, read, rid, id := f.jobs, f.read, f.request, f.opID
		return func() tea.Msg {
			// The job's end first: kill writes its record before it ends,
			// so an ended kill with no record has none.
			var st KillStatus
			if r, ok, err := jobs.Result(id); ok && err == nil {
				st.Ended = &r
			}
			if op, err := read.ReadOperation(context.Background(), id); err == nil {
				st.Operation = &op
			}
			return killPolled{request: rid, status: st}
		}
	case killPolled:
		if m.request != f.request || f.phase != Running {
			return nil
		}
		f.status = m.status
		op, ended := m.status.Operation, m.status.Ended
		detail := ""
		if ended != nil {
			detail = ended.Error
		}
		switch {
		case op != nil && op.Completed != nil:
			f.phase = Done
			if op.Status != lifecycle.KillClosed {
				f.phase, f.err = Failed, fmt.Errorf("the kill stopped (%s): %s", op.Status, detail)
			}
		case ended != nil && op == nil:
			// It ended before its record: its checks refused this time, or
			// it could not run. Nothing was closed.
			f.phase, f.err = Failed, &JobError{Code: ended.Code, Message: "the kill ended with no record: " + detail}
		case ended != nil:
			f.phase, f.err = Failed, fmt.Errorf("the kill ended at %q with no final outcome: %s; recent operations shows its record",
				op.Status, detail)
		default:
			return f.tick()
		}
	}
	return nil
}

func (f *KillForm) tick() tea.Cmd {
	id := f.request
	return tea.Tick(JobPoll, func(time.Time) tea.Msg { return killTick{request: id} })
}
