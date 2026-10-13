package actions

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/arosl/nhm/internal/core/ensemble"
)

// ParamRun is the ensemble run (its id, or its session) that stop stops.
const ParamRun = "run"

// ErrRunStopped: the review found the run stopped already; there is
// nothing to confirm.
var ErrRunStopped = errors.New("actions: the run is stopped already")

// runEnsembleStop is stop's review (ParamDryRun: the run as it is) and
// stop. A stopped run keeps every result and retires no agent.
func runEnsembleStop(ctx context.Context, core Core, req Request) (Result, error) {
	id := strings.TrimSpace(req.Params[ParamRun])
	if id == "" {
		return Result{Status: "refused"}, errors.New("actions: pick the run to stop")
	}
	review := on(req.Params, ParamDryRun)
	r, err := core.EnsembleStop(ctx, id, review, req.OperationID)
	out := Result{OperationID: req.OperationID}
	if r.ID != "" {
		out.Lines = append(out.Lines, fmt.Sprintf("run %s: preset %s, %s, session %s, %d angle(s)", r.ID, r.Preset, r.Status,
			r.Session, len(r.Angles)))
	}
	for _, a := range r.Angles {
		name := a.MailName
		if name == "" {
			name = fmt.Sprintf("angle %d", a.Index)
		}
		out.Targets = append(out.Targets, TargetResult{Name: name, Status: a.PromptStatus, Reason: a.Mode})
	}
	switch {
	case err != nil:
		out.Status = "failed"
	case review && r.Status == ensemble.RunStopped:
		out.Status, err = "refused", fmt.Errorf("%w: %s", ErrRunStopped, r.ID)
	case review:
		out.Status = "reviewed"
	default:
		out.Status = r.Status
	}
	return out, err
}
