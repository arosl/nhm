// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/robot/logs.go
// (GetLogs, capturePaneLogs), internal/robot/robot.go (GetPaneAddresses),
// internal/robot/watch_bead.go (GetWatchBead, findBeadMentionMatches) and
// the --robot-logs, --robot-pane-address and --robot-watch-bead branches
// of internal/cli/root.go at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: the session is a herdr
// workspace, named exactly by its label or id, and its panes are the
// workspace's (core/session.Panes), a pane without an agent being ntm's
// user pane; a pane is read with herdr's pane.read (recent); ntm's pane
// index is the slot's M; a pane index or selector takes nhm's pane forms
// (the operator's ruling of 2026-10-24: pane M of the agent tab); a
// pane's selector and target are its slot (or M on a session with one
// tab) and its herdr pane id; the bead's status is br show's in the cwd,
// as watch --bead reads it; the hints name nhm.
package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/session"
	"github.com/arosl/nhm/internal/robot"
)

// robotListHint is ntm's hint for a session it does not have.
const robotListHint = "Use 'nhm list' to see available sessions"

// robotSession is a robot call's session: a herdr workspace named exactly
// by its label or id, with its panes and active bindings.
type robotSession struct {
	name, workspace, activeTab string
	panes                      []session.Pane
	bound                      []bindings.Binding
}

// loadRobotSession finds the session; code is the robot error code of a
// failure.
func loadRobotSession(ctx context.Context, ie *instanceEnv, name string) (robotSession, string, error) {
	s := robotSession{name: strings.TrimSpace(name)}
	if s.name == "" {
		return s, robot.ErrCodeSessionNotFound, fmt.Errorf("session is required")
	}
	snap, err := ie.herdr.Snapshot(ctx)
	if err != nil {
		return s, robot.ErrCodeDependencyMissing, err
	}
	var labelled []string
	for _, w := range snap.Workspaces {
		switch {
		case w.WorkspaceID == s.name:
			s.workspace, s.activeTab, labelled = w.WorkspaceID, w.ActiveTabID, nil
		case w.Label == s.name && s.workspace == "":
			labelled = append(labelled, w.WorkspaceID)
			s.activeTab = w.ActiveTabID
		}
		if s.workspace != "" {
			break
		}
	}
	switch {
	case s.workspace != "":
	case len(labelled) == 1:
		s.workspace = labelled[0]
	case len(labelled) > 1:
		return s, robot.ErrCodeInvalidFlag, fmt.Errorf("session %q matches workspaces %s; give a workspace id", s.name, strings.Join(labelled, ", "))
	default:
		return s, robot.ErrCodeSessionNotFound, fmt.Errorf("session '%s' not found", s.name)
	}
	s.bound, err = bindings.NewStore(ie.db).List(ctx, bindings.Filter{WorkspaceID: s.workspace, Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return s, robot.ErrCodeInternalError, err
	}
	s.panes = session.Panes(snap, s.workspace, s.bound)
	return s, "", nil
}

// multiWindow reports whether the panes span several tabs (ntm's
// PanesSpanMultipleWindows).
func multiWindow(panes []session.Pane) bool {
	tabs := map[string]bool{}
	for _, p := range panes {
		tabs[p.TabID] = true
	}
	return len(tabs) > 1
}

// paneTargetKey is ntm's PaneTargetKey on herdr: the slot N.M on a session
// with several tabs, the slot's M otherwise, and the herdr pane id for a
// pane without a slot.
func paneTargetKey(p session.Pane, multi bool) string {
	switch {
	case p.Slot == "":
		return p.PaneID
	case multi:
		return p.Slot
	}
	return strconv.Itoa(p.Index)
}

// selectRobotPanes resolves a robot call's pane selectors (N, W.P, a herdr
// pane id) over the session's panes, in their order; a malformed selector
// is INVALID_FLAG, one that names no pane PANE_NOT_FOUND.
func (s robotSession) selectRobotPanes(selectors []string) ([]session.Pane, string, error) {
	var out []session.Pane
	taken := map[string]bool{}
	for _, sel := range selectors {
		t, err := session.ParsePane(sel, session.AgentTabs(s.bound), s.activeTab)
		if err != nil {
			return nil, robot.ErrCodeInvalidFlag, err
		}
		found := false
		for _, p := range s.panes {
			if session.MatchPane(p, t) {
				found = true
				if !taken[p.PaneID] {
					taken[p.PaneID] = true
					out = append(out, p)
				}
				break
			}
		}
		if !found {
			return nil, robot.ErrCodePaneNotFound, fmt.Errorf("pane %s not found in session %s", sel, s.name)
		}
	}
	// Topology order, as ntm resolves its selectors.
	var ordered []session.Pane
	for _, p := range s.panes {
		if taken[p.PaneID] {
			ordered = append(ordered, p)
		}
	}
	return ordered, "", nil
}

// splitSelectors is ntm's ParsePaneSelectorsArg: comma-separated
// selectors, blanks dropped; "all" or none is every pane.
func splitSelectors(arg string) []string {
	if strings.TrimSpace(arg) == "" || strings.EqualFold(strings.TrimSpace(arg), "all") {
		return nil
	}
	var out []string
	for _, p := range strings.Split(arg, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parsePanesArg is ntm's ParsePanesArg: comma-separated pane indices.
func parsePanesArg(arg string) ([]int, error) {
	if arg == "" || strings.ToLower(arg) == "all" {
		return []int{}, nil
	}
	var panes []int
	for _, part := range strings.Split(arg, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		idx, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("invalid pane index '%s': %w", part, err)
		}
		if idx < 0 {
			return nil, fmt.Errorf("pane index must be non-negative, got %d", idx)
		}
		panes = append(panes, idx)
	}
	return panes, nil
}

// robotParseFail is the INVALID_FLAG answer to a robot call's flags that
// do not parse.
func robotParseFail(stdout io.Writer, err error, now time.Time) int {
	msg := "unexpected arguments"
	if err != nil {
		msg = err.Error()
	}
	return robotFail(stdout, robot.ErrCodeInvalidFlag, msg, "nhm --robot-capabilities lists the robot flags", "auto", robotError, now)
}

// runRobotLogs answers ntm's --robot-logs=S [--logs-panes=1,2]
// [--logs-limit=N]: the last lines of every agent pane.
func runRobotLogs(args []string, stdout io.Writer) int {
	now := time.Now()
	fs, root := newRobotFlagSet(args, stdout)
	defer root.undo()
	name := fs.String("robot-logs", "", "")
	logsPanes := fs.String("logs-panes", "", "")
	limit := fs.Int("logs-limit", robot.DefaultLogsLimit, "")
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
	indices, err := parsePanesArg(*logsPanes)
	if err != nil {
		return robotFail(stdout, robot.ErrCodeInvalidFlag, err.Error(), "Use comma-separated numeric pane indices", root.format, robotError, now)
	}
	keep := map[string]bool{}
	for _, idx := range indices {
		t, err := session.ParsePane(strconv.Itoa(idx), session.AgentTabs(s.bound), s.activeTab)
		if err != nil {
			return robotFail(stdout, robot.ErrCodeInvalidFlag, err.Error(), "Use comma-separated numeric pane indices", root.format, robotError, now)
		}
		keep[t.Slot] = true
	}
	n := *limit
	if n <= 0 {
		n = robot.DefaultLogsLimit
	}
	out := robot.LogsOutput{RobotResponse: robot.NewResponse(true, root.format, now), Session: s.name, CapturedAt: now.UTC(),
		Panes: []robot.PaneLogs{}}
	for _, p := range s.panes {
		if len(keep) > 0 && (p.Slot == "" || !keep[p.Slot]) {
			continue
		}
		if p.Type == session.UserType {
			continue // user panes are left out, as in ntm
		}
		pl := robot.PaneLogs{Pane: p.Index, AgentType: p.Type, Lines: []string{}}
		text, err := readPane(ctx, ie.herdr, p.PaneID, max(n*2, 200))
		pl.CapturedAt = time.Now().UTC()
		if err != nil {
			pl.Lines = []string{fmt.Sprintf("[error capturing pane output: %v]", err)}
		} else {
			pl.Lines, pl.Truncated = logLines(text, n, nil)
		}
		pl.LineCount = len(pl.Lines)
		out.Panes = append(out.Panes, pl)
		out.Summary.TotalPanes++
		out.Summary.TotalLines += pl.LineCount
		if pl.Truncated {
			out.Summary.TruncatedPanes++
		}
	}
	return robotWrite(stdout, out, robotOK)
}

// runRobotPaneAddress answers ntm's --robot-pane-address=S: every pane's
// addressing card, in topology order.
func runRobotPaneAddress(args []string, stdout io.Writer) int {
	now := time.Now()
	fs, root := newRobotFlagSet(args, stdout)
	defer root.undo()
	name := fs.String("robot-pane-address", "", "")
	socket := fs.String("socket", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return robotParseFail(stdout, err, now)
	}
	if code, done := applyRobotRoot(root, now); done {
		return code
	}
	out := robot.PaneAddressOutput{RobotResponse: robot.NewResponse(true, root.format, now), Session: strings.TrimSpace(*name),
		Panes: []robot.PaneAddressEntry{}}
	fail := func(code, msg, hint string) int {
		out.RobotResponse = robot.NewError(code, msg, hint, root.format, now).RobotResponse
		out.Meta = &robot.ResponseMeta{ExitCode: robotError}
		return robotWrite(stdout, out, robotError)
	}
	ie, err := openInstance(*socket)
	if err != nil {
		return fail(robot.ErrCodeDependencyMissing, err.Error(), "run inside herdr or give --socket")
	}
	defer ie.db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, code, err := loadRobotSession(ctx, ie, *name)
	if err != nil {
		return fail(code, err.Error(), robotListHint)
	}
	multi := multiWindow(s.panes)
	out.MultiWindow = multi
	for _, p := range s.panes {
		out.Panes = append(out.Panes, robot.PaneAddressEntry{Selector: paneTargetKey(p, multi), PaneID: p.PaneID,
			WindowIndex: p.Window, PaneIndex: p.Index, Title: p.Title, Type: p.Type, TmuxTarget: p.PaneID})
	}
	return robotWrite(stdout, out, robotOK)
}

// runRobotWatchBead answers ntm's --robot-watch-bead=S --bead=ID
// [--panes=P] [--lines=N] [--interval=D]: the bead's mentions in each
// agent pane's recent output, once, and its status.
func runRobotWatchBead(args []string, stdout io.Writer) int {
	now := time.Now()
	fs, root := newRobotFlagSet(args, stdout)
	defer root.undo()
	name := fs.String("robot-watch-bead", "", "")
	beadID := fs.String("bead", "", "")
	panes := fs.String("panes", "", "")
	lines := fs.Int("lines", 200, "")
	intervalS := fs.String("interval", "", "")
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
	interval := 30 * time.Second
	if strings.TrimSpace(*intervalS) != "" {
		if interval, err = parseNTMDurationWithDefault(*intervalS, time.Millisecond); err != nil {
			return robotFail(stdout, robot.ErrCodeInvalidFlag, err.Error(), "Use a duration such as 30s or 500ms", root.format, robotError, now)
		}
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	n := *lines
	if n <= 0 {
		n = 200
	}
	out := robot.WatchBeadOutput{RobotResponse: robot.NewResponse(true, root.format, now), Session: s.name,
		BeadID: strings.TrimSpace(*beadID), CheckedAt: now.UTC(), Interval: interval.String(), Mentions: []robot.BeadMention{},
		BeadStatus: "unknown"}
	fail := func(code, msg, hint string) int {
		out.RobotResponse = robot.NewError(code, msg, hint, root.format, now).RobotResponse
		out.Meta = &robot.ResponseMeta{ExitCode: robotError}
		return robotWrite(stdout, out, robotError)
	}
	if out.BeadID == "" {
		return fail(robot.ErrCodeInvalidFlag, "bead ID is required", "Provide --bead=<id> with --robot-watch-bead")
	}
	mentionRE, err := beadMentionRegexp(out.BeadID)
	if err != nil {
		return fail(robot.ErrCodeInvalidFlag, err.Error(), "Use a non-empty bead identifier")
	}
	scanned := s.panes
	if sel := splitSelectors(*panes); len(sel) > 0 {
		var code string
		if scanned, code, err = s.selectRobotPanes(sel); err != nil {
			return fail(code, err.Error(), "Use comma-separated N, W.P or herdr pane id selectors, e.g. --panes=1,2.0")
		}
	}
	multi := multiWindow(s.panes)
	for _, p := range scanned {
		if p.Type == session.UserType {
			continue
		}
		text, err := readPane(ctx, ie.herdr, p.PaneID, n)
		if err != nil {
			continue
		}
		for i, line := range splitRobotLines(text) {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || !mentionRE.MatchString(trimmed) {
				continue
			}
			out.Mentions = append(out.Mentions, robot.BeadMention{Pane: p.Index, PaneRef: paneTargetKey(p, multi), PaneID: p.PaneID,
				AgentType: p.Type, Line: trimmed, LineNum: i + 1, Timestamp: out.CheckedAt})
		}
		out.PanesScanned++
	}
	if status, err := watchBeadStatus(ctx, "", out.BeadID); err != nil {
		out.StatusError = err.Error()
	} else {
		out.BeadStatus = status
	}
	return robotWrite(stdout, out, robotOK)
}

// splitRobotLines is ntm's splitLines: the lines of s, with \r\n and \r
// as line ends, without a last empty line.
func splitRobotLines(s string) []string {
	if s == "" {
		return []string{}
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
