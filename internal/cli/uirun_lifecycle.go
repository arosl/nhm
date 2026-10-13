package cli

import (
	"context"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/ui/runner"
)

func init() {
	registerJob(runner.VerbKill, runKillJob)
	registerJob(runner.VerbReap, runReapJob)
}

// runKillJob is nhm kill --yes --op-id <job id>: the same checks again,
// under the owner lock. Its record outlives the popup, also when the kill
// closes the popup's own workspace.
func runKillJob(ctx context.Context, ie *instanceEnv, j runner.Job) (any, int, error) {
	var req runner.KillRequest
	if err := j.Decode(&req); err != nil {
		return nil, exitRefused, err
	}
	lenv, err := ie.lifecycleEnv()
	if err != nil {
		return nil, exitError, err
	}
	return killUnderOwner(ctx, ie, lenv, lifecycle.KillRequest{WorkspaceID: req.WorkspaceID, Yes: true, NoBeads: req.NoBeads,
		OperationID: j.ID})
}

// runReapJob retires exactly the reviewed set, with nhm reap's keep list
// and mail check.
func runReapJob(ctx context.Context, ie *instanceEnv, j runner.Job) (any, int, error) {
	var req runner.ReapRequest
	if err := j.Decode(&req); err != nil {
		return nil, exitRefused, err
	}
	// A job never sweeps more than it reviewed: a missing set is an empty
	// one, which reap refuses.
	if req.Only == nil {
		req.Only = []lifecycle.ReapTarget{}
	}
	lenv, err := ie.lifecycleEnv()
	if err != nil {
		return nil, exitError, err
	}
	keep, err := lifecycle.LoadKeep(keepPaths()...)
	if err != nil {
		return nil, exitError, err
	}
	// The job's id is the sweep's record: its retires are its children.
	items, code, err := reapSweep(ctx, ie, lenv, lifecycle.ReapRequest{Idle: req.Idle, WorkspaceID: req.WorkspaceID, Keep: keep,
		Only: req.Only, OperationID: j.ID})
	return map[string]any{"agents": items}, code, err
}
