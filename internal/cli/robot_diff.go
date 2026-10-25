// Derived from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/robot.go (GetDiff), internal/robot/synthesis.go
// (ConflictDetector.GetGitStatus) and internal/cli/root.go (the
// --robot-diff branch, resolveRobotDiffSince, parseRobotSinceWindow) at
// commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md. Local
// changes: the session is a herdr workspace, named exactly by its label or
// id, and its panes are the workspace's (core/session.Panes), all of them
// (nhm has no service panes); each pane is read with herdr's pane.read
// (recent); a pane's agent type is its kind's ntm type ("user" without an
// agent) and its state herdr's agent state in ntm's pane-state names
// (snapshot.PaneState); git status runs once, where ntm runs it twice;
// the likely modifiers are herdr pane ids in pane order; the hints name
// nhm.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/robot"
)

// diffGitStatus is ntm's GetGitStatus: `git status --porcelain` in dir.
var diffGitStatus = func(ctx context.Context, dir string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "status", "--porcelain").Output()
	return string(out), err
}

// parseRobotSinceWindow is ntm's: a duration (a bare number in
// defaultUnit), else an RFC 3339 time in the past, as the time since it.
func parseRobotSinceWindow(value string, defaultUnit time.Duration) (time.Duration, error) {
	if duration, err := parseNTMDurationWithDefault(value, defaultUnit); err == nil {
		return duration, nil
	}

	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if ts, err := time.Parse(layout, value); err == nil {
			now := time.Now().UTC()
			ts = ts.UTC()
			if ts.After(now) {
				return 0, fmt.Errorf("timestamp %q is in the future", value)
			}
			return now.Sub(ts), nil
		}
	}

	return 0, fmt.Errorf("invalid time %q: use duration like 10m or RFC3339 timestamp", value)
}

// runRobotDiff answers ntm's --robot-diff=S [--since=D] (or the older
// --diff-since=D): each pane's activity and the files that git reports
// changed in the working directory, with the ones that may conflict.
func runRobotDiff(args []string, stdout io.Writer) int {
	fs, root := newRobotFlagSet(args, stdout)
	defer root.undo()
	name := fs.String("robot-diff", "", "")
	since := fs.String("since", "", "")
	diffSince := fs.String("diff-since", "15m", "")
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
	// ntm's resolveRobotDiffSince: --diff-since when given, else --since
	// when given, else 15m.
	value := *diffSince
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if !given["diff-since"] && given["since"] {
		value = *since
	}
	window, err := parseRobotSinceWindow(value, time.Minute)
	if err != nil {
		return robotFail(stdout, robot.ErrCodeInvalidFlag, err.Error(), "Use a duration or supported timestamp", root.format, robotError, now)
	}
	if window == 0 {
		window = 15 * time.Minute
	}

	at := time.Now().UTC()
	sinceTime := at.Add(-window)
	out := robot.DiffOutput{
		RobotResponse: robot.NewResponse(true, root.format, now),
		Session:       s.name,
		Timeframe: robot.DiffTimeframe{
			Since:      window.String(),
			SinceTS:    sinceTime.Format(time.RFC3339),
			CapturedAt: at.Format(time.RFC3339),
		},
		Files:         robot.DiffFiles{Modified: []string{}, PotentialConflicts: []robot.DiffConflict{}, Clean: []string{}},
		AgentActivity: []robot.DiffAgentInfo{},
	}

	detector := robot.NewConflictDetector()
	for _, p := range s.panes {
		captured, _ := readPane(ctx, ie.herdr, p.PaneID, 100)
		lines := splitRobotLines(captured)
		out.AgentActivity = append(out.AgentActivity, robot.DiffAgentInfo{Pane: p.Title, AgentType: p.Type,
			State: snapshot.PaneState(p.State), OutputLines: len(lines)})
		// Each pane's activity window is the whole look-back, as in ntm.
		detector.RecordActivity(p.PaneID, p.Type, sinceTime, at, len(lines) > 0)
	}

	// git status in the working directory, as ntm runs it.
	var issues []string
	wd, err := os.Getwd()
	if err != nil {
		wd = ""
	}
	status, gitErr := diffGitStatus(ctx, wd)
	var files []robot.GitFileStatus
	if gitErr == nil {
		files = robot.ParseGitStatusPorcelain(status, wd)
		for _, f := range files {
			out.Files.Modified = append(out.Files.Modified, f.Path)
		}
	} else if wd != "" {
		issues = append(issues, "Could not get git status", "Conflict detection incomplete")
	}
	for _, c := range detector.DetectConflicts(files) {
		modifiers := c.LikelyModifiers
		if modifiers == nil {
			modifiers = []string{}
		}
		out.Files.PotentialConflicts = append(out.Files.PotentialConflicts, robot.DiffConflict{File: c.Path,
			LikelyModifiers: modifiers, Reason: string(c.Reason), Confidence: c.Confidence})
	}

	hints := &robot.DiffAgentHints{
		Summary: fmt.Sprintf("%s activity: %d files modified, %d agents", s.name, len(out.Files.Modified), len(s.panes)),
	}
	hints.ConflictWarnings = append(hints.ConflictWarnings, issues...)
	if len(out.Files.PotentialConflicts) > 0 {
		hints.ConflictWarnings = append(hints.ConflictWarnings,
			fmt.Sprintf("%d potential conflict(s) detected", len(out.Files.PotentialConflicts)))
		hints.SuggestedActions = append(hints.SuggestedActions, "Review conflicts before committing")
	}
	if len(out.Files.Modified) == 0 {
		hints.SuggestedActions = append(hints.SuggestedActions, fmt.Sprintf("No file changes in the last %s", window.String()))
	}
	out.AgentHints = hints
	return robotWrite(stdout, out, robotOK)
}
