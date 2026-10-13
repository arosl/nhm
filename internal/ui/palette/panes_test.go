package palette

import (
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/ui"
)

func TestPanesFromTheCapturedWorkspace(t *testing.T) {
	ref := func(id, name string, gen int64, st bindings.Status) *snapshot.BindingRef {
		return &snapshot.BindingRef{ID: id, Generation: gen, Status: st, MailName: name}
	}
	m := &snapshot.Model{Agents: []snapshot.Agent{
		{PaneID: "w1:p1", WorkspaceID: "w1", Slot: "1.1", Kind: "claude", State: herdr.StatusIdle, Title: "t1",
			Binding: ref("b1", "IcyKnoll", 3, bindings.Active)},
		{PaneID: "w1:p2", WorkspaceID: "w1", Kind: "codex", State: herdr.StatusWorking},
		{PaneID: "w2:p1", WorkspaceID: "w2", Slot: "1.1", Kind: "claude", Binding: ref("b9", "Other", 1, bindings.Active)},
		{PaneID: "w1:p3", WorkspaceID: "w1", Slot: "1.3", Kind: "codex", State: herdr.StatusBlocked,
			Binding: ref("b3", "BlueLake", 1, bindings.Active), Session: &herdr.AgentSession{Kind: "id", Value: "s-3"}},
		{PaneID: "w1:p4", WorkspaceID: "w1", Kind: "pi", Binding: ref("b4", "Gone", 2, bindings.Displaced)},
	}}
	panes, unbound := PanesFrom(m, "w1")
	if unbound != 2 || len(panes) != 2 {
		t.Fatalf("panes = %+v, unbound = %d", panes, unbound)
	}
	p := panes[0]
	if p.ID != "w1:p1" || p.Index != 1 || p.Title != "IcyKnoll" || p.Type != "claude" || p.Slot != "1.1" ||
		p.State != herdr.StatusIdle || p.HerdrTitle != "t1" || p.BindingID != "b1" || p.Generation != 3 {
		t.Fatalf("pane = %+v", p)
	}
	if panes[1].ID != "w1:p3" || panes[1].Index != 2 {
		t.Fatalf("second pane = %+v", panes[1])
	}
	want := FrozenTarget{Target: ui.Target{BindingID: "b3", Generation: 1, PaneID: "w1:p3", Kind: "codex", Session: "id:s-3"},
		MailName: "BlueLake"}
	if got := panes[1].Frozen(); got != want || got.Target != ui.AgentTarget(m.Agents[3]) {
		t.Fatalf("target = %+v", got)
	}
	if panes, unbound := PanesFrom(nil, "w1"); panes != nil || unbound != 0 {
		t.Fatal("a nil snapshot has no panes")
	}
}
