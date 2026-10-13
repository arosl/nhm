// Package menu is nhm's menu (prefix+m, plan-v4 sections 3 and 6): every
// MVP command within two levels, as forms and views in one program. The
// rows here are data: the shared action table runs them by ID.
package menu

import "github.com/arosl/nhm/internal/ui"

// Kind says what a row opens.
type Kind string

// Row kinds.
const (
	// KindGroup opens the second level.
	KindGroup Kind = "group"
	// KindSurface starts another surface (palette, board) in the same
	// popup.
	KindSurface Kind = "surface"
	// KindView is read only.
	KindView Kind = "view"
	// KindAction runs at once and changes no agent (focus, zoom).
	KindAction Kind = "action"
	// KindForm changes something: a review, then a confirm.
	KindForm Kind = "form"
)

// Selection is what a row needs before it runs (plan-v4 section 2). A row
// that needs agents opens the agent picker; it never means "all".
type Selection string

// Selections.
const (
	SelectNone Selection = "none"
	SelectOne  Selection = "one"
	SelectMany Selection = "many"
)

// Item is one menu row.
type Item struct {
	// ID names the action in the shared action table.
	ID string
	// Parent is the group the row sits in; "" is the top level.
	Parent string
	// Key picks the row in its level.
	Key   string
	Label string
	// Help says what the row does, in one line.
	Help      string
	Kind      Kind
	Selection Selection
	// NeedsWorkspace rows act on the captured workspace.
	NeedsWorkspace bool
	// Covers are the CLI leaves of evidence/verb-coverage.md that the row
	// makes interactive.
	Covers []string
}

// Reserved are the keys every level keeps for itself: close, back, help
// and the cursor. Letters pick rows, so the cursor has no letter keys.
var Reserved = []string{"q", "esc", "?", "enter", "backspace", "left", "up", "down"}

// Items are the menu's rows, in display order.
var Items = []Item{
	{ID: "palette", Key: "a", Label: "palette", Help: "send a prompt from the command palette", Kind: KindSurface,
		Covers: []string{"send -t", "palette"}},
	{ID: "palette.go", Key: "A", Label: "palette (new)", Help: "the Go palette, until it is accepted", Kind: KindSurface},
	{ID: "board", Key: "d", Label: "board", Help: "the live agents of this workspace", Kind: KindSurface,
		Covers: []string{"board", "snapshot"}},
	{ID: "spawn.add", Key: "n", Label: "spawn", Help: "add agents to this workspace: wizard, dry run, confirm", Kind: KindForm,
		NeedsWorkspace: true, Covers: []string{"add"}},
	{ID: "send", Key: "s", Label: "send", Help: "type text to the agents you pick, with the memory it adds",
		Kind: KindForm, Selection: SelectMany, Covers: []string{"send", "memory"}},
	{ID: "operations", Key: "o", Label: "recent operations", Help: "what nhm started and how it ended, also after the popup closed",
		Kind: KindView, Covers: []string{"recent operations"}},
	{ID: "agent", Key: "g", Label: "agent", Help: "read, zoom, inbox, interrupt, retire, recycle, adopt", Kind: KindGroup},
	{ID: "workspace", Key: "w", Label: "workspace", Help: "focus, new, spawn a session, reap, kill, wake", Kind: KindGroup},
	{ID: "views", Key: "v", Label: "views", Help: "status, attention, usage, modes, ensembles, feedback", Kind: KindGroup},
	{ID: "diagnostics", Key: "x", Label: "diagnostics", Help: "doctor, health, capabilities, version, compatibility, unfinished operations",
		Kind: KindGroup},

	{ID: "agent.read", Parent: "agent", Key: "r", Label: "read", Help: "the agent's screen, with follow and pause", Kind: KindView,
		Selection: SelectOne, Covers: []string{"read", "watch"}},
	{ID: "agent.zoom", Parent: "agent", Key: "z", Label: "zoom", Help: "focus and zoom the agent's pane", Kind: KindAction,
		Selection: SelectOne, Covers: []string{"zoom"}},
	{ID: "agent.inbox", Parent: "agent", Key: "i", Label: "inbox", Help: "the agent's unread mail; it stays unread", Kind: KindView,
		Selection: SelectOne, Covers: []string{"mail inbox"}},
	{ID: "agent.interrupt", Parent: "agent", Key: "x", Label: "interrupt", Help: "stop the current turn of the agents you pick",
		Kind: KindForm, Selection: SelectMany, Covers: []string{"interrupt"}},
	{ID: "agent.retire", Parent: "agent", Key: "t", Label: "retire", Help: "close the agent's pane after retire's checks", Kind: KindForm,
		Selection: SelectOne, Covers: []string{"retire"}},
	{ID: "agent.recycle", Parent: "agent", Key: "c", Label: "recycle", Help: "a fresh session in the same pane, with a handoff",
		Kind: KindForm, Selection: SelectOne, Covers: []string{"recycle"}},
	{ID: "agent.adopt", Parent: "agent", Key: "p", Label: "adopt", Help: "bind an unbound agent to a Mail name", Kind: KindForm,
		Selection: SelectOne, Covers: []string{"adopt"}},

	{ID: "workspace.attach", Parent: "workspace", Key: "f", Label: "focus", Help: "focus a workspace", Kind: KindAction,
		Covers: []string{"attach"}},
	{ID: "workspace.new", Parent: "workspace", Key: "n", Label: "new", Help: "create a workspace: label and project directory",
		Kind: KindForm, Covers: []string{"new"}},
	{ID: "spawn.new", Parent: "workspace", Key: "s", Label: "spawn a session", Help: "create a session and spawn agents in it: wizard, dry run, confirm",
		Kind: KindForm, Covers: []string{"spawn"}},
	{ID: "workspace.reap", Parent: "workspace", Key: "r", Label: "reap", Help: "retire idle agents: a dry run, then exactly that set",
		Kind: KindForm, NeedsWorkspace: true, Covers: []string{"reap"}},
	{ID: "workspace.kill", Parent: "workspace", Key: "k", Label: "kill", Help: "close this workspace: type its label to confirm",
		Kind: KindForm, NeedsWorkspace: true, Covers: []string{"kill"}},
	{ID: "workspace.wake", Parent: "workspace", Key: "w", Label: "wake", Help: "tell the agents you pick about their new mail, once",
		Kind: KindForm, Selection: SelectMany, Covers: []string{"wake"}},

	{ID: "view.status", Parent: "views", Key: "s", Label: "status", Help: "one row per agent, every workspace", Kind: KindView,
		Covers: []string{"status", "list", "activity"}},
	{ID: "view.attention", Parent: "views", Key: "t", Label: "attention", Help: "what needs a decision", Kind: KindView,
		Covers: []string{"attention"}},
	{ID: "view.usage", Parent: "views", Key: "u", Label: "usage", Help: "quota pools and windows; agents at a limit (dry run)",
		Kind: KindView, Covers: []string{"quota", "limits"}},
	{ID: "view.modes", Parent: "views", Key: "m", Label: "modes", Help: "the reasoning modes; select one to explain it", Kind: KindView,
		Covers: []string{"modes list", "modes explain"}},
	{ID: "view.ensembles", Parent: "views", Key: "e", Label: "ensembles", Help: "presets, runs and their status; stop a run",
		Kind: KindView, Covers: []string{"ensemble presets", "ensemble runs", "ensemble status", "ensemble stop"}},
	{ID: "view.feedback", Parent: "views", Key: "f", Label: "feedback", Help: "the feedback recorded on operations", Kind: KindView,
		Covers: []string{"feedback list"}},

	{ID: "diag.doctor", Parent: "diagnostics", Key: "d", Label: "doctor", Help: "check what nhm needs", Kind: KindView,
		Covers: []string{"doctor"}},
	{ID: "diag.health", Parent: "diagnostics", Key: "h", Label: "health", Help: "each source and how fresh it is", Kind: KindView,
		Covers: []string{"health"}},
	{ID: "diag.capabilities", Parent: "diagnostics", Key: "c", Label: "capabilities", Help: "what this nhm answers, and why not",
		Kind: KindView, Covers: []string{"capabilities"}},
	{ID: "diag.version", Parent: "diagnostics", Key: "v", Label: "version", Help: "nhm's version and the herdr it supports",
		Kind: KindView, Covers: []string{"version"}},
	{ID: "diag.compatibility", Parent: "diagnostics", Key: "p", Label: "compatibility", Help: "the herdr protocol and schema in use",
		Kind: KindView, Covers: []string{"compatibility status"}},
	{ID: "diag.recover", Parent: "diagnostics", Key: "u", Label: "unfinished operations",
		Help: "operations with no final outcome, and displaced bindings", Kind: KindView, Covers: []string{"admin recover inspect"}},
}

// NotInMenu are the leaves of evidence/verb-coverage.md the menu does not
// offer, with the reason (plan-v4 section 3: every CLI-only row has one).
var NotInMenu = []struct{ Leaf, Reason string }{
	{"wait", "a scripting verb; the board shows states live"},
	{"resolve", "an agent's lookup of itself"},
	{"help", "text help; the menu has its own help (?)"},
	{"quota check", "scripting"},
	{"quota record-claude", "Claude's statusLine runs it"},
	{"maintain", "the timer runs it; a manual pass gives no decision the board does not show"},
	{"limits --loop", "a runner mode"},
	{"wake without --recipients", "the timer's maintain wakes every agent"},
	{"bind", "setup, run by install and acfs update"},
	{"admin", "rare operator-level recovery and ownership changes; only recover inspect is a view"},
	{"--robot-*", "machine output for scripts"},
	{"feedback outcomes", "later: a learning mutation (cm outcome), as a deliberate action"},
	{"feedback mark", "later"},
	{"ensemble spawn", "later: creation forms"},
	{"ensemble synthesize", "later: synthesis forms"},
	{"ui", "the surfaces themselves"},
	{"menu", "this menu"},
}

// Level returns the rows of a level: "" is the top level, else a group's
// ID.
func Level(parent string) []Item {
	var out []Item
	for _, it := range Items {
		if it.Parent == parent {
			out = append(out, it)
		}
	}
	return out
}

// ByID returns the row with an ID.
func ByID(id string) (Item, bool) {
	for _, it := range Items {
		if it.ID == id {
			return it, true
		}
	}
	return Item{}, false
}

// Unavailable says why a row cannot run in the captured context; "" when
// it can.
func (it Item) Unavailable(c ui.Context) string {
	if it.NeedsWorkspace && c.WorkspaceID == "" {
		return "the menu has no workspace: open it from a workspace"
	}
	return ""
}
