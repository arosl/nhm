// Package picker is the agent picker (plan-v4 section 4): the agents of
// the captured workspace, read from the in-process snapshot, to pick one
// or several as frozen targets. An action opens it when no selection was
// given; it never picks "all" by itself. Marks are kept by identity
// across refreshes and filters. A marked agent that was replaced or is
// gone stays on screen as invalid, is never swapped for the agent now in
// its place, and blocks the pick until it is unmarked.
//
// The picker is a component: a parent program embeds it, forwards its
// messages, and reads Picked or Canceled. It runs no I/O in Update; each
// snapshot read runs as a command, and an answer to an older read is
// dropped.
package picker

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/actions"
	"github.com/arosl/nhm/internal/ui/tui/theme"
)

// Source reads the snapshot: the in-process snapshot service the caller
// wires (the same build as nhm status). It must honour ctx's deadline.
type Source func(ctx context.Context) (*snapshot.Model, error)

// Options say what the picker picks.
type Options struct {
	// Title says what the agents are for ("interrupt").
	Title string
	// Workspace is the captured workspace's id; the picker shows its
	// agents only. Label names it in the header.
	Workspace, Label string
	// Many picks several agents; else exactly one.
	Many bool
	// Takes says which agents can be picked (actions.Takes).
	Takes actions.Takes
	// Timeout bounds one snapshot read (default 5 s).
	Timeout time.Duration
}

// ForAction is the picker for action a in context c.
func ForAction(a actions.Action, c ui.Context, src Source) Model {
	return New(src, Options{Title: a.Label, Workspace: c.WorkspaceID, Label: c.WorkspaceLabel,
		Many: a.Selection == actions.SelectMany, Takes: a.Takes})
}

// Picked is sent when the user picked: the frozen targets, in the order
// they were marked.
type Picked struct {
	ID      int64
	Targets []ui.Target
}

// Canceled is sent when the user left without a pick.
type Canceled struct{ ID int64 }

// loaded is the answer to snapshot read seq.
type loaded struct {
	id, seq int64
	model   *snapshot.Model
	err     error
}

// row is one agent of the workspace.
type row struct {
	target ui.Target
	name   string
	slot   string
	state  herdr.AgentStatus
	// why says why it cannot be picked; "" when it can.
	why string
}

// mark is a marked target and what was shown for it.
type mark struct {
	target ui.Target
	name   string
	// invalid says what the target's key names now; "" while it is on
	// the list and can be picked.
	invalid string
}

// lines are what the list shows: rows that pass the filter, then the
// invalid marks.
type line struct {
	row  *row
	mark *mark
}

var ids atomic.Int64

// Model is the picker.
type Model struct {
	id      int64
	src     Source
	opts    Options
	seq     int64
	loading bool
	pending bool
	err     error

	rows   []row
	marks  []mark
	cursor int
	filter string
	typing bool
	note   string

	width, height  int
	done, canceled bool
	picked         []ui.Target
}

// New returns a picker over src; Init starts the first read.
func New(src Source, opts Options) Model {
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Second
	}
	// Loading until the first answer: Init cannot change the model.
	return Model{id: ids.Add(1), src: src, opts: opts, loading: true}
}

// ID tells this picker's messages from another's.
func (m Model) ID() int64 { return m.id }

// Init reads the snapshot.
func (m Model) Init() tea.Cmd { return m.read() }

// Refresh reads the snapshot again. One read is in flight at a time:
// during a read, the refresh waits, and the answer to the older read is
// dropped when it comes.
func (m Model) Refresh() (Model, tea.Cmd) {
	m.seq++
	if m.loading {
		m.pending = true
		return m, nil
	}
	return m, m.read()
}

func (m *Model) read() tea.Cmd {
	m.loading = true
	id, seq, src, timeout := m.id, m.seq, m.src, m.opts.Timeout
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		mod, err := src(ctx)
		return loaded{id: id, seq: seq, model: mod, err: err}
	}
}

// Done reports whether the user picked or left.
func (m Model) Done() bool { return m.done || m.canceled }

// Canceled reports whether the user left without a pick.
func (m Model) Canceled() bool { return m.canceled }

// Targets are the picked targets, once Done and not Canceled.
func (m Model) Targets() []ui.Target { return append([]ui.Target{}, m.picked...) }

// Update handles a message.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if m.Done() {
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case loaded:
		if msg.id != m.id {
			return m, nil
		}
		m.loading = false
		if msg.seq != m.seq {
			// A refresh came during this read: drop the answer, read again.
			if m.pending {
				m.pending = false
				return m, m.read()
			}
			return m, nil
		}
		if msg.err != nil || msg.model == nil {
			m.err = msg.err
			if m.err == nil {
				m.err = fmt.Errorf("no snapshot")
			}
			return m, nil
		}
		m.err = nil
		m.setRows(msg.model)
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

// setRows replaces the rows from a snapshot and checks each mark against
// them.
func (m *Model) setRows(mod *snapshot.Model) {
	var current *ui.Target
	if ls := m.lines(); m.cursor < len(ls) && ls[m.cursor].row != nil {
		t := ls[m.cursor].row.target
		current = &t
	}
	// A new slice: an older copy of the model keeps its own rows.
	m.rows = make([]row, 0, len(mod.Agents))
	for _, a := range mod.Agents {
		if a.WorkspaceID != m.opts.Workspace {
			continue
		}
		r := row{target: ui.AgentTarget(a), slot: a.Slot, state: a.State, name: clean(firstNonEmpty(a.HerdrName, a.PaneID))}
		if a.Binding != nil {
			r.name = clean(a.Binding.MailName)
		}
		r.why = m.why(a)
		m.rows = append(m.rows, r)
	}
	for i := range m.marks {
		m.marks[i].invalid = m.invalidity(m.marks[i].target)
	}
	// The cursor stays on its agent; it does not move to the row now in
	// its place.
	m.cursor = 0
	if current != nil {
		for i, l := range m.lines() {
			if l.row != nil && l.row.target.Key() == current.Key() {
				m.cursor = i
			}
		}
	}
}

// why says why an agent cannot be picked for the options' agents.
func (m Model) why(a snapshot.Agent) string {
	switch m.opts.Takes {
	case actions.TakesAny:
		return ""
	case actions.TakesUnbound:
		switch {
		case a.Binding != nil:
			return "bound already, as " + clean(a.Binding.MailName)
		case a.Session == nil:
			// Without a session nhm cannot tell this agent from a later
			// one in the pane (adopt's ExpectedSession).
			return "herdr reports no agent session: adopt it by hand (nhm adopt <Name> --pane " + a.PaneID + ")"
		}
		return ""
	}
	switch {
	case a.Binding == nil:
		return "no binding: nhm adopt binds it"
	case a.Binding.Status != bindings.Active:
		return "its binding is " + string(a.Binding.Status) + ": adopt or import it first"
	}
	return ""
}

// invalidity says why a mark no longer names a pickable agent; "" when
// its key is on the list and can be picked.
func (m Model) invalidity(t ui.Target) string {
	for _, r := range m.rows {
		if r.target.Key() == t.Key() {
			return r.why
		}
	}
	for _, r := range m.rows {
		switch {
		case t.Bound() && r.target.BindingID == t.BindingID:
			return fmt.Sprintf("replaced: the binding is at generation %d now", r.target.Generation)
		case t.PaneID != "" && r.target.PaneID == t.PaneID:
			return "replaced: its pane holds another agent now"
		}
	}
	return "gone from the workspace"
}

// lines are the rows that pass the filter, then the marks that are not
// valid any more (so they can be seen and unmarked).
func (m Model) lines() []line {
	var out []line
	for i := range m.rows {
		if m.matches(m.rows[i]) {
			out = append(out, line{row: &m.rows[i]})
		}
	}
	for i := range m.marks {
		if m.marks[i].invalid != "" && !m.onList(m.marks[i].target) {
			out = append(out, line{mark: &m.marks[i]})
		}
	}
	return out
}

func (m Model) onList(t ui.Target) bool {
	for _, r := range m.rows {
		if r.target.Key() == t.Key() {
			return true
		}
	}
	return false
}

func (m Model) matches(r row) bool {
	if m.filter == "" {
		return true
	}
	f := strings.ToLower(m.filter)
	for _, s := range []string{r.name, r.slot, r.target.Kind, r.target.PaneID, string(r.state)} {
		if strings.Contains(strings.ToLower(s), f) {
			return true
		}
	}
	return false
}

func (m Model) markIndex(t ui.Target) int {
	for i, mk := range m.marks {
		if mk.target.Key() == t.Key() {
			return i
		}
	}
	return -1
}

func (m Model) key(k tea.KeyMsg) (Model, tea.Cmd) {
	m.note = ""
	if m.typing {
		switch k.Type {
		case tea.KeyEnter:
			m.typing = false
		case tea.KeyEsc:
			m.typing, m.filter = false, ""
		case tea.KeyBackspace:
			if r := []rune(m.filter); len(r) > 0 {
				m.filter = string(r[:len(r)-1])
			}
		case tea.KeySpace:
			m.filter += " "
		case tea.KeyRunes:
			m.filter += clean(string(k.Runes))
		}
		m.cursor = 0
		return m, nil
	}
	ls := m.lines()
	if m.cursor >= len(ls) {
		m.cursor = max(0, len(ls)-1)
	}
	switch k.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(ls)-1 {
			m.cursor++
		}
	case "/":
		m.typing = true
	case "r":
		return m.Refresh()
	case " ", "x":
		if m.cursor < len(ls) {
			m.toggle(ls[m.cursor])
		}
	case "enter":
		return m.pick(ls)
	case "esc":
		if m.filter != "" {
			m.filter, m.cursor = "", 0
			return m, nil
		}
		return m.cancel()
	case "q", "ctrl+c":
		return m.cancel()
	}
	return m, nil
}

func (m *Model) toggle(l line) {
	switch {
	case l.mark != nil:
		// An invalid mark: unmarking is all it allows.
		m.marks = removeMark(m.marks, l.mark.target)
	case !m.opts.Many:
		m.note = "enter picks the agent under the cursor"
	case m.markIndex(l.row.target) >= 0:
		m.marks = removeMark(m.marks, l.row.target)
	case l.row.why != "":
		m.note = l.row.name + ": " + l.row.why
	default:
		m.marks = append(m.marks, mark{target: l.row.target, name: l.row.name})
	}
}

func removeMark(ms []mark, t ui.Target) []mark {
	out := ms[:0]
	for _, mk := range ms {
		if mk.target.Key() != t.Key() {
			out = append(out, mk)
		}
	}
	return out
}

// pick confirms: the marks when there are any, else the agent under the
// cursor. Every target must be on the list and pickable now.
func (m Model) pick(ls []line) (Model, tea.Cmd) {
	if m.loading && m.rows == nil {
		m.note = "the agents are still loading"
		return m, nil
	}
	var targets []ui.Target
	if len(m.marks) > 0 {
		for _, mk := range m.marks {
			if mk.invalid != "" {
				m.note = mk.name + " " + mk.invalid + ": unmark it (x) before you pick"
				return m, nil
			}
			targets = append(targets, mk.target)
		}
	} else {
		if m.cursor >= len(ls) || ls[m.cursor].row == nil {
			m.note = "no agent under the cursor"
			return m, nil
		}
		r := ls[m.cursor].row
		if r.why != "" {
			m.note = r.name + ": " + r.why
			return m, nil
		}
		targets = []ui.Target{r.target}
	}
	if !m.opts.Many && len(targets) != 1 {
		m.note = "pick exactly one agent"
		return m, nil
	}
	m.done, m.picked = true, targets
	id, picked := m.id, append([]ui.Target{}, targets...)
	return m, func() tea.Msg { return Picked{ID: id, Targets: picked} }
}

func (m Model) cancel() (Model, tea.Cmd) {
	m.canceled = true
	id := m.id
	return m, func() tea.Msg { return Canceled{ID: id} }
}

// View draws the picker.
func (m Model) View() string {
	t := theme.Current()
	st := theme.NewStyles(t)
	var b strings.Builder
	what := "pick an agent"
	if m.opts.Many {
		what = "pick agents"
	}
	if m.opts.Title != "" {
		what += " to " + clean(m.opts.Title)
	}
	head := what + " · workspace " + clean(firstNonEmpty(m.opts.Label, m.opts.Workspace, "none"))
	b.WriteString(st.Title.Render(m.fit(head)) + "\n")
	switch {
	case m.err != nil:
		b.WriteString(st.Error.Render(m.fit("cannot read the agents: "+clean(m.err.Error())+" (r tries again)")) + "\n")
	case m.loading && m.rows == nil:
		b.WriteString(st.Dim.Render("reading the agents…") + "\n")
	case len(m.rows) == 0:
		b.WriteString(st.Dim.Render("no agents in this workspace") + "\n")
	}
	ls := m.lines()
	first, last := m.window(len(ls))
	for i := first; i < last; i++ {
		b.WriteString(m.renderLine(st, ls[i], i == m.cursor) + "\n")
	}
	if m.typing || m.filter != "" {
		b.WriteString(st.Info.Render(m.fit("/ "+m.filter)) + "\n")
	}
	if m.note != "" {
		b.WriteString(st.Warning.Render(m.fit(m.note)) + "\n")
	}
	keys := "enter pick · / filter · r refresh · esc cancel"
	if m.opts.Many {
		keys = fmt.Sprintf("x mark (%d) · ", len(m.marks)) + keys
	}
	b.WriteString(st.Help.Render(m.fit(keys)))
	return b.String()
}

func (m Model) renderLine(st theme.Styles, l line, cursor bool) string {
	pointer := "  "
	if cursor {
		pointer = "> "
	}
	if l.mark != nil {
		return st.Warning.Render(m.fit(pointer + "[!] " + l.mark.name + "  " + l.mark.invalid))
	}
	r := l.row
	box := ""
	if m.opts.Many {
		box = "[ ] "
		if i := m.markIndex(r.target); i >= 0 {
			box = "[x] "
			if m.marks[i].invalid != "" {
				box = "[!] "
			}
		}
	}
	text := fmt.Sprintf("%s%s%-5s %-16s %-8s %s", pointer, box, firstNonEmpty(r.slot, "-"), r.name, clean(r.target.Kind), r.state)
	if r.why != "" {
		return st.Dim.Render(m.fit(text + "  " + r.why))
	}
	color := theme.Current().Semantic().AgentColor(r.target.Kind)
	line := m.fit(text)
	if cursor {
		return st.Bold.Foreground(color).Render(line)
	}
	return lipgloss.NewStyle().Foreground(color).Render(line)
}

// window is the range of lines that fits the height around the cursor.
func (m Model) window(n int) (int, int) {
	room := m.height - 5
	if m.height <= 0 || room >= n {
		return 0, n
	}
	if room < 1 {
		room = 1
	}
	first := m.cursor - room/2
	if first < 0 {
		first = 0
	}
	if first+room > n {
		first = n - room
	}
	return first, first + room
}

// fit cuts a line to the width.
func (m Model) fit(s string) string {
	if m.width <= 0 {
		return s
	}
	return ansi.Truncate(s, m.width, "…")
}

// clean drops terminal controls from text that pane titles, names or
// errors carry (plan-v4 section 4).
func clean(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
