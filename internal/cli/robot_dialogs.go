// Derived from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/dialogs.go (GetDialogs, PrintDialogs),
// internal/robot/lifecycle.go (resolveLifecycleTargets),
// internal/robot/restart_pane.go (restartTargetIsAgent,
// restartPaneAgentType), internal/tmux/session.go (ResolvePaneSelectors
// and its PaneSelectorError texts) and the --robot-dialogs branch of
// internal/cli/root.go at commit 9d5ed977; see LICENSE in this directory
// and PROVENANCE.md. Local changes: the session is a herdr workspace,
// named exactly by its label or id; its panes are the workspace's
// (core/session.Panes), a pane without an agent being ntm's user pane; a
// pane's type is its kind's (herdr's detection replaces ntm's title
// parsing); a selector takes nhm's pane forms (N, W.P, a herdr pane id or
// an agent's name), not tmux's %N; the screen is herdr's pane.read
// (visible) without its control characters; a pane's key is its slot (or
// its M on a session with one tab), its target the herdr pane id; herdr
// out of reach is DEPENDENCY_MISSING; the hints name nhm.
package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/panestate"
	"github.com/arosl/nhm/internal/core/session"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/kinds/dialogs"
	"github.com/arosl/nhm/internal/robot"
)

// runRobotDialogs answers ntm's --robot-dialogs=S [--panes=P]: the dialog
// on each agent pane of the session, from its visible screen, with the
// options it shows. It only reads.
func runRobotDialogs(args []string, stdout io.Writer) int {
	now := time.Now()
	fs, root := newRobotFlagSet(args, stdout)
	defer root.undo()
	name := fs.String("robot-dialogs", "", "")
	panes := fs.String("panes", "", "")
	socket := fs.String("socket", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return robotParseFail(stdout, err, now)
	}
	if code, done := applyRobotRoot(root, now); done {
		return code
	}
	if *name == "" {
		return robotFail(stdout, robot.ErrCodeInvalidFlag, "--robot-dialogs requires a session name",
			"Use --robot-dialogs=SESSION; 'nhm list' shows available sessions", "json", robotError, now)
	}
	out := robot.DialogsOutput{RobotResponse: robot.NewResponse(true, root.format, now), Session: strings.TrimSpace(*name),
		Panes: []robot.PaneDialog{}}
	ie, err := openInstance(*socket)
	if err != nil {
		out.RobotResponse = robotFailure(robot.ErrCodeDependencyMissing, err.Error(), "run inside herdr or give --socket", now)
		return robotWrite(stdout, out, robotError)
	}
	defer ie.db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, targets, multi, fail := lifecycleTargets(ctx, ie, *name, robotPaneList(*panes))
	if fail != nil {
		out.RobotResponse = *fail
		return robotWrite(stdout, out, robotError)
	}
	out.Session = s.name
	for _, p := range targets {
		pd := robot.PaneDialog{Pane: paneTargetKey(p, multi), Target: p.PaneID, AgentType: paneAgentType(p)}
		screen, err := readVisible(ctx, ie.herdr, p.PaneID)
		if err != nil {
			pd.Dialog = dialogs.State{Class: dialogs.Unknown, Evidence: fmt.Sprintf("capture failed: %v", err)}
		} else {
			pd.Dialog = dialogs.Classify(screen, pd.AgentType)
		}
		out.Panes = append(out.Panes, pd)
	}
	return robotWrite(stdout, out, robotOK)
}

// robotPaneList is ntm's --panes of the lifecycle and dialog verbs: the
// value split at commas as given, so "1," holds an empty selector; none
// for an empty value.
func robotPaneList(arg string) []string {
	if arg == "" {
		return nil
	}
	return strings.Split(arg, ",")
}

// robotFailure is a failed robot answer's envelope, as ntm writes it: in
// JSON, whatever format the call asked for.
func robotFailure(code, msg, hint string, now time.Time) robot.RobotResponse {
	r := robot.NewError(code, msg, hint, "json", now).RobotResponse
	r.Meta = &robot.ResponseMeta{ExitCode: robotError, Command: robotCommand}
	return r
}

// readVisible reads a pane's visible screen, without its control
// characters (ntm's capture-pane of the visible rows).
func readVisible(ctx context.Context, h *herdr.Client, paneID string) (string, error) {
	read, err := h.PaneRead(ctx, herdr.PaneReadParams{PaneID: paneID, Source: herdr.ReadVisible})
	if err != nil {
		return "", err
	}
	return stripControl(read.Text), nil
}

// paneAgentType is ntm's restartPaneAgentType on herdr: the pane's type,
// resolved; herdr's detection gives it, so no title is parsed.
func paneAgentType(p session.Pane) string {
	return panestate.ResolveAgentType(p.Type)
}

// isAgentType is ntm's restartTargetIsAgent: a type that names an agent
// CLI, not a user shell or a pane nothing identifies.
func isAgentType(t string) bool {
	switch t {
	case "", session.UserType, "unknown":
		return false
	}
	return true
}

// lifecycleTargets is ntm's resolveLifecycleTargets, which the dialog and
// lifecycle verbs share: the session, then the panes that selectors name
// or, without selectors, every agent pane; never a pane without an agent.
// fail is the call's error envelope.
func lifecycleTargets(ctx context.Context, ie *instanceEnv, name string, selectors []string) (s robotSession, targets []session.Pane, multi bool, fail *robot.RobotResponse) {
	now := time.Now()
	failWith := func(code, msg, hint string) *robot.RobotResponse {
		r := robotFailure(code, msg, hint, now)
		return &r
	}
	s, code, err := loadRobotSession(ctx, ie, name)
	if err != nil {
		return s, nil, false, failWith(code, err.Error(), robotListHint)
	}
	multi = multiWindow(s.panes)
	if len(selectors) > 0 {
		targets, err = resolvePaneSelectors(s, selectors, multi)
		if err != nil {
			return s, nil, multi, failWith(robot.ErrCodeInvalidFlag, err.Error(),
				"Use --panes with N, W.P or herdr pane id selectors; --robot-pane-address=SESSION lists them")
		}
	} else {
		for _, p := range s.panes {
			if isAgentType(paneAgentType(p)) {
				targets = append(targets, p)
			}
		}
	}
	if len(targets) == 0 {
		return s, nil, multi, failWith(robot.ErrCodeInvalidFlag, "no agent panes matched",
			"Use --panes to select agent panes; --robot-pane-address=SESSION lists them")
	}
	// The verbs act on the agent; a user pane has none, and keys typed
	// into it reach the operator's shell.
	for _, p := range targets {
		if t := paneAgentType(p); !isAgentType(t) {
			return s, nil, multi, failWith(robot.ErrCodeInvalidFlag,
				fmt.Sprintf("pane %s is not an agent pane (type %s)", paneTargetKey(p, multi), t),
				"Restrict --panes to agent panes; --robot-pane-address=SESSION shows pane types")
		}
	}
	return s, targets, multi, nil
}

// resolvePaneSelectors is ntm's ResolvePaneSelectors on nhm's pane forms:
// each selector must name a pane; the panes, each once, in the session's
// order. Its errors are ntm's texts.
func resolvePaneSelectors(s robotSession, selectors []string, multi bool) ([]session.Pane, error) {
	taken := map[string]bool{}
	for _, raw := range selectors {
		text := strings.TrimSpace(raw)
		if text == "" {
			return nil, fmt.Errorf("invalid pane selector %q: expected N, W.P, or a herdr pane id", raw)
		}
		t, err := session.ParsePane(text, session.AgentTabs(s.bound), s.activeTab)
		if err != nil {
			return nil, err
		}
		found := false
		for _, p := range s.panes {
			if session.MatchPane(p, t) {
				found, taken[p.PaneID] = true, true
			}
		}
		if !found {
			refs := make([]string, 0, len(s.panes))
			for _, p := range s.panes {
				refs = append(refs, paneTargetKey(p, multi)+" ("+p.PaneID+")")
			}
			return nil, fmt.Errorf("pane selector %q not found; available: %s", raw, strings.Join(refs, ", "))
		}
	}
	var out []session.Pane
	for _, p := range s.panes {
		if taken[p.PaneID] {
			out = append(out, p)
		}
	}
	return out, nil
}
