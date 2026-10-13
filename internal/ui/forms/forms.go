// Package forms holds the forms and views of nhm's menu as models
// (plan-v4 sections 4 and 6), for the menu and the board: reap, kill,
// new, wake and recent operations. A form's review (a dry run, or the
// checks) only reads, and runs in the popup. Its confirm runs as a job of
// its own (package runner), so closing the popup is not cancellation: the
// form starts the job with the exact request it reviewed, then reads how
// it ended. Each answer carries the id of the request it answers, so a
// stale one is dropped, and a form submits once.
package forms

import (
	"fmt"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/ui/runner"
)

// Phase is where a form is.
type Phase int

// Phases.
const (
	// Editing: the operator fills the form; nothing is reviewed.
	Editing Phase = iota
	// Reviewing: the review (a dry run, or the checks) runs.
	Reviewing
	// Reviewed: the review passed; the confirm is open.
	Reviewed
	// Refused: the review refused, or found nothing to do; there is no
	// confirm.
	Refused
	// Running: the job is started and has not ended.
	Running
	// Done: the job ended, and did what it was asked.
	Done
	// Failed: the review failed, the job did not start, or it ended with
	// an error or a refusal (Err says which).
	Failed
)

var phaseNames = [...]string{"editing", "reviewing", "reviewed", "refused", "running", "done", "failed"}

func (p Phase) String() string {
	if p < 0 || int(p) >= len(phaseNames) {
		return "unknown"
	}
	return phaseNames[p]
}

// lastRequest numbers the requests of every form in the process, so an
// answer to a form that was closed never matches a newer form.
var lastRequest atomic.Uint64

func nextRequest() uint64 { return lastRequest.Add(1) }

// Jobs starts a form's job and reads how it ended: runner.Client.
type Jobs interface {
	Start(verb string, req any) (id string, err error)
	Result(id string) (runner.Result, bool, error)
}

// JobPoll is how often a form reads a running job.
const JobPoll = time.Second

type jobStarted struct {
	request uint64
	id      string
	err     error
}

type jobTick struct{ request uint64 }

type jobRead struct {
	request uint64
	result  runner.Result
	ended   bool
}

// tracker follows a form's one job.
type tracker struct {
	jobs    Jobs
	request uint64
	id      string
	// startErr is set when the job did not start; ended when it ended.
	startErr error
	ended    *runner.Result
}

// start starts the job; it runs once per tracker.
func (t *tracker) start(verb string, req any) tea.Cmd {
	if t.request != 0 {
		return nil
	}
	t.request = nextRequest()
	jobs, rid := t.jobs, t.request
	return func() tea.Msg {
		id, err := jobs.Start(verb, req)
		return jobStarted{request: rid, id: id, err: err}
	}
}

func (t *tracker) tick() tea.Cmd {
	rid := t.request
	return tea.Tick(JobPoll, func(time.Time) tea.Msg { return jobTick{request: rid} })
}

// handle takes the job's messages. done is true once the job ended or
// did not start; other messages give (nil, false).
func (t *tracker) handle(msg tea.Msg) (cmd tea.Cmd, done bool) {
	switch m := msg.(type) {
	case jobStarted:
		if m.request != t.request || t.id != "" || t.startErr != nil {
			return nil, false
		}
		t.id = m.id
		if m.err != nil {
			t.startErr = m.err
			return nil, true
		}
		return t.tick(), false
	case jobTick:
		if m.request != t.request || t.ended != nil || t.id == "" {
			return nil, false
		}
		jobs, rid, id := t.jobs, t.request, t.id
		return func() tea.Msg {
			r, ok, err := jobs.Result(id)
			return jobRead{request: rid, result: r, ended: ok && err == nil}
		}, false
	case jobRead:
		if m.request != t.request || t.ended != nil {
			return nil, false
		}
		if !m.ended {
			return t.tick(), false
		}
		r := m.result
		t.ended = &r
		return nil, true
	}
	return nil, false
}

// jobError is the error a job ended with, or nil when it did what it was
// asked.
func jobError(r runner.Result) error {
	if r.Code == runner.CodeOK && r.Error == "" {
		return nil
	}
	return &JobError{Code: r.Code, Message: r.Error}
}

// JobError is how a job ended when it did not do what it was asked: Code
// is the CLI verb's exit code (2 refused, 1 an error).
type JobError struct {
	Code    int
	Message string
}

func (e *JobError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("the job ended with code %d", e.Code)
	}
	return e.Message
}

// Refused reports a refusal by a rule, not an error.
func (e *JobError) Refused() bool { return e.Code == runner.CodeRefused }
