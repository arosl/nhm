// Taken from ntm (github.com/Dicklesworthstone/ntm) internal/palette/keymap.go
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: this header; import paths. Behaviour (plan-v4 section 5):
// the target phase's per-kind bindings (2-5 and 7) are one TargetKind
// binding over nhm's kind digits (KindKeys); ctrl+o in the command phase
// and m in the target phase show or hide the memory preview, and c and s
// switch CM and CASS for the sends (nhm-t63); the result
// and variables phases and the Composer are nhm's and have their own
// keymaps.

package palette

// Phase keymaps: the single source of truth for palette key handling AND the
// help UI (H3, bd-ws7-docs-ux-truth-tqh3l.3).
//
// Each phase's Update handler matches ONLY against its phase keymap, and every
// help surface (compact help bar, the ?/f1 overlay, the xf hint lines) is
// GENERATED from the same binding values via the helpEntry lists below, so the
// advertised keys cannot diverge from the handled keys again.
// TestPaletteHelpHonesty (keymap_test.go) enforces both directions:
// advertised ⊆ handled AND handled ⊆ advertised.
//
// The command phase keeps the filter input permanently focused, so printable
// runes (letters, digits, '?', space) always belong to the search box.
// Command-phase bindings therefore use only non-printable keys. The previously
// advertised 1-9 quick-select, `q`, and `?` were dead there; quick-select was
// REJECTED per the recorded H3 decision (a live filter owns digit keys, and
// recents/pinning already solve fast access), so those keys are gone rather
// than wired.

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"

	"github.com/arosl/nhm/internal/ui/tui/components"
	"github.com/arosl/nhm/internal/ui/tui/layout"
)

// commandKeyMap covers PhaseCommand (filter always focused: non-rune keys only).
type commandKeyMap struct {
	Up             key.Binding
	Down           key.Binding
	PageUp         key.Binding
	PageDown       key.Binding
	HalfPageUp     key.Binding
	HalfPageDown   key.Binding
	Home           key.Binding
	End            key.Binding
	Select         key.Binding
	Back           key.Binding
	Quit           key.Binding
	Help           key.Binding
	TogglePin      key.Binding
	ToggleFavorite key.Binding
	XFSearch       key.Binding
	Compose        key.Binding
	Memory         key.Binding
}

var commandKeys = commandKeyMap{
	Up:             key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "up")),
	Down:           key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "down")),
	PageUp:         key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up")),
	PageDown:       key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "page down")),
	HalfPageUp:     key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("ctrl+u", "half page up")),
	HalfPageDown:   key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("ctrl+d", "half page down")),
	Home:           key.NewBinding(key.WithKeys("home"), key.WithHelp("home", "top")),
	End:            key.NewBinding(key.WithKeys("end"), key.WithHelp("end", "bottom")),
	Select:         key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
	Back:           key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "quit")),
	Quit:           key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
	Help:           key.NewBinding(key.WithKeys("f1"), key.WithHelp("f1", "help")),
	TogglePin:      key.NewBinding(key.WithKeys("ctrl+p"), key.WithHelp("ctrl+p", "pin")),
	ToggleFavorite: key.NewBinding(key.WithKeys("ctrl+f"), key.WithHelp("ctrl+f", "favorite")),
	XFSearch:       key.NewBinding(key.WithKeys("ctrl+k"), key.WithHelp("ctrl+k", "xf search")),
	Compose:        key.NewBinding(key.WithKeys("ctrl+n"), key.WithHelp("ctrl+n", "custom msg")),
	Memory:         key.NewBinding(key.WithKeys("ctrl+o"), key.WithHelp("ctrl+o", "memory")),
}

// targetKeyMap covers PhaseTarget (no focused text input: rune keys are live).
type targetKeyMap struct {
	Target1      key.Binding
	TargetKind   key.Binding
	SelectAgents key.Binding
	Edit         key.Binding
	Memory       key.Binding
	MemoryCM     key.Binding
	MemoryCASS   key.Binding
	Help         key.Binding
	Back         key.Binding
	Quit         key.Binding
}

var targetKeys = targetKeyMap{
	Target1:      key.NewBinding(key.WithKeys(KeyAll), key.WithHelp(KeyAll, "all agents")),
	TargetKind:   key.NewBinding(key.WithKeys(freeKindKeys...), key.WithHelp("2-9", "one kind")),
	SelectAgents: key.NewBinding(key.WithKeys(KeySelect), key.WithHelp(KeySelect, "pick agents")),
	Edit:         key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit prompt")),
	Memory:       key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "memory")),
	MemoryCM:     key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "CM on/off")),
	MemoryCASS:   key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "CASS on/off")),
	Help:         key.NewBinding(key.WithKeys("?", "f1"), key.WithHelp("?", "help")),
	Back:         key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	Quit:         key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
}

// selectAgentsKeyMap covers PhaseSelectAgents (#205; runes are live).
type selectAgentsKeyMap struct {
	Up     key.Binding
	Down   key.Binding
	Toggle key.Binding
	All    key.Binding
	None   key.Binding
	Select key.Binding
	Back   key.Binding
	Quit   key.Binding
}

var selectAgentsKeys = selectAgentsKeyMap{
	Up:     key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "move")),
	Down:   key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "move")),
	Toggle: key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "toggle")),
	All:    key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "all")),
	None:   key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "none")),
	Select: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
	Back:   key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	Quit:   key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
}

// editKeyMap covers PhaseEdit. The textarea is focused, so runes belong to the
// text: Quit is ctrl+c ONLY (a `q` binding here used to quit the palette while
// typing a prompt containing 'q').
type editKeyMap struct {
	ConfirmEdit key.Binding
	Back        key.Binding
	Quit        key.Binding
}

var editKeys = editKeyMap{
	ConfirmEdit: key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "save & pick target")),
	Back:        key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
	Quit:        key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
}

// xfSearchKeyMap covers PhaseXFSearch. The query input is focused, so runes
// belong to the query: Quit is ctrl+c ONLY (same 'q' trap as the edit phase).
type xfSearchKeyMap struct {
	Select key.Binding
	Back   key.Binding
	Quit   key.Binding
}

var xfSearchKeys = xfSearchKeyMap{
	Select: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "search")),
	Back:   key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	Quit:   key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
}

// xfResultsKeyMap covers PhaseXFResults (no focused input: runes are live).
type xfResultsKeyMap struct {
	Up     key.Binding
	Down   key.Binding
	Select key.Binding
	Help   key.Binding
	Back   key.Binding
	Quit   key.Binding
}

var xfResultsKeys = xfResultsKeyMap{
	Up:     key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:   key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	Select: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send to agent")),
	Help:   key.NewBinding(key.WithKeys("?", "f1"), key.WithHelp("?", "help")),
	Back:   key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	Quit:   key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
}

// resultKeyMap covers PhaseResult, nhm's receipts screen (runes are live).
// esc always leaves: the core finishes a send in flight on its own.
type resultKeyMap struct {
	Again     key.Binding
	Reconcile key.Binding
	Quit      key.Binding
}

var resultKeys = resultKeyMap{
	Again:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "back to the palette")),
	Reconcile: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "read the result again")),
	Quit:      key.NewBinding(key.WithKeys("esc", "q", "ctrl+c"), key.WithHelp("esc", "close")),
}

// varsKeyMap covers PhaseVars, where the palette asks for the template
// variables the core found unfilled. An input is focused: runes belong to it.
type varsKeyMap struct {
	Next key.Binding
	Prev key.Binding
	Back key.Binding
	Quit key.Binding
}

var varsKeys = varsKeyMap{
	Next: key.NewBinding(key.WithKeys("enter", "tab"), key.WithHelp("enter", "next / send")),
	Prev: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "previous")),
	Back: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	Quit: key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
}

// composerKeyMap covers the Composer, the editor the menu and the board
// embed. The textarea is focused: runes and enter belong to the text, and
// quitting is the host's.
type composerKeyMap struct {
	Done   key.Binding
	Cancel key.Binding
	Memory key.Binding
}

var composerKeys = composerKeyMap{
	Done:   key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "send")),
	Cancel: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
	Memory: key.NewBinding(key.WithKeys("ctrl+o"), key.WithHelp("ctrl+o", "memory")),
}

// helpEntry documents one or more handled bindings under a single label in a
// help surface. Entries with no bindings are purely informational (e.g. "type
// to filter") and exempt from the honesty check.
type helpEntry struct {
	label    string
	desc     string
	bindings []key.Binding
}

// entry builds a helpEntry whose label is the joined Help().Key of the
// bindings it documents (e.g. Up+Down -> "↑/↓").
func entry(desc string, bindings ...key.Binding) helpEntry {
	labels := make([]string, 0, len(bindings))
	for _, b := range bindings {
		labels = append(labels, b.Help().Key)
	}
	return helpEntry{label: strings.Join(labels, "/"), desc: desc, bindings: bindings}
}

// entryLabeled builds a helpEntry with an explicit label (for compact combined
// labels like "1-5" or "pgup/dn" that the raw Help().Key join would bloat).
func entryLabeled(label, desc string, bindings ...key.Binding) helpEntry {
	return helpEntry{label: label, desc: desc, bindings: bindings}
}

// infoEntry builds a purely informational helpEntry with no bindings.
func infoEntry(label, desc string) helpEntry {
	return helpEntry{label: label, desc: desc}
}

// commandHelpEntries is the command-phase compact help bar, tier-gated like
// the rest of the palette chrome. canScroll adds the scroll hint when the list
// overflows its viewport.
func commandHelpEntries(tier layout.Tier, canScroll bool) []helpEntry {
	k := commandKeys
	entries := []helpEntry{
		entryLabeled("↑/↓", "navigate", k.Up, k.Down),
		entry("select", k.Select),
		entry("custom msg", k.Compose),
		entry("quit", k.Back),
	}

	if canScroll {
		entries = append(entries, entryLabeled("pgup/dn", "scroll", k.PageUp, k.PageDown))
	}

	if tier >= layout.TierWide {
		entries = append(entries,
			entry("pin", k.TogglePin),
			entry("favorite", k.ToggleFavorite),
			entry("memory", k.Memory),
			entry("quit", k.Quit),
			entry("help", k.Help),
		)
	}

	if tier >= layout.TierUltra {
		entries = append(entries,
			entry("xf search", k.XFSearch),
			infoEntry("type", "filter commands"),
		)
	}

	return entries
}

// commandOverlayEntries lists EVERY handled command-phase binding for the help
// overlay — including the ones the compact bar has no room for.
func commandOverlayEntries() []helpEntry {
	k := commandKeys
	return []helpEntry{
		entryLabeled("↑/↓", "Move up / down", k.Up, k.Down),
		entryLabeled("pgup/pgdn", "Page up / down", k.PageUp, k.PageDown),
		entryLabeled("ctrl+u/ctrl+d", "Half page up / down", k.HalfPageUp, k.HalfPageDown),
		entryLabeled("home/end", "Jump to top / bottom", k.Home, k.End),
		entry("Select command", k.Select),
		infoEntry("Type", "Filter commands"),
		entry("Pin / unpin command", k.TogglePin),
		entry("Favorite / unfavorite command", k.ToggleFavorite),
		entry("Compose a custom message", k.Compose),
		entry("Show / hide what memory adds", k.Memory),
		entry("Search xf archive", k.XFSearch),
		entry("Toggle this help", k.Help),
		entry("Quit palette", k.Back),
		entry("Force quit", k.Quit),
	}
}

// targetHelpEntries is the target-phase compact help bar.
func targetHelpEntries() []helpEntry {
	k := targetKeys
	return []helpEntry{
		entry("all agents", k.Target1),
		entryLabeled("2-9", "one kind", k.TargetKind),
		entry("pick agents", k.SelectAgents),
		entry("edit prompt", k.Edit),
		entry("memory", k.Memory),
		entry("CM on/off", k.MemoryCM),
		entry("CASS on/off", k.MemoryCASS),
		entry("help", k.Help),
		entry("back", k.Back),
		entry("quit", k.Quit),
	}
}

// targetOverlayEntries lists every handled target-phase binding for the overlay.
func targetOverlayEntries() []helpEntry {
	k := targetKeys
	return []helpEntry{
		entry("Send to every agent shown", k.Target1),
		entryLabeled("2-9", "Send to every agent of one kind (the digits are listed)", k.TargetKind),
		entry("Pick specific agents", k.SelectAgents),
		entry("Edit prompt before sending", k.Edit),
		entry("Show / hide what memory adds", k.Memory),
		entry("Send with CM's rules, or without", k.MemoryCM),
		entry("Send with CASS's hits, or without", k.MemoryCASS),
		entry("Toggle this help", k.Help),
		entry("Back to command list", k.Back),
		entry("Quit palette", k.Quit),
	}
}

// selectAgentsHelpEntries is the per-agent multi-select compact help bar.
func selectAgentsHelpEntries() []helpEntry {
	k := selectAgentsKeys
	return []helpEntry{
		entryLabeled("↑/↓", "move", k.Up, k.Down),
		entry("toggle", k.Toggle),
		entry("all", k.All),
		entry("none", k.None),
		entry("send", k.Select),
		entry("back", k.Back),
		entry("quit", k.Quit),
	}
}

// editHelpEntries is the prompt-editor compact help bar.
func editHelpEntries() []helpEntry {
	k := editKeys
	return []helpEntry{
		entry("save & pick target", k.ConfirmEdit),
		entry("cancel", k.Back),
		entry("quit", k.Quit),
	}
}

// xfSearchHelpEntries is the xf query-input hint line.
func xfSearchHelpEntries() []helpEntry {
	k := xfSearchKeys
	return []helpEntry{
		entry("search", k.Select),
		entry("back", k.Back),
		entry("quit", k.Quit),
	}
}

// xfResultsHelpEntries is the xf results hint line.
func xfResultsHelpEntries() []helpEntry {
	k := xfResultsKeys
	return []helpEntry{
		entry("send to agent", k.Select),
		entryLabeled("↑/↓", "navigate", k.Up, k.Down),
		entry("help", k.Help),
		entry("back", k.Back),
		entry("quit", k.Quit),
	}
}

// resultHelpEntries is the result screen's help bar.
func resultHelpEntries() []helpEntry {
	k := resultKeys
	return []helpEntry{
		entry("back to the palette", k.Again),
		entry("read the result again", k.Reconcile),
		entry("close", k.Quit),
	}
}

// varsHelpEntries is the variables screen's help bar.
func varsHelpEntries() []helpEntry {
	k := varsKeys
	return []helpEntry{
		entry("next / send", k.Next),
		entry("previous", k.Prev),
		entry("back", k.Back),
		entry("quit", k.Quit),
	}
}

// composerHelpEntries is the Composer's help bar.
func composerHelpEntries() []helpEntry {
	k := composerKeys
	return []helpEntry{
		entry("send", k.Done),
		entry("cancel", k.Cancel),
		entry("memory", k.Memory),
		infoEntry("enter", "new line"),
	}
}

// renderHelpEntriesPlain renders help entries as an unstyled "key: desc" hint
// line (the xf phases use this compact form).
func renderHelpEntriesPlain(entries []helpEntry) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		parts = append(parts, e.label+": "+e.desc)
	}
	return strings.Join(parts, "  ")
}

// helpSectionFromEntries converts generated help entries into an overlay section.
func helpSectionFromEntries(title string, entries []helpEntry) components.HelpSection {
	hints := make([]components.KeyHint, 0, len(entries))
	for _, e := range entries {
		hints = append(hints, components.KeyHint{Key: e.label, Desc: e.desc})
	}
	return components.HelpSection{Title: title, Hints: hints}
}

// paletteHelpSections builds the ?/f1 overlay content from the phase keymaps —
// the same bindings the update loop matches — so the overlay cannot advertise
// keys the palette does not handle.
func paletteHelpSections() []components.HelpSection {
	return []components.HelpSection{
		helpSectionFromEntries("Command List", commandOverlayEntries()),
		helpSectionFromEntries("Target Selection", targetOverlayEntries()),
		helpSectionFromEntries("Select Agents", selectAgentsHelpEntries()),
		helpSectionFromEntries("Edit Prompt", editHelpEntries()),
		helpSectionFromEntries("Result", resultHelpEntries()),
	}
}
