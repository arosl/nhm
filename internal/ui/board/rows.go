package board

import (
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/ui"
)

// Key is a row's identity, and so a target's (plan-v4 section 4): the
// context's ui.Target. A bound agent is its binding id and generation; an
// unbound agent (an adoption candidate) is its pane and herdr session. A
// recycled agent keeps its binding id and gets a new generation: it is
// another target. Two keys are the same target when their Key() strings
// are equal; the other fields only describe it.
type Key = ui.Target

// AgentKey is a snapshot agent's key.
func AgentKey(a snapshot.Agent) Key {
	k := Key{PaneID: a.PaneID, Kind: a.Kind}
	if a.Binding != nil {
		k.BindingID, k.Generation = a.Binding.ID, a.Binding.Generation
	}
	if a.Session != nil {
		k.Session = a.Session.Kind + ":" + a.Session.Value
	}
	return k
}

// same reports whether two keys name the same target.
func same(a, b Key) bool { return a.Key() == b.Key() }

// Cell is a value from a slower source, matched to its row by key. Known
// is false when the source has not answered for this row: the board
// shows it as unknown, never as zero.
type Cell[T any] struct {
	Value T
	Known bool
	// At is when the source read it.
	At time.Time
}

// Row is one line of the board: an agent herdr shows, or an active
// binding whose pane holds no agent (Absent).
type Row struct {
	Key Key
	// Name is the Mail name, or herdr's name for an unbound agent.
	Name   string
	Agent  *snapshot.Agent
	Absent *bindings.Binding
	Mail   Cell[snapshot.MailState]
	Beads  Cell[[]string]
}

// Slot is the row's N.M, or "".
func (r Row) Slot() string {
	if r.Agent != nil {
		return r.Agent.Slot
	}
	if r.Absent != nil {
		return r.Absent.Slot
	}
	return ""
}

// Kind is the agent's kind.
func (r Row) Kind() string {
	if r.Agent != nil {
		return r.Agent.Kind
	}
	if r.Absent != nil {
		return r.Absent.Kind
	}
	return ""
}

// PaneID is the row's pane, or "" for an absent binding without one.
func (r Row) PaneID() string {
	if r.Agent != nil {
		return r.Agent.PaneID
	}
	if r.Absent != nil {
		return r.Absent.PaneID
	}
	return ""
}

// MailByKey is the mail source's value: each bound agent's inbox, by
// its key's Key().
type MailByKey map[string]snapshot.MailState

// BeadsByKey is the beads source's value: each bound agent's beads in
// progress (none: an empty list), by its key's Key().
type BeadsByKey map[string][]string

// BuildRows joins the sources into rows: the agents in the snapshot's
// order (workspace, slot, pane), then the absent bindings. A nil model
// gives no rows. Mail and beads cells are known only for the rows their
// source answered for.
func BuildRows(m *snapshot.Model, mail State[MailByKey], beads State[BeadsByKey]) []Row {
	if m == nil {
		return nil
	}
	rows := make([]Row, 0, len(m.Agents)+len(m.Absent))
	for i := range m.Agents {
		a := &m.Agents[i]
		r := Row{Key: AgentKey(*a), Name: a.HerdrName, Agent: a}
		if a.Binding != nil {
			r.Name = a.Binding.MailName
			if v, ok := mail.Value[r.Key.Key()]; ok && mail.Has {
				r.Mail = Cell[snapshot.MailState]{Value: v, Known: true, At: mail.At}
			}
			if v, ok := beads.Value[r.Key.Key()]; ok && beads.Has {
				r.Beads = Cell[[]string]{Value: v, Known: true, At: beads.At}
			}
		}
		rows = append(rows, r)
	}
	for i := range m.Absent {
		b := &m.Absent[i]
		rows = append(rows, Row{Key: Key{BindingID: b.ID, Generation: b.Generation, PaneID: b.PaneID, Kind: b.Kind},
			Name: b.MailName, Absent: b})
	}
	return rows
}
