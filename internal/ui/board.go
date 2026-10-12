package ui

import (
	"fmt"
	"strings"

	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/guards"
)

// BoardKeys are the board's keys, for fzf's --expect, with what each does.
var BoardKeys = []struct{ Key, What string }{
	{"enter", "read"},
	{"ctrl-p", "palette"},
	{"ctrl-x", "interrupt"},
	{"ctrl-t", "attention"},
	{"ctrl-r", "refresh"},
}

// BoardHeader is the board's header: what it acts on, and its keys.
func BoardHeader(c Context, m *snapshot.Model) string {
	keys := make([]string, 0, len(BoardKeys)+1)
	for _, k := range BoardKeys {
		keys = append(keys, k.Key+" "+k.What)
	}
	keys = append(keys, "esc close")
	head := c.Header()
	if deg := m.Degraded(); len(deg) > 0 {
		head += "  \x1b[33m(not fresh: " + strings.Join(deg, ", ") + ")\x1b[0m"
	}
	return head + "\n" + strings.Join(keys, " · ")
}

// BoardRow is one agent's line. The first fields are hidden ids that
// the key actions read: pane id, binding id, mail name ("-" for none).
type BoardRow struct {
	PaneID    string
	BindingID string
	MailName  string
	Text      string
}

// Line is the row as fzf reads it: the hidden ids, then the text, split
// by tabs.
func (r BoardRow) Line() string {
	return strings.Join([]string{r.PaneID, dashIf(r.BindingID), dashIf(r.MailName), r.Text}, "\t")
}

// ParseBoardLine reads back a line fzf printed.
func ParseBoardLine(line string) (BoardRow, bool) {
	f := strings.SplitN(line, "\t", 4)
	if len(f) < 4 || f[0] == "" {
		return BoardRow{}, false
	}
	return BoardRow{PaneID: f[0], BindingID: undash(f[1]), MailName: undash(f[2]), Text: f[3]}, true
}

// BoardRows are the board's lines: blocked agents first, then the rest by
// slot, then the bindings whose pane holds no agent.
func BoardRows(m *snapshot.Model) []BoardRow {
	var rows, blocked []BoardRow
	for _, a := range m.Agents {
		name, bid, mail := firstNonEmpty(a.HerdrName, "(unbound)"), "", ""
		if a.Binding != nil {
			name, bid, mail = a.Binding.MailName, a.Binding.ID, a.Binding.MailName
		}
		mailText := ""
		if a.Mail != nil && a.Mail.Status == snapshot.Fresh && a.Mail.Unread > 0 {
			mailText = fmt.Sprintf("  \x1b[36m✉ %d\x1b[0m", a.Mail.Unread)
			if a.Mail.PendingAcks > 0 {
				mailText += fmt.Sprintf(" \x1b[36m(%d ack)\x1b[0m", a.Mail.PendingAcks)
			}
		}
		extra := ""
		if a.Limit != "" {
			extra += "  \x1b[31mat limit\x1b[0m"
		}
		if g := unguarded(a.Guards); g != "" {
			extra += "  \x1b[33munguarded: " + g + "\x1b[0m"
		}
		text := fmt.Sprintf("%-5s %s  \x1b[1m%-14s\x1b[0m %-7s%s%s", firstNonEmpty(a.Slot, "-"), stateIcon(a.State), name, a.Kind, mailText, extra)
		row := BoardRow{PaneID: a.PaneID, BindingID: bid, MailName: mail, Text: text}
		if a.State == herdr.StatusBlocked {
			blocked = append(blocked, row)
		} else {
			rows = append(rows, row)
		}
	}
	rows = append(blocked, rows...)
	for _, b := range m.Absent {
		rows = append(rows, BoardRow{PaneID: firstNonEmpty(b.PaneID, "-"), BindingID: b.ID, MailName: b.MailName,
			Text: fmt.Sprintf("%-5s \x1b[2m- absent \x1b[0m \x1b[1m%-14s\x1b[0m %-7s  \x1b[2mits pane holds no agent\x1b[0m", firstNonEmpty(b.Slot, "-"), b.MailName, b.Kind)})
	}
	return rows
}

func stateIcon(s herdr.AgentStatus) string {
	switch s {
	case herdr.StatusWorking:
		return "\x1b[33m● working\x1b[0m"
	case herdr.StatusBlocked:
		return "\x1b[31m▲ blocked\x1b[0m"
	case herdr.StatusDone:
		return "\x1b[32m✓ done   \x1b[0m"
	case herdr.StatusIdle:
		return "\x1b[2m○ idle   \x1b[0m"
	default:
		return "\x1b[2m? " + fmt.Sprintf("%-7s", string(s)) + "\x1b[0m"
	}
}

func unguarded(states map[string]guards.State) string {
	var bad []string
	for _, g := range guards.Guards {
		switch states[g] {
		case guards.Missing, guards.Untrusted, guards.Stale:
			bad = append(bad, g)
		}
	}
	return strings.Join(bad, ",")
}

// MenuItem is one line of the nhm menu: what it says and the nhm verb
// (arguments) it runs. Popup marks a verb that takes over the terminal
// (a popup of its own); the others run and show their output.
type MenuItem struct {
	Label string
	Args  []string
	Popup bool
}

// MenuItems are the menu's lines for a captured context.
func MenuItems(c Context) []MenuItem {
	ws := firstNonEmpty(c.WorkspaceID)
	scoped := func(args ...string) []string {
		if ws == "" {
			return args
		}
		return append(args[:1:1], append([]string{ws}, args[1:]...)...)
	}
	return []MenuItem{
		{Label: "palette     send a prompt from the command palette", Args: []string{"ui", "palette-popup"}, Popup: true},
		{Label: "board       the agents of this workspace, with keys", Args: []string{"ui", "board-popup"}, Popup: true},
		{Label: "status      one row per agent", Args: scoped("status")},
		{Label: "attention   what needs a decision", Args: scoped("attention")},
		{Label: "quota       usage windows per pool", Args: []string{"quota"}},
		{Label: "limits      agents stopped at a limit (dry run)", Args: scoped("limits", "--dry-run")},
		{Label: "doctor      check what nhm needs", Args: []string{"doctor"}},
		{Label: "capabilities what this nhm answers, and why not", Args: []string{"capabilities"}},
	}
}

func dashIf(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func undash(s string) string {
	if s == "-" {
		return ""
	}
	return s
}
