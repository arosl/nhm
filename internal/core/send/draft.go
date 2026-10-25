package send

import (
	"context"
	"fmt"

	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/kinds"
)

// Receipt error codes of a target that the draft check refuses.
const (
	CodeShellDraft   = "shell_draft"
	CodeScreenUnread = "screen_unread"
)

// draftLines is how many of the pane's last lines the draft check reads,
// as spawn's and recycle's dialog checks do.
const draftLines = 40

// ScreenReader reads a pane's last lines; *herdr.Client has it.
type ScreenReader interface {
	AgentRead(ctx context.Context, p herdr.AgentReadParams) (herdr.PaneRead, error)
}

// ShellDraft is the check that nhm runs right before it types into an
// agent's input box: a send's prompt, wake's nudge and recycle's exit
// command. herdr types the text after what the box holds, so a "!" that a
// person left there makes it a shell command that no guard screens
// (proved with Claude Code, Codex and pi; nhm-gb0). It reads the pane's
// last lines, and gives a receipt code and why when the kind's screen
// rule (kinds.Adapter.ShellDraft) finds such a draft, or when the read
// fails: nhm types into no box of such a kind that it cannot read. A kind
// without a rule is not read (the operator's ruling, 2026-10-24;
// nhm-mowp).
func ShellDraft(ctx context.Context, h ScreenReader, pane string, a kinds.Adapter) (code, reason string) {
	if a.ShellDraft == nil {
		return "", ""
	}
	n := draftLines
	screen, err := h.AgentRead(ctx, herdr.AgentReadParams{Target: pane, Source: herdr.ReadRecent, Lines: &n})
	if err != nil {
		return CodeScreenUnread, fmt.Sprintf("nhm could not read the agent's input box (%v), and a %q that a person left there "+
			"would make nhm's text a shell command; try again", err, a.Escape())
	}
	d := a.ShellDraft(screen.Text)
	if d == "" {
		return "", ""
	}
	return CodeShellDraft, fmt.Sprintf("the input box shows %s: herdr types nhm's text after what a person left there, "+
		"so the agent would run it as a shell command that no guard such as dcg screens; clear the input box in pane %s", d, pane)
}
