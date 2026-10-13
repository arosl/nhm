package views

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/arosl/nhm/internal/core/ensemble"
	"github.com/arosl/nhm/internal/ui/tui/components"
	"github.com/arosl/nhm/internal/ui/tui/theme"
)

// modes is nhm modes list and explain: ntm's catalog, the core tier
// first (a: every tier); enter explains the selected mode with ntm's card.
type modes struct {
	env    Env
	cat    *ensemble.Catalog
	err    error
	all    bool
	rows   []ensemble.ModesListRow
	list   list
	detail *page
	width  int
	high   int
}

func newModes(env Env) *modes {
	m := &modes{env: env, width: 80, high: 24}
	m.cat, m.err = ensemble.Builtin()
	m.filter()
	return m
}

func (m *modes) Title() string { return "modes" }

func (m *modes) Init() tea.Cmd { return nil }

// filter lists the core tier, or every tier, and keeps the cursor on the
// mode it was on.
func (m *modes) filter() {
	if m.cat == nil {
		m.rows = nil
		return
	}
	was := ""
	if m.list.cursor < len(m.rows) {
		was = m.rows[m.list.cursor].ID
	}
	m.rows = ensemble.ModesList(m.cat, "", "", m.all, m.env.now()).Modes
	m.list.cursor = 0
	for i, r := range m.rows {
		if r.ID == was {
			m.list.cursor = i
		}
	}
	m.list.fit(len(m.rows), m.listHeight())
}

// listHeight is the rows in sight: the frame takes three lines, the
// column header one, and the selected mode's description two.
func (m *modes) listHeight() int { return max(1, m.high-6) }

func (m *modes) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.high = msg.Width, msg.Height
		m.list.fit(len(m.rows), m.listHeight())
		if m.detail != nil {
			m.detail.Update(msg)
		}
	case loaded:
		if m.detail != nil {
			_, cmd := m.detail.Update(msg)
			return m, cmd
		}
	case tea.KeyMsg:
		if m.detail != nil {
			if isBack(msg) {
				m.detail = nil
				return m, nil
			}
			_, cmd := m.detail.Update(msg)
			return m, cmd
		}
		switch k := msg.String(); {
		case isBack(msg):
			return m, backCmd
		case k == "enter" && m.list.cursor < len(m.rows):
			return m, m.explain(m.rows[m.list.cursor])
		case k == "a":
			m.all = !m.all
			m.filter()
		default:
			m.list.key(k, len(m.rows), m.listHeight())
		}
	}
	return m, nil
}

// explain opens ntm's card of a mode.
func (m *modes) explain(row ensemble.ModesListRow) tea.Cmd {
	cat := m.cat
	m.detail = newPage(row.Name, m.env, func(context.Context) (string, error) {
		card, err := cat.ModeCard(row.ID)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		err = ensemble.RenderModesExplain(&b, ensemble.ModesExplainOutput{Card: card}, "text")
		return clean(b.String()), err
	})
	m.detail.Update(tea.WindowSizeMsg{Width: m.width, Height: m.high})
	return m.detail.Init()
}

func (m *modes) View() string {
	if m.detail != nil {
		return m.detail.View()
	}
	t := theme.Current()
	dim := lipgloss.NewStyle().Foreground(t.Overlay)
	tier := "core tier"
	if m.all {
		tier = "every tier"
	}
	note := dim.Render(fmt.Sprintf("%s, %d modes", tier, len(m.rows)))
	var body string
	switch {
	case m.err != nil:
		body = lipgloss.NewStyle().Foreground(t.Error).Render(clean(m.err.Error()))
	case len(m.rows) == 0:
		body = dim.Render("no modes")
	default:
		rows := make([]string, len(m.rows))
		for i, r := range m.rows {
			rows[i] = modeRow(r.Code, r.Name, r.Category, r.Tier)
		}
		desc := clean(m.rows[m.list.cursor].ShortDesc)
		body = strings.Join([]string{
			dim.Render(ansi.Truncate("  "+modeRow("CODE", "NAME", "CATEGORY", "TIER"), m.width, "…")),
			m.list.render(rows, m.width, m.listHeight()),
			"",
			dim.Render(ansi.Truncate(desc, m.width, "…")),
		}, "\n")
	}
	return frame("modes", note, body, []components.KeyHint{
		{Key: "↑/↓", Desc: "move"}, {Key: "enter", Desc: "explain"}, {Key: "a", Desc: "all tiers"}, {Key: "esc", Desc: "back"},
	}, m.width)
}

func modeRow(code, name, category, tier string) string {
	return fmt.Sprintf("%-5s %-30s %-14s %s", clean(code), clean(name), clean(category), clean(tier))
}
