package actions

// table is the action table, in menu order. The menu's levels and letters
// are ScarletBass's (nhm-qbi.4); the board's keys keep ntm's dashboard
// meaning (plan-v4 section 2), and nhm's per-agent actions sit in the
// board's action menu (space). A row without a handler is not built yet
// and not shown: a track sets its handler here when it lands.
var table = []Action{
	{ID: "palette", Label: "palette", Help: "send a prompt from the command palette", Kind: KindSurface, Scope: ScopeNone,
		Menu: MenuPlace{Key: "a"}, Board: BoardPlace{Space: "a"}, Covers: []string{"send -t", "palette"}},
	{ID: "board", Label: "board", Help: "the live agents of this workspace", Kind: KindSurface, Scope: ScopeNone,
		Menu: MenuPlace{Key: "d"}, Covers: []string{"board", "snapshot"}},
	{ID: "spawn.add", Label: "spawn", Help: "add agents to this workspace: wizard, dry run, confirm", Kind: KindForm,
		Scope: ScopeWorkspace, Menu: MenuPlace{Key: "n"}, Board: BoardPlace{Keys: []string{"ctrl+w"}}, Covers: []string{"add"}},
	{ID: "send", Label: "send", Help: "type text to the agents you pick", Kind: KindForm, Scope: ScopeAgents,
		Selection: SelectMany, Menu: MenuPlace{Key: "s"}, Board: BoardPlace{Space: "s"}, Covers: []string{"send", "memory"},
		run: runSend, needs: needsSend},
	{ID: "operations", Label: "recent operations", Help: "what nhm started and how it ended, also after the popup closed",
		Kind: KindView, Scope: ScopeNone, Menu: MenuPlace{Key: "o"}, Covers: []string{"recent operations"}},
	{ID: "agent", Label: "agent", Help: "read, zoom, inbox, interrupt, retire, recycle, adopt", Kind: KindGroup,
		Menu: MenuPlace{Key: "g"}},
	{ID: "workspace", Label: "workspace", Help: "focus, new, spawn a session, reap, kill, wake", Kind: KindGroup,
		Menu: MenuPlace{Key: "w"}},
	{ID: "views", Label: "views", Help: "status, attention, usage, modes, ensembles, feedback", Kind: KindGroup,
		Menu: MenuPlace{Key: "v"}},
	{ID: "diagnostics", Label: "diagnostics", Help: "doctor, health, capabilities, version, compatibility, unfinished operations",
		Kind: KindGroup, Menu: MenuPlace{Key: "x"}},

	{ID: "agent.read", Label: "read", Help: "the agent's screen, with follow and pause", Kind: KindView, Scope: ScopeAgents,
		Selection: SelectOne, Takes: TakesAny, Menu: MenuPlace{Parent: "agent", Key: "r"}, Board: BoardPlace{Space: "r"},
		Covers: []string{"read", "watch"}},
	{ID: "agent.zoom", Label: "zoom", Help: "focus and zoom the agent's pane", Kind: KindAction, Scope: ScopeAgents,
		Selection: SelectOne, Takes: TakesAny, Menu: MenuPlace{Parent: "agent", Key: "z"},
		Board: BoardPlace{Keys: []string{"z", "enter"}}, Covers: []string{"zoom"}},
	{ID: "agent.inbox", Label: "inbox", Help: "the agent's unread mail; it stays unread", Kind: KindView, Scope: ScopeAgents,
		Selection: SelectOne, Menu: MenuPlace{Parent: "agent", Key: "i"}, Board: BoardPlace{Keys: []string{"i"}},
		Covers: []string{"mail inbox"}},
	{ID: "agent.interrupt", Label: "interrupt", Help: "stop the current turn of the agents you pick", Kind: KindForm,
		Scope: ScopeAgents, Selection: SelectMany, Menu: MenuPlace{Parent: "agent", Key: "x"}, Board: BoardPlace{Space: "x"},
		Covers: []string{"interrupt"}, run: runInterrupt, needs: needsInterrupt},
	{ID: "agent.retire", Label: "retire", Help: "close the agent's pane after retire's checks", Kind: KindForm,
		Scope: ScopeAgents, Selection: SelectOne, Menu: MenuPlace{Parent: "agent", Key: "t"}, Board: BoardPlace{Space: "t"},
		Covers: []string{"retire"}, run: runRetire, needs: needsRetire},
	{ID: "agent.recycle", Label: "recycle", Help: "a fresh session in the same pane, with a handoff", Kind: KindForm,
		Scope: ScopeAgents, Selection: SelectOne, Menu: MenuPlace{Parent: "agent", Key: "c"}, Board: BoardPlace{Space: "c"},
		Covers: []string{"recycle"}, run: runRecycle, needs: needsRecycle},
	{ID: "agent.adopt", Label: "adopt", Help: "bind an unbound agent to a Mail name", Kind: KindForm, Scope: ScopeAgents,
		Selection: SelectOne, Takes: TakesUnbound, Menu: MenuPlace{Parent: "agent", Key: "p"}, Board: BoardPlace{Space: "p"},
		Covers: []string{"adopt"}},

	{ID: "workspace.attach", Label: "focus", Help: "focus a workspace", Kind: KindAction, Scope: ScopeNone,
		Menu: MenuPlace{Parent: "workspace", Key: "f"}, Covers: []string{"attach"}},
	{ID: "workspace.new", Label: "new", Help: "create a workspace: label and project directory", Kind: KindForm, Scope: ScopeNone,
		Menu: MenuPlace{Parent: "workspace", Key: "n"}, Covers: []string{"new"}},
	{ID: "spawn.new", Label: "spawn a session", Help: "create a session and spawn agents in it: wizard, dry run, confirm",
		Kind: KindForm, Scope: ScopeNone, Menu: MenuPlace{Parent: "workspace", Key: "s"}, Covers: []string{"spawn"}},
	{ID: "workspace.reap", Label: "reap", Help: "retire idle agents: a dry run, then exactly that set", Kind: KindForm,
		Scope: ScopeWorkspace, Menu: MenuPlace{Parent: "workspace", Key: "r"}, Covers: []string{"reap"}},
	{ID: "workspace.kill", Label: "kill", Help: "close this workspace: type its label to confirm", Kind: KindForm,
		Scope: ScopeWorkspace, Menu: MenuPlace{Parent: "workspace", Key: "k"}, Covers: []string{"kill"}},
	{ID: "workspace.wake", Label: "wake", Help: "tell the agents you pick about their new mail, once", Kind: KindForm,
		Scope: ScopeAgents, Selection: SelectMany, Menu: MenuPlace{Parent: "workspace", Key: "w"}, Covers: []string{"wake"}},

	{ID: "view.status", Label: "status", Help: "one row per agent, every workspace", Kind: KindView, Scope: ScopeNone,
		Menu: MenuPlace{Parent: "views", Key: "s"}, Covers: []string{"status", "list", "activity"}},
	{ID: "view.attention", Label: "attention", Help: "what needs a decision", Kind: KindView, Scope: ScopeNone,
		Menu: MenuPlace{Parent: "views", Key: "t"}, Covers: []string{"attention"}},
	{ID: "view.usage", Label: "usage", Help: "quota pools and windows; agents at a limit (dry run)", Kind: KindView,
		Scope: ScopeNone, Menu: MenuPlace{Parent: "views", Key: "u"}, Board: BoardPlace{Keys: []string{"$"}},
		Covers: []string{"quota", "limits"}},
	{ID: "view.modes", Label: "modes", Help: "the reasoning modes; select one to explain it", Kind: KindView, Scope: ScopeNone,
		Menu: MenuPlace{Parent: "views", Key: "m"}, Covers: []string{"modes list", "modes explain"}},
	{ID: "view.ensembles", Label: "ensembles", Help: "presets, runs and their status; stop a run", Kind: KindView,
		Scope: ScopeNone, Menu: MenuPlace{Parent: "views", Key: "e"},
		Covers: []string{"ensemble presets", "ensemble runs", "ensemble status", "ensemble stop"}},
	{ID: "view.feedback", Label: "feedback", Help: "the feedback recorded on operations", Kind: KindView, Scope: ScopeNone,
		Menu: MenuPlace{Parent: "views", Key: "f"}, Covers: []string{"feedback list"}},

	{ID: "diag.doctor", Label: "doctor", Help: "check what nhm needs", Kind: KindView, Scope: ScopeNone,
		Menu: MenuPlace{Parent: "diagnostics", Key: "d"}, Covers: []string{"doctor"}},
	{ID: "diag.health", Label: "health", Help: "each source and how fresh it is", Kind: KindView, Scope: ScopeNone,
		Menu: MenuPlace{Parent: "diagnostics", Key: "h"}, Board: BoardPlace{Keys: []string{"d", "ctrl+d"}},
		Covers: []string{"health"}},
	{ID: "diag.capabilities", Label: "capabilities", Help: "what this nhm answers, and why not", Kind: KindView,
		Scope: ScopeNone, Menu: MenuPlace{Parent: "diagnostics", Key: "c"}, Covers: []string{"capabilities"}},
	{ID: "diag.version", Label: "version", Help: "nhm's version and the herdr it supports", Kind: KindView, Scope: ScopeNone,
		Menu: MenuPlace{Parent: "diagnostics", Key: "v"}, Covers: []string{"version"}},
	{ID: "diag.compatibility", Label: "compatibility", Help: "the herdr protocol and schema in use", Kind: KindView,
		Scope: ScopeNone, Menu: MenuPlace{Parent: "diagnostics", Key: "p"}, Covers: []string{"compatibility status"}},
	{ID: "diag.recover", Label: "unfinished operations", Help: "operations with no final outcome, and displaced bindings",
		Kind: KindView, Scope: ScopeNone, Menu: MenuPlace{Parent: "diagnostics", Key: "u"},
		Covers: []string{"admin recover inspect"}},
}

// MenuReserved are the keys every menu level keeps for itself: close,
// back, help and the cursor (ScarletBass's menu.Reserved). Letters pick
// rows, so the cursor has no letter keys.
var MenuReserved = []string{"q", "esc", "?", "enter", "backspace", "left", "up", "down"}

// SpaceReserved are the keys the board's action menu keeps for itself.
var SpaceReserved = []string{"esc", "q", "?", "enter", "up", "down"}

// BoardReserved are the board's own keys, which run no action (AmberLotus,
// nhm-qbi.5): ntm's navigation, refresh, pause, help and quit, and nhm's
// marks (x), filter (/), clear (esc) and action menu (space).
var BoardReserved = []string{"up", "k", "down", "j", "tab", "shift+tab", "r", "p", "m", "?", "q",
	"1", "2", "3", "4", "5", "6", "7", "8", "9", "x", "/", "esc", "space"}

// NTMBoardKeys are ntm's dashboard keys at 9d5ed977
// (internal/tui/dashboard/keymap.go), with ntm's binding. A board key of
// an action that ntm has keeps ntm's meaning (TestBoardKeysKeepNTMMeaning).
var NTMBoardKeys = map[string]string{
	"up": "Up", "k": "Up", "down": "Down", "j": "Down", "z": "Zoom", "enter": "Zoom", "tab": "NextPanel",
	"shift+tab": "PrevPanel", "r": "Refresh", "p": "Pause", "q": "Quit", "c": "ContextRefresh", "m": "MailRefresh",
	"i": "InboxToggle", "ctrl+s": "CassSearch", "e": "EnsembleModes", "?": "Help", "d": "Diagnostics",
	"ctrl+d": "Diagnostics", "u": "ScanToggle", "ctrl+k": "Checkpoint", "ctrl+x": "ToastDismiss", "n": "ToastHistory",
	"ctrl+w": "SpawnWizard", "w": "WorkflowToggle", "v": "ViewToggle", "$": "QuotaToggle", "o": "RateLimitToggle",
	"a": "AccountsToggle", "1": "Num1", "2": "Num2", "3": "Num3", "4": "Num4", "5": "Num5", "6": "Num6", "7": "Num7",
	"8": "Num8", "9": "Num9",
}

// ntmMeaning is the ntm dashboard binding each action's board keys stand
// for.
var ntmMeaning = map[string]string{
	"agent.zoom": "Zoom", "agent.inbox": "InboxToggle", "spawn.add": "SpawnWizard", "diag.health": "Diagnostics",
	"view.usage": "QuotaToggle",
}
