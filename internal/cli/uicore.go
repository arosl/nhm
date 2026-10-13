package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/actions"
	"github.com/arosl/nhm/internal/ui/runner"
)

// uiCore is actions.Core for a surface's context c (plan-v4 sections 2
// and 4): the one wiring for the menu, the board and the palette. A read
// or a dry run runs in the surface, with the same env as the verb; the
// surface runs in c's directory, as the verb would. A mutation runs as a
// job of jobs (uiJobs) whose id is the operation id the guard gave: the
// seam waits for the job's result, and the end of the wait (the surface
// closing) never ends the job.
func uiCore(ie *instanceEnv, c ui.Context, jobs runner.Client) actions.Core {
	return actions.Core{
		SendBase: func(ctx context.Context) (send.Request, error) { return uiSendBase(ctx, ie, c) },
		Send: func(ctx context.Context, req send.Request) (send.Result, error) {
			if req.DryRun {
				res, _, err := runSendCore(ctx, ie, req)
				return res, err
			}
			var res send.Result
			return res, uiJob(ctx, jobs, runner.VerbSend, req.OperationID, req, &res, nil)
		},
		Interrupt: func(ctx context.Context, expect map[string]int64, opID string) (wait.InterruptResult, error) {
			var res wait.InterruptResult
			return res, uiJob(ctx, jobs, runner.VerbInterrupt, opID, runner.InterruptRequest{Expect: expect}, &res, nil)
		},
		Retire: func(ctx context.Context, req lifecycle.RetireRequest) (lifecycle.RetireResult, error) {
			if req.DryRun {
				lenv, err := ie.lifecycleEnv()
				if err != nil {
					return lifecycle.RetireResult{}, err
				}
				return lifecycle.Retire(ctx, lenv, req)
			}
			var res lifecycle.RetireResult
			return res, uiJob(ctx, jobs, runner.VerbRetire, req.OperationID, req, &res, lifecycle.ErrRefused)
		},
		Recycle: func(ctx context.Context, req lifecycle.RecycleRequest) (lifecycle.RecycleResult, error) {
			var res lifecycle.RecycleResult
			return res, uiJob(ctx, jobs, runner.VerbRecycle, req.OperationID, req, &res, nil)
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
// ended with an error returns it (uiJobError); refused, when not nil, is
// the core's refusal sentinel the error wraps on exit code 2, as the
// verb's in-process error would.
func uiJob(ctx context.Context, jobs runner.Client, verb, id string, req, out any, refused error) error {
	if id == "" {
		return fmt.Errorf("%s: a mutation needs the operation id of its review", verb)
	}
	r, err := jobs.Run(ctx, verb, id, req)
	if err != nil {
		return err
	}
	if len(r.Result) > 0 && string(r.Result) != "null" {
		if err := json.Unmarshal(r.Result, out); err != nil {
			return fmt.Errorf("%s: the job's result cannot be read: %w", verb, err)
		}
	}
	if r.Error == "" {
		return nil
	}
	return &uiJobError{Code: r.Code, Message: r.Error, refused: refused}
}

// uiJobError is how a job ended when it gave an error: its message and
// the verb's exit code.
type uiJobError struct {
	Code    int
	Message string
	refused error
}

func (e *uiJobError) Error() string { return e.Message }

// Unwrap gives the core's refusal sentinel for a refusal (exit code 2).
func (e *uiJobError) Unwrap() error {
	if e.Code == exitRefused {
		return e.refused
	}
	return nil
}
