// Package forms holds the forms and views of nhm's menu as models
// (plan-v4 sections 2, 4 and 6), for the menu and the board: reap, kill,
// new, wake and recent operations. A form collects input and runs its
// action only through the action table (actions.Action.Do) with a guard:
// the review (a dry run, or the checks) under the review's operation id,
// then one submission under the same id. The CLI's Core runs a mutation
// as a job of its own, so closing the popup is not cancellation. Each
// answer carries the id of the request it answers, so a stale one is
// dropped; an edit drops the review; a form submits once.
package forms

import (
	"context"
	"sync/atomic"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/ui/actions"
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
	// Running: the request is submitted and its answer is not in.
	Running
	// Done: the core answered, with no error.
	Done
	// Failed: the review failed, or the submission ended with an error or
	// a refusal (Err says which; the result says per target).
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

// Runner is what a form needs to run its action: the row, the core of
// the surface's context, and the guard of the form.
type Runner struct {
	Action actions.Action
	Core   actions.Core
	Guard  *actions.Guard
}

// done is an action's answer to a form's request.
type done struct {
	request uint64
	res     actions.Result
	err     error
}

// do runs req through the action in a command.
func (r Runner) do(request uint64, req actions.Request) tea.Cmd {
	a, core := r.Action, r.Core
	return func() tea.Msg {
		res, err := a.Do(context.Background(), core, req)
		return done{request: request, res: res, err: err}
	}
}

// review fixes the operation id of the review of req, the request as it
// will be submitted (key); a dry run of it runs with ParamDryRun added,
// under that id.
func (r Runner) review(req actions.Request) (key, opID string, err error) {
	key = actions.ReviewKey(r.Action.ID, req)
	opID, err = r.Guard.Review(key)
	return key, opID, err
}

// withParam is req with one more param, on a copy of its params.
func withParam(req actions.Request, k, v string) actions.Request {
	p := make(map[string]string, len(req.Params)+1)
	for pk, pv := range req.Params {
		p[pk] = pv
	}
	p[k] = v
	req.Params = p
	return req
}
