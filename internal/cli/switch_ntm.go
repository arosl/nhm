// Derived from ntm (github.com/Dicklesworthstone/ntm)
// internal/cli/agent_switch.go (newSwitchAgentCmd, encodeAgentSwitchJSON)
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: ntm's agent registry is nhm's bindings in the live panes
// of each herdr workspace (core/session.Agents); herdr has no attach, so
// switch always selects: herdr's pane.focus moves the attached clients to
// the pane, its tab and its workspace, where ntm runs tmux switch-client
// and select-pane; the sessions are searched by name, as tmux lists them;
// the output goes to cobra's writers.
package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/arosl/nhm/internal/core/session"
)

const switchUsage = `Usage: nhm switch <agent-name> [--session S] [--socket PATH] [--json]

ntm's switch on herdr: focus the pane of an Agent Mail agent. nhm looks
the name up among the agents bound in each herdr workspace (or only in
--session, a workspace's label or id) and focuses the first exact
match: herdr's attached clients move to that pane, its tab and its
workspace.
`

func newSwitchAgentCmd() *cobra.Command {
	var sessionFlag, socket string
	cmd := &cobra.Command{
		Use:   "switch <agent-name>",
		Short: "Switch herdr focus to a pane by Agent Mail agent name",
		Long: `Resolves <agent-name> through the session's bound agents to a live herdr
pane and focuses it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agentName := strings.TrimSpace(args[0])
			if agentName == "" {
				return fmt.Errorf("agent name cannot be empty")
			}
			return runSwitch(cmd, socket, sessionFlag, agentName)
		},
	}
	cmd.Flags().StringVar(&sessionFlag, "session", "", "Restrict lookup to a single session (default: search all)")
	cmd.Flags().StringVar(&socket, "socket", "", "herdr API socket (nhm)")
	return cmd
}

func runSwitch(cmd *cobra.Command, socket, sessionFlag, agentName string) error {
	ie, err := openInstance(socket)
	if err != nil {
		return err
	}
	defer ie.db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sessions, err := allSessionPanes(ctx, ie)
	if err != nil {
		return fmt.Errorf("listing herdr sessions: %w", err)
	}
	for _, s := range sessions {
		// --session names a workspace by its label or id.
		if sessionFlag != "" && s.session != sessionFlag && s.workspace != sessionFlag {
			continue
		}
		for _, e := range session.Agents(s.panes) {
			if e.Name != agentName {
				continue
			}
			if _, err := ie.herdr.PaneFocus(ctx, e.PaneID); err != nil {
				return fmt.Errorf("herdr pane.focus %s: %w", e.PaneID, err)
			}
			if wantJSON(cmd) {
				return encodeJSONIndent(cmd.OutOrStdout(), map[string]any{"action": "selected", "agent_name": e.Name,
					"session": s.session, "pane_index": e.PaneIndex, "pane_id": e.PaneID, "pane_title": e.PaneTitle})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Switched to pane %s (%s, pane %d) in session %s\n", e.PaneID, e.Name, e.PaneIndex, s.session)
			return nil
		}
	}
	return fmt.Errorf("no agent named %q found in any session", agentName)
}
