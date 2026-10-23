package arrange

import (
	"fmt"
	"math"
	"math/rand"
	"slices"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/herdr"
)

const proj = "/work/proj" // its tab label is "proj"

func agent(name, slot string) Agent {
	return Agent{BindingID: "b-" + name, MailName: name, Project: proj, PaneID: "p-" + name, Slot: slot}
}

// tab is a tab whose panes GridSplit placed one by one, in the order
// given, on the grid of perTab.
func tab(id, label string, perTab int, agents ...string) Tab {
	tb := Tab{TabID: id, Label: label}
	for _, a := range agents {
		tb.Panes = join(tb.Panes, "p-"+a, perTab)
	}
	return tb
}

// join adds pane to a tab as tabs.Join does: into the next free cell of
// the grid, or as the whole of a new tab.
func join(panes []herdr.LayoutPane, pane string, perTab int) []herdr.LayoutPane {
	if len(panes) == 0 {
		return []herdr.LayoutPane{{PaneID: pane, Rect: herdr.LayoutRect{Width: 240, Height: 60}}}
	}
	_, target, dir, ratio, ok := spawn.GridSplit(panes, perTab)
	if !ok {
		panic(fmt.Sprintf("no free cell for %s", pane))
	}
	out := slices.Clone(panes)
	for i, p := range out {
		if p.PaneID != target {
			continue
		}
		r := p.Rect
		n := herdr.LayoutPane{PaneID: pane, Rect: r}
		if dir == "right" {
			keep := uint16(math.Round(float64(r.Width) * ratio))
			out[i].Rect.Width, n.Rect.X, n.Rect.Width = keep, r.X+keep, r.Width-keep
		} else {
			keep := uint16(math.Round(float64(r.Height) * ratio))
			out[i].Rect.Height, n.Rect.Y, n.Rect.Height = keep, r.Y+keep, r.Height-keep
		}
		out = append(out, n)
	}
	return out
}

// apply makes plan's moves on in as herdr and tabs.Join would, and
// checks that each pane lands in the cell the plan names. A tab that
// loses a pane keeps the others' places, so a cell may be left with no
// pane (herdr would give the room to a neighbour); a tab left with one
// pane gets all of it, and an empty tab is closed.
func apply(t *testing.T, in Input, plan Plan) Input {
	t.Helper()
	tabs := slices.Clone(in.Tabs)
	newTab := map[int]string{}
	for _, m := range plan.Moves {
		if m.Swap {
			swap(t, tabs, m, plan.PerTab)
			continue
		}
		for i := range tabs {
			tabs[i].Panes = slices.DeleteFunc(slices.Clone(tabs[i].Panes), func(p herdr.LayoutPane) bool { return p.PaneID == m.PaneID })
			if len(tabs[i].Panes) == 1 {
				tabs[i].Panes[0].Rect = herdr.LayoutRect{Width: 240, Height: 60}
			}
		}
		tabs = slices.DeleteFunc(tabs, func(tb Tab) bool { return len(tb.Panes) == 0 })
		if m.NewTab {
			id := fmt.Sprintf("new%d", m.Group)
			newTab[m.Group] = id
			tabs = append(tabs, Tab{TabID: id, Label: plan.Groups[m.Group].NewTabLabel, Panes: join(nil, m.PaneID, plan.PerTab)})
			continue
		}
		to := m.ToTab
		if to == "" {
			to = newTab[m.Group]
		}
		i := slices.IndexFunc(tabs, func(tb Tab) bool { return tb.TabID == to })
		if i < 0 {
			t.Fatalf("move %+v: no tab %q", m, to)
		}
		if cell, _, _, _, _ := spawn.GridSplit(tabs[i].Panes, plan.PerTab); cell != m.Cell {
			t.Fatalf("move %+v: the pane goes into cell %d of %s, the plan says %d", m, cell, to, m.Cell)
		}
		tabs[i].Panes = join(tabs[i].Panes, m.PaneID, plan.PerTab)
		if len(tabs[i].Panes) == 2 {
			tabs[i].Label = "proj" // as tabs.Join relabels a tab that becomes shared
		}
	}
	return Input{PerTab: in.PerTab, Tabs: tabs, Agents: in.Agents}
}

// swap makes the swap m on tabs as herdr's pane.swap does: the two panes
// of tab m.ToTab exchange their rects. It checks that m's pane ends in
// the cell the plan names.
func swap(t *testing.T, tabs []Tab, m Move, perTab int) {
	t.Helper()
	i := slices.IndexFunc(tabs, func(tb Tab) bool { return tb.TabID == m.ToTab })
	if i < 0 {
		t.Fatalf("swap %+v: no tab %q", m, m.ToTab)
	}
	tabs[i].Panes = slices.Clone(tabs[i].Panes)
	a := slices.IndexFunc(tabs[i].Panes, func(p herdr.LayoutPane) bool { return p.PaneID == m.PaneID })
	b := slices.IndexFunc(tabs[i].Panes, func(p herdr.LayoutPane) bool { return p.PaneID == m.WithPaneID })
	if a < 0 || b < 0 || a == b {
		t.Fatalf("swap %+v: the panes are not two panes of %s", m, m.ToTab)
	}
	tabs[i].Panes[a].Rect, tabs[i].Panes[b].Rect = tabs[i].Panes[b].Rect, tabs[i].Panes[a].Rect
	if order, _ := spawn.GridOrder(tabs[i].Panes, perTab); slices.Index(order, m.PaneID)+1 != m.Cell {
		t.Fatalf("swap %+v: the pane is in cell %d of %v, the plan says %d", m, slices.Index(order, m.PaneID)+1, order, m.Cell)
	}
}

// moves is plan's moves as "<name>→<tab>:<cell>", "new" for a new tab,
// and a swap as "<name>⇄<with>:<cell>".
func moves(plan Plan) string {
	var out []string
	for _, m := range plan.Moves {
		if m.Swap {
			out = append(out, fmt.Sprintf("%s⇄%s:%d", m.MailName, m.With, m.Cell))
			continue
		}
		to := m.ToTab
		if m.NewTab {
			to = "new"
		} else if to == "" {
			to = "new" + fmt.Sprint(m.Group)
		}
		out = append(out, fmt.Sprintf("%s→%s:%d", m.MailName, to, m.Cell))
	}
	return strings.Join(out, " ")
}

// groups is plan's groups as "<tab>[a b c]", with "=" for a kept group.
func groups(plan Plan) string {
	var out []string
	for _, g := range plan.Groups {
		tb := g.TabID
		if g.NewTab {
			tb = "new"
		}
		if g.Kept {
			tb += "="
		}
		out = append(out, tb+"["+strings.Join(g.Agents, " ")+"]")
	}
	return strings.Join(out, " ")
}

// The bead's case: 7 agents in 4 tabs of 1, 1, 2 and 3 become a full tab
// of 6, in slot order, and the seventh in a tab of its own. Its tab loses
// its two others to the full tab, so it stays where it is.
func TestPlanSevenAgentsInFourTabs(t *testing.T) {
	in := Input{PerTab: 6, Tabs: []Tab{
		tab("tA", "A1", 6, "A1"), tab("tB", "A2", 6, "A2"), tab("tC", "proj", 6, "A3", "A4"), tab("tD", "proj", 6, "A5", "A6", "A7"),
	}, Agents: []Agent{agent("A1", "1.1"), agent("A2", "2.1"), agent("A3", "3.1"), agent("A4", "3.2"), agent("A5", "4.1"),
		agent("A6", "4.2"), agent("A7", "4.3")}}
	plan := Make(in)
	if got, want := groups(plan), "tA[A1 A2 A3 A4 A5 A6] tD=[A7]"; got != want {
		t.Errorf("groups %s, want %s", got, want)
	}
	if got, want := moves(plan), "A2→tA:2 A3→tA:3 A4→tA:4 A5→tA:5 A6→tA:6"; got != want {
		t.Errorf("moves %s, want %s", got, want)
	}
	if len(plan.Left) != 0 {
		t.Errorf("left out: %+v", plan.Left)
	}
	again := Make(apply(t, in, plan))
	if len(again.Moves) != 0 || groups(again) != "tA=[A1 A2 A3 A4 A5 A6] tD=[A7]" {
		t.Errorf("a second plan: groups %s, moves %s; want both kept and no move", groups(again), moves(again))
	}
}

// A tab that holds its agents in slot order on the grid is kept, and a
// plan for it moves nothing.
func TestPlanKeepsAnArrangedTab(t *testing.T) {
	in := Input{PerTab: 6, Tabs: []Tab{tab("tA", "proj", 6, "A1", "A2", "A3", "A4", "A5", "A6"), tab("tB", "A7", 6, "A7")},
		Agents: []Agent{agent("A1", "1.1"), agent("A2", "1.2"), agent("A3", "1.3"), agent("A4", "1.4"), agent("A5", "1.5"),
			agent("A6", "1.6"), agent("A7", "2.1")}}
	plan := Make(in)
	if len(plan.Moves) != 0 || groups(plan) != "tA=[A1 A2 A3 A4 A5 A6] tB=[A7]" {
		t.Errorf("groups %s, moves %s; want both kept and no move", groups(plan), moves(plan))
	}
}

// A tab that holds part of a group in slot order takes the rest after
// them; a tab out of slot order, or with a gap, cannot: its first agent
// opens a new tab, and the others join it there.
func TestPlanTheFirstAgentsTab(t *testing.T) {
	agents := []Agent{agent("A1", "1.1"), agent("A2", "1.2"), agent("A3", "1.3"), agent("A4", "2.1")}
	// A tab of 4 whose second pane, X, closed: herdr gave its room to its
	// sibling in the split tree, A2. A3's cell, below A1, now comes after
	// a free one.
	gap := tab("tA", "proj", 6, "A1", "X", "A2", "A3")
	for i := range gap.Panes {
		if gap.Panes[i].PaneID == "p-A2" {
			gap.Panes[i].Rect = herdr.LayoutRect{X: 80, Y: 0, Width: 160, Height: 60}
		}
	}
	gap.Panes = slices.DeleteFunc(gap.Panes, func(p herdr.LayoutPane) bool { return p.PaneID == "p-X" })
	for _, c := range []struct {
		name          string
		perTab        int
		tabs          []Tab
		groups, moves string
	}{
		{"in order", 6, []Tab{tab("tA", "proj", 6, "A1", "A2"), tab("tB", "proj", 6, "A3", "A4")},
			"tA[A1 A2 A3 A4]", "A3→tA:3 A4→tA:4"},
		// On the grid with no gap, it holds A1 to A3: sorted in place.
		{"out of order", 6, []Tab{tab("tA", "proj", 6, "A2", "A1", "A3"), tab("tB", "A4", 6, "A4")},
			"tA[A1 A2 A3 A4]", "A1⇄A2:1 A4→tA:4"},
		// It holds A1 and A3, not A1 and A2: no swap puts A2 in cell 2.
		{"out of order, not the first agents", 6, []Tab{tab("tA", "proj", 6, "A3", "A1"), tab("tB", "proj", 6, "A2", "A4")},
			"new[A1 A2 A3 A4]", "A1→new:1 A2→new0:2 A3→new0:3 A4→new0:4"},
		{"a gap", 6, []Tab{gap, tab("tB", "A4", 6, "A4")},
			"new[A1 A2 A3 A4]", "A1→new:1 A2→new0:2 A3→new0:3 A4→new0:4"},
		// Laid out for another --per-tab: one row of 3 is off the grid of 4.
		{"off the grid", 4, []Tab{tab("tA", "proj", 3, "A1", "A2", "A3"), tab("tB", "A4", 6, "A4")},
			"new[A1 A2 A3 A4]", "A1→new:1 A2→new0:2 A3→new0:3 A4→new0:4"},
	} {
		in := Input{PerTab: c.perTab, Tabs: c.tabs, Agents: agents}
		plan := Make(in)
		if groups(plan) != c.groups || moves(plan) != c.moves {
			t.Errorf("%s: groups %s, moves %s; want %s, %s", c.name, groups(plan), moves(plan), c.groups, c.moves)
			continue
		}
		if again := Make(apply(t, in, plan)); len(again.Moves) != 0 {
			t.Errorf("%s: a second plan moves %s", c.name, moves(again))
		}
	}
}

// A tab on the grid that holds a group's first agents out of slot order
// is sorted in place with herdr's pane.swap, at most one swap fewer than
// it has panes, and the rest of the group joins it after them (nhm-her).
// The live case: 5 agents placed by hand, the sixth cell free, and a
// sixth agent in a tab of its own.
func TestPlanSortsATabInPlace(t *testing.T) {
	six := []Agent{agent("A1", "1.1"), agent("A2", "1.2"), agent("A3", "1.3"), agent("A4", "1.4"), agent("A5", "1.5"), agent("A6", "2.1")}
	for _, c := range []struct {
		name          string
		tabs          []Tab
		groups, moves string
	}{
		{"reversed", []Tab{tab("tA", "proj", 6, "A6", "A5", "A4", "A3", "A2", "A1")},
			"tA[A1 A2 A3 A4 A5 A6]", "A1⇄A6:1 A2⇄A5:2 A3⇄A4:3"},
		{"by hand, the sixth apart", []Tab{tab("tA", "proj", 6, "A3", "A4", "A5", "A1", "A2"), tab("tB", "A6", 6, "A6")},
			"tA[A1 A2 A3 A4 A5 A6]", "A1⇄A3:1 A2⇄A4:2 A3⇄A5:3 A4⇄A5:4 A6→tA:6"},
		{"in order", []Tab{tab("tA", "proj", 6, "A1", "A2", "A3", "A4", "A5"), tab("tB", "A6", 6, "A6")},
			"tA[A1 A2 A3 A4 A5 A6]", "A6→tA:6"},
	} {
		in := Input{PerTab: 6, Tabs: c.tabs, Agents: six}
		plan := Make(in)
		if groups(plan) != c.groups || moves(plan) != c.moves {
			t.Errorf("%s: groups %s, moves %s; want %s, %s", c.name, groups(plan), moves(plan), c.groups, c.moves)
			continue
		}
		if again := Make(apply(t, in, plan)); len(again.Moves) != 0 {
			t.Errorf("%s: a second plan moves %s", c.name, moves(again))
		}
	}
}

// --per-tab splits a tab that holds more than one group: an agent of a
// later group in the first agent's tab would leave a gap among the
// group, so the group opens a new tab, and the later group keeps the old
// one once it holds only its own first agent.
func TestPlanPerTab(t *testing.T) {
	in := Input{PerTab: 2, Tabs: []Tab{tab("tA", "proj", 3, "A1", "A2", "A3")},
		Agents: []Agent{agent("A1", "1.1"), agent("A2", "1.2"), agent("A3", "1.3")}}
	plan := Make(in)
	if got, want := groups(plan), "new[A1 A2] tA=[A3]"; got != want {
		t.Errorf("groups %s, want %s", got, want)
	}
	if got, want := moves(plan), "A1→new:1 A2→new0:2"; got != want {
		t.Errorf("moves %s, want %s", got, want)
	}
	if again := Make(apply(t, in, plan)); len(again.Moves) != 0 {
		t.Errorf("a second plan moves %s", moves(again))
	}
}

// Slots order the agents as numbers, and an agent without a slot comes
// last. A group of one in its own tab moves nothing.
func TestPlanSlotOrder(t *testing.T) {
	in := Input{PerTab: 6, Tabs: []Tab{tab("tA", "Z", 6, "Z"), tab("tB", "B", 6, "B"), tab("tC", "C", 6, "C")},
		Agents: []Agent{agent("Z", ""), agent("B", "10.1"), agent("C", "9.2")}}
	plan := Make(in)
	if got, want := groups(plan), "tC[C B Z]"; got != want {
		t.Errorf("groups %s, want %s", got, want)
	}
	if got, want := moves(plan), "B→tC:2 Z→tC:3"; got != want {
		t.Errorf("moves %s, want %s", got, want)
	}
}

// Each project gets tabs of its own, and its agents never join another
// project's tab.
func TestPlanProjectsApart(t *testing.T) {
	other := func(name, slot string) Agent {
		a := agent(name, slot)
		a.Project = "/work/other"
		return a
	}
	in := Input{PerTab: 6, Tabs: []Tab{tab("tA", "A1", 6, "A1"), tab("tO", "O1", 6, "O1"), tab("tB", "A2", 6, "A2"),
		tab("tP", "O2", 6, "O2")}, Agents: []Agent{agent("A1", "1.1"), other("O1", "2.1"), agent("A2", "3.1"), other("O2", "4.1")}}
	plan := Make(in)
	if got, want := groups(plan), "tA[A1 A2] tO[O1 O2]"; got != want {
		t.Errorf("groups %s, want %s", got, want)
	}
	if got, want := moves(plan), "A2→tA:2 O2→tO:2"; got != want {
		t.Errorf("moves %s, want %s", got, want)
	}
}

// On random workspaces, 1 to 14 agents in tabs of random sizes and slot
// orders, laid out for one --per-tab and arranged for another: each
// pane lands in the cell the plan names (apply), each group ends alone
// in a clean tab in slot order, no tab is left empty, and a second plan
// moves nothing (a randomized check by the fresh review of nhm-yr6).
func TestPlanRandomWorkspaces(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	kinds := map[string]int{} // how often each kind of move came up
	defer func() {
		if kinds["swap"] == 0 || kinds["join"] == 0 || kinds["new tab"] == 0 {
			t.Errorf("the random workspaces gave %v: each kind of move must come up", kinds)
		}
	}()
	for iter := range 2000 {
		n, perTab := 1+rng.Intn(14), 1+rng.Intn(9)
		laidOut := []int{perTab, 6, 4, 3, 2, 9}[rng.Intn(6)]
		agents := make([]Agent, n)
		for i := range agents {
			// M is unique, so no two agents share a slot; N orders them.
			agents[i] = agent(fmt.Sprintf("A%02d", i), fmt.Sprintf("%d.%d", 1+rng.Intn(4), i+1))
			if rng.Intn(10) == 0 {
				agents[i].Slot = ""
			}
		}
		var tabs []Tab
		order := rng.Perm(n)
		for i := 0; i < n; {
			k := 1 + rng.Intn(min(laidOut, n-i))
			var names []string
			for _, j := range order[i : i+k] {
				names = append(names, agents[j].MailName)
			}
			label := "proj"
			if k == 1 && rng.Intn(2) == 0 {
				label = names[0]
			}
			tabs = append(tabs, tab(fmt.Sprintf("t%d", len(tabs)), label, laidOut, names...))
			i += k
		}
		in := Input{PerTab: perTab, Tabs: tabs, Agents: agents}
		t.Run(fmt.Sprint(iter), func(t *testing.T) {
			plan := Make(in)
			for _, m := range plan.Moves {
				switch {
				case m.Swap:
					kinds["swap"]++
				case m.NewTab:
					kinds["new tab"]++
				default:
					kinds["join"]++
				}
			}
			out := apply(t, in, plan)
			if again := Make(out); len(again.Moves) != 0 {
				t.Fatalf("per-tab %d, laid out for %d: groups %s, moves %s; a second plan moves %s", perTab, laidOut,
					groups(plan), moves(plan), moves(again))
			}
			var got, want []string
			for _, tb := range out.Tabs {
				o, clean := spawn.GridOrder(tb.Panes, perTab)
				if !clean || len(o) > perTab {
					t.Fatalf("tab %s ends with %v, clean %v", tb.TabID, o, clean)
				}
				got = append(got, strings.Join(o, " "))
			}
			for _, g := range plan.Groups {
				var panes []string
				for _, a := range g.Agents {
					panes = append(panes, "p-"+a)
				}
				want = append(want, strings.Join(panes, " "))
			}
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("the tabs hold %q, the groups are %q", got, want)
			}
		})
	}
}

// Any tab that spawn would not join is left as it is: its agents do not
// move, and none moves into it. The others are arranged without them.
func TestPlanLeavesOtherTabsAlone(t *testing.T) {
	shell := tab("tS", "proj", 6, "S1", "S2")
	shell.Panes = append(shell.Panes, herdr.LayoutPane{PaneID: "p-shell"})
	broken := tab("tE", "E1", 6, "E1")
	broken.LayoutErr = "pane not found"
	mixed := tab("tM", "proj", 6, "M1", "M2")
	moved := agent("U1", "7.1")
	moved.Problem = "U1 is mismatch in p-U1, not verified"
	in := Input{PerTab: 6, Tabs: []Tab{
		tab("tA", "A1", 6, "A1"),
		tab("tP", "MistyDesert (pusher)", 6, "MistyDesert"),
		shell,
		broken,
		mixed,
		tab("tU", "U1", 6, "U1"),
		tab("tL", "work", 6, "L1", "L2"),
		tab("tB", "A2", 6, "A2"),
	}, Agents: []Agent{agent("A1", "1.1"), agent("MistyDesert", "2.1"), agent("S1", "3.1"), agent("S2", "3.2"),
		agent("E1", "4.1"), agent("M1", "5.1"), {BindingID: "b-M2", MailName: "M2", Project: "/work/other", PaneID: "p-M2", Slot: "5.2"},
		moved, agent("L1", "8.1"), agent("L2", "8.2"), agent("A2", "9.1")}}
	plan := Make(in)
	if got, want := groups(plan), "tA[A1 A2]"; got != want {
		t.Errorf("groups %s, want %s", got, want)
	}
	if got, want := moves(plan), "A2→tA:2"; got != want {
		t.Errorf("moves %s, want %s", got, want)
	}
	want := map[string]string{
		"tP": `its label "MistyDesert (pusher)" is neither the project's ("proj") nor its agent's name`,
		"tS": "pane p-shell holds no agent that nhm binds here (a shell, a tool, or another agent)",
		"tE": "its layout could not be read: pane not found",
		"tM": "it holds the agents of more than one project",
		"tU": "U1 is mismatch in p-U1, not verified",
		"tL": `its label "work" is not the project's ("proj")`,
	}
	for _, l := range plan.Left {
		if want[l.TabID] != l.Reason {
			t.Errorf("left out %s (%v): %q, want %q", l.TabID, l.Agents, l.Reason, want[l.TabID])
		}
		delete(want, l.TabID)
	}
	for tb, reason := range want {
		t.Errorf("%s not left out (%s)", tb, reason)
	}
}
