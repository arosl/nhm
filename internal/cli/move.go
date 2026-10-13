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
	"github.com/arosl/nhm/internal/herdr"
)

const moveUsage = `Usage: nhm move <Name> --workspace W [--dry-run] [--socket PATH] [--json]

Move an agent's pane to a new tab of workspace W (a workspace id or
label) with herdr's pane.move, and move its binding with it. In another
workspace herdr gives the pane a new id. The binding follows it: the same
generation, a new slot in W, Agent Mail's identity file for the new id,
the herdr name, and the tab's label. nhm maintain does the same for a pane
that was moved by hand. herdr closes the tab, and the workspace, that the
move leaves empty; the result says so.

It refuses, and moves nothing:
- unless nhm owns the herdr server;
- unless the binding is active and verified in its pane;
- when nhm cannot tell the agent by its process or its native session,
  because then the binding could not follow the pane;
- when the agent is in W already, or W names no workspace or more than one;
- when another nhm command (send, recycle, retire) acts on the agent, or
  the agent's binding changed while nhm move waited for it.
--dry-run shows the move and changes nothing.
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
	ToPane        string   `json:"to_pane_id,omitempty"`
	Slot          string   `json:"slot,omitempty"`
	DryRun        bool     `json:"dry_run,omitempty"`
	Actions       []string `json:"actions"`
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
	dryRun := fs.Bool("dry-run", false, "show the move, change nothing")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	if len(pos) != 1 || *workspace == "" {
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
	case errors.Is(err, errMoveRefused):
		code = exitRefused
	case err != nil:
		code = exitError
	}
	env.Result, env.Warnings = res, warnings
	return output(stdout, stderr, *asJSON, env, err, code, func(w io.Writer) {
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

// movable resolves the agent and the workspace, and checks that the
// binding can follow the move. A refusal wraps errMoveRefused.
func movable(ctx context.Context, ie *instanceEnv, name, workspace string, res *MoveResult) (bindings.Binding, error) {
	refuse := func(format string, args ...any) (bindings.Binding, error) {
		return bindings.Binding{}, fmt.Errorf("%w: %s", errMoveRefused, fmt.Sprintf(format, args...))
	}
	r, err := bindings.Resolve(ctx, bindings.NewStore(ie.db), ie.herdr, bindings.Query{Target: name}, os.Getenv)
	if errors.Is(err, bindings.ErrNotFound) {
		return refuse("%s: no such binding (nhm list shows the agents)", name)
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
