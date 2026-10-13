package palette

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/arosl/nhm/internal/ui"
)

// handed is a selection another surface handed on with the palette (the
// board's marks, frozen there: plan-v4 sections 2 and 4). The palette
// sends to exactly it, at the generations it was frozen at, and never
// widens it.
type handed struct {
	// by is the surface that picked ("board").
	by      string
	targets []ui.Target
}

func (h handed) present() bool { return len(h.targets) > 0 }

// surface names the picking surface for the screen.
func (h handed) surface() string {
	if h.by == "" {
		return "the surface that opened the palette"
	}
	return "the " + clean(h.by)
}

// handedTargets is the handed selection as send targets: the bound ones,
// named from the last read of the panes. unbound counts the others,
// which get no prompt.
func (m Model) handedTargets() (ts Targets, unbound int) {
	names := map[string]string{}
	for _, p := range m.panes {
		names[p.BindingID] = p.Title
	}
	for _, t := range m.handed.targets {
		if !t.Bound() {
			unbound++
			continue
		}
		ts = append(ts, FrozenTarget{Target: t, MailName: names[t.BindingID]})
	}
	return ts, unbound
}

// handedNotice says why a key that picks other agents does nothing.
func (m Model) handedNotice() string {
	return strings.ToUpper(m.handed.surface()[:1]) + m.handed.surface()[1:] +
		" picked the agents: 1 sends to them. Open the palette with prefix+a to pick others."
}

// renderHanded lists the handed agents on the target screen.
func (m Model) renderHanded() string {
	if !m.handed.present() {
		return ""
	}
	t := m.theme
	ts, unbound := m.handedTargets()
	names := make([]string, 0, len(ts))
	for _, ft := range ts {
		name := clean(ft.MailName)
		if name == "" {
			name = ft.BindingID
		}
		names = append(names, name)
	}
	dim := lipgloss.NewStyle().Foreground(t.Subtext)
	var b strings.Builder
	b.WriteString("  " + dim.Render("To: "+strings.Join(names, ", ")) + "\n")
	if unbound > 0 {
		b.WriteString("  " + dim.Render(fmt.Sprintf("%d picked agent(s) have no binding and get no prompt (nhm adopt binds one).",
			unbound)) + "\n")
	}
	b.WriteString("\n")
	return b.String()
}
