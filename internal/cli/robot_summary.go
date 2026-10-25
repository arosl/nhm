// Derived from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/synthesis.go (GetSummary, SummarizeSession,
// getPanesForSession) and internal/cli/root.go (the --robot-summary
// branch, resolveRobotSummarySince) at commit 9d5ed977; see LICENSE in
// this directory and PROVENANCE.md. Local changes: the session is a herdr
// workspace, named exactly by its label or id, and its panes are the
// workspace's (core/session.Panes), shells as ntm's user panes; each pane
// is read with herdr's pane.read (500 recent lines); a pane's agent type
// is ntm's short type of its kind (cc, cod; user without an agent); its
// state is herdr's agent state in ntm's pane-state names
// (snapshot.PaneState), where ntm always writes idle.
package cli

import (
	"context"
	"flag"
	"io"
	"os"
	"time"

	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/robot"
	"github.com/arosl/nhm/internal/ui/tui/agent"
)

// runRobotSummary answers ntm's --robot-summary=S [--since=D] (or the
// older --summary-since=D): each pane's output counts, files, commands,
// errors and key actions, and the files git reports changed.
func runRobotSummary(args []string, stdout io.Writer) int {
	fs, root := newRobotFlagSet(args, stdout)
	defer root.undo()
	name := fs.String("robot-summary", "", "")
	since := fs.String("since", "", "")
	summarySince := fs.String("summary-since", "30m", "")
	socket := fs.String("socket", "", "")
	now := time.Now()
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return robotParseFail(stdout, err, now)
	}
	if code, done := applyRobotRoot(root, now); done {
		return code
	}
	ie, err := openInstance(*socket)
	if err != nil {
		return robotFail(stdout, robot.ErrCodeDependencyMissing, err.Error(), "run inside herdr or give --socket", root.format, robotError, now)
	}
	defer ie.db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, code, err := loadRobotSession(ctx, ie, *name)
	if err != nil {
		return robotFail(stdout, code, err.Error(), robotListHint, root.format, robotError, now)
	}
	// ntm's resolveRobotSummarySince: --summary-since when given, else
	// --since when given, else 30m.
	value := *summarySince
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if !given["summary-since"] && given["since"] {
		value = *since
	}
	window, err := parseRobotSinceWindow(value, time.Minute)
	if err != nil {
		return robotFail(stdout, robot.ErrCodeInvalidFlag, err.Error(), "Use a duration or supported timestamp", root.format, robotError, now)
	}
	if window == 0 {
		window = 30 * time.Minute
	}

	var data []robot.AgentActivityData
	for _, p := range s.panes {
		agentType := string(agent.AgentType(p.Type).Canonical())
		// As ntm, a pane of no known type is left out.
		if agentType == "" || agentType == string(agent.AgentTypeUnknown) {
			continue
		}
		output, _ := readPane(ctx, ie.herdr, p.PaneID, 500)
		data = append(data, robot.AgentActivityData{PaneID: p.PaneID, PaneTitle: p.Title, AgentType: agentType,
			Output: output, State: snapshot.PaneState(p.State)})
	}

	// The conflict detector over git status in the working directory, with
	// no activity recorded, as ntm runs it.
	wd, _ := os.Getwd()
	var conflicts []robot.DetectedConflict
	if status, err := diffGitStatus(ctx, wd); err == nil {
		conflicts = robot.NewConflictDetector().DetectConflicts(robot.ParseGitStatusPorcelain(status, wd))
	}
	out := robot.SessionSummaryResponse{RobotResponse: robot.NewResponse(true, root.format, now),
		Summary: robot.GenerateSummary(s.name, window, time.Now(), data, conflicts)}
	return robotWrite(stdout, out, robotOK)
}
