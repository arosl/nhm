package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/ui"
)

// ParamSpawn is the spawn wizard's request, as JSON (SpawnParam).
const ParamSpawn = "spawn"

// ErrNothingToSpawn: a spawn with no agent.
var ErrNothingToSpawn = errors.New("actions: nothing to spawn: give at least one agent")

// SpawnParam is ParamSpawn's value for a request.
func SpawnParam(r SpawnRequest) (string, error) {
	data, err := json.Marshal(r)
	return string(data), err
}

// runSpawnAdd adds agents to the context's workspace (nhm add).
func runSpawnAdd(ctx context.Context, core Core, req Request) (Result, error) {
	return runSpawn(ctx, core, req, true)
}

// runSpawnNew creates a session and spawns agents in it (nhm spawn).
func runSpawnNew(ctx context.Context, core Core, req Request) (Result, error) {
	return runSpawn(ctx, core, req, false)
}

// runSpawn is the wizard's review (ParamDryRun: admission and the plan)
// and spawn (the reviewed request under the review's id; admission runs
// again). spawn and add keep their difference: add takes the context's
// workspace, spawn the request's new session.
func runSpawn(ctx context.Context, core Core, req Request, add bool) (Result, error) {
	var sr SpawnRequest
	if err := json.Unmarshal([]byte(req.Params[ParamSpawn]), &sr); err != nil {
		return Result{}, fmt.Errorf("actions: the spawn request: %w", err)
	}
	sr.Add, sr.DryRun = add, on(req.Params, ParamDryRun)
	if add {
		sr.Session = req.Context.WorkspaceID
	} else if err := ValidLabel(sr.Session); err != nil {
		return Result{Status: "refused"}, err
	}
	total := 0
	for _, w := range sr.Agents {
		if w.Count < 0 {
			return Result{Status: "refused"}, fmt.Errorf("actions: %s: a count below zero", w.Kind)
		}
		total += w.Count
	}
	if total == 0 {
		return Result{Status: "refused"}, ErrNothingToSpawn
	}
	res, err := core.Spawn(ctx, sr, req.OperationID)
	out := Result{OperationID: res.OperationID, Warnings: res.Warnings}
	if d := res.Admission.Decision; d != "" {
		out.Lines = append(out.Lines, "admission: "+d)
	}
	for _, r := range res.Admission.Reasons {
		out.Lines = append(out.Lines, "  "+r)
	}
	for _, w := range res.Admission.Warnings {
		out.Lines = append(out.Lines, "  warning: "+w)
	}
	for _, t := range res.Targets {
		name := t.MailName
		if name == "" {
			name = strings.TrimSpace(t.Kind + " " + t.Model)
		}
		out.Targets = append(out.Targets, TargetResult{Target: ui.Target{BindingID: t.BindingID, PaneID: t.PaneID, Kind: t.Kind},
			Name: name, Status: t.Status, Reason: t.Error})
	}
	switch {
	case res.Admission.Decision == spawn.Refuse:
		out.Status = "refused"
	case err != nil:
		out.Status = "failed"
	case sr.DryRun:
		out.Status = "admitted"
	case res.Replayed:
		out.Status = "replayed"
	case res.Ready():
		out.Status = "started"
	default:
		out.Status = "partial"
	}
	return out, err
}
