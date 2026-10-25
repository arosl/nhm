// Derived from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/dialogs.go (AnswerDialog, PrintAnswerDialog) and the
// --robot-answer-dialog branch of internal/cli/root.go at commit
// 9d5ed977; see LICENSE in this directory and PROVENANCE.md. Local
// changes: the session and its panes are herdr's, as --robot-dialogs
// finds them (robot_dialogs.go); the screens are herdr's visible pane
// reads; the keys go through the send core (send.Keys), which holds a
// bound agent's target lock for the whole answer and checks the binding
// and the agent first, and herdr types them under its own key names;
// keys_sent keeps ntm's names; a failure before any key, of the lock or
// the checks, is INTERNAL_ERROR; the hints name nhm and herdr.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/integrations/kinds/dialogs"
	"github.com/arosl/nhm/internal/robot"
)

// ntm's pauses: between two keys of an answer, and before the screen is
// read again to see that the dialog went.
const (
	answerKeyGap    = 150 * time.Millisecond
	answerSettleGap = 700 * time.Millisecond
)

// answerStop is a check before a key that refused to send it.
type answerStop struct {
	err  error
	hint string
}

func (a *answerStop) Error() string { return a.err.Error() }

// runRobotAnswerDialog answers ntm's --robot-answer-dialog=S --panes=P
// --choice=C: on one agent pane, the dialog is classified, the choice is
// mapped to its keys under ntm's policy (no accept on a destructive
// confirm), the keys are typed 150 ms apart (a cursor menu is read again
// before Enter), and the screen is read again after 700 ms: resolved only
// when no dialog is left.
func runRobotAnswerDialog(args []string, stdout io.Writer) int {
	now := time.Now()
	fs, root := newRobotFlagSet(args, stdout)
	defer root.undo()
	name := fs.String("robot-answer-dialog", "", "")
	panes := fs.String("panes", "", "")
	choice := fs.String("choice", "", "")
	socket := fs.String("socket", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return robotParseFail(stdout, err, now)
	}
	if code, done := applyRobotRoot(root, now); done {
		return code
	}
	if *name == "" {
		return robotFail(stdout, robot.ErrCodeInvalidFlag, "--robot-answer-dialog requires a session name",
			"Use --robot-answer-dialog=SESSION; 'nhm list' shows available sessions", "json", robotError, now)
	}
	out := robot.AnswerDialogOutput{RobotResponse: robot.NewResponse(true, root.format, now), Session: strings.TrimSpace(*name),
		Choice: strings.TrimSpace(*choice)}
	fail := func(code, msg, hint string) int {
		out.RobotResponse = robotFailure(code, msg, hint, now)
		return robotWrite(stdout, out, robotError)
	}
	selectors := robotPaneList(*panes)
	if len(selectors) == 0 {
		return fail(robot.ErrCodeInvalidFlag, "--panes is required", "Answering a dialog targets exactly one pane: --panes=SELECTOR")
	}
	ie, err := openInstance(*socket)
	if err != nil {
		return fail(robot.ErrCodeDependencyMissing, err.Error(), "run inside herdr or give --socket")
	}
	defer ie.db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, targets, multi, f := lifecycleTargets(ctx, ie, *name, selectors)
	if f != nil {
		out.RobotResponse = *f
		return robotWrite(stdout, out, robotError)
	}
	out.Session = s.name
	if len(targets) != 1 {
		return fail(robot.ErrCodeInvalidFlag, fmt.Sprintf("--robot-answer-dialog requires exactly one target pane, resolved %d", len(targets)),
			"Narrow --panes to a single selector")
	}
	p := targets[0]
	out.Pane, out.Target = paneTargetKey(p, multi), p.PaneID
	agentType := paneAgentType(p)

	screen, err := readVisible(ctx, ie.herdr, p.PaneID)
	if err != nil {
		return fail(robot.ErrCodeInternalError, err.Error(), "Failed to capture the pane")
	}
	out.Before = dialogs.Classify(screen, agentType)
	keys, err := dialogs.ResolveAnswer(out.Before, *choice)
	if err != nil {
		return fail(robot.ErrCodeInvalidFlag, err.Error(), "Use --robot-dialogs to inspect the dialog class and extracted options first")
	}
	keys = dialogs.AdaptKeys(keys, out.Before, agentType)
	herdrKeys := make([]string, len(keys))
	for i, k := range keys {
		herdrKeys[i] = dialogs.HerdrKey(k)
	}
	// Enter on a cursor menu only when the screen shows the cursor on the
	// chosen option: a dropped key or a new modal must not take it.
	verify := func(i int) error {
		if out.Before.InputMode != "cursor" || keys[i] != "Enter" {
			return nil
		}
		current, err := readVisible(ctx, ie.herdr, p.PaneID)
		if err != nil {
			return &answerStop{err, "Could not verify the selected option; Enter was not sent"}
		}
		out.After = dialogs.Classify(current, agentType)
		if err := dialogs.VerifyCursorSelection(out.Before, out.After, *choice); err != nil {
			return &answerStop{err, "Enter was not sent; re-run --robot-dialogs before retrying"}
		}
		return nil
	}
	sent, err := send.Keys(ctx, send.KeysEnv{Herdr: ie.herdr, Bindings: bindings.NewStore(ie.db), InstanceDir: ie.inst.Dir},
		send.KeysRequest{PaneID: p.PaneID, Binding: p.Binding, Keys: herdrKeys, Gap: answerKeyGap, Before: verify})
	out.KeysSent = keys[:sent]
	if err != nil {
		var stop *answerStop
		var ke *send.KeyError
		switch {
		case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
			return fail(robot.ErrCodeTimeout, err.Error(), "Retry after the cancellation clears")
		case errors.As(err, &stop):
			return fail(robot.ErrCodeInternalError, stop.Error(), stop.hint)
		case errors.As(err, &ke):
			return fail(robot.ErrCodeInternalError, fmt.Sprintf("send key %q: %v", keys[ke.Index], ke.Err), "Check the pane's agent: nhm --robot-dialogs=SESSION")
		}
		return fail(robot.ErrCodeInternalError, err.Error(), "No key was sent; re-run --robot-dialogs before retrying")
	}

	timer := time.NewTimer(answerSettleGap)
	select {
	case <-ctx.Done():
		timer.Stop()
	case <-timer.C:
	}
	if after, err := readVisible(ctx, ie.herdr, p.PaneID); err == nil {
		out.After = dialogs.Classify(after, agentType)
	} else {
		out.After = dialogs.State{Class: dialogs.Unknown, Evidence: fmt.Sprintf("post-answer capture failed: %v", err)}
	}
	robot.ApplyDialogResolution(&out)
	if !out.Success {
		out.OutputFormat = "json"
		out.Meta = &robot.ResponseMeta{ExitCode: robotError, Command: robotCommand}
		return robotWrite(stdout, out, robotError)
	}
	return robotWrite(stdout, out, robotOK)
}
