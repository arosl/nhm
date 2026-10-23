package tabs

import (
	"context"
	"errors"
	"fmt"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

// NewTabLocked moves b's pane out of its tab into a new tab of its own
// workspace, labelled label, for a caller that holds b's mutation lock:
// nhm arrange, when the first agent of a group is in a tab that is out
// of slot order (nhm-yr6). It reads the binding again and verifies it
// (recheck), moves the pane with herdr's pane.move to a new tab, where
// the pane keeps its id, and records the new tab in the binding
// (follow); the slot stays. It refuses a pane that is alone in its tab
// already: the move would only swap one tab for another.
func NewTabLocked(ctx context.Context, env Env, b bindings.Binding, label string) (Result, error) {
	res := Result{Actions: []string{}, Warnings: []string{}}
	cur, err := recheck(ctx, env, b)
	if err != nil {
		return res, err
	}
	snap, err := env.Herdr.Snapshot(ctx)
	if err != nil {
		return res, err
	}
	from, others := "", 0
	for _, p := range snap.Panes {
		if p.PaneID == cur.PaneID {
			from = p.TabID
		}
	}
	for _, p := range snap.Panes {
		if from != "" && p.TabID == from && p.PaneID != cur.PaneID {
			others++
		}
	}
	switch {
	case from == "":
		return res, refuse("herdr shows no pane %s", cur.PaneID)
	case others == 0:
		return res, refuse("%s is alone in tab %s already", cur.MailName, from)
	}
	mv, err := env.Herdr.PaneMove(ctx, herdr.PaneMoveParams{PaneID: cur.PaneID,
		Destination: herdr.PaneMoveDestination{Type: "new_tab", WorkspaceID: cur.WorkspaceID, Label: label}})
	if err == nil && !mv.Changed {
		err = errors.New("herdr says nothing changed")
	}
	if err != nil {
		return res, fmt.Errorf("herdr did not move %s: %w", cur.PaneID, err)
	}
	to := mv.Pane
	if to.PaneID == "" {
		to.PaneID = cur.PaneID
	}
	if to.TabID == "" && mv.CreatedTab != nil {
		to.TabID = mv.CreatedTab.TabID
	}
	if to.PaneID != cur.PaneID || to.TabID == "" || to.TabID == from || (to.WorkspaceID != "" && to.WorkspaceID != cur.WorkspaceID) {
		return res, fmt.Errorf("herdr moved %s to %s in tab %s, not to a new tab of workspace %s; nhm maintain follows it on its next pass",
			cur.PaneID, to.PaneID, to.TabID, cur.WorkspaceID)
	}
	res.Cell = Cell{TabID: to.TabID, TabLabel: label, FromTab: from, Cell: 1}
	nb, err := follow(ctx, env, cur, from, to.TabID)
	if err != nil {
		return res, err
	}
	res.Binding = nb
	res.Actions = append(res.Actions, fmt.Sprintf("%s moved from tab %s to a new tab %s (%s), slot %s", nb.MailName, from, to.TabID, label, nb.Slot))
	return res, nil
}
