// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/cli/diff.go (newDiffCmd, runDiff, resolvePane) and
// internal/tmux/session.go (FormatPaneName); see LICENSE in this directory
// and PROVENANCE.md. Local changes: a session is a herdr workspace and its
// panes are the workspace's (core/session.Panes); ntm's index is the
// grammar's pane forms (the slot's M in the session's agent tab, a slot,
// a herdr pane id); a pane's title is its herdr label, and an agent pane
// also answers to ntm's title, <session>__<type>_<n>, numbered by type in
// slot order, so ntm's suffix form (cc_1) finds it; nhm's Agent Mail and
// herdr names and kinds still find an agent; a pane is read with herdr's
// pane.read (recent), without control sequences; --socket beside ntm's
// flags; the output goes to cobra's writer.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/panediff"
	"github.com/arosl/nhm/internal/core/session"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/ui/tui/agent"
)

const diffUsage = `Usage: nhm diff [session] <pane1> <pane2> [-u|--unified] [--code-only] [--socket PATH] [--json]

ntm's diff on herdr: compare the output of two panes (the last 1000
lines of each): their line counts and how similar they are, as a
percentage. -u adds the diff (diff-match-patch's patch text, as ntm
prints it); --code-only compares only the fenced code blocks.

Without a session: the calling pane's workspace, the cwd's, the only
one, or ntm's session picker. A pane is ntm's index (the slot's M in the
session's agent tab), a slot N.M, a herdr pane id, a pane title or its
end, ntm's title <session>__<type>_<n> or its end (cc_1: the first
Claude), an Agent Mail or herdr name, or a kind (its first agent).
--json gives ntm's DiffResult (pane1, pane2, lines1, lines2, similarity,
diff).
`

// diffLines is how much of each pane diff reads, as ntm's capture.
const diffLines = 1000

func newDiffCmd() *cobra.Command {
	var unified, codeOnly bool
	var socket string
	cmd := &cobra.Command{
		Use:   "diff [session] <pane1> <pane2>",
		Short: "Compare output from two agent panes",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			var name, pane1, pane2 string
			if len(args) == 3 {
				name, pane1, pane2 = args[0], args[1], args[2]
			} else {
				pane1, pane2 = args[0], args[1]
			}
			return runDiffNTM(name, pane1, pane2, unified, codeOnly, socket, wantJSON(cmd), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().BoolVarP(&unified, "unified", "u", false, "Show unified diff")
	cmd.Flags().BoolVar(&codeOnly, "code-only", false, "Compare only extracted code blocks")
	cmd.Flags().StringVar(&socket, "socket", "", "herdr API socket")
	return cmd
}

// runDiffNTM is ntm's runDiff. Every error is the root's, as in ntm:
// "Error: <msg>", or the robot error envelope under --json; exit 1.
func runDiffNTM(name, pane1, pane2 string, unified, codeOnly bool, socket string, asJSON bool, stdout, stderr io.Writer) error {
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
		return errors.New("session is required")
	}
	snap, err := ie.herdr.Snapshot(ctx)
	if err != nil {
		return err
	}
	bound, err := bindings.NewStore(ie.db).List(ctx, bindings.Filter{WorkspaceID: r.WorkspaceID, Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return err
	}
	activeTab := ""
	for _, w := range snap.Workspaces {
		if w.WorkspaceID == r.WorkspaceID {
			activeTab = w.ActiveTabID
		}
	}
	panes := session.Panes(snap, r.WorkspaceID, bound)
	names := ntmPaneTitles(r.Session, panes)
	var titles, contents [2]string
	for i, text := range []string{pane1, pane2} {
		p, err := diffPaneOf(panes, names, text, r.Session, session.AgentTabs(bound), activeTab)
		if err != nil {
			return err
		}
		n := diffLines
		read, err := ie.herdr.PaneRead(ctx, herdr.PaneReadParams{PaneID: p.PaneID, Source: herdr.ReadRecent, Lines: &n})
		if err != nil {
			return fmt.Errorf("capturing pane %d: %w", i+1, err)
		}
		titles[i], contents[i] = firstOf(p.Title, names[p.PaneID], p.PaneID), stripControl(read.Text)
		if codeOnly {
			contents[i] = codeBlocks(contents[i])
		}
	}
	res := panediff.ComputeDiff(titles[0], contents[0], titles[1], contents[1])
	if asJSON {
		return writeNTMJSON(stdout, res)
	}
	fmt.Fprintf(stdout, "Comparing %s vs %s:\n", res.Pane1, res.Pane2)
	fmt.Fprintf(stdout, "  Lines: %d vs %d\n", res.LineCount1, res.LineCount2)
	fmt.Fprintf(stdout, "  Similarity: %.1f%%\n", res.Similarity*100)
	if unified {
		fmt.Fprintf(stdout, "\nDiff:\n%s\n", res.UnifiedDiff)
	}
	return nil
}

// ntmPaneTitles is each agent pane's ntm title (FormatPaneName),
// <session>__<type>_<n>: ntm numbers the agents of a type from 1 in the
// order it starts them, and nhm takes the panes in slot order, which is
// the order spawn and add gave them.
func ntmPaneTitles(sessionName string, panes []session.Pane) map[string]string {
	out := map[string]string{}
	count := map[string]int{}
	for _, p := range panes {
		if p.Type == session.UserType {
			continue
		}
		short := string(agent.AgentType(p.Type).Canonical())
		count[short]++
		out[p.PaneID] = sessionName + "__" + short + "_" + strconv.Itoa(count[short])
	}
	return out
}

// diffPaneOf is ntm's resolvePane on herdr: the index (and nhm's slot and
// herdr pane id), the title, the end of the title (ntm's title too: cc_1
// is <session>__cc_1), then nhm's Agent Mail or herdr name and kind.
func diffPaneOf(panes []session.Pane, names map[string]string, text, sessionName string, tabs []session.AgentTab, activeTab string) (session.Pane, error) {
	if t, err := session.ParsePane(text, tabs, activeTab); err == nil {
		for _, p := range panes {
			if (t.Form == session.PaneSlot && p.Slot == t.Slot) || (t.Form == session.PaneHerdrID && p.PaneID == t.PaneID) {
				return p, nil
			}
		}
	}
	for _, p := range panes {
		if p.Title == text || names[p.PaneID] == text {
			return p, nil
		}
	}
	for _, p := range panes {
		if (p.Title != "" && strings.HasSuffix(p.Title, text)) || (names[p.PaneID] != "" && strings.HasSuffix(names[p.PaneID], text)) {
			return p, nil
		}
	}
	for _, p := range panes {
		if b := p.Binding; b != nil && (strings.EqualFold(b.MailName, text) || strings.EqualFold(b.HerdrAlias, text)) {
			return p, nil
		}
	}
	kind := ""
	if a, ok := kinds.Default().ByFlag(strings.ToLower(text)); ok {
		kind = a.Name
	} else if a, ok := kinds.Get(strings.ToLower(text)); ok {
		kind = a.Name
	}
	for _, p := range panes {
		if kind != "" && p.Kind == kind {
			return p, nil
		}
	}
	return session.Pane{}, fmt.Errorf("pane '%s' not found in session '%s'", text, sessionName)
}
