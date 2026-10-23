// Package arrange is nhm arrange: it packs a workspace's agents into as
// few tabs as they fit, each an even grid in slot order, one project per
// tab (nhm-yr6, part of nhm-t4b). It plans from one reading of the
// workspace, and moves each pane with internal/core/tabs, the code that
// nhm move --tab runs.
package arrange

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/herdr"
)

// Agent is an active binding of the workspace, as the planner sees it.
type Agent struct {
	BindingID string
	MailName  string
	Project   string // the binding's Agent Mail project key
	PaneID    string
	Slot      string // "N.M", or "" when the binding has none
	// Problem is why the agent cannot share a tab or move, a sentence that
	// names it, such as "U1 is mismatch in w1:p3, not verified"; "" when
	// it can. Its tab is left as it is.
	Problem string
}

// Tab is one tab of the workspace, with its panes where herdr lays them
// out (pane.layout).
type Tab struct {
	TabID string
	Label string
	Panes []herdr.LayoutPane
	// LayoutErr is why the tab's layout could not be read; the tab is
	// then left as it is.
	LayoutErr string
}

// Input is one reading of a workspace.
type Input struct {
	PerTab int
	Tabs   []Tab   // the workspace's tabs
	Agents []Agent // the workspace's active bindings
}

// Plan is what nhm arrange does, in order.
type Plan struct {
	PerTab int     `json:"per_tab"`
	Groups []Group `json:"groups"`
	Moves  []Move  `json:"moves"`
	Left   []Left  `json:"left_out"`
}

// Group is the agents of one project that end in one tab, in slot order:
// the first is in the tab's first cell, and so on in reading order.
type Group struct {
	Project string   `json:"project"`
	Agents  []string `json:"agents"`
	// TabID is the tab the group ends in: a tab that exists, or, for a
	// group whose first agent opens a new tab, "" until the move made it.
	TabID string `json:"tab_id,omitempty"`
	// NewTab: the group's first agent moves to a new tab, labelled
	// NewTabLabel, and the others join it there.
	NewTab      bool   `json:"new_tab,omitempty"`
	NewTabLabel string `json:"new_tab_label,omitempty"`
	// Kept: none of the group's agents moves. They are in their tab and
	// cells already, or the group's one agent is, once the agents of
	// earlier groups have left its tab.
	Kept bool     `json:"kept,omitempty"`
	ids  []string // the agents' binding ids, as Agents
}

// Move is one pane move, or a swap of two panes of one tab.
type Move struct {
	Group     int    `json:"group"` // the index in Plan.Groups
	MailName  string `json:"mail_name"`
	BindingID string `json:"binding_id"`
	PaneID    string `json:"pane_id"`
	Slot      string `json:"slot,omitempty"`
	FromTab   string `json:"from_tab_id"`
	// ToTab is the tab the pane joins. It is "" for the group's new tab,
	// whose id is known only once the move that opens it (NewTab) is made.
	ToTab  string `json:"to_tab_id,omitempty"`
	NewTab bool   `json:"new_tab,omitempty"`
	// Swap: the pane takes the place of With's pane in its tab, and
	// With's pane takes its place (herdr's pane.swap); neither leaves the
	// tab, ToTab, which is FromTab (nhm-her).
	Swap          bool   `json:"swap,omitempty"`
	With          string `json:"with,omitempty"`
	WithBindingID string `json:"with_binding_id,omitempty"`
	WithPaneID    string `json:"with_pane_id,omitempty"`
	// Cell is the grid cell, 1 in reading order, where the pane ends.
	Cell int `json:"cell"`
}

// Left is a tab that nhm arrange leaves as it is, and why. Its agents do
// not move, and nothing moves into it.
type Left struct {
	TabID  string   `json:"tab_id"`
	Label  string   `json:"label"`
	Agents []string `json:"agents,omitempty"`
	Reason string   `json:"reason"`
}

// Make plans the arrangement of in. A tab takes part when it holds only
// active bindings of one project that can move, and its label is the
// project's tab label or, for a tab of one agent, the agent's own name:
// the tabs that spawn makes and joins. Any other tab is left as it is.
//
// The agents of the tabs that take part are grouped per project in slot
// order, PerTab to a group. A group stays in the tab of its first agent
// when that tab is its start already: on the grid of PerTab, with the
// group's first agents in its first cells in slot order, and nothing
// else, so the others join it cell by cell (spawn.GridOrder). It stays
// there too when the tab's other agents all belong to earlier groups:
// they move out first, and the first agent then fills the tab. A tab on
// the grid that holds the group's first agents and nothing else, but out
// of slot order, is put in order in place, by swaps (sortIn, nhm-her).
// Otherwise the first agent moves to a new tab of its own, and the others
// join it there. A tab that loses all its agents is closed by herdr. So
// the plan moves only what it must, and a second plan of its result
// moves nothing.
func Make(in Input) Plan {
	perTab := max(in.PerTab, 1)
	plan := Plan{PerTab: perTab, Groups: []Group{}, Moves: []Move{}, Left: []Left{}}
	byPane := map[string]*Agent{}
	for i := range in.Agents {
		byPane[in.Agents[i].PaneID] = &in.Agents[i]
	}
	tabOf := map[string]*Tab{}         // an agent's pane: its tab, when the tab takes part
	members := map[*Tab][]*Agent{}     // a tab that takes part: its agents
	byProject := map[string][]*Agent{} // the agents that take part
	for i := range in.Tabs {
		tb := &in.Tabs[i]
		agents, reason := part(tb, byPane)
		if reason != "" {
			l := Left{TabID: tb.TabID, Label: tb.Label, Reason: reason}
			for _, a := range agents {
				l.Agents = append(l.Agents, a.MailName)
			}
			plan.Left = append(plan.Left, l)
			continue
		}
		members[tb] = agents
		for _, a := range agents {
			tabOf[a.PaneID] = tb
			byProject[a.Project] = append(byProject[a.Project], a)
		}
	}
	projects := make([]string, 0, len(byProject))
	for p, agents := range byProject {
		slices.SortFunc(agents, bySlot)
		projects = append(projects, p)
	}
	// The projects in the order of their first agents.
	slices.SortFunc(projects, func(a, b string) int { return bySlot(byProject[a][0], byProject[b][0]) })

	var groups [][]*Agent
	groupOf := map[*Agent]int{}
	for _, p := range projects {
		agents := byProject[p]
		for start := 0; start < len(agents); start += perTab {
			g := agents[start:min(start+perTab, len(agents))]
			for _, a := range g {
				groupOf[a] = len(groups)
			}
			groups = append(groups, g)
		}
	}
	for k, g := range groups {
		first := g[0]
		home := tabOf[first.PaneID]
		grp := Group{Project: first.Project, Agents: names(g)}
		for _, a := range g {
			grp.ids = append(grp.ids, a.BindingID)
		}
		held := stays(home, members[home], g, groupOf, k, perTab)
		var swaps []Move
		if held == 0 {
			if sw, ok := sortIn(k, home, members[home], g, groupOf, perTab, byPane); ok {
				swaps, held = sw, len(members[home])
			}
		}
		if held > 0 {
			grp.TabID = home.TabID
			plan.Moves = append(plan.Moves, swaps...)
		} else {
			grp.NewTab, grp.NewTabLabel, held = true, first.MailName, 1
			plan.Moves = append(plan.Moves, Move{Group: k, MailName: first.MailName, BindingID: first.BindingID,
				PaneID: first.PaneID, Slot: first.Slot, FromTab: home.TabID, NewTab: true, Cell: 1})
		}
		for i, a := range g[held:] {
			plan.Moves = append(plan.Moves, Move{Group: k, MailName: a.MailName, BindingID: a.BindingID, PaneID: a.PaneID,
				Slot: a.Slot, FromTab: tabOf[a.PaneID].TabID, ToTab: grp.TabID, Cell: held + i + 1})
		}
		grp.Kept = !grp.NewTab && len(swaps) == 0 && held == len(g)
		plan.Groups = append(plan.Groups, grp)
	}
	return plan
}

// part gives the agents of tab tb when it takes part, or why it does not.
func part(tb *Tab, byPane map[string]*Agent) ([]*Agent, string) {
	var agents []*Agent
	var other []string
	for _, p := range tb.Panes {
		if a := byPane[p.PaneID]; a != nil {
			agents = append(agents, a)
		} else {
			other = append(other, p.PaneID)
		}
	}
	slices.SortFunc(agents, bySlot)
	switch {
	case tb.LayoutErr != "":
		return agents, "its layout could not be read: " + tb.LayoutErr
	case len(tb.Panes) == 0:
		return agents, "herdr shows no panes in it"
	case len(other) == 1:
		return agents, "pane " + other[0] + " holds no agent that nhm binds here (a shell, a tool, or another agent)"
	case len(other) > 1:
		return agents, "panes " + strings.Join(other, ", ") + " hold no agent that nhm binds here (a shell, a tool, or another agent)"
	}
	for _, a := range agents {
		if a.Project != agents[0].Project {
			return agents, "it holds the agents of more than one project"
		}
	}
	project := bindings.ProjectTabLabel(agents[0].Project)
	if tb.Label != project && (len(agents) != 1 || tb.Label != agents[0].MailName) {
		if len(agents) == 1 {
			return agents, fmt.Sprintf("its label %q is neither the project's (%q) nor its agent's name", tb.Label, project)
		}
		return agents, fmt.Sprintf("its label %q is not the project's (%q)", tb.Label, project)
	}
	for _, a := range agents {
		if a.Problem != "" {
			return agents, a.Problem
		}
	}
	return agents, ""
}

// stays is how many of group k's first agents stay where they are in
// home, the tab of the group's first agent, with the others joining it
// after them; 0 when the first agent must move to a new tab.
func stays(home *Tab, members, g []*Agent, groupOf map[*Agent]int, k, perTab int) int {
	var later []*Agent // the agents that are still in home when group k's turn comes
	for _, a := range members {
		if groupOf[a] >= k {
			later = append(later, a)
		}
	}
	for _, a := range later {
		if groupOf[a] != k {
			// It moves out later, after the group joins: it would leave
			// a gap among them.
			return 0
		}
	}
	if len(later) < len(members) {
		// Agents of earlier groups move out of home before this group's
		// turn. Where the others stay cannot be known now, but one pane
		// left alone fills its tab.
		if len(later) == 1 {
			return 1
		}
		return 0
	}
	order, clean := spawn.GridOrder(home.Panes, perTab)
	if !clean || len(order) > len(g) {
		return 0
	}
	for i, pane := range order {
		if g[i].PaneID != pane {
			return 0
		}
	}
	return len(order)
}

// sortIn gives the swaps that put home, the tab of group k's first agent,
// in slot order, when swaps alone can (nhm-her): home lies on the grid
// with no gap (spawn.GridOrder), and holds group k's first agents, as many
// as it has panes, and nothing else. herdr's pane.swap exchanges the
// places of two panes of a tab, and the places keep their cells, so a
// selection sort, which brings each cell's agent there in turn, takes at
// most one swap fewer than the tab has panes. ok is false when the tab
// cannot be sorted so.
func sortIn(k int, home *Tab, members, g []*Agent, groupOf map[*Agent]int, perTab int, byPane map[string]*Agent) ([]Move, bool) {
	for _, a := range members {
		if groupOf[a] != k {
			return nil, false
		}
	}
	order, clean := spawn.GridOrder(home.Panes, perTab)
	if !clean || len(order) != len(members) || len(order) > len(g) {
		return nil, false
	}
	first := map[string]bool{}
	for _, a := range g[:len(order)] {
		first[a.PaneID] = true
	}
	for _, pane := range order {
		if !first[pane] {
			return nil, false
		}
	}
	swaps := []Move{}
	cur := slices.Clone(order)
	for i, a := range g[:len(cur)] {
		if cur[i] == a.PaneID {
			continue
		}
		other := byPane[cur[i]]
		swaps = append(swaps, Move{Group: k, MailName: a.MailName, BindingID: a.BindingID, PaneID: a.PaneID, Slot: a.Slot,
			FromTab: home.TabID, ToTab: home.TabID, Swap: true, With: other.MailName, WithBindingID: other.BindingID,
			WithPaneID: other.PaneID, Cell: i + 1})
		j := slices.Index(cur, a.PaneID)
		cur[i], cur[j] = cur[j], cur[i]
	}
	return swaps, true
}

// bySlot orders agents by their slot N.M; an agent without a slot comes
// after those with one; then by name.
func bySlot(a, b *Agent) int {
	sa, ea := bindings.ParseSlot(a.Slot)
	sb, eb := bindings.ParseSlot(b.Slot)
	switch {
	case ea == nil && eb != nil:
		return -1
	case ea != nil && eb == nil:
		return 1
	case ea == nil:
		if c := cmp.Or(cmp.Compare(sa.Tab, sb.Tab), cmp.Compare(sa.Pane, sb.Pane)); c != 0 {
			return c
		}
	}
	return cmp.Or(strings.Compare(a.MailName, b.MailName), strings.Compare(a.BindingID, b.BindingID))
}

func names(agents []*Agent) []string {
	out := make([]string, len(agents))
	for i, a := range agents {
		out[i] = a.MailName
	}
	return out
}
