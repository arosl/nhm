package actions

import (
	"context"
	"errors"
	"strings"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/store/history"
	"github.com/arosl/nhm/internal/ui"
)

// Errors of the agent handlers.
var (
	// ErrEmptyText: a send with no text is refused; it never falls back to
	// a template (plan-v4 section 4).
	ErrEmptyText = errors.New("actions: the text is empty")
	// ErrNoPane: none of the targets has a pane to send to.
	ErrNoPane = errors.New("actions: none of the agents has a pane now")
)

// Params of the agent handlers.
const (
	// ParamText is send's text, sent as typed.
	ParamText = "text"
	// ParamDryRun "1" runs retire's checks only: the review step.
	ParamDryRun = "dry_run"
	// ParamKeepIdentity "1": retire leaves the Agent Mail identity active.
	ParamKeepIdentity = "keep_identity"
	// ParamNoBeads "1": the agent worked no beads (a reviewer).
	ParamNoBeads = "no_beads"
	// ParamTemplate and ParamPrompt are recycle's kickoff: a template or
	// palette key, or text. ParamNoHandoff "1" skips the handoff.
	ParamTemplate  = "template"
	ParamPrompt    = "prompt"
	ParamNoHandoff = "no_handoff"
)

func on(p map[string]string, k string) bool { return p[k] == "1" }

// targetOf finds the request's target that a core result names: by
// binding id, else by pane; a result for neither keeps what it names.
func targetOf(ts []ui.Target, bindingID, paneID string) ui.Target {
	for _, t := range ts {
		if bindingID != "" && t.BindingID == bindingID || bindingID == "" && paneID != "" && t.PaneID == paneID {
			return t
		}
	}
	return ui.Target{BindingID: bindingID, PaneID: paneID}
}

// runSend sends the text as typed to the frozen targets: the selector
// names their panes and Expect their bindings and generations, so the
// core refuses a target that changed, or the agent now in its pane, under
// the target's lock and before any prompt. One receipt per target.
func runSend(ctx context.Context, core Core, req Request) (Result, error) {
	text := req.Params[ParamText]
	if strings.TrimSpace(text) == "" {
		return Result{}, ErrEmptyText
	}
	base, err := core.SendBase(ctx)
	if err != nil {
		return Result{}, err
	}
	// A target whose pane is gone is left to Expect, which refuses it;
	// with no pane at all the send is refused here, because an empty
	// selector means every bound agent.
	var panes []string
	for _, t := range req.Targets {
		if t.PaneID != "" {
			panes = append(panes, t.PaneID)
		}
	}
	if len(panes) == 0 {
		return Result{}, ErrNoPane
	}
	sreq := base
	sreq.Selector = send.Selector{Panes: panes}
	sreq.Expect = ui.Expect(req.Targets)
	sreq.Message, sreq.Template, sreq.Vars, sreq.FileContent, sreq.BeadID = text, "", nil, "", ""
	sreq.WhenAny, sreq.Force, sreq.DryRun = false, false, false
	sreq.OperationID = req.OperationID
	// The UI's sends are the palette's: its editor is the composer.
	sreq.Source = history.SourcePalette
	res, err := core.Send(ctx, sreq)
	out := Result{OperationID: res.OperationID, Warnings: res.Warnings}
	switch {
	case res.Replayed:
		out.Status = "replayed"
		out.Lines = append(out.Lines, "operation "+res.OperationID+" was claimed before; nothing was sent again")
	case res.InProgress:
		out.Status = "in progress"
	}
	for _, r := range res.Targets {
		out.Targets = append(out.Targets, TargetResult{Target: targetOf(req.Targets, r.BindingID, r.PaneID), Name: r.MailName,
			Status: r.Status, Reason: firstNonEmpty(r.Result.Reason, r.ErrorCode)})
	}
	if out.Status == "" {
		out.Status = sendStatus(res)
	}
	return out, err
}

// sendStatus sums the receipts up in their own words: one status when
// all agree, else "partial".
func sendStatus(res send.Result) string {
	counts := res.Counts()
	if len(counts) == 1 {
		for s := range counts {
			return s
		}
	}
	if len(counts) == 0 {
		return "no targets"
	}
	return "partial"
}

// runInterrupt sends each frozen target its kind's interrupt key; a
// binding that is gone or at another generation is refused, never
// replaced.
func runInterrupt(ctx context.Context, core Core, req Request) (Result, error) {
	res, err := core.Interrupt(ctx, ui.Expect(req.Targets), req.OperationID)
	out := Result{OperationID: res.OperationID, Status: res.Status}
	if res.Existing {
		out.Lines = append(out.Lines, "operation "+res.OperationID+" was claimed before ("+res.Status+"); nothing was sent again")
	}
	for _, t := range res.Targets {
		reason := t.Reason
		if t.Status == wait.InterruptSent && len(t.Keys) > 0 {
			reason = strings.Join(t.Keys, " ")
		}
		out.Targets = append(out.Targets, TargetResult{Target: targetOf(req.Targets, t.BindingID, t.PaneID), Name: t.MailName,
			Status: t.Status, Reason: reason})
	}
	return out, err
}

// runRetire retires the one frozen target: by binding id, at the
// generation it was reviewed at. ParamDryRun runs the checks only (the
// review); it claims no operation.
func runRetire(ctx context.Context, core Core, req Request) (Result, error) {
	t := req.Targets[0]
	rreq := lifecycle.RetireRequest{Target: t.BindingID, ExpectedGeneration: t.Generation, DryRun: on(req.Params, ParamDryRun),
		KeepIdentity: on(req.Params, ParamKeepIdentity), NoBeads: on(req.Params, ParamNoBeads), Reason: "retire"}
	if !rreq.DryRun {
		rreq.OperationID = req.OperationID
	}
	res, err := core.Retire(ctx, rreq)
	out := Result{OperationID: res.OperationID, Lines: res.Actions, Warnings: res.Warnings}
	// In the core's words: the operation's record says done or failed; a
	// refusal and the dry run record nothing, and nhm retire says
	// "refused" and "would retire" for them.
	tr := TargetResult{Target: t, Name: res.MailName}
	switch {
	case len(res.Refusals) > 0:
		out.Status, tr.Status, tr.Reason = "refused", "refused", strings.Join(res.Refusals, "; ")
	case res.DryRun:
		out.Status, tr.Status = "would retire", "would retire"
	case res.Done:
		out.Status, tr.Status = "done", "done"
	default:
		out.Status, tr.Status = "failed", "failed"
		if err != nil {
			tr.Reason = err.Error()
		}
	}
	out.Targets = []TargetResult{tr}
	return out, err
}

// runRecycle recycles the one frozen target: by binding id, at the
// generation it was reviewed at.
func runRecycle(ctx context.Context, core Core, req Request) (Result, error) {
	t := req.Targets[0]
	res, err := core.Recycle(ctx, lifecycle.RecycleRequest{Target: t.BindingID, ExpectedGeneration: t.Generation,
		Template: req.Params[ParamTemplate], Prompt: req.Params[ParamPrompt], NoHandoff: on(req.Params, ParamNoHandoff),
		OperationID: req.OperationID})
	out := Result{OperationID: res.OperationID, Status: res.Stage, Warnings: res.Warnings}
	tr := TargetResult{Target: t, Name: res.MailName, Status: res.Stage, Reason: res.Error}
	if res.KickoffOp != "" {
		out.Lines = append(out.Lines, "kickoff "+res.KickoffOp+": "+res.KickoffStatus)
	}
	out.Targets = []TargetResult{tr}
	return out, err
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
