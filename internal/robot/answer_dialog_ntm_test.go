// Ported from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/dialogs_test.go (TestApplyDialogResolution) at commit
// 9d5ed977; see LICENSE in this directory and PROVENANCE.md. Local
// changes: the names; a table without subtests.

package robot

import (
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/integrations/kinds/dialogs"
)

// bd-70c00: Resolved used to be `After == None || After != Before`, so a
// dialog REPLACED by a different dialog reported resolved:true with a modal
// still blocking the pane. The caller then sent work the new dialog swallowed
// — success without verified effect, the exact failure ntm-epu6 exists to
// eliminate.
func TestApplyDialogResolution(t *testing.T) {
	for _, tt := range []struct {
		name                     string
		before, after            string
		wantResolved, wantOK     bool
		wantFollowUp, wantErrHas string
	}{
		{name: "dialog cleared", before: dialogs.DestructiveConfirm, after: dialogs.None, wantResolved: true, wantOK: true},
		{name: "same dialog still present", before: dialogs.TrustPrompt, after: dialogs.TrustPrompt, wantErrHas: "still present"},
		{name: "answering surfaced a DIFFERENT dialog", before: dialogs.DestructiveConfirm, after: dialogs.RateLimitOptions,
			wantFollowUp: dialogs.RateLimitOptions, wantErrHas: "follow-up dialog"},
		{name: "post-answer capture failed", before: dialogs.TrustPrompt, after: dialogs.Unknown,
			wantFollowUp: dialogs.Unknown, wantErrHas: "follow-up dialog"},
	} {
		out := &AnswerDialogOutput{RobotResponse: NewResponse(true, "json", time.Now()),
			Before: dialogs.State{Class: tt.before}, After: dialogs.State{Class: tt.after}}
		ApplyDialogResolution(out)
		if out.Resolved != tt.wantResolved || out.Success != tt.wantOK || out.FollowUpDialog != tt.wantFollowUp ||
			(tt.wantErrHas != "" && !strings.Contains(out.Error, tt.wantErrHas)) || (!tt.wantOK && out.ErrorCode != ErrCodeInternalError) {
			t.Errorf("%s: %+v", tt.name, out)
		}
	}
}
