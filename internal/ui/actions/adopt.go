package actions

import (
	"context"
	"errors"
	"strings"

	"github.com/arosl/nhm/internal/core/bindings"
)

// Params of adopt.
const (
	// ParamMailName is the Agent Mail identity the agent takes.
	ParamMailName = "mail_name"
	// ParamHandoffFrom is the binding whose identity the agent takes over
	// (nhm adopt --handoff-from); the generation goes up.
	ParamHandoffFrom = "handoff_from"
)

// ErrNoMailName: adopt needs the identity, or the binding it takes over.
var ErrNoMailName = errors.New("actions: adopt needs the Agent Mail name, or the binding it takes over")

// runAdopt binds the one unbound target to an identity. The target is
// its pane, its kind and its agent session as the review saw them: the
// core refuses, before it binds anything, when another agent is in the
// pane now (bindings.AdoptRequest.ExpectedSession).
func runAdopt(ctx context.Context, core Core, req Request) (Result, error) {
	t := req.Targets[0]
	name, from := strings.TrimSpace(req.Params[ParamMailName]), strings.TrimSpace(req.Params[ParamHandoffFrom])
	if name == "" && from == "" {
		return Result{}, ErrNoMailName
	}
	res, err := core.Adopt(ctx, bindings.AdoptRequest{MailName: name, HandoffFrom: from, PaneID: t.PaneID, Kind: t.Kind,
		ExpectedSession: t.Session, OperationID: req.OperationID})
	out := Result{OperationID: res.OperationID, Warnings: res.Warnings}
	// In the core's words, as the operation's record says them: refused
	// when another agent is in the pane, failed for every other error (an
	// identity in use, no agent in the pane), else done. From a job, every
	// refusal (exit 2) arrives as ErrAgentChanged, and stays refused.
	tr := TargetResult{Target: t, Name: firstNonEmpty(res.Binding.MailName, name)}
	switch {
	case errors.Is(err, bindings.ErrAgentChanged):
		out.Status, tr.Status, tr.Reason = "refused", "refused", err.Error()
	case err != nil:
		out.Status, tr.Status, tr.Reason = "failed", "failed", err.Error()
	default:
		out.Status, tr.Status = "done", "done"
		verb := "bound to"
		if !res.Created {
			verb = "took over"
		}
		out.Lines = append(out.Lines, res.Binding.MailName+" "+verb+" binding "+res.Binding.ID+" in pane "+res.Binding.PaneID)
	}
	out.Targets = []TargetResult{tr}
	return out, err
}

func needsAdopt(c Core) string {
	if c.Adopt == nil {
		return "this surface cannot adopt"
	}
	return ""
}
