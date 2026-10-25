// Derived from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/lifecycle.go (GetExitCLI, PrintExitCLI) and the
// --robot-exit-cli branch of internal/cli/root.go at commit 9d5ed977; see
// LICENSE in this directory and PROVENANCE.md. Local changes: the session
// and its agent panes are herdr's, as --robot-dialogs finds them
// (lifecycleTargets); each pane's exit and relaunch is core/lifecycle's
// (ExitAgent, Relaunch on recycle's start stage), under nhm's owner lock;
// the relaunch check needs a binding for each pane where ntm reads a saved
// launch command; the hints name nhm.
package cli

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/robot"
)

// exitCLITimeout bounds a whole --robot-exit-cli call: two rounds of 6 s
// for each pane, and a relaunch's ready wait.
const exitCLITimeout = 10 * time.Minute

// exitCLIEnv, when set, adjusts the lifecycle env of --robot-exit-cli
// (tests: the shell's jobs and the clock).
var exitCLIEnv func(*lifecycle.Env)

// runRobotExitCLI answers ntm's --robot-exit-cli=S [--panes=P]
// [--relaunch]: the agent CLI in each agent pane exits, and the pane and
// its shell stay. With --relaunch every pane is checked first, then each
// agent that exited starts again in its pane.
func runRobotExitCLI(args []string, stdout io.Writer) int {
	now := time.Now()
	fs, root := newRobotFlagSet(args, stdout)
	defer root.undo()
	name := fs.String("robot-exit-cli", "", "")
	panes := fs.String("panes", "", "")
	relaunch := fs.Bool("relaunch", false, "")
	socket := fs.String("socket", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return robotParseFail(stdout, err, now)
	}
	if code, done := applyRobotRoot(root, now); done {
		return code
	}
	if *name == "" {
		return robotFail(stdout, robot.ErrCodeInvalidFlag, "--robot-exit-cli requires a session name",
			"Use --robot-exit-cli=SESSION; 'nhm list' shows available sessions", "json", robotError, now)
	}
	out := robot.ExitCLIOutput{RobotResponse: robot.NewResponse(true, root.format, now), Session: strings.TrimSpace(*name),
		Results: []robot.LifecyclePaneResult{}}
	fail := func(code, msg, hint string) int {
		out.RobotResponse = robotFailure(code, msg, hint, now)
		return robotWrite(stdout, out, robotError)
	}
	ie, err := openInstance(*socket)
	if err != nil {
		return fail(robot.ErrCodeDependencyMissing, err.Error(), "run inside herdr or give --socket")
	}
	defer ie.db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), exitCLITimeout)
	defer cancel()
	s, targets, multi, f := lifecycleTargets(ctx, ie, *name, robotPaneList(*panes))
	if f != nil {
		out.RobotResponse = *f
		return robotWrite(stdout, out, robotError)
	}
	out.Session = s.name
	env := lifecycle.Env{DB: ie.db, Bindings: bindings.NewStore(ie.db), Herdr: ie.herdr, InstanceDir: ie.inst.Dir,
		LaunchSettings: spawn.LaunchSettings(exec.LookPath, ie.inst.Dir), ProcStart: bindings.ProcStart}
	if exitCLIEnv != nil {
		exitCLIEnv(&env)
	}
	reqs := make([]lifecycle.ExitRequest, len(targets))
	for i, p := range targets {
		reqs[i] = lifecycle.ExitRequest{PaneID: p.PaneID, Binding: p.Binding, Kind: p.Kind, NTMType: paneAgentType(p)}
	}
	var checkErr error
	lctx, lcancel := context.WithTimeout(ctx, lockWait)
	defer lcancel()
	err = core.WithOwnerShared(lctx, ie.inst.Dir, func(o core.Owner) error {
		if err := core.RequireOwner(o, core.OwnerNhm); err != nil {
			return err
		}
		if *relaunch {
			if checkErr = lifecycle.RelaunchCheck(env, reqs); checkErr != nil {
				return nil
			}
		}
		for i, p := range targets {
			r := robot.LifecyclePaneResult{Pane: paneTargetKey(p, multi), Target: p.PaneID, AgentType: reqs[i].NTMType}
			o := lifecycle.ExitAgent(ctx, env, reqs[i])
			r.ShellPID, r.Exited, r.ShellPreserved, r.VerificationFailed, r.Detail = o.ShellPID, o.Exited, o.ShellPreserved, o.VerificationFailed, o.Detail
			if *relaunch && r.Exited && r.ShellPreserved {
				var detail string
				r.Relaunched, detail = lifecycle.Relaunch(ctx, env, reqs[i], o.ShellPID)
				if detail != "" {
					r.Detail = strings.TrimPrefix(r.Detail+"; "+detail, "; ")
				}
			}
			out.Results = append(out.Results, r)
		}
		return nil
	})
	switch {
	case errors.Is(err, core.ErrNotOwner):
		return fail(robot.ErrCodeNotOwner, err.Error(), "")
	case err != nil:
		return fail(robot.ErrCodeInternalError, err.Error(), "")
	case checkErr != nil:
		return fail(robot.ErrCodeInternalError, checkErr.Error(),
			"Fix the saved launch specification or its dependencies before retrying; no agent was exited")
	}
	for _, r := range out.Results {
		if !r.Exited || !r.ShellPreserved || (*relaunch && !r.Relaunched) {
			out.Success = false
			break
		}
	}
	if !out.Success {
		out.Error, out.ErrorCode = "one or more panes did not complete the requested lifecycle transition", robot.ErrCodeInternalError
		out.Hint = "Inspect per-pane results; escalate stuck panes with --robot-kill-agent"
		out.OutputFormat = "json"
		out.Meta = &robot.ResponseMeta{ExitCode: robotError, Command: robotCommand}
		return robotWrite(stdout, out, robotError)
	}
	return robotWrite(stdout, out, robotOK)
}
