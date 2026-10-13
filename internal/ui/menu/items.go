// Package menu is nhm's menu (prefix+m, plan-v4 sections 3 and 6): every
// MVP row of the action table (internal/ui/actions) within two levels,
// with the forms, views and picker it opens, in one program. The table is
// the one source of the rows; this package lays them out and adds only
// what is the menu's own: the Go palette's row until its acceptance, and
// the reasons in NotInMenu.
package menu

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
	{"move", "later: a form that picks the target workspace (nhm move <Name> --workspace W)"},
	{"ui", "the surfaces themselves"},
	{"menu", "this menu"},
}
