package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/core/wake"
	"github.com/arosl/nhm/internal/ui/runner"
)

func init() {
	registerJob(runner.VerbSpawn, runSpawnJob)
	registerJob(runner.VerbNew, runNewJob)
	registerJob(runner.VerbWake, runWakeJob)
}

// runSpawnJob is nhm spawn or add with the wizard's reviewed request and
// --op-id <job id>. Admission runs again: a refusal now is the result.
func runSpawnJob(ctx context.Context, ie *instanceEnv, j runner.Job) (any, int, error) {
	var req runner.SpawnRequest
	if err := j.Decode(&req); err != nil {
		return nil, exitRefused, err
	}
	enrichment, err := spawnEnrichment(&spawnFlags{withMemory: req.Memory, withCASS: req.CASS, noCASSContext: !req.CASS})
	if err != nil {
		return nil, exitRefused, err
	}
	sreq := spawn.Request{Session: req.Session, Add: req.Add, Agents: req.Agents, Label: req.Label, Group: req.Group,
		Prompt: req.Prompt, NoKickoff: req.NoKickoff, Enrich: enrichment, OperationID: j.ID}
	res, code, err := spawnUnderOwner(ctx, ie, &sreq, "")
	if err == nil {
		code = spawnExit(res)
	}
	return res, code, err
}

// runNewJob is nhm new.
func runNewJob(ctx context.Context, ie *instanceEnv, j runner.Job) (any, int, error) {
	var req runner.NewRequest
	if err := j.Decode(&req); err != nil {
		return nil, exitRefused, err
	}
	res, _, code, err := newWorkspace(ctx, ie, req.Label, req.Project)
	return res, code, err
}

// runWakeJob is nhm wake --recipients with the picked agents. A wake
// addresses an agent's Mail identity, which a recycle keeps, so the
// generation is not checked; a binding that is no longer active is
// refused. No recipient is refused: it never means every agent.
func runWakeJob(ctx context.Context, ie *instanceEnv, j runner.Job) (any, int, error) {
	var req runner.WakeRequest
	if err := j.Decode(&req); err != nil {
		return nil, exitRefused, err
	}
	if len(req.Recipients) == 0 {
		return nil, exitRefused, errors.New("a wake job names no recipient")
	}
	w, err := ie.wakeEnv(true)
	if err != nil {
		return nil, exitError, err
	}
	targets := make([]bindings.Binding, 0, len(req.Recipients))
	for _, r := range req.Recipients {
		if !r.Bound() {
			return nil, exitRefused, fmt.Errorf("pane %s has no binding: wake reaches bound agents only", r.PaneID)
		}
		b, err := w.Bindings.Get(ctx, r.BindingID)
		if err != nil {
			return nil, exitRefused, err
		}
		if b.Status != bindings.Active {
			return nil, exitRefused, fmt.Errorf("%s is %s, not active", b.MailName, b.Status)
		}
		targets = append(targets, b)
	}
	res, err := ie.wakePass(ctx, w, targets, wake.TriggerManual)
	code := exitOK
	if err != nil {
		code = exitError
		if errors.Is(err, core.ErrNotOwner) {
			code = exitRefused
		}
	}
	return res, code, err
}
