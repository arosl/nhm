// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/dialogs.go (AnswerDialogOutput, applyDialogResolution)
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: only the output type and its resolution; the dialog
// states are the classifier's (integrations/kinds/dialogs); the
// resolution is exported (ApplyDialogResolution).

package robot

import (
	"fmt"

	"github.com/arosl/nhm/internal/integrations/kinds/dialogs"
)

// AnswerDialogOutput is the structured output for --robot-answer-dialog.
type AnswerDialogOutput struct {
	RobotResponse
	Session  string        `json:"session"`
	Pane     string        `json:"pane"`
	Target   string        `json:"target"`
	Before   dialogs.State `json:"before"`
	Choice   string        `json:"choice"`
	KeysSent []string      `json:"keys_sent,omitempty"`
	After    dialogs.State `json:"after"`
	// Resolved means the pane is CLEAR: no dialog is blocking it. It is not
	// "the dialog changed" — a destructive confirm that, on declining, presents
	// a follow-up usage overlay has changed class while the pane is still
	// blocked, and a caller told resolved:true proceeds to send work the new
	// dialog swallows.
	Resolved bool `json:"resolved"`
	// FollowUpDialog names the dialog class that REPLACED the one that was
	// answered, when answering surfaced a different dialog rather than
	// clearing the pane. It is the actionable signal a class change carries:
	// the caller can answer the next one.
	FollowUpDialog string `json:"follow_up_dialog,omitempty"`
}

// ApplyDialogResolution decides whether answering actually cleared the
// pane. Resolved means the pane is CLEAR; a class change is reported as
// the follow-up dialog to answer next, not as resolution (ntm-epu6).
func ApplyDialogResolution(output *AnswerDialogOutput) {
	output.Resolved = output.After.Class == dialogs.None
	if output.Resolved {
		return
	}

	output.Success = false
	output.ErrorCode = ErrCodeInternalError
	if output.After.Class != output.Before.Class {
		output.FollowUpDialog = output.After.Class
		output.Error = fmt.Sprintf("answering surfaced a follow-up dialog (%s); the pane is still blocked", output.After.Class)
		output.Hint = "Answer the follow-up dialog with --robot-answer-dialog --choice, then re-check with --robot-dialogs"
		return
	}
	output.Error = "dialog still present after the answer"
	output.Hint = "Re-run --robot-dialogs; the dialog may need a different choice"
}
