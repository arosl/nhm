package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/spawnassign"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/robot"
)

// runRobotAssign answers ntm's --robot-assign=SESSION [--beads=a,b]
// [--strategy=simple|balanced|speed|quality|dependency] with ntm's
// AssignOutput (nhm-y55): which ready bead each idle agent of the session
// should take. It reads only: it claims, mails and sends nothing. The
// steps and refusals are ntm's (internal/cli/root.go): the session, its
// project, the project's assignment policy, then ntm's GetAssign
// (spawnassign). The session is a herdr workspace; its project is the
// project of most of its bound agents, as for nhm mail; its agents are
// that project's bound agents in the workspace.
func runRobotAssign(args []string, stdout io.Writer) int {
	now := time.Now()
	fs := flag.NewFlagSet("nhm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	session := fs.String("robot-assign", "", "")
	beads := fs.String("beads", "", "")
	strategy := fs.String("strategy", "simple", "")
	format := fs.String("robot-format", "auto", "")
	socket := fs.String("socket", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		msg := "unexpected arguments"
		if err != nil {
			msg = err.Error()
		}
		return robotFail(stdout, robot.ErrCodeInvalidFlag, msg, "nhm --robot-capabilities lists the robot flags", "json", robotError, now)
	}
	switch *format {
	case "auto", "json":
		*format = "json"
	default:
		return robotFail(stdout, robot.ErrCodeInvalidFlag, fmt.Sprintf("--robot-format=%s: nhm writes json only", *format),
			"use --robot-format=json", "json", robotError, now)
	}
	// As ntm's resolveRobotLiveSession: the name trimmed; a name of spaces
	// only is "session is required". An empty one is GetAssign's refusal.
	name := strings.TrimSpace(*session)
	switch {
	case *session == "":
		return robotFail(stdout, robot.ErrCodeInvalidFlag, "session name is required",
			"Provide session name: nhm --robot-assign=myproject", *format, robotError, now)
	case name == "":
		return robotFail(stdout, robot.ErrCodeSessionNotFound, "session is required",
			"Use 'nhm list' to see available sessions", *format, robotError, now)
	}
	ie, err := openInstance(*socket)
	if err != nil {
		return robotFail(stdout, robot.ErrCodeDependencyMissing, err.Error(), "run inside herdr or give --socket", *format, robotError, now)
	}
	defer ie.db.Close()
	ctx := context.Background()
	ws, err := sessionWorkspace(ctx, ie.herdr, name)
	if err != nil {
		return robotFail(stdout, robot.ErrCodeSessionNotFound, err.Error(), "Use 'nhm list' to see available sessions", *format, robotError, now)
	}
	store := bindings.NewStore(ie.db)
	bound, err := store.List(ctx, bindings.Filter{WorkspaceID: ws, Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return robotFail(stdout, robot.ErrCodeInternalError, err.Error(), "Ensure the target session has a resolvable project directory", *format, robotError, now)
	}
	key := sessionProject(bound)
	if key == "" {
		return robotFail(stdout, robot.ErrCodeInternalError,
			fmt.Sprintf("getting project root failed: no agent of session '%s' is bound to a project", name),
			"Ensure the target session has a resolvable project directory", *format, robotError, now)
	}
	scope, err := projectScope(ctx, ie.inst.ID, key)
	if err != nil {
		return robotFail(stdout, robot.ErrCodeInternalError, err.Error(), "Ensure the target session has a resolvable project directory", *format, robotError, now)
	}
	policyHint := "Fix [assign] in nhm's config and the project's .ntm/config.toml"
	globalPath, err := kinds.ConfigPath()
	if err != nil {
		return robotFail(stdout, robot.ErrCodeInvalidFlag, "locate nhm's config for the assignment safety policy: "+err.Error(),
			"Set HOME or XDG_CONFIG_HOME", *format, robotError, now)
	}
	policy, err := spawnassign.LoadPolicy(scope.ProjectRoot, globalPath)
	if err == nil {
		err = spawnassign.ConfigurePolicy(scope.ProjectRoot, policy)
	}
	if err != nil {
		return robotFail(stdout, robot.ErrCodeInvalidFlag, "load assignment safety policy: "+err.Error(), policyHint, *format, robotError, now)
	}
	var beadIDs []string
	if *beads != "" {
		beadIDs = strings.Split(*beads, ",")
	}
	topo := spawnassign.Topology{Bindings: store, Herdr: ie.herdr, WorkspaceID: ws, ProjectKey: key}
	out, err := spawnassign.GetAssign(ctx, spawnassign.AssignOptions{Session: name, ProjectDir: scope.ProjectRoot,
		Beads: beadIDs, Strategy: *strategy, Ledger: ie.inst.ID},
		&spawnassign.RobotAssignDependencies{ListOccupants: topo.ListOccupants, ObserveOccupant: topo.ObserveOccupant})
	if err == nil && out == nil {
		err = errors.New("robot assign failed")
	}
	if err != nil {
		return robotFail(stdout, robot.ErrCodeInternalError, err.Error(), "", *format, robotError, now)
	}
	out.NeverNull()
	if !out.Success {
		return robotWrite(stdout, out, robotError)
	}
	return robotWrite(stdout, out, robotOK)
}
