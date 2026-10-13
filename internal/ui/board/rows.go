package board

import (
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/snapshot"
)

// Key is a row's identity, and so a target's (plan-v4 section 4): a
// bound agent is its binding id and generation. An unbound agent (an
// adoption candidate) has no binding: its pane, kind and herdr session
// stand for it. A recycled agent keeps its binding id and gets a new
// generation: it is another key.
type Key struct {
	BindingID  string `json:"binding_id,omitempty"`
	Generation int64  `json:"generation,omitempty"`
	PaneID     string `json:"pane_id,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Session    string `json:"session,omitempty"`
}

// Bound reports whether the key is a binding's.
func (k Key) Bound() bool { return k.BindingID != "" }

// IsZero reports whether the key names nothing.
func (k Key) IsZero() bool { return k == Key{} }

// AgentKey is a snapshot agent's key.
func AgentKey(a snapshot.Agent) Key {
	if a.Binding != nil {
		return Key{BindingID: a.Binding.ID, Generation: a.Binding.Generation}
	}
	k := Key{PaneID: a.PaneID, Kind: a.Kind}
	if a.Session != nil {
		k.Session = a.Session.Kind + ":" + a.Session.Value
	}
	return k
}

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

// MailByKey is the mail source's value: each bound agent's inbox.
type MailByKey map[Key]snapshot.MailState

// BeadsByKey is the beads source's value: each bound agent's beads in
// progress (none: an empty list).
type BeadsByKey map[Key][]string

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
			if v, ok := mail.Value[r.Key]; ok && mail.Has {
				r.Mail = Cell[snapshot.MailState]{Value: v, Known: true, At: mail.At}
			}
			if v, ok := beads.Value[r.Key]; ok && beads.Has {
				r.Beads = Cell[[]string]{Value: v, Known: true, At: beads.At}
			}
		}
		rows = append(rows, r)
	}
	for i := range m.Absent {
		b := &m.Absent[i]
		rows = append(rows, Row{Key: Key{BindingID: b.ID, Generation: b.Generation}, Name: b.MailName, Absent: b})
	}
	return rows
}
