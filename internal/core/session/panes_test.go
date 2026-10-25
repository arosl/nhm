package session

import (
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

// panesSnap is workspace w1: tab w1:t1 holds two bound agents (w1:p1 at
// 3.1, w1:p2 at 3.2) and an unbound codex (w1:p5); tab w1:t2 holds a
// shell (w1:p3). The active tab is w1:t1 and its focused pane w1:p2.
// Workspace w2's pane is left out.
func panesSnap() (herdr.SessionSnapshot, []bindings.Binding) {
	snap := herdr.SessionSnapshot{
		Workspaces: []herdr.WorkspaceInfo{{WorkspaceID: "w1", ActiveTabID: "w1:t1"}, {WorkspaceID: "w2", ActiveTabID: "w2:t1"}},
		Panes: []herdr.PaneInfo{
			{PaneID: "w1:p3", TabID: "w1:t2", WorkspaceID: "w1", TerminalTitleStripped: "user@host:/p", Focused: true},
			{PaneID: "w1:p2", TabID: "w1:t1", WorkspaceID: "w1", Agent: "codex", Label: "BlueLake gpt-5"},
			{PaneID: "w1:p1", TabID: "w1:t1", WorkspaceID: "w1", Agent: "claude", Label: "IcyKnoll opus"},
			{PaneID: "w1:p5", TabID: "w1:t1", WorkspaceID: "w1", Agent: "codex", Title: "codex"},
			{PaneID: "w2:p1", TabID: "w2:t1", WorkspaceID: "w2", Agent: "claude"},
		},
		Layouts: []herdr.TabLayout{{WorkspaceID: "w1", TabID: "w1:t1", FocusedPaneID: "w1:p2"},
			{WorkspaceID: "w1", TabID: "w1:t2", FocusedPaneID: "w1:p3"}},
	}
	bound := []bindings.Binding{
		{MailName: "IcyKnoll", HerdrAlias: "icyknoll", Kind: "claude", Status: bindings.Active, WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p1", Slot: "3.1"},
		{MailName: "BlueLake", HerdrAlias: "bluelake", Kind: "codex", Status: bindings.Active, WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p2", Slot: "3.2"},
	}
	return snap, bound
}

// A session's panes are its workspace's, agents and shells, in ntm's
// window and pane order: a pane without a slot is index 0 of its tab's
// window, so it comes first, as ntm's user pane does.
func TestPanesOfWorkspace(t *testing.T) {
	snap, bound := panesSnap()
	got := Panes(snap, "w1", bound)
	type row struct {
		id, slot, title, kind, typ string
		window, index              int
		active                     bool
	}
	want := []row{
		{"w1:p3", "", "user@host:/p", "", UserType, 0, 0, false},
		{"w1:p5", "", "codex", "codex", "codex", 3, 0, false},
		{"w1:p1", "3.1", "IcyKnoll opus", "claude", "claude", 3, 1, false},
		{"w1:p2", "3.2", "BlueLake gpt-5", "codex", "codex", 3, 2, true},
	}
	if len(got) != len(want) {
		t.Fatalf("panes = %+v", got)
	}
	for i, w := range want {
		g := got[i]
		if (row{g.PaneID, g.Slot, g.Title, g.Kind, g.Type, g.Window, g.Index, g.Active}) != w {
			t.Errorf("pane %d = %+v, want %+v", i, g, w)
		}
	}
	if got[2].Binding == nil || got[2].Binding.MailName != "IcyKnoll" || got[0].Binding != nil {
		t.Errorf("bindings: %+v, %+v", got[2].Binding, got[0].Binding)
	}
	if got[0].Ref() != "w1:p3" || got[2].Ref() != "1" {
		t.Errorf("refs = %q, %q", got[0].Ref(), got[2].Ref())
	}
}

// Without a layout for the active tab, the active pane is herdr's
// focused pane, when it is in the active tab.
func TestPanesActiveWithoutLayout(t *testing.T) {
	snap, bound := panesSnap()
	snap.Layouts = nil
	for _, p := range Panes(snap, "w1", bound) {
		if p.Active {
			t.Errorf("active %s: the focused pane w1:p3 is not in the active tab", p.PaneID)
		}
	}
	snap.Panes[1].Focused = true
	var active []string
	for _, p := range Panes(snap, "w1", bound) {
		if p.Active {
			active = append(active, p.PaneID)
		}
	}
	if len(active) != 1 || active[0] != "w1:p2" {
		t.Errorf("active = %v", active)
	}
}

// ntm's AgentFilter: --all takes every pane, the type flags their four
// types only.
func TestTypeFilter(t *testing.T) {
	if !(TypeFilter{}).IsEmpty() || (TypeFilter{Codex: true}).IsEmpty() {
		t.Error("IsEmpty")
	}
	for _, c := range []struct {
		f    TypeFilter
		typ  string
		want bool
	}{
		{TypeFilter{All: true}, UserType, true},
		{TypeFilter{All: true}, "pi", true},
		{TypeFilter{Claude: true}, "claude", true},
		{TypeFilter{Claude: true}, "codex", false},
		{TypeFilter{Codex: true}, "codex", true},
		{TypeFilter{Gemini: true}, "gemini", true},
		{TypeFilter{Antigravity: true}, "antigravity", true},
		{TypeFilter{Claude: true, Codex: true}, "pi", false},
		{TypeFilter{Claude: true}, UserType, false},
	} {
		if got := c.f.Matches(c.typ); got != c.want {
			t.Errorf("%+v.Matches(%s) = %v", c.f, c.typ, got)
		}
	}
}

// The pane after "session:": nhm's pane forms, an agent's name in any
// case, or the end of a herdr pane id (ntm's suffix match).
func TestMatchPane(t *testing.T) {
	snap, bound := panesSnap()
	panes := Panes(snap, "w1", bound)
	tabs := AgentTabs(bound)
	for _, c := range []struct {
		spec, want string
	}{
		{"1", "w1:p1"},
		{"3.2", "w1:p2"},
		{"w1:p5", "w1:p5"},
		{"icyknoll", "w1:p1"},
		{"BLUELAKE", "w1:p2"},
		{"p3", "w1:p3"},
		{"7", ""},
		{"nobody", ""},
	} {
		target, err := ParsePane(c.spec, tabs, "w1:t1")
		if err != nil {
			t.Fatalf("%s: %v", c.spec, err)
		}
		got := CopyTargets(panes, &target, TypeFilter{All: true})
		switch {
		case c.want == "" && len(got) != 0:
			t.Errorf("%s matched %+v", c.spec, got)
		case c.want != "" && (len(got) != 1 || got[0].PaneID != c.want):
			t.Errorf("%s = %+v, want %s", c.spec, got, c.want)
		}
	}
}

// Agents lists the bound agents of live panes by name, each name once;
// a pane without a binding is not an agent entry.
func TestAgents(t *testing.T) {
	snap, bound := panesSnap()
	// A second pane that the same name holds (a stale binding) is left
	// out.
	snap.Panes = append(snap.Panes, herdr.PaneInfo{PaneID: "w1:p9", TabID: "w1:t1", WorkspaceID: "w1", Agent: "claude"})
	bound = append(bound, bindings.Binding{MailName: "BlueLake", Kind: "claude", Status: bindings.Active, WorkspaceID: "w1",
		TabID: "w1:t1", PaneID: "w1:p9", Slot: "3.9"})
	got := Agents(Panes(snap, "w1", bound))
	if len(got) != 2 || got[0].Name != "BlueLake" || got[0].PaneID != "w1:p2" || got[0].PaneIndex != 2 ||
		got[0].PaneTitle != "BlueLake gpt-5" || got[1].Name != "IcyKnoll" || got[1].PaneID != "w1:p1" {
		t.Errorf("agents = %+v", got)
	}
}

// copy's order (ntm's runCopy): the pane named wins over the type flags;
// with neither, the active pane, else the first.
func TestCopyTargetsOrder(t *testing.T) {
	snap, bound := panesSnap()
	panes := Panes(snap, "w1", bound)
	ids := func(ps []Pane) []string {
		var out []string
		for _, p := range ps {
			out = append(out, p.PaneID)
		}
		return out
	}
	eq := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	if got := ids(CopyTargets(panes, nil, TypeFilter{})); !eq(got, []string{"w1:p2"}) {
		t.Errorf("no filter = %v, want the active pane", got)
	}
	if got := ids(CopyTargets(panes, nil, TypeFilter{Codex: true})); !eq(got, []string{"w1:p5", "w1:p2"}) {
		t.Errorf("--cod = %v", got)
	}
	if got := ids(CopyTargets(panes, nil, TypeFilter{All: true})); len(got) != 4 {
		t.Errorf("--all = %v", got)
	}
	spec := PaneTarget{Form: PaneSlot, Slot: "3.1"}
	if got := ids(CopyTargets(panes, &spec, TypeFilter{Codex: true})); !eq(got, []string{"w1:p1"}) {
		t.Errorf("spec and --cod = %v", got)
	}
	for i := range panes {
		panes[i].Active = false
	}
	if got := ids(CopyTargets(panes, nil, TypeFilter{})); !eq(got, []string{"w1:p3"}) {
		t.Errorf("no active pane = %v, want the first", got)
	}
	if got := CopyTargets(nil, nil, TypeFilter{}); got != nil {
		t.Errorf("no panes = %v", got)
	}
}
