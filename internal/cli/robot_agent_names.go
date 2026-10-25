// Derived from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/agent_names.go (GetAgentNames, PrintAgentNames,
// BuildNameMapFromSession, resolveAgentTypeForNaming) and the
// --robot-agent-names branch of internal/cli/root.go at commit 9d5ed977;
// see LICENSE in this directory and PROVENANCE.md. Local changes: the
// session is a herdr workspace, named exactly by its label or id (ntm
// passes the name as given); its panes are herdr's (core/session.Panes),
// a pane without an agent being ntm's user pane; a pane's type is its
// kind's (herdr's detection replaces ntm's title parsing); the pane
// reference is the slot N.M, or the herdr pane id for a pane without a
// slot, where ntm writes 0.<index>; herdr out of reach is ntm's
// DEPENDENCY_MISSING; the hint names nhm.
package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/panestate"
	"github.com/arosl/nhm/internal/core/session"
	"github.com/arosl/nhm/internal/robot"
)

// runRobotAgentNames answers ntm's --robot-agent-names=S
// [--spawn-names=a,b]: a name for each pane of the session, from one NATO
// count over all its panes: ntm's code on herdr's real panes (the
// operator's standing ruling of 2026-10-24, as MistyDesert applied it on
// 2026-10-25).
func runRobotAgentNames(args []string, stdout io.Writer) int {
	now := time.Now()
	fs, root := newRobotFlagSet(args, stdout)
	defer root.undo()
	name := fs.String("robot-agent-names", "", "")
	spawnNames := fs.String("spawn-names", "", "")
	socket := fs.String("socket", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		msg := "unexpected arguments"
		if err != nil {
			msg = err.Error()
		}
		return robotFail(stdout, robot.ErrCodeInvalidFlag, msg, "nhm --robot-capabilities lists the robot flags", "auto", robotError, now)
	}
	if code, done := applyRobotRoot(root, now); done {
		return code
	}
	if *name == "" {
		return robotFail(stdout, robot.ErrCodeInvalidFlag, "--robot-agent-names requires a session name", "", root.format, robotError, now)
	}
	out := robot.AgentNamesOutput{RobotResponse: robot.NewResponse(true, root.format, now), Session: *name,
		Agents: []robot.AgentNameEntry{}}
	fail := func(code, msg, hint string) int {
		out.RobotResponse = robot.NewError(code, msg, hint, root.format, now).RobotResponse
		out.Meta = &robot.ResponseMeta{ExitCode: robotError, Command: robotCommand}
		return robotWrite(stdout, out, robotError)
	}
	ie, err := openInstance(*socket)
	if err != nil {
		return fail(robot.ErrCodeDependencyMissing, err.Error(), "run inside herdr or give --socket")
	}
	defer ie.db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	snap, err := ie.herdr.Snapshot(ctx)
	if err != nil {
		return fail(robot.ErrCodeDependencyMissing, err.Error(), "run inside herdr or give --socket")
	}
	workspace := ""
	for _, w := range snap.Workspaces {
		if w.Label == *name || w.WorkspaceID == *name {
			workspace = w.WorkspaceID
			break
		}
	}
	if workspace == "" {
		return fail(robot.ErrCodeSessionNotFound, fmt.Sprintf("session '%s' not found", *name),
			"Use 'nhm --robot-status' to see available sessions")
	}
	bound, err := bindings.NewStore(ie.db).List(ctx, bindings.Filter{WorkspaceID: workspace, Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return fail(robot.ErrCodeInternalError, err.Error(), "")
	}
	nameMap := robot.NewAgentNameMap(*name)
	if custom := robot.ParseCustomNames(*spawnNames); len(custom) > 0 {
		nameMap = robot.NewAgentNameMapWithCustomNames(*name, custom)
	}
	for _, p := range session.Panes(snap, workspace, bound) {
		nameMap.AssignNew(namingType(p), firstOf(p.Slot, p.PaneID))
	}
	out.Agents = nameMap.AllNames()
	out.Count = len(out.Agents)
	return robotWrite(stdout, out, robotOK)
}

// namingType is ntm's resolveAgentTypeForNaming on herdr: the pane's type,
// resolved; a pane whose type is unknown is "user".
func namingType(p session.Pane) string {
	if t := panestate.ResolveAgentType(p.Type); t != "" && t != "unknown" {
		return t
	}
	return session.UserType
}
