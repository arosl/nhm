package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/arrange"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/core/tabs"
)

const arrangeUsage = `Usage: nhm arrange [session] [--per-tab N] [--dry-run] [--socket PATH] [--json]

Pack the agents of a session (a workspace id or label; without one, the
calling pane's workspace) into as few tabs as they fit: each tab an even
grid of up to N agents (--per-tab, 6: 2 rows of 3, as nhm add's) in slot
order, one project per tab.

nhm arrange plans from one reading of the workspace, then moves the
panes one at a time, as nhm move --tab does: each into its tab's next
free cell. A group stays in the tab of its first agent when that tab
holds the group's first agents in their cells already. A tab on the grid
that holds them out of slot order, and nothing else, is put in order in
place: herdr's pane.swap exchanges two of its panes at a time. Otherwise
the first agent moves to a new tab, and the others join it there. A tab
that is full and in order is kept, and herdr closes the tabs left empty.
A moved agent keeps its pane, its slot and its generation; its binding
records the new tab. A second nhm arrange moves nothing.

Only the tabs that nhm add makes and joins take part: those that hold
only bound, verified agents of one project, whose processes set
AGENT_MAIL_AGENT, and that carry the project's tab label or, with one
agent, its name. Any other tab, such as one with another label
("MistyDesert (pusher)"), a shell or tool pane, or an unbound agent, is
left as it is, and the result says why.

It refuses, and moves nothing:
- unless nhm owns the herdr server;
- when another nhm command (send, recycle, retire, move) acts on one of
  the agents it arranges, or the workspace changed while nhm arrange
  waited for them.
A move that fails stops the run. The moves made stay, since each one
leaves a valid layout, and the error names them.
--dry-run shows the plan and changes nothing.
`

func runArrange(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("arrange", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, arrangeUsage) }
	perTab := fs.Int("per-tab", spawn.DefaultPerTab, "the most agents in a tab: its grid")
	dryRun := fs.Bool("dry-run", false, "show the plan, change nothing")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	if len(pos) > 1 || *perTab < 1 {
		fmt.Fprint(stderr, arrangeUsage)
		return exitRefused
	}
	session := ""
	if len(pos) == 1 {
		session = pos[0]
	}
	env := envelope{Command: "arrange"}
	ctx := context.Background()
	ie, err := openInstance(*socket)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	var res arrange.Result
	lctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	err = core.WithOwnerShared(lctx, ie.inst.Dir, func(o core.Owner) error {
		if err := core.RequireOwner(o, core.OwnerNhm); err != nil {
			return fmt.Errorf("%w: %w", tabs.ErrRefused, err)
		}
		ws, err := sessionWorkspace(ctx, ie.herdr, session)
		if err != nil {
			return fmt.Errorf("%w: %w", tabs.ErrRefused, err)
		}
		tenv := tabs.Env{Bindings: bindings.NewStore(ie.db), Herdr: ie.herdr, InstanceDir: ie.inst.Dir, LockWait: moveTargetWait}
		res, err = arrange.Run(ctx, tenv, arrange.Request{WorkspaceID: ws, PerTab: *perTab, DryRun: *dryRun})
		return err
	})
	code := exitOK
	switch {
	case errors.Is(err, tabs.ErrRefused):
		code = exitRefused
	case err != nil:
		code = exitError
	}
	env.Result, env.Warnings = res, res.Warnings
	return output(stdout, stderr, *asJSON, env, err, code, func(w io.Writer) { arrangeText(w, res) })
}

// arrangeText is nhm arrange's result as text: the tabs the groups end
// in, the moves, and the tabs left as they are.
func arrangeText(w io.Writer, res arrange.Result) {
	p := res.Plan
	agents := 0
	for _, g := range p.Groups {
		agents += len(g.Agents)
	}
	switch {
	case len(p.Moves) == 0:
		fmt.Fprintf(w, "nothing to arrange in workspace %s: %s in %s of up to %d, each in slot order\n",
			res.WorkspaceID, many(agents, "agent"), many(len(p.Groups), "tab"), p.PerTab)
	case res.DryRun:
		fmt.Fprintf(w, "would arrange %s of workspace %s in %s of up to %d:\n", many(agents, "agent"), res.WorkspaceID,
			many(len(p.Groups), "tab"), p.PerTab)
	default:
		fmt.Fprintf(w, "arranged %s of workspace %s in %s of up to %d:\n", many(agents, "agent"), res.WorkspaceID,
			many(len(p.Groups), "tab"), p.PerTab)
	}
	for _, g := range p.Groups {
		where := "tab " + g.TabID
		if g.NewTab && g.TabID == "" {
			where = "a new tab"
		} else if g.NewTab {
			where = "new tab " + g.TabID
		}
		kept := ""
		if g.Kept {
			kept = " (stays)"
		}
		fmt.Fprintf(w, "  %s: %s%s\n", where, strings.Join(g.Agents, " "), kept)
	}
	if res.DryRun {
		for _, m := range p.Moves {
			if m.Swap {
				fmt.Fprintf(w, "  would swap %s (slot %s) with %s in tab %s, so %s is in cell %d\n", m.MailName, dash(m.Slot), m.With,
					m.ToTab, m.MailName, m.Cell)
				continue
			}
			to := "tab " + m.ToTab
			switch {
			case m.NewTab:
				to = fmt.Sprintf("a new tab %q", p.Groups[m.Group].NewTabLabel)
			case m.ToTab == "":
				to = "the new tab"
			}
			fmt.Fprintf(w, "  would move %s (slot %s) from tab %s to %s, cell %d\n", m.MailName, dash(m.Slot), m.FromTab, to, m.Cell)
		}
	}
	for _, d := range res.Done {
		for _, a := range d.Actions {
			fmt.Fprintln(w, "  "+a)
		}
	}
	if len(p.Left) > 0 {
		fmt.Fprintln(w, "left as they are:")
		for _, l := range p.Left {
			fmt.Fprintf(w, "  tab %s %q: %s\n", l.TabID, l.Label, l.Reason)
		}
	}
}

// many is "1 tab" or "n tabs".
func many(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
