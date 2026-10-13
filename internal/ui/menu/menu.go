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
	open   screen
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
		if m.open != nil {
			// The open screen gets the size below the menu's header.
			cmd, _ := m.open.Update(tea.WindowSizeMsg{Width: msg.Width, Height: msg.Height - 2})
			return m, cmd
		}
		return m, nil
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
	size, _ := s.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height - 2})
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
		m.help = !m.help
		return nil
	case "up":
		m.cursor = max(0, m.cursor-1)
		return nil
	case "down":
		m.cursor = min(len(rows)-1, m.cursor+1)
		return nil
	case "enter":
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

// View implements tea.Model.
func (m *Model) View() string {
	t := m.theme
	head := lipgloss.NewStyle().Foreground(t.Primary).Bold(true).Render("nhm") + "  " +
		lipgloss.NewStyle().Foreground(t.Subtext).Render(clip(m.env.Context.Header(), max(10, m.width-6)))
	if m.open != nil {
		return head + "\n\n" + m.open.View(m.width, m.height-2)
	}
	var b strings.Builder
	b.WriteString(head + "\n")
	if m.level != "" {
		if g, ok := actions.ByID(m.level); ok {
			b.WriteString(lipgloss.NewStyle().Foreground(t.Overlay).Render("› "+g.Label) + "\n")
		}
	}
	b.WriteString("\n")
	if m.help {
		b.WriteString(helpText)
	} else {
		rows := m.Rows(m.level)
		for i, a := range rows {
			label := a.Label
			if a.Kind == actions.KindGroup {
				label += " ›"
			}
			line := fmt.Sprintf("%s  %-18s %s", a.Menu.Key, label, a.Help)
			style := lipgloss.NewStyle()
			if why := m.unavailable(a); why != "" {
				style = style.Foreground(t.Overlay)
			}
			cursor := "  "
			if i == m.cursor {
				cursor, style = "▶ ", style.Bold(true)
			}
			b.WriteString(cursor + style.Render(clip(line, max(10, m.width-4))) + "\n")
		}
	}
	if m.note != "" {
		b.WriteString("\n" + lipgloss.NewStyle().Foreground(t.Warning).Render(clip(m.note, max(10, m.width-2))) + "\n")
	}
	keys := "letter or enter: open · ↑/↓ · ?: help · q: close"
	if m.level != "" {
		keys = "letter or enter: open · ↑/↓ · esc: back · ?: help · q: close"
	}
	b.WriteString("\n" + lipgloss.NewStyle().Foreground(t.Overlay).Render(keys))
	return b.String()
}

const helpText = `The menu runs every nhm command that has a form or a view.
A letter opens its row; enter opens the row under the cursor.
A group (›) opens its rows; esc goes back, and closes the menu at the top.
A form shows its review first; nothing changes before you confirm.
What a form starts runs on when the menu closes: recent operations (o)
shows how it ended. Closing the menu is not cancelling.
`

// clip shortens s to width columns.
func clip(s string, width int) string {
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width <= 1 {
		return string(r[:max(0, width)])
	}
	return string(r[:width-1]) + "…"
}
