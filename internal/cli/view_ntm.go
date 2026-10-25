// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/cli/view.go (newViewCmd, runView, buildViewResponse); see
// LICENSE in this directory and PROVENANCE.md. Local changes: a session
// is a herdr workspace; ntm's tiling is the zoom off in every tab of the
// workspace (unzoomWorkspace), as herdr lays out the panes itself; ntm's
// attach is the workspace's focus; --socket beside ntm's flags; the
// output goes to cobra's writers.
package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/arosl/nhm/internal/ui/tui/theme"
)

const viewUsage = `Usage: nhm view [session-name] [--socket PATH] [--json]   (also tile, v)

ntm's view on herdr: take the zoom off in every tab of the session's
workspace, so that each tab shows all its panes, then focus the
workspace, as ntm attaches. Open herdr to see it. herdr lays out the
panes itself: nhm applies no layout of its own, and its spawn grid stays.

Without a session: the calling pane's workspace, the cwd's, the only
one, or ntm's session picker; a unique prefix names one. Prints "✓ Tiled
layout applied to 'S'"; --json gives ntm's SuccessResponse and focuses
nothing. Changes no agent, so it runs whoever owns this herdr instance.
`

func newViewCmd() *cobra.Command {
	var socket string
	cmd := &cobra.Command{
		Use:     "view [session-name]",
		Aliases: []string{"v", "tile"},
		Short:   "View all panes in a session (unzoom, tile, attach)",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var name string
			if len(args) > 0 {
				name = args[0]
			}
			return runViewNTM(name, socket, wantJSON(cmd), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&socket, "socket", "", "herdr API socket")
	return cmd
}

// runViewNTM is ntm's runView. As in ntm, an error before the session is
// known is the root's (the robot error envelope under --json), and one
// after it ntm's JSON failure envelope.
func runViewNTM(name, socket string, asJSON bool, stdout, stderr io.Writer) error {
	ctx := context.Background()
	ie, err := openInstance(socket)
	if err != nil {
		return err
	}
	defer ie.db.Close()
	r, err := resolveSession(ctx, ie, name, asJSON, stdout, stderr)
	if err != nil {
		return err
	}
	if r.Session == "" {
		return nil
	}
	if err := unzoomWorkspace(ctx, ie.herdr, r.WorkspaceID); err != nil {
		err = fmt.Errorf("failed to apply layout: %w", err)
		if asJSON {
			return jsonFailure(stdout, err)
		}
		return err
	}
	if asJSON {
		return writeNTMJSON(stdout, successResponse{Success: true, Message: fmt.Sprintf("tiled layout applied to '%s'", r.Session)})
	}
	t := theme.Current()
	fmt.Fprintf(stdout, "%s✓%s Tiled layout applied to '%s'\n", colorize(t.Success), colorize(t.Text), r.Session)
	return ie.herdr.WorkspaceFocus(ctx, r.WorkspaceID)
}
