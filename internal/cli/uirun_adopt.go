package cli

import (
	"context"
	"errors"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/ui/runner"
)

func init() { registerJob(runner.VerbAdopt, runAdoptJob) }

// runAdoptJob is nhm adopt with the handler's request: the identity, the
// pane, and the agent session the surface reviewed there (adopt refuses
// before it binds anything when another agent is in the pane). The
// project is the job's directory's, the surface's, unless the request
// hands an identity over.
func runAdoptJob(ctx context.Context, ie *instanceEnv, j runner.Job) (any, int, error) {
	var req bindings.AdoptRequest
	if err := j.Decode(&req); err != nil {
		return nil, exitRefused, err
	}
	if req.PaneID == "" {
		return nil, exitRefused, errors.New("an adopt job names no pane")
	}
	req.OperationID = j.ID
	if req.HandoffFrom == "" {
		s, err := projectScope(ctx, ie.inst.ID, "")
		if err != nil {
			return nil, exitRefused, err
		}
		req.ProjectKey, req.ProjectID = s.MailProjectKey, s.ProjectID
	}
	return adoptUnderOwner(ctx, ie, req)
}
