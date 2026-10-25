package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/hooks"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/core/tabs"
	"github.com/arosl/nhm/internal/herdr"
)

const moveUsage = `Usage: nhm move <Name> --workspace W [--dry-run] [--socket PATH] [--json]
       nhm move <Name> --tab T [--per-tab N] [--dry-run] [--socket PATH] [--json]

Move an agent's pane to a new tab of workspace W (a workspace id or
label) with herdr's pane.move, and move its binding with it. In another
workspace herdr gives the pane a new id. The binding follows it: the same
generation, a new slot in W, Agent Mail's identity file for the new id,
the herdr name, and the tab's label. nhm maintain does the same for a pane
that was moved by hand. herdr closes the tab, and the workspace, that the
move leaves empty; the result says so.

With --tab, move the pane into tab T of its own workspace, into the
tab's next free cell of the grid, where nhm add puts a new agent. T is
a herdr tab id (w1:t2), the slot N of the agents in the tab (a number,
as nhm status shows the slots), or the label of one tab of the
workspace. --per-tab N is the grid (6: 2 rows of 3, as nhm add's). The
pane keeps its id; the binding records the new tab and keeps its slot.
A tab labelled with herdr's number or with an agent's name takes the
project's label.

It refuses, and moves nothing:
- unless nhm owns the herdr server;
- unless the binding is active and verified in its pane;
- when nhm cannot tell the agent by its process or its native session,
  because then the binding could not follow the pane (--workspace);
- when the agent is in W already, or W names no workspace or more than one;
- when T names no tab or more than one, is in another workspace, holds
  the agent already, has N panes, lies off the grid of N, or holds a
  pane that is not a bound agent of the agent's project;
- when an agent that would share tab T sets no AGENT_MAIL_AGENT: ACFS's
  mail hook would read the tab label as its name;
- when another nhm command (send, recycle, retire) acts on the agent, or
  the agent's binding changed while nhm move waited for it.
--dry-run shows the move (with --tab, the cell) and changes nothing.
`

// MoveResult is nhm move's result.
type MoveResult struct {
	MailName      string   `json:"mail_name"`
	BindingID     string   `json:"binding_id"`
	Generation    int64    `json:"generation"`
	Kind          string   `json:"kind"`
	FromPane      string   `json:"from_pane_id"`
	FromWorkspace string   `json:"from_workspace_id"`
	ToWorkspace   string   `json:"to_workspace_id"`
	ToTab         string   `json:"to_tab_id,omitempty"`
	ToPane        string   `json:"to_pane_id,omitempty"`
	Slot          string   `json:"slot,omitempty"`
	DryRun        bool     `json:"dry_run,omitempty"`
	Actions       []string `json:"actions"`
	// Cell is where --tab puts the pane.
	Cell *tabs.Cell `json:"cell,omitempty"`
}

// errMoveRefused marks the refusals that leave everything as it was.
var errMoveRefused = errors.New("refused")

// moveTargetWait bounds the wait for the binding's mutation lock, as
// recycle's wait does.
var moveTargetWait = 2 * time.Second

func runMove(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("move", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, moveUsage) }
	workspace := fs.String("workspace", "", "the workspace to move the agent to (an id or a label)")
	tab := fs.String("tab", "", "the tab of its workspace to move the agent into (a tab id, a slot N or a label)")
	perTab := fs.Int("per-tab", spawn.DefaultPerTab, "with --tab: the grid of the tab, the most panes it holds")
	dryRun := fs.Bool("dry-run", false, "show the move, change nothing")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	if len(pos) != 1 || (*workspace == "") == (*tab == "") || (*tab == "" && *perTab != spawn.DefaultPerTab) {
		fmt.Fprint(stderr, moveUsage)
		return exitRefused
	}
	env := envelope{Command: "move"}
	ctx := context.Background()
	ie, err := openInstance(*socket)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	res := MoveResult{Actions: []string{}, DryRun: *dryRun}
	var warnings []string
	lctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	err = core.WithOwnerShared(lctx, ie.inst.Dir, func(o core.Owner) error {
		if err := core.RequireOwner(o, core.OwnerNhm); err != nil {
			return fmt.Errorf("%w: %w", errMoveRefused, err)
		}
		if *tab != "" {
			return moveIntoTab(ctx, ie, pos[0], *tab, *perTab, *dryRun, &res, &warnings)
		}
		b, err := movable(ctx, ie, pos[0], *workspace, &res)
		if err == nil {
			// Before the move, as add and adopt: the workspace may hold
			// other projects' agents only (nhm-8ze).
			if others, oerr := bindings.NewStore(ie.db).OtherProjectsIn(ctx, res.ToWorkspace, b.MailProjectKey); oerr != nil {
				warnings = append(warnings, "the workspace's projects: "+oerr.Error())
			} else if others != nil {
				warnings = append(warnings, bindings.MixedWorkspaceWarning(res.ToWorkspace, b.MailProjectKey, others))
			}
		}
		if err != nil || *dryRun {
			return err
		}
		// The binding's mutation lock, which send, recycle and retire
		// take: none of them acts on the agent while its pane moves and
		// the binding follows. Under the lock, check everything again:
		// the writer that held it may have changed the binding.
		tctx, tcancel := context.WithTimeout(ctx, moveTargetWait)
		unlock, err := send.LockTarget(tctx, ie.inst.Dir, b.ID)
		tcancel()
		if errors.Is(err, send.ErrTargetBusy) {
			return fmt.Errorf("%w: %s is being sent to, recycled or retired; try again", errMoveRefused, b.MailName)
		}
		if err != nil {
			return err
		}
		defer unlock()
		cur, err := movable(ctx, ie, pos[0], *workspace, &res)
		if err != nil {
			return err
		}
		if cur.ID != b.ID || cur.Generation != b.Generation || cur.PaneID != b.PaneID {
			return fmt.Errorf("%w: %s's binding changed while nhm move waited for it (%s generation %d in %s, now %s generation %d in %s); try again",
				errMoveRefused, b.MailName, b.ID, b.Generation, b.PaneID, cur.ID, cur.Generation, cur.PaneID)
		}
		mv, err := ie.herdr.PaneMove(ctx, herdr.PaneMoveParams{PaneID: b.PaneID, Destination: herdr.NewTabIn(res.ToWorkspace)})
		if err == nil && !mv.Changed {
			err = errors.New("herdr says nothing changed")
		}
		if err != nil {
			return fmt.Errorf("herdr did not move %s: %w", b.PaneID, err)
		}
		res.ToPane = mv.Pane.PaneID
		if mv.ClosedWorkspaceID != "" {
			res.Actions = append(res.Actions, fmt.Sprintf("herdr closed workspace %s: its last pane moved", mv.ClosedWorkspaceID))
		} else if mv.ClosedTabID != "" {
			res.Actions = append(res.Actions, fmt.Sprintf("herdr closed tab %s: its last pane moved", mv.ClosedTabID))
		}
		henv := hooks.Env{DB: ie.db, Bindings: bindings.NewStore(ie.db), Herdr: ie.herdr, InstanceDir: ie.inst.Dir,
			Getenv: os.Getenv, Now: time.Now, LockWait: time.Second}
		r, err := hooks.FollowMove(ctx, henv, b, mv.Pane.PaneID)
		res.Actions = append(res.Actions, r.Actions...)
		warnings = append(warnings, r.Warnings...)
		if err != nil {
			return fmt.Errorf("herdr moved %s to %s, but the binding did not follow (%w); nhm maintain follows it on its next pass", b.PaneID, mv.Pane.PaneID, err)
		}
		if nb, err := bindings.NewStore(ie.db).Get(ctx, b.ID); err == nil {
			res.Slot, res.Generation = nb.Slot, nb.Generation
		}
		return nil
	})
	code := exitOK
	switch {
	case errors.Is(err, errMoveRefused), errors.Is(err, tabs.ErrRefused):
		code = exitRefused
	case err != nil:
		code = exitError
	}
	env.Result, env.Warnings = res, warnings
	return output(stdout, stderr, *asJSON, env, err, code, func(w io.Writer) {
		if c := res.Cell; c != nil {
			where := fmt.Sprintf("from tab %s to cell %d of %d in tab %s", c.FromTab, c.Cell, c.PerTab, c.TabID)
			if res.DryRun {
				fmt.Fprintf(w, "would move %s (%s) %s %q (split %s %s)\n", res.MailName, res.Kind, where, c.TabLabel, c.Target, c.Split)
				if c.NewLabel != "" {
					fmt.Fprintf(w, "  tab %s would take the label %s\n", c.TabID, c.NewLabel)
				}
				return
			}
			fmt.Fprintf(w, "moved %s (%s) %s, slot %s\n", res.MailName, res.Kind, where, dash(res.Slot))
			for _, a := range res.Actions {
				fmt.Fprintln(w, "  "+a)
			}
			return
		}
		if res.DryRun {
			fmt.Fprintf(w, "would move %s (%s) from %s to a new tab of workspace %s\n", res.MailName, res.Kind, res.FromPane, res.ToWorkspace)
			return
		}
		fmt.Fprintf(w, "moved %s (%s) from %s to %s, slot %s\n", res.MailName, res.Kind, res.FromPane, res.ToPane, dash(res.Slot))
		for _, a := range res.Actions {
			fmt.Fprintln(w, "  "+a)
		}
	})
}

// moveIntoTab is nhm move --tab: core's tabs.Join, which takes the
// binding's mutation lock and checks everything again under it.
func moveIntoTab(ctx context.Context, ie *instanceEnv, name, spec string, perTab int, dryRun bool, res *MoveResult, warnings *[]string) error {
	b, err := resolveMovable(ctx, ie, name, res)
	if err != nil {
		return err
	}
	env := tabs.Env{Bindings: bindings.NewStore(ie.db), Herdr: ie.herdr, InstanceDir: ie.inst.Dir, LockWait: moveTargetWait}
	tabID, err := tabs.FindTab(ctx, env, b.WorkspaceID, spec)
	if err != nil {
		return err
	}
	res.ToWorkspace, res.ToTab = b.WorkspaceID, tabID
	if dryRun {
		c, err := tabs.Check(ctx, env, b, tabID, perTab)
		if err != nil {
			return err
		}
		res.Cell = &c
		*warnings = append(*warnings, c.Warnings...)
		return nil
	}
	r, err := tabs.Join(ctx, env, b, tabID, perTab)
	res.Actions = append(res.Actions, r.Actions...)
	*warnings = append(*warnings, r.Warnings...)
	if r.Cell.TabID != "" {
		res.Cell = &r.Cell
	}
	if err != nil {
		return err
	}
	res.ToPane = r.Binding.PaneID
	res.Slot, res.Generation = r.Binding.Slot, r.Binding.Generation
	return nil
}

// resolveMovable resolves the agent, and checks that its binding is
// active and verified in its pane. A refusal wraps errMoveRefused.
func resolveMovable(ctx context.Context, ie *instanceEnv, name string, res *MoveResult) (bindings.Binding, error) {
	refuse := func(format string, args ...any) (bindings.Binding, error) {
		return bindings.Binding{}, fmt.Errorf("%w: %s", errMoveRefused, fmt.Sprintf(format, args...))
	}
	r, err := bindings.Resolve(ctx, bindings.NewStore(ie.db), ie.herdr, bindings.Query{Target: name}, os.Getenv)
	if errors.Is(err, bindings.ErrNotFound) {
		return refuse("%s: no such binding (nhm status shows the agents)", name)
	}
	if err != nil {
		return bindings.Binding{}, err
	}
	b := r.Binding
	res.MailName, res.BindingID, res.Generation, res.Kind = b.MailName, b.ID, b.Generation, b.Kind
	res.FromPane, res.FromWorkspace = b.PaneID, b.WorkspaceID
	if b.Status != bindings.Active {
		return refuse("%s's binding is %s, not active", b.MailName, b.Status)
	}
	if r.Verification.Verdict != bindings.Verified {
		return refuse("%s is %s in %s, not verified (nhm resolve %s says why)", b.MailName, r.Verification.Verdict, b.PaneID, b.MailName)
	}
	return b, nil
}

// movable resolves the agent and the workspace, and checks that the
// binding can follow the move. A refusal wraps errMoveRefused.
func movable(ctx context.Context, ie *instanceEnv, name, workspace string, res *MoveResult) (bindings.Binding, error) {
	refuse := func(format string, args ...any) (bindings.Binding, error) {
		return bindings.Binding{}, fmt.Errorf("%w: %s", errMoveRefused, fmt.Sprintf(format, args...))
	}
	b, err := resolveMovable(ctx, ie, name, res)
	if err != nil {
		return bindings.Binding{}, err
	}
	ws, err := sessionWorkspace(ctx, ie.herdr, workspace)
	if err != nil {
		return refuse("%v", err)
	}
	res.ToWorkspace = ws
	if ws == b.WorkspaceID {
		return refuse("%s is in workspace %s already", b.MailName, ws)
	}
	// FollowMove proves the agent in its new pane by its process or its
	// native session: when that proof fails here, it would fail there,
	// after the pane moved.
	a, err := ie.herdr.AgentGet(ctx, b.PaneID)
	if err != nil {
		return bindings.Binding{}, err
	}
	if a.PaneID == "" {
		a.PaneID = b.PaneID
	}
	if a.Agent != b.Kind || !bindings.SameOccupant(ctx, ie.herdr, b, a) {
		return refuse("nhm cannot tell %s by its process or its native session in %s, so the binding could not follow the pane", b.MailName, b.PaneID)
	}
	return b, nil
}
