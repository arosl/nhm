// Taken from ntm (github.com/Dicklesworthstone/ntm) internal/tui/dashboard/keymap.go
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: this header; the import path. Behaviour: the keys with
// no nhm action are gone, so the board shows no dead keys (plan-v4
// section 2): c, ctrl+s, e, u, ctrl+k, ctrl+x, n, w, v, o, a. nhm's
// board keys are added: space (the action menu), x (mark), / (filter)
// and esc (back), with their own help group.

package dashboard

import (
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"

	"github.com/arosl/nhm/internal/ui/tui/theme"
)

// KeyMap defines dashboard keybindings.
type KeyMap struct {
	Up          key.Binding
	Down        key.Binding
	Zoom        key.Binding
	NextPanel   key.Binding // Tab to cycle panels
	PrevPanel   key.Binding // Shift+Tab to cycle back
	Refresh     key.Binding
	Pause       key.Binding
	Quit        key.Binding
	MailRefresh key.Binding // 'm' to refresh Agent Mail data
	InboxToggle key.Binding // 'i' to toggle inbox details
	Help        key.Binding // '?' to toggle help overlay
	Diagnostics key.Binding // 'd' to toggle diagnostics
	SpawnWizard key.Binding // ctrl+w opens spawn wizard [tui-upgrade: bd-uz09d]
	// C6-wire panel toggles [reality-bridge: bd-ws2-wire-or-delete-ykmcz.6]
	QuotaToggle key.Binding // '$' toggles the usage/quota panel
	// nhm: the board's own keys.
	Actions  key.Binding // space opens the action menu for the marked rows, else the row
	Mark     key.Binding // 'x' marks or unmarks the row
	Filter   key.Binding // '/' filters the rows
	Back     key.Binding // esc closes a view, else clears the filter and the marks
	Tab      key.Binding
	ShiftTab key.Binding
	Num1     key.Binding
	Num2     key.Binding
	Num3     key.Binding
	Num4     key.Binding
	Num5     key.Binding
	Num6     key.Binding
	Num7     key.Binding
	Num8     key.Binding
	Num9     key.Binding
}

// ShortHelp returns the short help bindings for the footer bar.
// Implements help.KeyMap interface.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Help, k.Quit, k.Tab, k.Zoom, k.Refresh, k.Actions}
}

// FullHelp returns the full help bindings for the help overlay.
// Implements help.KeyMap interface.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Zoom}, // Navigation
		{k.NextPanel, k.PrevPanel, k.QuotaToggle, k.InboxToggle}, // Panels & Actions
		{k.Refresh, k.MailRefresh},                               // Data
		{k.Help, k.Quit, k.Pause, k.Diagnostics, k.SpawnWizard},  // Control
		{k.Actions, k.Mark, k.Filter, k.Back},                    // nhm: agents
	}
}

// Compile-time check that KeyMap implements help.KeyMap.
var _ help.KeyMap = KeyMap{}

// newHelpModel creates a styled help.Model using the current theme.
func newHelpModel(t theme.Theme) help.Model {
	h := help.New()
	h.Styles.ShortKey = lipgloss.NewStyle().Foreground(t.Primary).Bold(true)
	h.Styles.ShortDesc = lipgloss.NewStyle().Foreground(t.Subtext)
	h.Styles.ShortSeparator = lipgloss.NewStyle().Foreground(t.Overlay)
	h.Styles.FullKey = lipgloss.NewStyle().Foreground(t.Primary).Bold(true)
	h.Styles.FullDesc = lipgloss.NewStyle().Foreground(t.Subtext)
	h.Styles.FullSeparator = lipgloss.NewStyle().Foreground(t.Overlay)
	return h
}

var dashKeys = KeyMap{
	Up:          key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:        key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	Zoom:        key.NewBinding(key.WithKeys("z", "enter"), key.WithHelp("z/enter", "zoom")),
	NextPanel:   key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next panel")),
	PrevPanel:   key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev panel")),
	Refresh:     key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
	Pause:       key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pause/resume auto-refresh")),
	Quit:        key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	MailRefresh: key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "refresh mail")),
	InboxToggle: key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "inbox details")),
	Help:        key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "toggle help")),
	Diagnostics: key.NewBinding(key.WithKeys("d", "ctrl+d"), key.WithHelp("d/ctrl+d", "toggle diagnostics")),
	SpawnWizard: key.NewBinding(key.WithKeys("ctrl+w"), key.WithHelp("ctrl+w", "spawn wizard")),
	// C6-wire panel toggles [reality-bridge: bd-ws2-wire-or-delete-ykmcz.6]
	QuotaToggle: key.NewBinding(key.WithKeys("$"), key.WithHelp("$", "toggle usage/quota")),
	Actions:     key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "actions")),
	Mark:        key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "mark")),
	Filter:      key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
	Back:        key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	Tab:         key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next panel")),
	ShiftTab:    key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev panel")),
	Num1:        key.NewBinding(key.WithKeys("1")),
	Num2:        key.NewBinding(key.WithKeys("2")),
	Num3:        key.NewBinding(key.WithKeys("3")),
	Num4:        key.NewBinding(key.WithKeys("4")),
	Num5:        key.NewBinding(key.WithKeys("5")),
	Num6:        key.NewBinding(key.WithKeys("6")),
	Num7:        key.NewBinding(key.WithKeys("7")),
	Num8:        key.NewBinding(key.WithKeys("8")),
	Num9:        key.NewBinding(key.WithKeys("9")),
}
