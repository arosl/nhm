// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/dialogs.go (PaneDialog, DialogsOutput) at commit
// 9d5ed977; see LICENSE in this directory and PROVENANCE.md. Local
// changes: only the output types; a pane's dialog is the classifier's
// state (integrations/kinds/dialogs), whose fields and JSON are ntm's
// DialogState.

package robot

import "github.com/arosl/nhm/internal/integrations/kinds/dialogs"

// PaneDialog pairs a pane identity with its dialog state.
type PaneDialog struct {
	Pane      string        `json:"pane"`
	Target    string        `json:"target"`
	AgentType string        `json:"agent_type"`
	Dialog    dialogs.State `json:"dialog"`
}

// DialogsOutput is the structured output for --robot-dialogs.
type DialogsOutput struct {
	RobotResponse
	Session string       `json:"session"`
	Panes   []PaneDialog `json:"panes"`
}
