package dashboard

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/guards"
	"github.com/arosl/nhm/internal/ui/board"
)

// Layout bounds.
const (
	minWidth       = 40
	minHeight      = 10
	previewMinRows = 6
)

// View renders the board.
func (m *Model) View() string {
	w, h := max(m.width, minWidth), max(m.height, minHeight)
	if m.view == viewRead {
		return m.readView(w, h)
	}
	main := m.mainView(w, h)
	var modal string
	switch m.view {
	case viewHelp:
		modal = m.box("Keys", m.helpBody(w-6), w)
	case viewInbox:
		modal = m.box("Inbox of "+m.inbox.name+" (a peek: nothing is marked read)", m.inboxBody(w), w)
	case viewDiagnostics:
		modal = m.box("Diagnostics", m.diagnosticsBody(), w)
	case viewUsage:
		modal = m.box("Usage", m.usageBody(), w)
	case viewActions:
		modal = m.box("Actions for "+m.actionTargets(), m.actionsBody(), w)
	case viewForm:
		if m.form != nil {
			modal = m.box(m.form.Action().Label, m.form.View(min(w-10, 96)), w)
		}
	case viewSend:
		modal = m.box("send", m.sendView(w), w)
	case viewWizard:
		// The wizard draws its own box (ntm's).
		if m.wizard != nil {
			modal = m.wizard.View()
		}
	default:
		return main
	}
	return renderModalOverlay(main, modal, w, h, m.theme)
}

// box frames a modal's body with its title: as wide as they are, within
// the board's width.
func (m *Model) box(title, body string, w int) string {
	inner := min(max(lipgloss.Width(body), lipgloss.Width(title), 20), w-6, 100)
	style := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(m.theme.Primary).Padding(0, 1).Width(inner + 2)
	head := lipgloss.NewStyle().Bold(true).Foreground(m.theme.Primary).Render(ansi.Truncate(title, inner, "…"))
	foot := lipgloss.NewStyle().Foreground(m.theme.Overlay).Render("esc close")
	return style.Render(head + "\n\n" + body + "\n\n" + foot)
}

// helpBody is the full help: its groups side by side when they fit in
// width, else one under the other.
func (m *Model) helpBody(width int) string {
	full := m.help
	full.Width = 0 // the box sizes itself; help must not cut its columns
	groups := m.keys.FullHelp()
	if side := full.FullHelpView(groups); lipgloss.Width(side) <= width {
		return side
	}
	var out []string
	for _, g := range groups {
		if v := full.FullHelpView([][]key.Binding{g}); strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return strings.Join(out, "\n\n")
}

// mainView is the header, the agent table with the preview (and, on a
// wide board, the side panels beside them) and the footer.
func (m *Model) mainView(w, h int) string {
	header := m.header(w)
	footer := m.footer(w)
	rest := max(h-lipgloss.Height(header)-lipgloss.Height(footer), 1)
	leftW := w
	var side string
	if slices.Contains(m.visiblePanels(), PanelAttention) {
		leftW = max(w*3/5, minWidth)
		side = m.sidePanels(w-leftW-1, rest)
	}
	left := m.agentColumn(leftW, rest)
	if side != "" {
		left = lipgloss.NewStyle().Width(leftW).MaxWidth(leftW).Render(left)
		left = lipgloss.JoinHorizontal(lipgloss.Top, left, " ", side)
	}
	body := lipgloss.JoinVertical(lipgloss.Left, header, left)
	body = lipgloss.NewStyle().Height(h - lipgloss.Height(footer)).MaxHeight(h - lipgloss.Height(footer)).Render(body)
	return lipgloss.JoinVertical(lipgloss.Left, body, footer)
}

// agentColumn is the agent table and, when there is room, the preview.
func (m *Model) agentColumn(w, rest int) string {
	tableRows := len(m.b.Sel.Rows()) + 1
	previewRows := 0
	if rest-tableRows >= previewMinRows+2 {
		previewRows = rest - tableRows
	} else if rest >= 2*previewMinRows+2 {
		previewRows = previewMinRows + 2
	}
	parts := []string{m.table(w, rest-previewRows)}
	if previewRows > 0 {
		parts = append(parts, m.preview(w, previewRows))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// sidePanels are the panels beside the agent table: attention, and the
// history under it when the board is tall enough.
func (m *Model) sidePanels(w, h int) string {
	if !slices.Contains(m.visiblePanels(), PanelHistory) {
		m.attention.SetSize(w, h)
		return m.attention.View()
	}
	top := h * 45 / 100
	m.attention.SetSize(w, top)
	m.history.SetSize(w, h-top)
	return lipgloss.JoinVertical(lipgloss.Left, m.attention.View(), m.history.View())
}

// tableHeight is how many rows a page holds.
func (m *Model) tableHeight() int { return max(m.height-8, 1) }

// countAgents counts the rows that hold an agent (not an absent binding).
func countAgents(rows []board.Row) int {
	n := 0
	for _, r := range rows {
		if r.Agent != nil {
			n++
		}
	}
	return n
}

// header names what the board acts on, its counts, and what is not fresh.
func (m *Model) header(w int) string {
	c := m.cfg.Context
	ws := c.WorkspaceLabel
	if ws == "" {
		ws = c.WorkspaceID
	} else if c.WorkspaceID != "" {
		ws += " (" + c.WorkspaceID + ")"
	}
	parts := []string{"nhm board", "workspace " + board.Clean(ws)}
	// Unknown is never shown as zero: "?" until herdr's state answers.
	switch shown, all := countAgents(m.b.Sel.Rows()), countAgents(m.b.Rows()); {
	case m.b.Model() == nil:
		parts = append(parts, "? agents")
	case shown != all:
		parts = append(parts, fmt.Sprintf("%d of %d agents", shown, all))
	default:
		parts = append(parts, fmt.Sprintf("%d agents", all))
	}
	if n := len(m.b.Sel.Marks()); n > 0 {
		parts = append(parts, fmt.Sprintf("%d marked", n))
	}
	if f := m.b.Sel.Filter(); f != "" || m.filtering {
		text := f
		if m.filtering {
			text = m.filterText + "▏"
		}
		parts = append(parts, "filter: "+text)
	}
	if m.b.Paused() {
		parts = append(parts, "paused")
	}
	if !slices.Contains(m.visiblePanels(), PanelAttention) {
		// No room for the attention panel: its counts, in the header.
		if action, interesting := attentionCounts(m.attnItems); action+interesting > 0 {
			parts = append(parts, fmt.Sprintf("attention: %d action, %d interesting", action, interesting))
		}
	}
	title := lipgloss.NewStyle().Bold(true).Foreground(m.theme.Primary).Render(ansi.Truncate(strings.Join(parts, " · "), w, "…"))
	lines := []string{title}
	warn := lipgloss.NewStyle().Foreground(m.theme.Warning)
	for _, p := range m.b.Problems(m.cfg.Now()) {
		lines = append(lines, warn.Render(ansi.Truncate("not fresh: "+board.Clean(p), w, "…")))
	}
	switch err := m.frozen; {
	case errors.Is(err, board.ErrUpgraded):
		lines = append(lines, lipgloss.NewStyle().Foreground(m.theme.Error).Bold(true).Render(
			ansi.Truncate("nhm was upgraded on disk: this board takes no new actions; reopen it (prefix+d)", w, "…")))
	case err != nil:
		lines = append(lines, lipgloss.NewStyle().Foreground(m.theme.Error).Bold(true).Render(
			ansi.Truncate(board.Clean(err.Error()), w, "…")))
	}
	if m.b.Model() == nil && len(m.b.Problems(m.cfg.Now())) == 0 {
		lines = append(lines, lipgloss.NewStyle().Foreground(m.theme.Overlay).Render("reading…"))
	}
	return strings.Join(lines, "\n")
}

// footer is the status message and the short help.
func (m *Model) footer(w int) string {
	var lines []string
	if m.status != "" {
		st := lipgloss.NewStyle().Foreground(m.theme.Subtext)
		if m.statusErr {
			st = st.Foreground(m.theme.Error)
		}
		lines = append(lines, st.Render(ansi.Truncate(m.status, w, "…")))
	}
	m.help.Width = w
	lines = append(lines, m.help.ShortHelpView(m.keys.ShortHelp()))
	return strings.Join(lines, "\n")
}

// Column widths of the agent table.
const (
	colMark  = 2
	colSlot  = 5
	colName  = 16
	colKind  = 8
	colState = 10
	colMail  = 8
	colBead  = 14
)

// table renders the agent table within height lines, the cursor's row
// highlighted and kept in view.
func (m *Model) table(w, height int) string {
	rows := m.b.Sel.Rows()
	headColor := m.theme.Lavender
	if len(m.visiblePanels()) > 1 {
		// Panels share the screen: the table's header shows its focus.
		headColor = m.focusBorderColor(m.theme.Overlay, PanelPaneList)
	}
	head := lipgloss.NewStyle().Bold(true).Foreground(headColor)
	out := []string{head.Render(ansi.Truncate(cells(w, "", "slot", "name", "kind", "state", "mail", "bead", "notes"), w, "…"))}
	if len(rows) == 0 {
		msg := "no agents in this workspace"
		if m.b.Sel.Filter() != "" {
			msg = "no agent matches the filter"
		}
		if m.b.Model() == nil {
			msg = "no agent state yet"
		}
		return strings.Join(append(out, lipgloss.NewStyle().Foreground(m.theme.Overlay).Render(msg)), "\n")
	}
	visible := max(height-1, 1)
	more := len(rows) > visible
	if more {
		visible = max(visible-1, 1) // a line for "… N of M rows"
	}
	cur := m.b.Sel.Index()
	start := 0
	if cur >= visible {
		start = cur - visible + 1
	}
	end := min(start+visible, len(rows))
	for i := start; i < end; i++ {
		r := rows[i]
		line := ansi.Truncate(m.rowText(r, w), w, "…")
		st := lipgloss.NewStyle().Foreground(m.theme.Subtext)
		switch {
		case r.Absent != nil:
			st = st.Foreground(m.theme.Overlay)
		case r.Agent.State == herdr.StatusBlocked:
			st = st.Foreground(m.theme.Red)
		}
		if i == cur {
			st = st.Background(m.theme.Surface0).Foreground(m.theme.Text)
			line += strings.Repeat(" ", max(w-lipgloss.Width(line), 0))
		}
		out = append(out, st.Render(line))
	}
	if more {
		out = append(out, lipgloss.NewStyle().Foreground(m.theme.Overlay).Render(
			fmt.Sprintf("… rows %d to %d of %d", start+1, end, len(rows))))
	}
	return strings.Join(out, "\n")
}

// rowText is one row's cells.
func (m *Model) rowText(r board.Row, w int) string {
	mark := ""
	if m.b.Sel.Marked(r.Key) {
		mark = "✓"
	}
	slot := r.Slot()
	if slot == "" {
		slot = "-"
	}
	name := board.Clean(r.Name)
	if name == "" {
		name = "(unbound)"
	}
	if r.Absent != nil {
		return cells(w, mark, slot, name, r.Kind(), "absent", "", "", "its pane holds no agent")
	}
	a := r.Agent
	return cells(w, mark, slot, name, board.Clean(a.Kind), stateText(a.State), mailCell(r), beadCell(r), notes(a))
}

// cells lays out the table's columns; the last takes what is left.
func cells(w int, mark, slot, name, kind, state, mail, bead, notes string) string {
	fixed := []struct {
		s string
		n int
	}{{mark, colMark}, {slot, colSlot}, {name, colName}, {kind, colKind}, {state, colState}, {mail, colMail}, {bead, colBead}}
	var b strings.Builder
	for _, c := range fixed {
		cell := ansi.Truncate(c.s, c.n-1, "…")
		b.WriteString(cell + strings.Repeat(" ", max(c.n-lipgloss.Width(cell), 0)))
	}
	b.WriteString(notes)
	return b.String()
}

func stateText(s herdr.AgentStatus) string {
	switch s {
	case herdr.StatusWorking:
		return "● working"
	case herdr.StatusBlocked:
		return "▲ blocked"
	case herdr.StatusDone:
		return "✓ done"
	case herdr.StatusIdle:
		return "○ idle"
	default:
		return "? " + board.Clean(string(s))
	}
}

// mailCell is the row's unread count: "?" while the mail source has not
// answered for it, never 0 in its place.
func mailCell(r board.Row) string {
	if r.Agent == nil || r.Agent.Binding == nil {
		return ""
	}
	if !r.Mail.Known {
		return "?"
	}
	s := r.Mail.Value
	if s.Unread == 0 {
		return "-"
	}
	out := fmt.Sprintf("✉ %d", s.Unread)
	if s.Urgent > 0 {
		out += "!"
	}
	return out
}

// beadCell is the row's first bead in progress, "?" while unknown.
func beadCell(r board.Row) string {
	if r.Agent == nil || r.Agent.Binding == nil {
		return ""
	}
	if !r.Beads.Known {
		return "?"
	}
	switch len(r.Beads.Value) {
	case 0:
		return "-"
	case 1:
		return board.Clean(r.Beads.Value[0])
	default:
		return fmt.Sprintf("%s +%d", board.Clean(r.Beads.Value[0]), len(r.Beads.Value)-1)
	}
}

// notes are the row's warnings: a limit on screen, missing guards, a
// herdr name that is not the binding's alias.
func notes(a *snapshot.Agent) string {
	var out []string
	if a.Limit != "" {
		out = append(out, "at limit")
	}
	var bad []string
	for _, g := range guards.Guards {
		switch a.Guards[g] {
		case guards.Missing, guards.Untrusted, guards.Stale:
			bad = append(bad, g)
		}
	}
	if len(bad) > 0 {
		out = append(out, "unguarded: "+strings.Join(bad, ","))
	}
	if a.Binding != nil && !a.Binding.NameMatches {
		out = append(out, "herdr name differs")
	}
	if a.Binding != nil && a.Mail != nil && a.Mail.OverdueAcks > 0 {
		out = append(out, fmt.Sprintf("%d acks overdue", a.Mail.OverdueAcks))
	}
	return strings.Join(out, "; ")
}

// preview is the cursor's pane, its last lines, within height lines.
func (m *Model) preview(w, height int) string {
	title := lipgloss.NewStyle().Foreground(m.theme.Overlay)
	r, ok := m.b.Sel.Cursor()
	if !ok || r.Agent == nil {
		return title.Render(strings.Repeat("─", w))
	}
	p, ok := m.b.Preview()
	name := board.Clean(rowName(r))
	if !strings.Contains(name, r.Agent.PaneID) {
		name += " · " + r.Agent.PaneID
	}
	head := title.Render(ansi.Truncate("── "+name+" "+strings.Repeat("─", w), w, ""))
	if !ok {
		return head + "\n" + title.Render("reading the pane…")
	}
	if strings.TrimSpace(p.Text) == "" {
		return head + "\n" + title.Render("(the pane shows nothing)")
	}
	lines := strings.Split(p.Text, "\n")
	room := max(height-1, 1)
	if len(lines) > room {
		lines = lines[len(lines)-room:]
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, w, "…")
	}
	return head + "\n" + strings.Join(lines, "\n")
}

// actionTargets names what the action menu acts on: its frozen targets.
func (m *Model) actionTargets() string {
	tg := m.menuTargets
	switch {
	case tg.From == board.FromNone:
		return "no agent"
	case tg.From == board.FromCursor && len(tg.Rows) == 1:
		return board.Clean(rowName(tg.Rows[0]))
	}
	s := fmt.Sprintf("%d marked agents", len(tg.Rows)+len(tg.Invalid))
	if len(tg.Invalid) > 0 {
		s += fmt.Sprintf(" (%d no longer valid)", len(tg.Invalid))
	}
	return s
}

// actionsBody lists the action menu: each action's letter, and why it
// cannot run here when it cannot.
func (m *Model) actionsBody() string {
	var lines []string
	dim := lipgloss.NewStyle().Foreground(m.theme.Overlay)
	for _, a := range m.menu {
		line := fmt.Sprintf("%s  %-10s %s", a.Board.Space, a.Label, a.Help)
		// The read view is the board's own, and the palette a surface it
		// opens: neither is a handler of the table.
		if why := a.Unavailable(m.cfg.Context, m.cfg.Core); why != "" && a.ID != readActionID && a.ID != paletteActionID {
			line = dim.Render(line + " — " + why)
		}
		lines = append(lines, line)
	}
	for _, mk := range m.menuTargets.Invalid {
		lines = append(lines, lipgloss.NewStyle().Foreground(m.theme.Error).Render(
			"marked "+board.Clean(mk.Name)+": "+mk.Reason+" (esc on the board clears the marks)"))
	}
	return strings.Join(lines, "\n")
}

// inboxBody lists the agent's unread mail, without bodies.
func (m *Model) inboxBody(w int) string {
	in := m.inbox
	switch {
	case in.loading:
		return "reading the inbox…"
	case in.err != nil:
		return lipgloss.NewStyle().Foreground(m.theme.Error).Render("Agent Mail: " + board.Clean(in.err.Error()))
	case len(in.msgs) == 0:
		return "no unread mail"
	}
	width := min(w-10, 96)
	var lines []string
	for _, msg := range in.msgs {
		flag := " "
		if msg.Importance == "urgent" || msg.Importance == "high" {
			flag = "!"
		}
		ack := "   "
		if msg.AckRequired && msg.AckTS == "" {
			ack = "ack"
		}
		line := fmt.Sprintf("%s %s %-14s %s", flag, ack, ansi.Truncate(board.Clean(msg.From), 14, "…"), board.Clean(msg.Subject))
		lines = append(lines, ansi.Truncate(line, width, "…"))
	}
	return strings.Join(lines, "\n")
}

// diagnosticsBody is each source's state: the board's feeds, then the
// snapshot's own sources.
func (m *Model) diagnosticsBody() string {
	now := m.cfg.Now()
	var lines []string
	lines = append(lines, "board sources:")
	for _, h := range m.b.Health() {
		state := "fresh"
		switch {
		case h.Overdue:
			state = "overdue"
		case h.Err != "":
			state = "failing"
		case !h.Has:
			state = "no value yet"
		}
		if h.Reading && !h.Overdue {
			state += ", reading"
		}
		if h.Paused {
			state += ", paused"
		}
		age := "-"
		if h.Has {
			age = now.Sub(h.At).Round(time.Second).String() + " old"
		}
		line := fmt.Sprintf("  %-8s %-22s %-10s reads %d", h.Source, state, age, h.Reads)
		if h.Err != "" {
			line += "  " + board.Clean(h.Err)
		}
		lines = append(lines, line)
	}
	if model := m.b.Model(); model != nil {
		lines = append(lines, "", "snapshot sources (last agent state):")
		names := make([]string, 0, len(model.Sources))
		for n := range model.Sources {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			s := model.Sources[n]
			line := fmt.Sprintf("  %-10s %-11s %4d ms", n, s.Status, s.DurationMs)
			if s.Error != "" {
				line += "  " + board.Clean(s.Error)
			}
			lines = append(lines, line)
		}
		if model.HerdrVersion != "" {
			lines = append(lines, "", fmt.Sprintf("herdr %s, protocol %d", board.Clean(model.HerdrVersion), model.Protocol))
		}
	}
	return strings.Join(lines, "\n")
}

// usageBody is the quota table and the agents at a limit. An unknown
// value shows as unknown, never as 0.
func (m *Model) usageBody() string {
	model := m.b.Model()
	if model == nil {
		return "no state read yet"
	}
	var lines []string
	if q, ok := model.Sources[snapshot.SourceQuota]; ok && q.Status != snapshot.Fresh {
		lines = append(lines, "quota: "+q.Status+" "+board.Clean(q.Error))
	}
	if len(model.Quota) == 0 {
		lines = append(lines, "no quota observation yet (nhm quota records one)")
	}
	for _, q := range model.Quota {
		used := "unknown"
		if q.UsedPct != nil {
			used = fmt.Sprintf("%3.0f%% %s", *q.UsedPct, bar(*q.UsedPct, 10))
		}
		line := fmt.Sprintf("  %-10s %-8s %-16s", board.Clean(q.PoolID), board.Clean(q.Window), used)
		if q.Limited {
			line += " LIMITED"
		}
		if q.ResetsAt != "" {
			line += " resets " + board.Clean(q.ResetsAt)
		}
		if q.Stale {
			line += fmt.Sprintf(" (stale, %s old)", m.cfg.Now().Sub(q.ObservedAt).Round(time.Minute))
		}
		lines = append(lines, line)
	}
	var limited []string
	for _, a := range model.Agents {
		if a.Limit != "" {
			name := a.HerdrName
			if a.Binding != nil {
				name = a.Binding.MailName
			}
			limited = append(limited, fmt.Sprintf("  %-16s %s", board.Clean(name), board.Clean(a.Limit)))
		}
	}
	if len(limited) > 0 {
		lines = append(lines, "", "at a limit (what nhm limits --dry-run saw on screen):")
		lines = append(lines, limited...)
	}
	return strings.Join(lines, "\n")
}

// bar is a used-percent bar of n cells.
func bar(pct float64, n int) string {
	full := int(pct/100*float64(n) + 0.5)
	full = min(max(full, 0), n)
	return strings.Repeat("█", full) + strings.Repeat("░", n-full)
}
