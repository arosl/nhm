// Package palette is ntm's command palette (ntm internal/palette at
// 9d5ed977) on nhm, opened by prefix+a in a herdr popup (plan-v4 section
// 5): pick a prompt from the palette catalogs, edit it, pick the agents
// that get it, and read a receipt per agent.
//
// ntm's code is ported as written, over small stand-ins for what it took
// from ntm's other packages:
//
//   - tmux's panes are the bound agents of the captured workspace, read
//     from nhm's snapshot (Pane, PanesFrom);
//   - config's palette types are filled from nhm's palette catalogs, and
//     the pins, favorites and recents live in nhm's state directory, one
//     key per line (PaletteCmd, PaletteState, Store);
//   - the kind number keys come from nhm's kinds (KindKeys).
//
// The palette never sends on its own. ntm's sendWith typed into tmux and
// quit; here a submission is one call of nhm's send operation, which the
// caller hands in (Sender), made off bubbletea's update loop, with the
// targets frozen as {binding, generation} at the keypress (Submitter,
// BuildRequest). Its receipts are shown on a result screen, not a quit.
//
// The palette's editor is also the Composer, the component the menu and
// the board embed to write a message: the same editor, keys, empty-draft
// rule and memory preview.
package palette
