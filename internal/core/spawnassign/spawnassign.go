// Package spawnassign is --robot-spawn --spawn-assign-work's assignment
// phase: ntm's consented one-shot assignment (internal/robot/spawn.go's
// assignWorkToAgentsWithError at 9d5ed977) on nhm's occupants, ledger
// (internal/core/assignment), guarded claim (internal/integrations/bv) and
// send core. Ordinary spawns never reach it. It is not a scheduler: one
// call claims and delivers at most one task per spawned agent, from the
// actionable plan verified before the spawn (nhm-i85, ACFS acfs-f2mc.2).
package spawnassign

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/integrations/bv"
	"github.com/arosl/nhm/internal/integrations/worksource"
	"github.com/arosl/nhm/internal/robot"
	nhmstore "github.com/arosl/nhm/internal/store"
)

// Error codes and hints of ntm's assignment preflight.
const (
	ErrCodeStaleWorkCoordination = worksource.StaleCode
	ErrCodeTimeout               = "TIMEOUT"
	staleWorkHint                = "The work source could not be verified as one revision; the error names why. Inspect the tracker and checkout, then retry. nhm does not repair Beads state on your behalf"
)

// PreflightError is a refusal of the assignment preflight: nothing was
// created. Code and Hint are ntm's.
type PreflightError struct {
	Code, Hint string
	Err        error
	// WorkSourceMismatch is ntm's STALE_WORK_COORDINATION receipt.
	WorkSourceMismatch *worksource.StaleError
}

func (e *PreflightError) Error() string { return e.Err.Error() }
func (e *PreflightError) Unwrap() error { return e.Err }

// Plan is a verified assignment plan: the strategy, the strict policy and
// the actionable work, captured before the spawn creates anything.
type Plan struct {
	ProjectDir string
	Strategy   string
	Policy     Policy
	Triage     *bv.TriageResponse
	// Context is --with-cass and --with-memory: the context of each fresh
	// work prompt, prepared before its intent is recorded.
	Context PromptContextOptions
	// Pacer paces the work prompts on the spawn's stagger schedule, as
	// ntm's robot spawn does; nil delivers them at once.
	Pacer *spawn.Pacer
}

// Preflight is ntm GetSpawn's assignment preflight, run before any
// workspace or agent exists: the strategy strictly, the project's policy
// registered for planning and claiming, and the actionable plan verified
// against the tracker and checkout. fetch is bv.GetActionableRecommendationsContext
// in production.
func Preflight(ctx context.Context, projectDir, strategy string, policy Policy,
	fetch func(context.Context, string, int) ([]bv.TriageRecommendation, error)) (Plan, error) {
	normalized, err := normalizeAssignStrategyStrict(strategy)
	if err != nil {
		return Plan{}, &PreflightError{Code: robot.ErrCodeInvalidFlag, Err: err,
			Hint: "Use --strategy top-n, diverse, dependency-aware or skill-matched"}
	}
	if _, err := nhmstore.StateRoot(); err != nil {
		return Plan{}, &PreflightError{Code: robot.ErrCodeInternalError, Err: fmt.Errorf("the assignment ledger needs nhm's state root: %w", err),
			Hint: "Run nhm with HOME (or an absolute XDG_STATE_HOME) set"}
	}
	policyErr := bv.ConfigureProjectOperatorGatedLabels(projectDir, policy.OperatorGatedLabels)
	if policyErr == nil {
		policyErr = bv.ConfigureProjectWorkSourcePolicy(projectDir, policy.WorkSource)
	}
	if policyErr != nil {
		return Plan{}, &PreflightError{Code: robot.ErrCodeInvalidFlag, Err: fmt.Errorf("register spawn assignment safety policy: %w", policyErr),
			Hint: "Use an authoritative project directory for spawn assignment"}
	}
	if fetch == nil {
		fetch = bv.GetActionableRecommendationsContext
	}
	actionable, err := fetch(ctx, projectDir, 0)
	if err != nil {
		if cancelErr := spawnCancellationError(ctx, err); cancelErr != nil {
			return Plan{}, &PreflightError{Code: ErrCodeTimeout, Err: cancelErr, Hint: "Retry the command after cancellation"}
		}
		perr := &PreflightError{Code: robot.ErrCodeInternalError, Err: fmt.Errorf("verify actionable spawn work: %w", err),
			Hint: "Ensure bv plan output and live br labels are complete and valid"}
		if errors.Is(err, worksource.ErrStale) {
			perr.Code, perr.Hint = ErrCodeStaleWorkCoordination, staleWorkHint
			var receipt *worksource.StaleError
			if errors.As(err, &receipt) {
				perr.WorkSourceMismatch = receipt
			}
		}
		return Plan{}, perr
	}
	actionable = filterAssignableActionableRecommendationsForProject(projectDir, actionable, 0)
	return Plan{ProjectDir: projectDir, Strategy: normalized, Policy: policy, Triage: restrictTriageToAssignable(nil, actionable)}, nil
}

// instanceIDPattern is an nhm instance id, the assignment ledger's name.
var instanceIDPattern = regexp.MustCompile(`^inst-[0-9a-f]{12}$`)

// ledgerUsable refuses an assignment whose ledger would not be the
// instance's own: a name that is not an instance id, or no state root
// (assignment.StorageDir would fall back to a shared temp directory, where
// a later run with a state root never sees this run's barriers).
func ledgerUsable(instanceID string) error {
	if !instanceIDPattern.MatchString(instanceID) {
		return fmt.Errorf("assignment ledger %q is not an nhm instance id", instanceID)
	}
	if _, err := nhmstore.StateRoot(); err != nil {
		return fmt.Errorf("the assignment ledger needs nhm's state root: %w", err)
	}
	return nil
}

// Assign is ntm GetSpawn's orchestrator phase after the agents are up: it
// claims and delivers one task per agent of out.Agents through the verified
// plan, then applies ntm's output rules. Any agent without a claimed and
// sent task makes the envelope ASSIGNMENT_FAILED; the caller decides what a
// partial result means. instanceID names the assignment ledger: one per
// instance, since its occupancy key, a binding id, is the instance's and
// keeps its id across workspaces (ntm's ledger and pane ids share one tmux
// session). prompt is --spawn-prompt, which prefixes every work prompt.
// deps carries nhm's topology and delivery.
func Assign(ctx context.Context, out *SpawnOutput, plan Plan, instanceID, prompt string, deps *SpawnAssignmentDependencies) {
	out.Mode = "orchestrator"
	out.AssignStrategy = plan.Strategy
	if err := ledgerUsable(instanceID); err != nil {
		out.Success, out.Error, out.ErrorCode = false, err.Error(), robot.ErrCodeInternalError
		out.Hint = "Run nhm with HOME (or an absolute XDG_STATE_HOME) set"
		ensureSpawnAssignmentCoverage(out)
		return
	}
	policy := plan.Policy
	var pacer *spawnStaggerPacer
	if plan.Pacer != nil {
		pacer = &spawnStaggerPacer{p: plan.Pacer}
	}
	assignments, assignmentErr := assignWorkToAgentsWithError(
		ctx, out, plan.ProjectDir, instanceID, out.AssignStrategy, &policy,
		false, nil, deps, plan.Triage,
		prompt, plan.Context, pacer,
	)
	out.Assignments = assignments
	ensureSpawnAssignmentCoverage(out)
	if cancelErr := spawnCancellationError(ctx, assignmentErr); cancelErr != nil {
		out.Success = false
		out.Error = cancelErr.Error()
		out.ErrorCode = ErrCodeTimeout
		out.Hint = "Retry the command after cancellation"
		return
	}
	finalizeSpawnAssignmentOutput(out)
	if err := ctx.Err(); err != nil {
		out.Success = false
		out.Error = err.Error()
		out.ErrorCode = ErrCodeTimeout
		out.Hint = "Retry the command after cancellation"
	}
}
