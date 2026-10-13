// Package theme is ntm's Catppuccin theme for nhm's UI (taken from ntm's
// internal/tui/theme; see PROVENANCE.md).
//
// The rule for every nhm surface: call
//
//	theme.ApplyLipGlossDefaults(theme.Current())
//
// before tea.NewProgram, as ntm does. It sets Lip Gloss's background and
// colour profile from the theme and the environment, without terminal
// I/O; otherwise the first adaptive colour asks the terminal, while
// bubbletea holds its input. The nhm binary must also link package
// internal/ui/tui/termbg (cmd/nhm does), which keeps bubbletea's own init
// from asking.
package theme
