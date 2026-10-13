package cli

// --robot-spawn --spawn-assign-work: ntm's consented one-shot assignment
// (nhm-i85, ACFS acfs-f2mc.2). The flags, the preflight that runs before
// anything is created, and the assignment phase after the agents are up;
// the logic is internal/core/spawnassign.

import (
	"context"
	"errors"
	"flag"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/core/spawnassign"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/robot"
)

// robotAssignFlags are ntm's --spawn-assign-work flags of --robot-spawn.
type robotAssignFlags struct {
	fs                                         *flag.FlagSet
	strategy, legacyStrategy, reservationPaths *string
	requireReservation, withCASS, withMemory   *bool
}

func addRobotAssignFlags(fs *flag.FlagSet) robotAssignFlags {
	return robotAssignFlags{fs: fs,
		strategy:           fs.String("strategy", "", ""),
		legacyStrategy:     fs.String("spawn-assign-strategy", "top-n", ""),
		reservationPaths:   fs.String("spawn-reservation-paths", "", ""),
		requireReservation: fs.Bool("spawn-require-reservation", false, ""),
		withCASS:           fs.Bool("with-cass", false, ""),
		withMemory:         fs.Bool("with-memory", false, ""),
	}
}

// strategyValue is ntm's resolveRobotSharedFlag: an explicit
// --spawn-assign-strategy wins, then an explicit --strategy, then
// --spawn-assign-strategy's default.
func (f robotAssignFlags) strategyValue() string {
	set := map[string]bool{}
	f.fs.Visit(func(fl *flag.Flag) { set[fl.Name] = true })
	switch {
	case set["spawn-assign-strategy"]:
		return *f.legacyStrategy
	case set["strategy"]:
		return *f.strategy
	}
	return *f.legacyStrategy
}

// robotAssignPreflight runs ntm's assignment preflight for project before
// the spawn creates anything: no reservations (nhm does not take ntm's
// reservation runtime), the strict policy, the verified actionable plan. A
// refusal is the envelope to write, with ntm's code.
func robotAssignPreflight(ctx context.Context, ie *instanceEnv, f robotAssignFlags, project string) (spawnassign.Plan, core.Scope, *robot.RobotResponse) {
	refuse := func(code, msg, hint string) (spawnassign.Plan, core.Scope, *robot.RobotResponse) {
		return spawnassign.Plan{}, core.Scope{}, &robot.RobotResponse{Success: false, ErrorCode: code, Error: msg, Hint: hint}
	}
	if *f.requireReservation || strings.TrimSpace(*f.reservationPaths) != "" {
		return refuse(robot.ErrCodeInvalidFlag, "nhm does not reserve files for spawn assignments (--spawn-require-reservation, --spawn-reservation-paths)",
			"drop the reservation flags; agents reserve their own files")
	}
	s, err := projectScope(ctx, ie.inst.ID, project)
	if err != nil {
		return refuse(robot.ErrCodeInvalidFlag, err.Error(), "give --spawn-dir a project directory")
	}
	globalPath, err := kinds.ConfigPath()
	if err != nil {
		return refuse(robot.ErrCodeInvalidFlag, "locate nhm's config for the spawn assignment safety policy: "+err.Error(),
			"Set HOME or XDG_CONFIG_HOME")
	}
	policy, err := spawnassign.LoadPolicy(s.ProjectRoot, globalPath)
	if err != nil {
		return refuse(robot.ErrCodeInvalidFlag, "load spawn assignment safety policy: "+err.Error(),
			"Fix [assign] in nhm's config and the project's .ntm/config.toml")
	}
	plan, err := spawnassign.Preflight(ctx, s.ProjectRoot, f.strategyValue(), policy, nil)
	if err != nil {
		var perr *spawnassign.PreflightError
		if errors.As(err, &perr) {
			return refuse(perr.Code, perr.Error(), perr.Hint)
		}
		return refuse(robot.ErrCodeInternalError, err.Error(), "")
	}
	plan.Context = spawnassign.PromptContextOptions{WithCASS: *f.withCASS, WithMemory: *f.withMemory}
	return plan, s, nil
}

// robotAssignPhase runs the assignment after a spawn whose agents are all
// up, under the owner lock as every send runs, and applies ntm's output
// rules to out. scope is the preflight's project scope.
func robotAssignPhase(ctx context.Context, ie *instanceEnv, res spawn.Result, scope core.Scope, plan spawnassign.Plan, prompt string, out *robot.SpawnOutput) {
	senv := ie.sendEnv()
	topo := spawnassign.Topology{Bindings: bindings.NewStore(ie.db), Herdr: ie.herdr, WorkspaceID: res.WorkspaceID,
		ProjectKey: scope.MailProjectKey}
	port := spawnassign.SendPort{Env: senv, Session: res.Session, WorkspaceID: res.WorkspaceID,
		ProjectRoot: res.ProjectRoot, ProjectKey: topo.ProjectKey, PreparedDir: spawnassign.PreparedDir(ie.inst.ID)}
	deps := &spawnassign.SpawnAssignmentDependencies{
		ListOccupants: topo.ListOccupants, ObserveOccupant: topo.ObserveOccupant, Dispatch: port, Enrich: port,
	}
	lctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	err := core.WithOwnerShared(lctx, ie.inst.Dir, func(o core.Owner) error {
		if err := core.RequireOwner(o, core.OwnerNhm); err != nil {
			return err
		}
		spawnassign.Assign(ctx, out, plan, ie.inst.ID, prompt, deps)
		return nil
	})
	if err != nil {
		out.Success, out.Error = false, err.Error()
		out.ErrorCode = robot.ErrCodeInternalError
		if errors.Is(err, core.ErrNotOwner) {
			out.ErrorCode = robot.ErrCodeNotOwner
		}
	}
}

// robotAssignRefusal writes a preflight refusal as a --robot-spawn answer.
func robotAssignRefusal(r *robot.RobotResponse, session, project, format string, now time.Time) robot.SpawnOutput {
	out := spawn.SpawnOutput(spawn.Result{Session: session, ProjectRoot: project}, errors.New(r.Error), format, now)
	out.ErrorCode, out.Hint = r.ErrorCode, r.Hint
	return out
}
