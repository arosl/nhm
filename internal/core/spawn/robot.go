package spawn

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/arosl/nhm/internal/core/pressure"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/robot"
)

// AdmissionHint is the hint of an admission that nhm's own checks
// (quota, guards) refused.
const AdmissionHint = "nhm quota and nhm doctor say why"

// admissionDefaultHint is ntm's hint when a deferral or refusal has none
// (robot/spawn.go).
const admissionDefaultHint = "Reduce requested agents or wait for resource headroom"

// RobotAdmission projects an admission onto ntm's SpawnAdmission: ntm's
// fields when ntm's admission ran, with the decision, reason and hint of
// the whole admission (nhm's quota and guards too), and nhm's reasons
// and warnings.
func RobotAdmission(ad Admission, session string, requested int) *robot.SpawnAdmission {
	out := &robot.SpawnAdmission{Reasons: ad.Reasons, Warnings: ad.Warnings}
	if ad.Pressure != nil {
		out.SpawnAdmission = *ad.Pressure
	} else {
		out.RequestedAgents, out.Session = requested, session
	}
	out.Decision, out.Reason, out.Hint = pressure.SpawnAdmissionDecision(ad.Decision), ad.Reason, ad.Hint
	// No admission ran before an early error (decision ""): no hint.
	if (out.Decision == pressure.SpawnAdmissionDefer || out.Decision == pressure.SpawnAdmissionRefuse) && out.Hint == "" {
		out.Hint = admissionDefaultHint
	}
	return out
}

// ntmType is a kind's ntm agent type, as robot output writes it.
func ntmType(kind string) string { return kinds.Lookup(kind).NTMType }

// SpawnOutput projects a spawn onto ntm's --robot-spawn answer (plan-v8
// 2.12): agents[] (or would_create[] in a dry run) with the slot N.M as
// "pane", admission.decision, the kickoffs' stagger plan when the spawn
// delivers prompts (ntm's), and assignments empty: --spawn-assign-work's
// assignment phase (internal/core/spawnassign) fills them and their
// stagger plan after the spawn. A spawn that does not wait (NoWait)
// succeeds once every agent started, as ntm's without --spawn-wait.
func SpawnOutput(res Result, err error, format string, now time.Time) robot.SpawnOutput {
	if res.DryRun && errors.Is(err, ErrRefused) {
		// ntm's dry run reports the admission's decision and succeeds
		// (internal/robot/spawn.go:1003): only a real spawn is refused.
		err = nil
	}
	ok := err == nil && (res.DryRun || res.Ready() || (res.NoWait && res.Started()))
	out := robot.SpawnOutput{RobotResponse: robot.NewResponse(ok, format, now), Session: res.Session,
		CreatedAt: now.UTC().Format(time.RFC3339), WorkingDir: res.ProjectRoot, Agents: []robot.SpawnedAgent{},
		Layout: "tabs", TotalStartupMs: res.TotalStartupMS, DryRun: res.DryRun, Assignments: []robot.SpawnAssignment{},
		Admission: RobotAdmission(res.Admission, res.Session, len(res.Targets)),
		NHM: map[string]any{"operation_id": res.OperationID, "workspace_id": res.WorkspaceID, "agents": res.Targets,
			"warnings": res.Warnings, "exceptions": res.Admission.Exceptions}}
	if !res.CreatedAt.IsZero() {
		out.CreatedAt = res.CreatedAt.UTC().Format(time.RFC3339)
	}
	if resolved, rerr := filepath.EvalSymlinks(res.ProjectRoot); rerr == nil && resolved != res.ProjectRoot {
		out.EffectiveProjectKey = resolved
	}
	if err != nil {
		out.Error = err.Error()
		out.ErrorCode = robot.ErrCodeInternalError
		switch {
		case errors.Is(err, ErrRefused) && res.Admission.fenceErr != nil:
			// ntm's beginSpawnAdmission: the lock was busy or failed.
			out.ErrorCode = robot.ErrCodeResourceBusy
			out.Error = fmt.Sprintf("spawn admission unavailable: %v", res.Admission.fenceErr)
			out.Hint = out.Admission.Hint
		case errors.Is(err, ErrRefused) && res.Admission.Decision != Admit:
			// ntm's refusal or deferral: RESOURCE_BUSY, "spawn admission
			// <decision>: <reason>" and the admission's hint.
			out.ErrorCode = robot.ErrCodeResourceBusy
			out.Error = fmt.Sprintf("spawn admission %s: %s", res.Admission.Decision, res.Admission.Reason)
			out.Hint = out.Admission.Hint
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			// ntm's setSpawnCancellation.
			out.ErrorCode, out.Hint = robot.ErrCodeTimeout, "Retry the command after cancellation"
		}
	}
	for _, t := range res.Targets {
		a := robot.SpawnedAgent{Pane: t.Slot, PaneID: t.PaneID, AgentMailName: t.MailName, Name: t.HerdrName,
			Type: ntmType(t.Kind), Variant: t.Model, Title: t.MailName, Ready: t.Ready,
			StartupMs: t.StartupMS, Error: t.Error}
		if res.DryRun {
			out.WouldCreate = append(out.WouldCreate, a)
		} else {
			out.Agents = append(out.Agents, a)
		}
	}
	out.Stagger = res.Stagger
	return out
}
