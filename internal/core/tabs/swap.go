package tabs

import (
	"context"
	"fmt"
	"slices"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/herdr"
)

// SwapLocked exchanges the places of a's and b's panes in their tab with
// herdr's pane.swap, for a caller that holds both bindings' mutation
// locks: nhm arrange, to put a tab in slot order without moving its panes
// out of it (nhm-her). It reads both bindings again and verifies them
// (recheck), and refuses unless their panes are in one tab. The panes
// keep their ids and their tab, so neither binding changes. The result's
// Cell is where a's pane is after the swap, in the reading order of the
// grid of perTab (spawn.GridOrder), 0 when herdr's layout does not show
// it; its Target is b's pane.
func SwapLocked(ctx context.Context, env Env, a, b bindings.Binding, perTab int) (Result, error) {
	res := Result{Actions: []string{}, Warnings: []string{}}
	curA, err := recheck(ctx, env, a)
	if err != nil {
		return res, err
	}
	curB, err := recheck(ctx, env, b)
	if err != nil {
		return res, err
	}
	snap, err := env.Herdr.Snapshot(ctx)
	if err != nil {
		return res, err
	}
	tabOf := map[string]string{}
	for _, p := range snap.Panes {
		tabOf[p.PaneID] = p.TabID
	}
	tab := tabOf[curA.PaneID]
	switch {
	case curA.PaneID == curB.PaneID:
		return res, refuse("%s and %s share pane %s", curA.MailName, curB.MailName, curA.PaneID)
	case tab == "" || tabOf[curB.PaneID] != tab:
		return res, refuse("%s (tab %s) and %s (tab %s) are not in one tab: herdr swaps panes within a tab", curA.MailName, tab,
			curB.MailName, tabOf[curB.PaneID])
	}
	sw, err := env.Herdr.PaneSwap(ctx, herdr.PaneSwapParams{SourcePaneID: curA.PaneID, TargetPaneID: curB.PaneID})
	if err == nil && !sw.Changed {
		err = fmt.Errorf("herdr says nothing changed (%s)", sw.Reason)
	}
	if err != nil {
		return res, fmt.Errorf("herdr did not swap %s and %s: %w", curA.PaneID, curB.PaneID, err)
	}
	res.Binding = curA
	res.Cell = Cell{TabID: tab, FromTab: tab, PerTab: perTab, Target: curB.PaneID}
	order, _ := spawn.GridOrder(sw.Layout.Panes, perTab)
	if i := slices.Index(order, curA.PaneID); i >= 0 {
		res.Cell.Cell = i + 1
	}
	res.Actions = append(res.Actions, fmt.Sprintf("%s and %s swapped places in tab %s: %s is in cell %d", curA.MailName, curB.MailName, tab,
		curA.MailName, res.Cell.Cell))
	return res, nil
}
