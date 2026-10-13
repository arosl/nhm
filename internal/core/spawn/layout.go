package spawn

import (
	"context"
	"encoding/json"
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
// marked spawn (paneEnvKey), it has fewer than PerTab of them, and its
// label is the project's or its one agent's name. A request with --label
// or --group, or one agent per tab, joins nothing.
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
		if ok {
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

// splitTarget picks the pane to split for a new pane in anchor's tab, and
// the direction: the biggest pane, cut in half along its longer side. A
// cell counts as about twice as tall as it is wide, so a pane is wider
// than it is tall when its width is more than twice its height; a square
// pane is cut down. When the layout cannot be read, anchor is cut to the
// right, as spawn did before nhm-4c4.
func splitTarget(ctx context.Context, h Herdr, anchor string) (string, string) {
	l, err := h.PaneLayout(ctx, anchor)
	if err != nil || len(l.Panes) == 0 {
		return anchor, "right"
	}
	best := l.Panes[0]
	for _, p := range l.Panes[1:] {
		if area(p.Rect) > area(best.Rect) {
			best = p
		}
	}
	if int(best.Rect.Width) > 2*int(best.Rect.Height) {
		return best.PaneID, "right"
	}
	return best.PaneID, "down"
}

func area(r herdr.LayoutRect) int {
	return int(r.Width) * int(r.Height)
}
