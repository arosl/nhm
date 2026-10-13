package cli

import (
	"context"
	"errors"

	"github.com/arosl/nhm/internal/core/ensemble"
	"github.com/arosl/nhm/internal/ui/runner"
)

func init() { registerJob(runner.VerbEnsembleStop, runEnsembleStopJob) }

// runEnsembleStopJob is nhm ensemble stop: the run's results stay, and no
// agent retires. The run is its id, or its session's newest run.
func runEnsembleStopJob(_ context.Context, ie *instanceEnv, j runner.Job) (any, int, error) {
	var req runner.EnsembleStopRequest
	if err := j.Decode(&req); err != nil {
		return nil, exitRefused, err
	}
	r, err := ensembleStop(ie, req.Run, false)
	switch {
	case errors.Is(err, ensemble.ErrNoRun):
		return r, exitRefused, err
	case err != nil:
		return r, exitError, err
	}
	return r, exitOK, nil
}

// ensembleStop stops run (an id or a session), or with dryRun only reads
// it, as nhm ensemble stop and status do.
func ensembleStop(ie *instanceEnv, run string, dryRun bool) (ensemble.Run, error) {
	renv := ie.runEnv("")
	id, err := resolveRun(renv.RunsDir, run)
	if err != nil {
		return ensemble.Run{}, err
	}
	if dryRun {
		return ensemble.Load(renv.RunsDir, id)
	}
	return ensemble.Stop(renv, id)
}
