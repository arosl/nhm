package palette

import (
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/ui"
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

	// BindingID, Generation and Session are the agent's ui.Target.
	BindingID  string
	Generation int64
	Session    string
}

// Frozen is the pane frozen as a send target.
func (p Pane) Frozen() FrozenTarget {
	return FrozenTarget{Target: ui.Target{BindingID: p.BindingID, Generation: p.Generation, PaneID: p.ID, Kind: p.Type,
		Session: p.Session}, MailName: p.Title}
}

// Session is the palette's stand-in for ntm's tmux.Session: a herdr
// workspace, for the selector.
type Session struct {
	ID string
	// Name is the workspace's label, else its id.
	Name string
	// Windows is the workspace's tab count.
	Windows int
	// Attached is true for the focused workspace.
	Attached bool
}

// SessionsFrom returns a snapshot's workspaces, in its order.
func SessionsFrom(m *snapshot.Model) []Session {
	if m == nil {
		return nil
	}
	out := make([]Session, 0, len(m.Workspaces))
	for _, w := range m.Workspaces {
		name := w.Label
		if name == "" {
			name = w.ID
		}
		out = append(out, Session{ID: w.ID, Name: name, Windows: int(w.TabCount), Attached: w.Focused})
	}
	return out
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
		// The identity is the board's and the picker's (ui.AgentTarget).
		t := ui.AgentTarget(a)
		panes = append(panes, Pane{ID: t.PaneID, Index: len(panes) + 1, Title: a.Binding.MailName, Type: t.Kind,
			Slot: a.Slot, State: a.State, HerdrTitle: a.Title, BindingID: t.BindingID, Generation: t.Generation,
			Session: t.Session})
	}
	return panes, unbound
}
