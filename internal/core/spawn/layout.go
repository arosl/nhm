package spawn

import (
	"context"
	"encoding/json"
	"math"
	"sort"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

// DefaultPerTab is the most agents of one project that share a tab, one
// pane each, when the request names no number (the operator, nhm-4c4).
const DefaultPerTab = 6

// paneEnvKey marks a spawn whose panes name their agent in
// AGENT_MAIL_AGENT. It is in the operation's stored request, so a later
// spawn can tell such a tab from one made before nhm-4c4: the panes of an
// older tab lack the variable, and ACFS's Agent Mail hook then reads the
// tab label as the agent's name. Only a tab of marked agents is joined.
const paneEnvKey = "agent_mail_agent"

// tabRoom is a tab that the plan puts targets into: one that exists in
// the workspace with room for the project's agents, or one this spawn
// opens.
type tabRoom struct {
	n       int    // the slot N of the tab
	tabID   string // herdr's id of a tab that exists; "" for a new one
	anchor  string // a pane of a tab that exists, to read its layout
	first   int    // the target that opens a new tab
	agents  int    // the agents in the tab
	maxPane int    // the highest slot M given in the tab
}

// joinable lists the tabs of the workspace that a new agent of the
// request's project may join, lowest slot N first. A tab is joinable
// when every agent in it is an active binding of the project, made by a
// marked spawn (paneEnvKey), it has fewer than PerTab of them, its
// label is the project's or its one agent's name, and its panes lie on
// the grid of PerTab (onGrid). A request with --label or --group, or one
// agent per tab, joins nothing.
func joinable(ctx context.Context, env Env, workspaceID string, req Request, tabN map[string]int,
	maxPane map[int]int) ([]*tabRoom, error) {
	if workspaceID == "" || req.PerTab <= 1 || req.Label != "" || req.Group != "" {
		return nil, nil
	}
	snap, err := env.Herdr.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	labels := map[string]string{}
	for _, tb := range snap.Tabs {
		if tb.WorkspaceID == workspaceID {
			labels[tb.TabID] = tb.Label
		}
	}
	paneTab, agentPanes := map[string]string{}, map[string]map[string]bool{}
	for _, p := range snap.Panes {
		if p.WorkspaceID != workspaceID {
			continue
		}
		paneTab[p.PaneID] = p.TabID
		if p.Agent != "" {
			if agentPanes[p.TabID] == nil {
				agentPanes[p.TabID] = map[string]bool{}
			}
			agentPanes[p.TabID][p.PaneID] = true
		}
	}
	list, err := env.Bindings.List(ctx, bindings.Filter{WorkspaceID: workspaceID, Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return nil, err
	}
	type group struct {
		binds []bindings.Binding
		other bool
	}
	groups := map[string]*group{}
	for _, b := range list {
		tab, live := paneTab[b.PaneID]
		if !live {
			continue
		}
		g := groups[tab]
		if g == nil {
			g = &group{}
			groups[tab] = g
		}
		g.binds = append(g.binds, b)
		if b.MailProjectKey != req.ProjectRoot {
			g.other = true
		}
	}
	marked := map[string]bool{}
	isMarked := func(opID string) bool {
		v, ok := marked[opID]
		if !ok {
			v = markedSpawn(ctx, env, opID)
			marked[opID] = v
		}
		return v
	}
	var rooms []*tabRoom
	for tab, g := range groups {
		n, slotted := tabN[tab]
		if g.other || !slotted || len(g.binds) >= req.PerTab {
			continue
		}
		label := labels[tab]
		if label != bindings.ProjectTabLabel(req.ProjectRoot) && (len(g.binds) != 1 || label != g.binds[0].MailName) {
			continue
		}
		// Every agent herdr sees in the tab must be one of these bindings:
		// an unbound agent may be another project's.
		bound := map[string]bool{}
		ok := true
		for _, b := range g.binds {
			bound[b.PaneID] = true
			if !isMarked(b.CreatedByOp) {
				ok = false
			}
		}
		for pane := range agentPanes[tab] {
			if !bound[pane] {
				ok = false
			}
		}
		if ok && onGrid(ctx, env.Herdr, g.binds[0].PaneID, req.PerTab) {
			rooms = append(rooms, &tabRoom{n: n, tabID: tab, anchor: g.binds[0].PaneID, first: -1,
				agents: len(g.binds), maxPane: maxPane[n]})
		}
	}
	sort.Slice(rooms, func(i, j int) bool { return rooms[i].n < rooms[j].n })
	return rooms, nil
}

// markedSpawn reports whether opID is a spawn whose stored request has
// paneEnvKey. An unknown, unreadable or other operation is not marked:
// its tab is not joined, and the spawn opens a tab instead.
func markedSpawn(ctx context.Context, env Env, opID string) bool {
	if opID == "" {
		return false
	}
	op, err := env.DB.Operation(ctx, opID)
	if err != nil || op.Verb != Verb {
		return false
	}
	var r map[string]any
	if json.Unmarshal([]byte(op.RequestJSON), &r) != nil {
		return false
	}
	v, _ := r[paneEnvKey].(bool)
	return v
}

// grid is the shape of a tab of perTab panes: rows of cols cells. A
// terminal is wider than it is tall, so a tab has at least as many
// columns as rows: rows is the integer square root of perTab, and cols
// is perTab/rows rounded up. 6 is 2 rows of 3 (the operator, nhm-cxh); 2
// and 3 are one row; 4 is 2x2; 5 is 2 rows of 3, the last cell empty; 8
// is 2 rows of 4; 9 is 3x3.
func grid(perTab int) (cols, rows int) {
	rows = 1
	for (rows+1)*(rows+1) <= perTab {
		rows++
	}
	return max(1, (perTab+rows-1)/rows), rows
}

// splitTarget picks how a new pane goes into anchor's tab: the pane to
// split, the direction and the ratio, which is the share the split pane
// keeps (herdr's pane.split). The tab is a grid of perTab cells (grid),
// filled in reading order (nhm-cxh): see gridSplit. When every cell has
// a pane of its own, the biggest pane is cut in half along its longer
// side. A cell counts as about twice as tall as it is wide, so a pane is
// wider than it is tall when its width is more than twice its height; a
// square pane is cut down. When the layout cannot be read, anchor is cut
// in half to the right, as spawn did before nhm-4c4.
func splitTarget(ctx context.Context, h Herdr, anchor string, perTab int) (string, string, float64) {
	l, err := h.PaneLayout(ctx, anchor)
	if err != nil || len(l.Panes) == 0 {
		return anchor, "right", 0.5
	}
	if pane, dir, ratio, ok := gridSplit(l.Panes, perTab); ok {
		return pane, dir, ratio
	}
	best := l.Panes[0]
	for _, p := range l.Panes[1:] {
		if area(p.Rect) > area(best.Rect) {
			best = p
		}
	}
	if int(best.Rect.Width) > 2*int(best.Rect.Height) {
		return best.PaneID, "right", 0.5
	}
	return best.PaneID, "down", 0.5
}

// gridSplit places a new pane in the first free cell of the tab's grid,
// in reading order. A pane holds the cells whose centres it covers; the
// first in reading order is its own, and every later one is free. The
// pane that covers the first free cell lies to the cell's left in the
// same row, and is split right at the cell's left edge, or above it in
// the same column, and is split down at the cell's top edge. So the
// first row fills from left to right, its last pane holding the room of
// the cells still empty, and each later row splits the panes above it: 6
// panes are 2 rows of 3, with the slots in reading order. Grown this way,
// no pane of a partial tab is narrower than a column: 2 panes of 6 are
// one third and two thirds of the width, and 4 are the first column cut
// in two beside two full-height columns. joinable joins only a tab on
// the grid (onGrid); should a tab leave it during a spawn, the new pane
// still fills the first free cell.
//
// herdr's panes tile the tab, so their bounds are the tab. pane.layout
// gives the rects of the split tree in a zoomed tab too, and a closed
// pane's room goes to its neighbour, so a tab with a closed pane gets
// its gap filled first. ok is false when no cell is free.
func gridSplit(panes []herdr.LayoutPane, perTab int) (pane, dir string, ratio float64, ok bool) {
	x0, y0, cw, ch, cols, rows := cells(panes, perTab)
	first := map[string]int{} // a pane's own cell: its row
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			left, top := x0+float64(c)*cw, y0+float64(r)*ch
			p, found := paneAt(panes, left+cw/2, top+ch/2)
			if !found {
				continue
			}
			row, held := first[p.PaneID]
			switch {
			case !held:
				first[p.PaneID] = r
			case row == r:
				return p.PaneID, "right", (left - float64(p.Rect.X)) / float64(p.Rect.Width), true
			default:
				return p.PaneID, "down", (top - float64(p.Rect.Y)) / float64(p.Rect.Height), true
			}
		}
	}
	return "", "", 0, false
}

// gridSlack is how far, in terminal cells, a pane's edge may lie from its
// grid line. gridSplit cuts within half a cell of the line, but herdr
// lays a tab out again from the split ratios when it is resized, and the
// rounding of nested splits adds up: about a cell more in a grid of 4
// columns (ScarletOrchid's review, nhm-cxh).
const gridSlack = 2

// cells is the grid of perTab over the panes' tab: its origin, the size
// of a cell, and the columns and rows.
func cells(panes []herdr.LayoutPane, perTab int) (x0, y0, cw, ch float64, cols, rows int) {
	left, top, right, bottom := math.MaxInt, math.MaxInt, 0, 0
	for _, p := range panes {
		left, top = min(left, int(p.Rect.X)), min(top, int(p.Rect.Y))
		right, bottom = max(right, int(p.Rect.X)+int(p.Rect.Width)), max(bottom, int(p.Rect.Y)+int(p.Rect.Height))
	}
	cols, rows = grid(perTab)
	return float64(left), float64(top), float64(right-left) / float64(cols), float64(bottom-top) / float64(rows), cols, rows
}

// onGrid reports whether the panes of anchor's tab lie on the grid of
// perTab: each edge within gridSlack terminal cells of a grid line, and
// each pane at least a grid cell wide and tall. A tab off the grid, laid
// out by nhm 0.1.8 to 0.1.10, by hand, for another --per-tab, or resized
// by hand, is not joined: a split there could leave a pane narrower than
// a column (nhm-cxh). Nor is a tab whose layout cannot be read. A tab of
// one row for --per-tab 3 lies on the lines of 6, and grows into it.
func onGrid(ctx context.Context, h Herdr, anchor string, perTab int) bool {
	l, err := h.PaneLayout(ctx, anchor)
	if err != nil || len(l.Panes) == 0 {
		return false
	}
	x0, y0, cw, ch, _, _ := cells(l.Panes, perTab)
	// line is the grid line nearest to v, and whether v is near it.
	line := func(v int, origin, size float64) (float64, bool) {
		k := math.Round((float64(v) - origin) / size)
		return k, math.Abs(float64(v)-origin-k*size) <= gridSlack
	}
	for _, p := range l.Panes {
		r := p.Rect
		left, ok1 := line(int(r.X), x0, cw)
		right, ok2 := line(int(r.X)+int(r.Width), x0, cw)
		top, ok3 := line(int(r.Y), y0, ch)
		bottom, ok4 := line(int(r.Y)+int(r.Height), y0, ch)
		if !ok1 || !ok2 || !ok3 || !ok4 || right <= left || bottom <= top {
			return false
		}
	}
	return true
}

// paneAt is the pane whose rect holds the point (x, y).
func paneAt(panes []herdr.LayoutPane, x, y float64) (herdr.LayoutPane, bool) {
	for _, p := range panes {
		r := p.Rect
		if x >= float64(r.X) && x < float64(r.X)+float64(r.Width) && y >= float64(r.Y) && y < float64(r.Y)+float64(r.Height) {
			return p, true
		}
	}
	return herdr.LayoutPane{}, false
}

func area(r herdr.LayoutRect) int {
	return int(r.Width) * int(r.Height)
}
