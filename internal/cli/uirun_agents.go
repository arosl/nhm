package cli

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/ui/runner"
)

// The per-agent jobs run the request the action table's handler built
// (internal/ui/actions), with the job's id as its operation id. A dry run
// is no job: it reads only, and runs in the surface.

func init() {
	registerJob(runner.VerbSend, runSendJob)
	registerJob(runner.VerbInterrupt, runInterruptJob)
	registerJob(runner.VerbRetire, runRetireJob)
	registerJob(runner.VerbRecycle, runRecycleJob)
}

var errDryRunJob = errors.New("a dry run is no job: it runs in the surface")

// runSendJob is nhm send with the handler's request: its selector and
// Expect, so a target that changed is refused under its lock.
func runSendJob(ctx context.Context, ie *instanceEnv, j runner.Job) (any, int, error) {
	var req send.Request
	if err := j.Decode(&req); err != nil {
		return nil, exitRefused, err
	}
	if req.DryRun {
		return nil, exitRefused, errDryRunJob
	}
	req.OperationID = j.ID
	res, code, err := runSendCore(ctx, ie, req)
	if err == nil {
		code = exitOK
	}
	return res, code, err
}

// runInterruptJob interrupts the reviewed bindings. A binding that is gone
// is left out of the targets, and Expect refuses it by name; one at
// another generation is refused too, never replaced.
func runInterruptJob(ctx context.Context, ie *instanceEnv, j runner.Job) (any, int, error) {
	var req runner.InterruptRequest
	if err := j.Decode(&req); err != nil {
		return nil, exitRefused, err
	}
	if len(req.Expect) == 0 {
		return nil, exitRefused, errors.New("an interrupt job names no agent")
	}
	store := bindings.NewStore(ie.db)
	ids := make([]string, 0, len(req.Expect))
	for id := range req.Expect {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var targets []bindings.Binding
	for _, id := range ids {
		b, err := store.Get(ctx, id)
		if errors.Is(err, bindings.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, exitError, err
		}
		targets = append(targets, b)
	}
	var res wait.InterruptResult
	code, err := underOwner(ctx, ie, func() error {
		var ierr error
		res, ierr = wait.Interrupt(ctx, ie.db, ie.herdr, store, wait.InterruptRequest{OperationID: j.ID,
			Scope: map[string]string{"instance_id": ie.inst.ID}, Targets: targets, Expect: req.Expect}, time.Now)
		return ierr
	})
	// As nhm interrupt: done (or a replay) is 0, refused 2, else 1.
	switch {
	case err != nil:
	case res.Existing, res.Status == "done":
		code = exitOK
	case res.Status == "refused":
		code = exitRefused
	default:
		code = exitError
	}
	return res, code, err
}

// runRetireJob retires the reviewed binding at the reviewed generation.
func runRetireJob(ctx context.Context, ie *instanceEnv, j runner.Job) (any, int, error) {
	var req lifecycle.RetireRequest
	if err := j.Decode(&req); err != nil {
		return nil, exitRefused, err
	}
	if req.DryRun {
		return nil, exitRefused, errDryRunJob
	}
	req.OperationID = j.ID
	lenv, err := ie.lifecycleEnv()
	if err != nil {
		return nil, exitError, err
	}
	var res lifecycle.RetireResult
	code, err := underOwner(ctx, ie, func() error {
		var rerr error
		res, rerr = lifecycle.Retire(ctx, lenv, req)
		return rerr
	})
	if errors.Is(err, lifecycle.ErrRefused) {
		code = exitRefused
	}
	return res, code, err
}

// runRecycleJob recycles the reviewed binding at the reviewed generation.
func runRecycleJob(ctx context.Context, ie *instanceEnv, j runner.Job) (any, int, error) {
	var req lifecycle.RecycleRequest
	if err := j.Decode(&req); err != nil {
		return nil, exitRefused, err
	}
	req.OperationID = j.ID
	lenv, err := ie.lifecycleEnv()
	if err != nil {
		return nil, exitError, err
	}
	var res lifecycle.RecycleResult
	code, err := underOwner(ctx, ie, func() error {
		var rerr error
		res, rerr = lifecycle.Recycle(ctx, lenv, req)
		return rerr
	})
	// As nhm recycle: a refusal at the check is 2.
	if err != nil && res.Stage == lifecycle.StageCheck && code == exitError {
		code = exitRefused
	}
	return res, code, err
}
