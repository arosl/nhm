package arrange

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/tabs"
)

// Request is one nhm arrange.
type Request struct {
	WorkspaceID string
	PerTab      int
	DryRun      bool
}

// Result is nhm arrange's plan, and the moves it made.
type Result struct {
	WorkspaceID string `json:"workspace_id"`
	DryRun      bool   `json:"dry_run,omitempty"`
	Plan        Plan   `json:"plan"`
	Done        []Done `json:"done"`
	// Warnings are what nhm could not prove, such as an agent's
	// environment it could not read.
	Warnings []string `json:"-"`
}

// Done is a move, or a swap, that nhm arrange made.
type Done struct {
	MailName string `json:"mail_name"`
	Slot     string `json:"slot,omitempty"`
	FromTab  string `json:"from_tab_id"`
	TabID    string `json:"tab_id"`
	// Swap: MailName's pane swapped places with With's in TabID.
	Swap      bool     `json:"swap,omitempty"`
	With      string   `json:"with,omitempty"`
	Cell      int      `json:"cell"`
	ClosedTab string   `json:"closed_tab_id,omitempty"`
	Actions   []string `json:"actions"`
}

// Run plans the arrangement of workspace req.WorkspaceID (Make) and,
// unless req.DryRun, makes the plan's moves in order, each with
// internal/core/tabs, as nhm move --tab does. The caller holds the owner
// lock.
//
// Before the first move, Run takes the mutation lock of every agent of
// the plan: send, recycle, retire and nhm move take it too. When one is
// held, it refuses and moves nothing. Under the locks it reads the
// workspace again, and refuses when the plan is not the same. A move
// that fails stops the run. The moves made stay, since each one leaves a
// valid layout, and the error names them, a move whose pane herdr moved
// before a later step failed among them; it is a refusal
// (tabs.ErrRefused) only when nothing moved.
func Run(ctx context.Context, env tabs.Env, req Request) (Result, error) {
	res := Result{WorkspaceID: req.WorkspaceID, DryRun: req.DryRun, Done: []Done{}}
	in, _, warn, err := read(ctx, env, req.WorkspaceID, req.PerTab)
	if err != nil {
		return res, err
	}
	res.Plan = Make(in)
	res.Warnings = sharing(res.Plan, warn)
	if req.DryRun || len(res.Plan.Moves) == 0 {
		return res, nil
	}
	unlock, err := lockAll(ctx, env, res.Plan)
	if err != nil {
		return res, err
	}
	defer unlock()
	in, byID, warn, err := read(ctx, env, req.WorkspaceID, req.PerTab)
	if err != nil {
		return res, err
	}
	if again := Make(in); !reflect.DeepEqual(again.Groups, res.Plan.Groups) || !reflect.DeepEqual(again.Moves, res.Plan.Moves) {
		res.Plan = again
		return res, fmt.Errorf("%w: the workspace changed while nhm arrange waited for its agents; try again", tabs.ErrRefused)
	}
	res.Warnings = sharing(res.Plan, warn)
	for _, m := range res.Plan.Moves {
		g := &res.Plan.Groups[m.Group]
		var r tabs.Result
		switch {
		case m.Swap:
			r, err = tabs.SwapLocked(ctx, env, byID[m.BindingID], byID[m.WithBindingID], res.Plan.PerTab)
		case m.NewTab:
			r, err = tabs.NewTabLocked(ctx, env, byID[m.BindingID], g.NewTabLabel)
			if err == nil {
				g.TabID = r.Cell.TabID
			}
		default:
			to := m.ToTab
			if to == "" {
				to = g.TabID
			}
			r, err = tabs.JoinLocked(ctx, env, byID[m.BindingID], to, res.Plan.PerTab)
		}
		res.Warnings = appendNew(res.Warnings, r.Warnings...)
		switch {
		case err == nil:
			res.Done = append(res.Done, Done{MailName: m.MailName, Slot: r.Binding.Slot, FromTab: r.Cell.FromTab, TabID: r.Cell.TabID,
				Swap: m.Swap, With: m.With, Cell: r.Cell.Cell, ClosedTab: r.ClosedTabID, Actions: r.Actions})
			if r.Cell.Cell != m.Cell {
				err = fmt.Errorf("it went into cell %d of tab %s, not cell %d: the tab changed while nhm arrange ran", r.Cell.Cell, r.Cell.TabID, m.Cell)
			}
		default:
			// herdr may have moved the pane before a later step failed,
			// such as the binding's record of its new tab.
			if tab := paneTab(ctx, env, m.PaneID); tab != "" && tab != m.FromTab {
				res.Done = append(res.Done, Done{MailName: m.MailName, Slot: m.Slot, FromTab: m.FromTab, TabID: tab,
					Cell: r.Cell.Cell, ClosedTab: r.ClosedTabID, Actions: r.Actions})
			}
		}
		if err != nil {
			return res, stopped(m, err, res.Done, len(res.Plan.Moves))
		}
	}
	return res, nil
}

// stopped is the error of the move m that stopped a run after the moves
// done of all: a refusal only when nothing moved.
func stopped(m Move, err error, done []Done, all int) error {
	if len(done) == 0 {
		return fmt.Errorf("%s: %w; nothing moved", m.MailName, err)
	}
	var made []string
	for _, d := range done {
		if d.Swap {
			made = append(made, d.MailName+" swapped with "+d.With+" in "+d.TabID)
			continue
		}
		made = append(made, d.MailName+" to "+d.TabID)
	}
	return fmt.Errorf("%s: %v; %d of %d moves made (%s), and nhm arrange stopped there (each move made leaves a valid layout; "+
		"nhm arrange --dry-run shows what is left)", m.MailName, err, len(done), all, strings.Join(made, ", "))
}

// paneTab is the tab herdr shows pane in now, or "" when herdr cannot
// tell.
func paneTab(ctx context.Context, env tabs.Env, pane string) string {
	snap, err := env.Herdr.Snapshot(ctx)
	if err != nil {
		return ""
	}
	for _, p := range snap.Panes {
		if p.PaneID == pane {
			return p.TabID
		}
	}
	return ""
}

// appendNew appends to list each of items that it does not hold yet: a
// join warns about every agent of its tab, so the same warning comes
// back from each join into the tab.
func appendNew(list []string, items ...string) []string {
	for _, it := range items {
		if !slices.Contains(list, it) {
			list = append(list, it)
		}
	}
	return list
}

// sharing gives the warnings in warn, by binding id, of the agents that
// end in a tab with another: only a shared tab's label could be read as
// an agent's name.
func sharing(plan Plan, warn map[string]string) []string {
	out := []string{}
	for _, g := range plan.Groups {
		if len(g.ids) < 2 {
			continue
		}
		for _, id := range g.ids {
			if w := warn[id]; w != "" {
				out = append(out, w)
			}
		}
	}
	return out
}

// lockAll takes the mutation lock of every agent of plan, within
// env.LockWait for all, and refuses when one is held. An agent of a group
// that does not move is locked too: its tab may lose the agents of
// earlier groups, and a move of its own, such as nhm move --tab into a
// tab the plan fills, would put the plan's cells out of step. It gives
// the function that releases them.
func lockAll(ctx context.Context, env tabs.Env, plan Plan) (func(), error) {
	name := map[string]string{}
	var ids []string
	for _, g := range plan.Groups {
		for i, id := range g.ids {
			name[id] = g.Agents[i]
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	lctx, cancel := context.WithTimeout(ctx, env.LockWait)
	defer cancel()
	var unlocks []func()
	release := func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}
	for _, id := range ids {
		unlock, err := send.LockTarget(lctx, env.InstanceDir, id)
		if errors.Is(err, send.ErrTargetBusy) {
			release()
			return nil, fmt.Errorf("%w: %s is being sent to, recycled, retired or moved; try again", tabs.ErrRefused, name[id])
		}
		if err != nil {
			release()
			return nil, err
		}
		unlocks = append(unlocks, unlock)
	}
	return release, nil
}
