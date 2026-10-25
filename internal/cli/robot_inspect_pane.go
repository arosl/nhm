// Derived from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/tui_parity.go (GetInspectPane) and the
// --robot-inspect-pane branch of internal/cli/root.go
// (resolveRobotSharedLines) at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: the session is a herdr
// workspace, named exactly by its label or id, and its panes are the
// workspace's (core/session.Panes); --index is a pane index under the
// operator's ruling of 2026-10-24 (pane M of the agent tab), with ntm's
// fallback to the first pane for index 0; the pane is read with herdr's
// pane.read (recent); the agent type is the pane's kind's ntm type and
// the variant its binding's model (herdr's detection replaces ntm's
// DetectAgentTypeEnhanced: confidence 1 for an agent, "unknown" and 0
// for a pane without one); the state is herdr's agent state in ntm's
// pane-state names (snapshot.PaneState, in place of ntm's
// determineState); the command is herdr's first foreground process; ntm's
// pane index is the slot's M; the hints name nhm.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/session"
	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/robot"
)

// runRobotInspectPane answers ntm's --robot-inspect-pane=S [--index=N]
// [--lines=N] [--code], with ntm's older --inspect-index, --inspect-lines
// and --inspect-code.
func runRobotInspectPane(args []string, stdout io.Writer) int {
	now := time.Now()
	fs, root := newRobotFlagSet(args, stdout)
	defer root.undo()
	name := fs.String("robot-inspect-pane", "", "")
	var index int
	fs.IntVar(&index, "index", 0, "")
	fs.IntVar(&index, "inspect-index", 0, "")
	sharedLines := fs.Int("lines", 100, "")
	inspectLines := fs.Int("inspect-lines", 100, "")
	var withCode bool
	fs.BoolVar(&withCode, "code", false, "")
	fs.BoolVar(&withCode, "inspect-code", false, "")
	socket := fs.String("socket", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return robotParseFail(stdout, err, now)
	}
	if code, done := applyRobotRoot(root, now); done {
		return code
	}
	// ntm's resolveRobotSharedLines: --inspect-lines when given, else
	// --lines when given, else 100.
	lines := *inspectLines
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if !given["inspect-lines"] && given["lines"] {
		lines = *sharedLines
	}
	if lines <= 0 {
		lines = 100
	}
	// last_lines is [] when empty: ntm's arrays-never-null rule.
	out := robot.InspectPaneOutput{RobotResponse: robot.NewResponse(true, root.format, now), Session: strings.TrimSpace(*name),
		Output: robot.InspectPaneOutput_{LastLines: []string{}}}
	fail := func(code, msg, hint string) int {
		out.RobotResponse = robot.NewError(code, msg, hint, root.format, now).RobotResponse
		out.Meta = &robot.ResponseMeta{ExitCode: robotError, Command: robotCommand}
		return robotWrite(stdout, out, robotError)
	}
	if out.Session == "" {
		return fail(robot.ErrCodeInvalidFlag, "session name required", "Specify session with --robot-inspect-pane=SESSION")
	}
	// As ntm, a session that cannot be read gets the plain failure.
	ie, err := openInstance(*socket)
	if err != nil {
		return robotFail(stdout, robot.ErrCodeDependencyMissing, err.Error(), "run inside herdr or give --socket", root.format, robotError, now)
	}
	defer ie.db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, code, err := loadRobotSession(ctx, ie, *name)
	if err != nil {
		return robotFail(stdout, code, err.Error(), robotListHint, root.format, robotError, now)
	}
	out.Session = s.name

	var target *session.Pane
	if t, err := session.ParsePane(strconv.Itoa(index), session.AgentTabs(s.bound), s.activeTab); err == nil {
		for i := range s.panes {
			if session.MatchPane(s.panes[i], t) {
				target = &s.panes[i]
				break
			}
		}
	}
	// Index 0 that names no pane is the first pane, as in ntm.
	if target == nil && index == 0 && len(s.panes) > 0 {
		target = &s.panes[0]
	}
	if target == nil {
		var valid []string
		for _, p := range s.panes {
			valid = append(valid, p.Ref())
		}
		hint := "No panes found"
		if len(valid) > 0 {
			hint = "Valid pane indices: " + strings.Join(valid, ", ")
		}
		out.PaneIndex = index
		return fail(robot.ErrCodePaneNotFound, fmt.Sprintf("pane %d not found in session '%s'", index, s.name), hint)
	}

	p := *target
	captured, captureErr := readPane(ctx, ie.herdr, p.PaneID, lines)
	out.PaneIndex, out.PaneID = p.Index, p.PaneID
	command := paneCommand(ctx, ie, p)
	// ntm's confidence is that of the type detection: 1 for a known agent,
	// 0 and "unknown" for a pane that herdr finds no agent in.
	out.Agent = robot.InspectPaneAgent{Type: "unknown", Title: p.Title, Command: command, ProcessRunning: command != ""}
	if p.Kind != "" {
		out.Agent.Type, out.Agent.StateConfidence = p.Type, 1
	}
	if p.Binding != nil {
		out.Agent.Variant = p.Binding.Model
	}
	if captureErr == nil {
		paneLines := splitRobotLines(captured)
		out.Agent.State = snapshot.PaneState(p.State)
		out.Output.Lines = len(paneLines)
		out.Output.Characters = len(captured)
		// The last lines, at most 50.
		if n := min(50, len(paneLines)); n > 0 {
			out.Output.LastLines = paneLines[len(paneLines)-n:]
		}
		out.Output.ErrorsFound = robot.InspectErrors(paneLines)
		if withCode {
			out.Output.CodeBlocks = robot.InspectCodeBlocks(paneLines)
		}
		out.Context.RecentFiles = robot.InspectFileReferences(paneLines)
	}

	var suggestions []robot.RobotAction
	var warnings []string
	switch out.Agent.State {
	case "error":
		warnings = append(warnings, "Agent is in error state")
		suggestions = append(suggestions, robot.RobotAction{Action: "investigate", Target: fmt.Sprintf("pane %d", index),
			Reason: "Error detected in output", Priority: 2})
	case "waiting":
		suggestions = append(suggestions, robot.RobotAction{Action: "send_prompt", Target: fmt.Sprintf("pane %d", index),
			Reason: "Agent is idle and ready for work", Priority: 1})
	}
	if len(out.Output.ErrorsFound) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d error(s) detected in recent output", len(out.Output.ErrorsFound)))
	}
	out.AgentHints = &robot.AgentHints{
		Summary:          fmt.Sprintf("%s agent in %s state, %d lines of output", out.Agent.Type, out.Agent.State, out.Output.Lines),
		Warnings:         warnings,
		SuggestedActions: suggestions,
	}
	return robotWrite(stdout, out, robotOK)
}
