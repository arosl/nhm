// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/lifecycle.go (LifecyclePaneResult, ExitCLIOutput) at
// commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: only the output types.

package robot

// LifecyclePaneResult is per-pane structured evidence for a lifecycle verb.
type LifecyclePaneResult struct {
	Pane           string `json:"pane"`
	Target         string `json:"target"`
	AgentType      string `json:"agent_type"`
	ShellPID       int    `json:"shell_pid,omitempty"`
	AgentPIDs      []int  `json:"agent_pids,omitempty"`
	Exited         bool   `json:"exited"`
	Killed         []int  `json:"killed,omitempty"`
	ShellPreserved bool   `json:"shell_preserved"`
	Relaunched     bool   `json:"relaunched,omitempty"`
	Detail         string `json:"detail,omitempty"`

	// VerificationFailed distinguishes "the pane is gone" from "the pane
	// could not be read, so nothing was verified": shell_preserved false
	// alone would say the verb destroyed the pane.
	VerificationFailed bool `json:"verification_failed,omitempty"`
}

// ExitCLIOutput is the structured output for --robot-exit-cli.
type ExitCLIOutput struct {
	RobotResponse
	Session string                `json:"session"`
	Results []LifecyclePaneResult `json:"results"`
}
