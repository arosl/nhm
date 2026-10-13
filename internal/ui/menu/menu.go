package menu

import (
	"fmt"
	"os/exec"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/actions"
	"github.com/arosl/nhm/internal/ui/forms"
	"github.com/arosl/nhm/internal/ui/palette"
	"github.com/arosl/nhm/internal/ui/picker"
	"github.com/arosl/nhm/internal/ui/tui/theme"
	"github.com/arosl/nhm/internal/ui/views"
)

// Env is what the menu reads and runs, wired by the CLI for the captured
// context. A nil field makes the rows that need it unavailable.
type Env struct {
	Context ui.Context
	Core    actions.Core
	// NewID makes operation ids (store.NewOperationID): each form gets a
	// guard over it.
	NewID func() (string, error)
	// Agents is the agent picker's snapshot source.
	Agents picker.Source
	// Views is what the read-only views read.
	Views views.Env
	// Operations reads recent operations and jobs.
	Operations forms.OperationsReader
	// Kinds are the kinds the spawn wizard offers (kinds.All()).
	Kinds []kinds.Adapter
	// Surface is the command of another surface ("palette", "palette.go",
	// "board"), which the menu runs in the same popup until it ends; nil
	// when it is not there.
	Surface func(id string) *exec.Cmd
	// Memory serves the composer's memory preview; nil: none.
	Memory *palette.Submitter
}

// paletteGo is the menu's own row for the Go palette until its
// acceptance (plan-v4 section 5): "palette (new)", after the palette.
var paletteGo = actions.Action{ID: "palette.go", Label: "palette (new)", Help: "the Go palette, until it is accepted",
	Kind: actions.KindSurface, Scope: actions.ScopeNone, Menu: actions.MenuPlace{Key: "A"}}

// Model is the menu: one program in a popup, every MVP row of the action
// table within two levels; forms and views open inside it and return to
// it; nothing opens another popup.
type Model struct {
	env    Env
	theme  theme.Theme
	width  int
	height int
	level  string
	cursor int
	note   string
	help   bool
	// helpTop is the first help line shown: the help scrolls in a short
	// popup.
	helpTop int
	open    screen
	// stack holds the screens under the open one: a form a view opened
	// returns to that view.
	stack []screen
}

// screen is what the menu shows instead of its list: a form, a view, the
// picker or the composer. It gets every message while it is open.
type screen interface {
	Init() tea.Cmd
	// Update takes a message; closed is true when the screen is done.
	Update(msg tea.Msg) (cmd tea.Cmd, closed bool)
	View(width, height int) string
}

// openScreen replaces the open screen (the picker hands on to the form
// of its pick).
type openScreen struct{ s screen }

// pushScreen opens a screen over the open one, which comes back when it
// closes (a view's form).
type pushScreen struct{ s screen }

// noteMsg closes the open screen and shows a line in the list.
type noteMsg struct{ text string }

// surfaceEnded is a surface the menu ran in its popup that ended.
type surfaceEnded struct {
	id  string
	err error
}

// New returns the menu for env.
func New(env Env) *Model {
	return &Model{env: env, theme: theme.Current(), width: 80, height: 24}
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd { return nil }

// Rows are the shown rows of a level, in order.
func (m *Model) Rows(level string) []actions.Action {
	var out []actions.Action
	for _, a := range actions.Level(level) {
		if m.shown(a) {
			out = append(out, a)
		}
		if a.ID == "palette" && level == "" && m.surface(paletteGo.ID) != nil {
			out = append(out, paletteGo)
		}
	}
	return out
}

// shown reports whether the menu shows a row: groups always; a surface
// when it is there; a view when the menu can open it; a form or an action
// when it is built. An action that is not built is never a dead key.
func (m *Model) shown(a actions.Action) bool {
	switch a.Kind {
	case actions.KindGroup:
		return true
	case actions.KindSurface:
		return m.surface(a.ID) != nil
	case actions.KindView:
		return m.canView(a.ID)
	}
	return a.Visible()
}

func (m *Model) surface(id string) *exec.Cmd {
	if m.env.Surface == nil {
		return nil
	}
	return m.env.Surface(id)
}

func (m *Model) canView(id string) bool {
	switch {
	case id == "operations":
		return m.env.Operations != nil
	case slices.Contains(views.ForIDs, id):
		// An agent's view needs the picker for its agent.
		return m.env.Agents != nil
	}
	_, ok := views.New(id, m.env.Views)
	return ok
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.helpTop = min(m.helpTop, m.helpMax())
		// Every screen gets the size below the menu's header: the open one
		// and those under it, which come back at this size.
		size := tea.WindowSizeMsg{Width: msg.Width, Height: m.screenHeight()}
		var cmds []tea.Cmd
		for _, s := range append(slices.Clone(m.stack), m.open) {
			if s != nil {
				cmd, _ := s.Update(size)
				cmds = append(cmds, cmd)
			}
		}
		return m, tea.Batch(cmds...)
	case openScreen:
		return m, m.show(msg.s)
	case pushScreen:
		if m.open != nil {
			m.stack = append(m.stack, m.open)
		}
		return m, m.show(msg.s)
	case noteMsg:
		m.open, m.stack, m.note = nil, nil, msg.text
		return m, nil
	case surfaceEnded:
		// The surface gave the terminal back: the menu shows again.
		m.open, m.stack, m.note = nil, nil, ""
		if msg.err != nil {
			m.note = msg.id + ": " + msg.err.Error()
		}
		return m, nil
	}
	if m.open != nil {
		cmd, closed := m.open.Update(msg)
		if closed {
			m.open = nil
			if n := len(m.stack); n > 0 {
				m.open, m.stack = m.stack[n-1], m.stack[:n-1]
			}
		}
		return m, cmd
	}
	if k, ok := msg.(tea.KeyMsg); ok {
		return m, m.key(k)
	}
	return m, nil
}

// show opens a screen at the menu's size.
func (m *Model) show(s screen) tea.Cmd {
	m.open, m.note, m.help = s, "", false
	if s == nil {
		return nil
	}
	size, _ := s.Update(tea.WindowSizeMsg{Width: m.width, Height: m.screenHeight()})
	return tea.Batch(size, s.Init())
}

func (m *Model) key(k tea.KeyMsg) tea.Cmd {
	rows := m.Rows(m.level)
	m.cursor = min(m.cursor, max(0, len(rows)-1))
	switch k.String() {
	case "q", "ctrl+c":
		return tea.Quit
	case "esc", "backspace", "left":
		if m.help {
			m.help = false
			return nil
		}
		if m.level == "" {
			return tea.Quit
		}
		m.cursor, m.level, m.note = m.parentIndex(), "", ""
		return nil
	case "?":
		m.help, m.helpTop = !m.help, 0
		return nil
	case "up":
		if m.help {
			m.helpTop = max(0, min(m.helpTop, m.helpMax())-1)
			return nil
		}
		m.cursor = max(0, m.cursor-1)
		return nil
	case "down":
		if m.help {
			m.helpTop = min(m.helpTop+1, m.helpMax())
			return nil
		}
		m.cursor = max(0, min(len(rows)-1, m.cursor+1))
		return nil
	}
	if m.help {
		// The help hides the rows: a row's key does not open it.
		return nil
	}
	if k.String() == "enter" {
		if len(rows) > 0 {
			return m.run(rows[m.cursor])
		}
		return nil
	}
	for i, a := range rows {
		if a.Menu.Key == k.String() {
			m.cursor = i
			return m.run(a)
		}
	}
	return nil
}

// parentIndex is the cursor of the group the menu came from.
func (m *Model) parentIndex() int {
	return max(0, slices.IndexFunc(m.Rows(""), func(a actions.Action) bool { return a.ID == m.level }))
}

// run opens a row.
func (m *Model) run(a actions.Action) tea.Cmd {
	m.note = ""
	if a.Kind == actions.KindGroup {
		m.level, m.cursor = a.ID, 0
		return nil
	}
	if why := m.unavailable(a); why != "" {
		m.note = a.Label + ": " + why
		return nil
	}
	s, err := m.screenFor(a)
	switch {
	case err != nil:
		m.note = a.Label + ": " + err.Error()
		return nil
	case s == nil:
		return nil
	}
	return m.show(s)
}

func (m *Model) unavailable(a actions.Action) string {
	switch a.Kind {
	case actions.KindSurface:
		if m.surface(a.ID) == nil {
			return "not available here"
		}
		return ""
	case actions.KindView:
		if a.Scope == actions.ScopeWorkspace && m.env.Context.WorkspaceID == "" {
			return "no workspace: open the menu from a workspace"
		}
		return ""
	}
	return a.Unavailable(m.env.Context, m.env.Core)
}

// compactHeight is the popup height under which the menu drops its blank
// lines and shows the breadcrumb on the header line. herdr sizes the
// menu to the popup's inside: 22x4 at a 40x12 client.
const compactHeight = 8

func (m *Model) compact() bool { return m.height < compactHeight }

// chrome is the number of lines the menu shows above an open screen.
func (m *Model) chrome() int {
	if m.compact() {
		return 1
	}
	return 2
}

// screenHeight is the height an open screen gets.
func (m *Model) screenHeight() int { return max(1, m.height-m.chrome()) }

// head is the header line. crumb is the open group's label, which a
// compact menu shows on it.
func (m *Model) head(crumb string) string {
	t := m.theme
	h := lipgloss.NewStyle().Foreground(t.Primary).Bold(true).Render("nhm")
	used := 3
	if crumb != "" {
		crumb = clip("› "+crumb, max(1, m.width-4))
		h += " " + lipgloss.NewStyle().Foreground(t.Overlay).Render(crumb)
		used += 1 + len([]rune(crumb))
	}
	if room := m.width - used - 2; room >= 4 || crumb == "" {
		h += "  " + lipgloss.NewStyle().Foreground(t.Subtext).Render(clip(m.env.Context.Header(), max(4, room)))
	}
	return h
}

// View implements tea.Model.
func (m *Model) View() string {
	if m.open != nil {
		sep := "\n\n"
		if m.compact() {
			sep = "\n"
		}
		return fit(m.head("")+sep+m.open.View(m.width, m.screenHeight()), m.width, m.height)
	}
	top, bottom := m.frame()
	n := m.bodyHeight()
	var body []string
	if m.help {
		lines := m.helpLines()
		start := min(m.helpTop, m.helpMax())
		body = lines[start:min(len(lines), start+n)]
	} else {
		// A list taller than the popup scrolls with the cursor; the header
		// and the keys stay.
		body = window(m.rowLines(), m.cursor, m.cursor, n)
	}
	return fit(strings.Join(append(append(top, body...), bottom...), "\n"), m.width, m.height)
}

// frame is what the menu shows above and below its list or its help. In
// a very short popup the keys and then the header give way, so the
// cursor row always shows.
func (m *Model) frame() (top, bottom []string) {
	t := m.theme
	crumb := ""
	if g, ok := actions.ByID(m.level); ok && m.level != "" {
		crumb = g.Label
	}
	// gap is the blank line between the parts, which a compact menu drops.
	var gap []string
	if !m.compact() {
		gap = []string{""}
	}
	if m.compact() {
		top = []string{m.head(crumb)}
	} else {
		top = []string{m.head("")}
		if crumb != "" {
			top = append(top, lipgloss.NewStyle().Foreground(t.Overlay).Render("› "+crumb))
		}
		top = append(top, gap...)
	}
	if m.note != "" {
		bottom = append(append(bottom, gap...), lipgloss.NewStyle().Foreground(t.Warning).Render(clip(m.note, max(10, m.width-2))))
	}
	keys := "letter or enter: open · ↑/↓ · ?: help · q: close"
	switch {
	case m.help && m.width < 40:
		keys = "↑/↓ more · esc back"
	case m.help:
		keys = "↑/↓: more · esc: back"
	case m.width < 40:
		keys = "? help · q close"
	case m.width < 64:
		keys = "enter · esc back · ? help · q close"
	case m.level != "":
		keys = "letter or enter: open · ↑/↓ · esc: back · ?: help · q: close"
	}
	bottom = append(append(bottom, gap...), lipgloss.NewStyle().Foreground(t.Overlay).Render(clip(keys, max(10, m.width-2))))
	if m.height-len(top)-len(bottom) < 1 {
		bottom = nil
	}
	if m.height-len(top) < 1 {
		top = nil
	}
	return top, bottom
}

// bodyHeight is the number of list or help lines the menu shows.
func (m *Model) bodyHeight() int {
	top, bottom := m.frame()
	return max(1, m.height-len(top)-len(bottom))
}

// helpMax is the last first line the help can show.
func (m *Model) helpMax() int { return max(0, len(m.helpLines())-m.bodyHeight()) }

// helpLines is the help text, wrapped to the popup.
func (m *Model) helpLines() []string {
	wrapped := lipgloss.NewStyle().Width(max(10, m.width-2)).Render(strings.TrimRight(helpText, "\n"))
	return strings.Split(wrapped, "\n")
}

// rowLines are the level's rows. The label column fits the level's
// longest label, so a help text never runs into a label; a popup too
// narrow for the help texts shows the labels alone.
func (m *Model) rowLines() []string {
	t := m.theme
	rows := m.Rows(m.level)
	labels := make([]string, len(rows))
	col := 0
	for i, a := range rows {
		labels[i] = a.Label
		if a.Kind == actions.KindGroup {
			labels[i] += " ›"
		}
		col = max(col, len([]rune(labels[i])))
	}
	width := max(10, m.width-4)
	bare := width < 3+col+2+8
	lines := make([]string, len(rows))
	for i, a := range rows {
		line := fmt.Sprintf("%s  %-*s  %s", a.Menu.Key, col, labels[i], a.Help)
		if bare {
			line = a.Menu.Key + "  " + labels[i]
		}
		style := lipgloss.NewStyle()
		if why := m.unavailable(a); why != "" {
			style = style.Foreground(t.Overlay)
		}
		cursor := "  "
		if i == m.cursor {
			cursor, style = "▶ ", style.Bold(true)
		}
		lines[i] = cursor + style.Render(clip(line, width))
	}
	return lines
}

// window is the part of lines, at most n long, that holds the lines from
// first to last (the cursor's), with last at its bottom once they are
// below the top. When they do not all fit, first stays.
func window(lines []string, first, last, n int) []string {
	n = max(1, n)
	start := min(max(0, last-n+1), first)
	return lines[start:min(len(lines), start+n)]
}

// fit keeps a view within height lines, from the top: what a screen
// shows first (its title, its review) stays, and a cut view of three
// lines or more says so. bubbletea would keep the tail instead.
func fit(s string, width, height int) string {
	lines := strings.Split(s, "\n")
	if height <= 0 || len(lines) <= height {
		return s
	}
	lines = lines[:height]
	if height >= 3 {
		more := "… more below: make the popup taller"
		if width < 36 {
			more = "… more below"
		}
		lines[height-1] = clip(more, max(4, width))
	}
	return strings.Join(lines, "\n")
}

const helpText = `The menu runs every nhm command that has a form or a view.
A letter opens its row; enter opens the row under the cursor.
A group (›) opens its rows; esc goes back, and closes the menu at the top.
A form shows its review first; nothing changes before you confirm.
What a form starts runs on when the menu closes: recent operations (o)
shows how it ended. Closing the menu is not cancelling.
`

// clip removes terminal controls from s (forms.Clean) and shortens it to
// width columns. Every line of outside text the menu shows (labels,
// statuses, reasons, errors) passes it.
func clip(s string, width int) string {
	r := []rune(forms.Clean(s))
	if len(r) <= width {
		return string(r)
	}
	if width <= 1 {
		return string(r[:max(0, width)])
	}
	return string(r[:width-1]) + "…"
}
