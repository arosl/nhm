package spawn

import (
	"path/filepath"
	"time"

	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/robot"
)

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
	ok := err == nil && (res.DryRun || res.Ready() || (res.NoWait && res.Started()))
	out := robot.SpawnOutput{RobotResponse: robot.NewResponse(ok, format, now), Session: res.Session,
		CreatedAt: now.UTC().Format(time.RFC3339), WorkingDir: res.ProjectRoot, Agents: []robot.SpawnedAgent{},
		Layout: "tabs", TotalStartupMs: res.TotalStartupMS, DryRun: res.DryRun, Assignments: []robot.SpawnAssignment{},
		Admission: &robot.SpawnAdmission{Decision: res.Admission.Decision, Reason: res.Admission.Reason,
			Session: res.Session, RequestedAgents: len(res.Targets), Reasons: res.Admission.Reasons, Warnings: res.Admission.Warnings},
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
		if res.Admission.Decision == Refuse {
			out.ErrorCode = "ADMISSION_REFUSED"
			out.Admission.Hint = "nhm quota, nhm doctor and the capacity provider say why"
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
