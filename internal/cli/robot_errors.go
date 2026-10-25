// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/robot/errors.go
// (GetErrors) and the --robot-errors branch of internal/cli/root.go at
// commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md. Local
// changes: the session is a herdr workspace, named exactly by its label or
// id, and its panes are the workspace's (core/session.Panes); herdr has no
// service panes; a pane's agent type is its kind's ntm type (herdr's
// detection replaces ntm's DetectAgentTypeEnhanced), so a pane without an
// agent is skipped; a pane is read with herdr's pane.read (recent); ntm's
// pane index is the slot's M; --panes takes nhm's pane forms (the
// operator's ruling of 2026-10-24); the times are in UTC; the hints name
// nhm.
package cli

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/panestate"
	"github.com/arosl/nhm/internal/core/session"
	"github.com/arosl/nhm/internal/robot"
)

// robotErrorsContext is ntm's fixed context of --robot-errors: lines
// before and after each error.
const robotErrorsContext = 2

// runRobotErrors answers ntm's --robot-errors=S [--panes=P] [--lines=N]
// [--type=K]: the error lines in each agent pane's recent output.
func runRobotErrors(args []string, stdout io.Writer) int {
	now := time.Now()
	fs, root := newRobotFlagSet(args, stdout)
	defer root.undo()
	name := fs.String("robot-errors", "", "")
	panes := fs.String("panes", "", "")
	lines := fs.Int("lines", 1000, "")
	agentType := fs.String("type", "", "")
	socket := fs.String("socket", "", "")
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
	out := robot.ErrorsOutput{RobotResponse: robot.NewResponse(true, root.format, now), Session: s.name, Errors: []robot.ErrorEntry{},
		Summary: robot.ErrorsSummary{ByType: map[string]int{}, ByAgent: map[string]int{}}}
	scanned := s.panes
	if sel := splitSelectors(*panes); len(sel) > 0 {
		var code string
		if scanned, code, err = s.selectRobotPanes(sel); err != nil {
			out.RobotResponse = robot.NewError(code, err.Error(), "Use comma-separated N, W.P or herdr pane id selectors, e.g. --panes=1,2.0",
				root.format, now).RobotResponse
			out.Meta = &robot.ResponseMeta{ExitCode: robotError, Command: robotCommand}
			return robotWrite(stdout, out, robotError)
		}
	}
	n := *lines
	if n <= 0 {
		n = 1000
	}
	filter := strings.TrimSpace(*agentType)
	for _, p := range scanned {
		text, err := readPane(ctx, ie.herdr, p.PaneID, n)
		if err != nil {
			continue
		}
		typ := p.Type
		if typ == session.UserType || typ == "unknown" || typ == "" {
			continue
		}
		if filter != "" && panestate.ResolveAgentType(typ) != panestate.ResolveAgentType(filter) {
			continue
		}
		paneLines := splitRobotLines(text)
		out.Summary.TotalLines += len(paneLines)
		out.Summary.PanesSearched++
		for i, line := range paneLines {
			isError, matchType := robot.IsRobotErrorLine(line)
			if !isError {
				continue
			}
			var ctxLines []string
			for j := max(0, i-robotErrorsContext); j < i; j++ {
				ctxLines = append(ctxLines, paneLines[j])
			}
			for j := i + 1; j <= i+robotErrorsContext && j < len(paneLines); j++ {
				ctxLines = append(ctxLines, paneLines[j])
			}
			out.Errors = append(out.Errors, robot.ErrorEntry{Timestamp: time.Now().UTC(), PaneID: p.PaneID, PaneName: p.Title,
				PaneIndex: p.Index, AgentType: typ, LineNumber: i + 1, Content: line, MatchType: matchType, Context: ctxLines})
			out.Summary.TotalErrors++
			out.Summary.ByType[matchType]++
			out.Summary.ByAgent[typ]++
		}
	}
	return robotWrite(stdout, out, robotOK)
}
