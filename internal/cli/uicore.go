package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/core/wake"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/actions"
	"github.com/arosl/nhm/internal/ui/runner"
)

// uiCore is actions.Core for a surface's context c (plan-v4 sections 2
// and 4): the one wiring for the menu, the board and the palette. A read
// or a dry run (retire's and send's DryRun, kill without Yes, reap's and
// spawn's dry run) runs in the surface, with the same env as the verb;
// the surface runs in c's directory, as the verb would. A mutation runs
// as a job of jobs (uiJobs) whose id is the operation id the guard gave:
// the seam waits for the job's result, and the end of the wait (the
// surface closing) never ends the job.
func uiCore(ie *instanceEnv, c ui.Context, jobs runner.Client) actions.Core {
	return actions.Core{
		SendBase: func(ctx context.Context) (send.Request, error) { return uiSendBase(ctx, ie, c) },
		Send: func(ctx context.Context, req send.Request) (send.Result, error) {
			if req.DryRun {
				res, _, err := runSendCore(ctx, ie, req)
				return res, err
			}
			return uiRun(ctx, jobs, runner.VerbSend, req.OperationID, req, nil, func(r *send.Result) *string { return &r.OperationID })
		},
		Interrupt: func(ctx context.Context, expect map[string]int64, opID string) (wait.InterruptResult, error) {
			return uiRun(ctx, jobs, runner.VerbInterrupt, opID, runner.InterruptRequest{Expect: expect}, nil,
				func(r *wait.InterruptResult) *string { return &r.OperationID })
		},
		Retire: func(ctx context.Context, req lifecycle.RetireRequest) (lifecycle.RetireResult, error) {
			if req.DryRun {
				lenv, err := ie.lifecycleEnv()
				if err != nil {
					return lifecycle.RetireResult{}, err
				}
				return lifecycle.Retire(ctx, lenv, req)
			}
			return uiRun(ctx, jobs, runner.VerbRetire, req.OperationID, req, lifecycle.ErrRefused,
				func(r *lifecycle.RetireResult) *string { return &r.OperationID })
		},
		Recycle: func(ctx context.Context, req lifecycle.RecycleRequest) (lifecycle.RecycleResult, error) {
			return uiRun(ctx, jobs, runner.VerbRecycle, req.OperationID, req, nil,
				func(r *lifecycle.RecycleResult) *string { return &r.OperationID })
		},
		Adopt: func(ctx context.Context, req bindings.AdoptRequest) (bindings.AdoptResult, error) {
			return uiRun(ctx, jobs, runner.VerbAdopt, req.OperationID, req, bindings.ErrAgentChanged,
				func(r *bindings.AdoptResult) *string { return &r.OperationID })
		},
		Kill: func(ctx context.Context, req lifecycle.KillRequest) (lifecycle.KillResult, error) {
			if !req.Yes {
				lenv, err := ie.lifecycleEnv()
				if err != nil {
					return lifecycle.KillResult{}, err
				}
				return lifecycle.KillWith(ctx, lenv, req)
			}
			return uiRun(ctx, jobs, runner.VerbKill, req.OperationID, runner.KillRequest{WorkspaceID: req.WorkspaceID,
				NoBeads: req.NoBeads}, lifecycle.ErrRefused, func(r *lifecycle.KillResult) *string { return &r.OperationID })
		},
		Reap: func(ctx context.Context, req lifecycle.ReapRequest, opID string) ([]lifecycle.ReapItem, error) {
			if req.DryRun {
				lenv, err := ie.lifecycleEnv()
				if err != nil {
					return nil, err
				}
				if req.Keep, err = lifecycle.LoadKeep(keepPaths()...); err != nil {
					return nil, fmt.Errorf("the keep list: %w; nobody is reaped", err)
				}
				items, _, err := reapSweep(ctx, ie, lenv, req)
				return items, err
			}
			var out struct {
				Agents []lifecycle.ReapItem `json:"agents"`
			}
			err := uiJob(ctx, jobs, runner.VerbReap, opID, runner.ReapRequest{WorkspaceID: req.WorkspaceID, Idle: req.Idle,
				Only: req.Only}, &out, lifecycle.ErrRefused)
			return out.Agents, err
		},
		Wake: func(ctx context.Context, ts []ui.Target, opID string) (wake.Report, error) {
			var res wakeResult
			err := uiJob(ctx, jobs, runner.VerbWake, opID, runner.WakeRequest{Recipients: ts}, &res, nil)
			return res.Report, err
		},
		New: func(ctx context.Context, label, project, opID string) (string, error) {
			var res newResult
			err := uiJob(ctx, jobs, runner.VerbNew, opID, runner.NewRequest{Label: label, Project: project}, &res, nil)
			return res.WorkspaceID, err
		},
		Spawn: func(ctx context.Context, req actions.SpawnRequest, opID string) (spawn.Result, error) {
			rreq := runner.SpawnRequest{Session: req.Session, Add: req.Add, Agents: req.Agents, Label: req.Label, Group: req.Group,
				Prompt: req.Prompt, NoKickoff: req.NoKickoff, Memory: req.Memory, CASS: req.CASS}
			if req.DryRun {
				sreq, err := spawnRequestOf(rreq)
				if err != nil {
					return spawn.Result{}, err
				}
				sreq.DryRun = true
				res, _, err := spawnUnderOwner(ctx, ie, &sreq, "")
				return res, err
			}
			return uiRun(ctx, jobs, runner.VerbSpawn, opID, rreq, spawn.ErrRefused, func(r *spawn.Result) *string { return &r.OperationID })
		},
		Operation: func(ctx context.Context, id string) (lifecycle.RecentOperation, error) {
			return lifecycle.ReadOperation(ctx, ie.db, id)
		},
	}
}

// uiJobs is a surface's runner client: this nhm binary with the surface's
// environment, the instance's job directory, and context c.
func uiJobs(ie *instanceEnv, c ui.Context) runner.Client {
	return runner.Client{Bin: self(), Env: nhmEnv(c), JobDir: runner.JobDir(ie.inst.Dir), Context: c}
}

// uiSendBase is nhm send's request for c's workspace: the session, the
// project (the surface's directory) and the enrichment defaults of nhm's
// config. It selects no agent.
func uiSendBase(ctx context.Context, ie *instanceEnv, c ui.Context) (send.Request, error) {
	var req send.Request
	if c.WorkspaceID == "" {
		return req, errors.New("the surface has no workspace: open it from a workspace")
	}
	cfg, err := loadEnrichConfig()
	if err != nil {
		return req, err
	}
	if req.Enrich, err = (sendFlags{}).enrichment(cfg); err != nil {
		return req, err
	}
	return req, ie.fillScope(ctx, &req, c.WorkspaceID, "")
}

// uiJob runs a job under id and decodes its result into out. A job that
// did not start returns that error: nothing ran. A job whose answer does
// not come in (the wait ended, or its result cannot be read) returns a
// uiLostError: it may run on, and its record says how it ends; nothing
// replays it. A job that ended with an error returns it (uiJobError);
// refused, when not nil, is the core's refusal sentinel the error wraps
// on exit code 2, as the verb's in-process error would.
func uiJob(ctx context.Context, jobs runner.Client, verb, id string, req, out any, refused error) error {
	if id == "" {
		return fmt.Errorf("%s: a mutation needs the operation id of its review", verb)
	}
	if err := jobs.StartAs(verb, id, req); err != nil {
		return err
	}
	r, err := jobs.Wait(ctx, id)
	if err != nil {
		return &uiLostError{ID: id, Err: err}
	}
	if len(r.Result) > 0 && string(r.Result) != "null" {
		if err := json.Unmarshal(r.Result, out); err != nil {
			return &uiLostError{ID: id, Err: fmt.Errorf("its result cannot be read: %w", err)}
		}
	}
	if r.Error == "" {
		return nil
	}
	return &uiJobError{Code: r.Code, Message: r.Error, refused: refused}
}

// uiRun is uiJob for a result that carries its operation id (opID gives
// the field): on a lost answer the result names the job's id, so the
// surface says "unknown" and reads the record, never a second submission.
func uiRun[T any](ctx context.Context, jobs runner.Client, verb, id string, req any, refused error, opID func(*T) *string) (T, error) {
	var res T
	err := uiJob(ctx, jobs, verb, id, req, &res, refused)
	var lost *uiLostError
	if errors.As(err, &lost) {
		*opID(&res) = lost.ID
	}
	return res, err
}

// uiLostError is a job whose answer did not come in.
type uiLostError struct {
	ID  string
	Err error
}

func (e *uiLostError) Error() string {
	return fmt.Sprintf("operation %s: no answer (%v); it may run on: recent operations shows its record", e.ID, e.Err)
}

func (e *uiLostError) Unwrap() error { return e.Err }

// uiJobError is how a job ended when it gave an error: its message and
// the verb's exit code.
type uiJobError struct {
	Code    int
	Message string
	refused error
}

func (e *uiJobError) Error() string { return e.Message }

// Unwrap gives the core's refusal sentinel for a refusal: exit code 2, or
// 3 for a spawn that admission refused.
func (e *uiJobError) Unwrap() error {
	if e.Code == exitRefused || e.Code == exitAdmission {
		return e.refused
	}
	return nil
}
