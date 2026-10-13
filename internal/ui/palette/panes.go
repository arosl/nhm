package palette

import (
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/herdr"
)

// Pane is the palette's stand-in for ntm's tmux.Pane: one bound agent of
// the captured workspace. ntm's palette reads a pane's ID, Index, Title
// and Type; nhm adds the binding that a send freezes as its target.
// There is no user pane: only bound agents are panes.
type Pane struct {
	// ID is herdr's pane id.
	ID string
	// Index is the pane's place in slot order, from 1.
	Index int
	// Title is the Agent Mail name.
	Title string
	// Type is nhm's kind ("claude").
	Type  string
	Slot  string
	State herdr.AgentStatus
	// HerdrTitle is the pane's title in herdr.
	HerdrTitle string

	BindingID  string
	Generation int64
}

// Target is the pane frozen as a send target.
func (p Pane) Target() Target {
	return Target{BindingID: p.BindingID, Generation: p.Generation, PaneID: p.ID, MailName: p.Title, Kind: p.Type}
}

// PanesFrom returns the bound agents of workspace ws in a snapshot, in
// the snapshot's slot order, and how many of its agents have no active
// binding: a send refuses those (nhm adopt binds one).
func PanesFrom(m *snapshot.Model, ws string) (panes []Pane, unbound int) {
	if m == nil {
		return nil, 0
	}
	for _, a := range m.Agents {
		if a.WorkspaceID != ws {
			continue
		}
		if a.Binding == nil || a.Binding.Status != bindings.Active {
			unbound++
			continue
		}
		panes = append(panes, Pane{ID: a.PaneID, Index: len(panes) + 1, Title: a.Binding.MailName, Type: a.Kind,
			Slot: a.Slot, State: a.State, HerdrTitle: a.Title, BindingID: a.Binding.ID, Generation: a.Binding.Generation})
	}
	return panes, unbound
}
