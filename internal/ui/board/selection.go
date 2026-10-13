package board

import (
	"strconv"
	"strings"
)

// Mark states. A mark is a frozen target: it keeps its key, and on each
// refresh nhm says what that key now names. It is never moved to the row
// that took its place (plan-v4 section 4).
const (
	// MarkValid: a row with the same key is on the board.
	MarkValid = "valid"
	// MarkReplaced: the binding has another generation now (a recycle or
	// a hand-off), or the pane holds another agent.
	MarkReplaced = "replaced"
	// MarkAbsent: the binding is active, but its pane holds no agent.
	MarkAbsent = "absent"
	// MarkMissing: nothing on the board has the key.
	MarkMissing = "missing"
)

// Mark is a marked target.
type Mark struct {
	Key Key
	// Name is what the row was called when it was marked.
	Name  string
	State string
	// Reason says what the key names now, for a mark that is not valid.
	Reason string
}

// Selection is the cursor, the marks and the filter, all by identity:
// a refresh, a filter or a new order keeps them on the same keys.
type Selection struct {
	rows    []Row
	visible []int // indexes into rows
	cursor  Key
	pos     int // the cursor's index in visible; -1 when there is none
	marks   []Mark
	filter  string
}

// Reconcile takes a refresh's rows. The cursor stays on its key; when
// the key is gone, it stays at the same place in the list. Each mark is
// checked again.
func (s *Selection) Reconcile(rows []Row) {
	s.rows = rows
	s.refilter()
	for i := range s.marks {
		s.marks[i].State, s.marks[i].Reason = s.resolve(s.marks[i].Key)
	}
}

func (s *Selection) refilter() {
	s.visible = s.visible[:0]
	for i := range s.rows {
		if s.matches(s.rows[i]) {
			s.visible = append(s.visible, i)
		}
	}
	if len(s.visible) == 0 {
		s.pos = -1
		return
	}
	for vi, ri := range s.visible {
		if s.rows[ri].Key == s.cursor && !s.cursor.IsZero() {
			s.pos = vi
			return
		}
	}
	// The cursor's row is gone or hidden: keep the place.
	s.pos = min(max(s.pos, 0), len(s.visible)-1)
	s.cursor = s.rows[s.visible[s.pos]].Key
}

func (s *Selection) matches(r Row) bool {
	if s.filter == "" {
		return true
	}
	hay := []string{r.Name, r.Kind(), r.Slot(), r.PaneID()}
	if r.Agent != nil {
		hay = append(hay, string(r.Agent.State), r.Agent.HerdrName)
	}
	if r.Absent != nil {
		hay = append(hay, "absent")
	}
	for _, h := range hay {
		if strings.Contains(strings.ToLower(h), s.filter) {
			return true
		}
	}
	return false
}

// resolve says what a mark's key names now.
func (s *Selection) resolve(k Key) (string, string) {
	for _, r := range s.rows {
		if r.Key == k {
			if r.Absent != nil {
				return MarkAbsent, "its pane holds no agent"
			}
			return MarkValid, ""
		}
	}
	for _, r := range s.rows {
		switch {
		case k.Bound() && r.Key.BindingID == k.BindingID:
			return MarkReplaced, "the identity has another occupant now (generation " + strconv.FormatInt(r.Key.Generation, 10) + ")"
		case !k.Bound() && r.PaneID() == k.PaneID && r.Agent != nil:
			if r.Key.Bound() {
				return MarkReplaced, "the pane's agent is bound as " + r.Name + " now"
			}
			return MarkReplaced, "the pane holds another agent now"
		}
	}
	return MarkMissing, "it is not on the board any more"
}

// Rows are the visible rows, in order.
func (s *Selection) Rows() []Row {
	out := make([]Row, len(s.visible))
	for i, ri := range s.visible {
		out[i] = s.rows[ri]
	}
	return out
}

// Index is the cursor's place among the visible rows, -1 when there is
// no row.
func (s *Selection) Index() int { return s.pos }

// Cursor is the row under the cursor.
func (s *Selection) Cursor() (Row, bool) {
	if s.pos < 0 || s.pos >= len(s.visible) {
		return Row{}, false
	}
	return s.rows[s.visible[s.pos]], true
}

// Move moves the cursor by delta rows, within the list.
func (s *Selection) Move(delta int) {
	if len(s.visible) == 0 {
		return
	}
	s.pos = min(max(s.pos+delta, 0), len(s.visible)-1)
	s.cursor = s.rows[s.visible[s.pos]].Key
}

// Top and Bottom move the cursor to the first or last row.
func (s *Selection) Top()    { s.Move(-len(s.visible)) }
func (s *Selection) Bottom() { s.Move(len(s.visible)) }

// SetCursor puts the cursor on a key; false when no visible row has it.
func (s *Selection) SetCursor(k Key) bool {
	for vi, ri := range s.visible {
		if s.rows[ri].Key == k {
			s.pos, s.cursor = vi, k
			return true
		}
	}
	return false
}

// SetFilter shows only the rows whose name, kind, slot, pane or state
// holds text (any case). Marks stay, also on hidden rows.
func (s *Selection) SetFilter(text string) {
	s.filter = strings.ToLower(strings.TrimSpace(text))
	s.refilter()
}

// Filter is the filter text.
func (s *Selection) Filter() string { return s.filter }

// Toggle marks the cursor's row, or unmarks it.
func (s *Selection) Toggle() {
	r, ok := s.Cursor()
	if !ok {
		return
	}
	if s.Marked(r.Key) {
		s.Unmark(r.Key)
		return
	}
	st, reason := s.resolve(r.Key)
	s.marks = append(s.marks, Mark{Key: r.Key, Name: r.Name, State: st, Reason: reason})
}

// Marked reports whether a key is marked.
func (s *Selection) Marked(k Key) bool {
	for _, m := range s.marks {
		if m.Key == k {
			return true
		}
	}
	return false
}

// Unmark removes a mark, valid or not.
func (s *Selection) Unmark(k Key) {
	for i, m := range s.marks {
		if m.Key == k {
			s.marks = append(s.marks[:i], s.marks[i+1:]...)
			return
		}
	}
}

// ClearMarks removes every mark.
func (s *Selection) ClearMarks() { s.marks = nil }

// Marks lists the marks in the order they were made.
func (s *Selection) Marks() []Mark { return append([]Mark(nil), s.marks...) }

// Target sources.
const (
	// FromMarks: the action acts on the marks.
	FromMarks = "marks"
	// FromCursor: no marks; the action acts on the cursor's row.
	FromCursor = "cursor"
	// FromNone: no marks and no row. The action opens the agent picker;
	// it never means "all".
	FromNone = "none"
)

// Targets is what an action would act on, frozen when the action's
// review opens.
type Targets struct {
	From string
	// Rows are the targets whose key is still on the board; an absent
	// binding's row (no Agent) is one, and each action says whether it
	// can act on it. Invalid are marks whose key is replaced or missing.
	Rows    []Row
	Invalid []Mark
	// Hidden counts the valid marks the filter hides.
	Hidden int
}

// Targets returns the marks when there are any, else the cursor's row.
// An invalid mark is reported, never dropped or swapped for another row.
func (s *Selection) Targets() Targets {
	if len(s.marks) > 0 {
		t := Targets{From: FromMarks}
		shown := map[Key]bool{}
		for _, ri := range s.visible {
			shown[s.rows[ri].Key] = true
		}
		for _, m := range s.marks {
			if m.State != MarkValid && m.State != MarkAbsent {
				t.Invalid = append(t.Invalid, m)
				continue
			}
			for _, r := range s.rows {
				if r.Key == m.Key {
					t.Rows = append(t.Rows, r)
				}
			}
			if !shown[m.Key] {
				t.Hidden++
			}
		}
		return t
	}
	if r, ok := s.Cursor(); ok {
		return Targets{From: FromCursor, Rows: []Row{r}}
	}
	return Targets{From: FromNone}
}

// Clear drops the cursor, the marks and the filter: a restored board
// starts with no old selection (plan-v4 section 7).
func (s *Selection) Clear() {
	*s = Selection{rows: s.rows}
	s.pos = -1
	s.refilter()
}
