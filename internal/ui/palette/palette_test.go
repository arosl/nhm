// Taken from ntm (github.com/Dicklesworthstone/ntm) internal/palette/palette_test.go
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: this header; import paths; config, tmux and history types
// are this package's stand-ins. Behaviour: the target tests use nhm's kind
// digits; the send tests check the request the palette hands to nhm's send
// operation (Submitter) in place of tmux send-keys; the pin and recents
// tests use the Store; the session selector returns workspace ids. The tests
// after "nhm" below are nhm's (plan-v4 sections 4, 5 and 10).

package palette

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/core/enrich"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/templates"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/ui"
)

// stripANSI removes ANSI escape sequences from text for test comparison.
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

// Test command fixtures
var testCommands = []PaletteCmd{
	{Key: "fix", Label: "Fix Bug", Category: "Quick Actions", Prompt: "Fix the bug"},
	{Key: "test", Label: "Run Tests", Category: "Quick Actions", Prompt: "Run all tests"},
	{Key: "refactor", Label: "Refactor Code", Category: "Code Quality", Prompt: "Refactor the code"},
	{Key: "docs", Label: "Add Documentation", Category: "Code Quality", Prompt: "Add docs"},
	{Key: "review", Label: "Code Review", Category: "Investigation", Prompt: "Review this code"},
}

func TestNew(t *testing.T) {
	m := New("test-session", testCommands)

	if m.session != "test-session" {
		t.Errorf("Expected session 'test-session', got %s", m.session)
	}

	if len(m.commands) != len(testCommands) {
		t.Errorf("Expected %d commands, got %d", len(testCommands), len(m.commands))
	}

	if len(m.filtered) != len(testCommands) {
		t.Errorf("Expected filtered to have %d commands initially, got %d", len(testCommands), len(m.filtered))
	}

	if m.phase != PhaseCommand {
		t.Errorf("Expected initial phase PhaseCommand, got %v", m.phase)
	}

	if m.cursor != 0 {
		t.Errorf("Expected cursor at 0, got %d", m.cursor)
	}
}

func TestNewEmptyCommands(t *testing.T) {
	m := New("test-session", nil)

	if len(m.commands) != 0 {
		t.Errorf("Expected 0 commands, got %d", len(m.commands))
	}

	if len(m.filtered) != 0 {
		t.Errorf("Expected 0 filtered, got %d", len(m.filtered))
	}
}

func TestPhaseConstants(t *testing.T) {
	// Verify phase constants are distinct
	if PhaseCommand == PhaseTarget {
		t.Error("PhaseCommand and PhaseTarget should be distinct")
	}
	if PhaseTarget == PhaseEdit {
		t.Error("PhaseTarget and PhaseEdit should be distinct")
	}
	if PhaseResult == PhaseVars || PhaseResult == PhaseSelectAgents {
		t.Error("nhm's phases should be distinct")
	}
}

func TestTargetConstants(t *testing.T) {
	// Verify target constants are distinct
	targets := []Target{TargetAll, TargetKind, TargetSelected}
	seen := make(map[Target]bool)
	for _, t := range targets {
		seen[t] = true
	}
	if len(seen) != 3 {
		t.Error("Expected 3 distinct Target values")
	}
}

func TestInit(t *testing.T) {
	m := New("test-session", testCommands)
	cmd := m.Init()

	// Init should return a batch command (textinput.Blink and tick)
	if cmd == nil {
		t.Error("Init should return a command")
	}
}

func TestUpdateWindowSize(t *testing.T) {
	m := New("test-session", testCommands)

	msg := tea.WindowSizeMsg{Width: 100, Height: 50}
	newModel, _ := m.Update(msg)
	m = newModel.(Model)

	if m.width != 100 {
		t.Errorf("Expected width 100, got %d", m.width)
	}

	if m.height != 50 {
		t.Errorf("Expected height 50, got %d", m.height)
	}
}

func TestReloadMsgClearsCommandsWhenReloadedEmpty(t *testing.T) {
	m := New("test-session", testCommands)

	updated, _ := m.Update(ReloadMsg{Commands: nil})
	result := updated.(Model)

	if len(result.commands) != 0 {
		t.Fatalf("expected commands to be cleared, got %d", len(result.commands))
	}
	if len(result.filtered) != 0 {
		t.Fatalf("expected filtered commands to be cleared, got %d", len(result.filtered))
	}
	if len(result.visualOrder) != 0 {
		t.Fatalf("expected visual order to be cleared, got %d", len(result.visualOrder))
	}
	if result.cursor != 0 {
		t.Fatalf("expected cursor reset to 0, got %d", result.cursor)
	}
}

func TestReloadMsgClearsSelectedCommandWhenRemoved(t *testing.T) {
	m := New("test-session", testCommands)
	m.selected = &m.commands[0]
	m.phase = PhaseTarget
	m.draft.Set("keep me?")

	updated, _ := m.Update(ReloadMsg{Commands: []PaletteCmd{
		{Key: "other", Label: "Other", Category: "Quick Actions", Prompt: "Something else"},
	}})
	result := updated.(Model)

	if result.selected != nil {
		t.Fatal("expected removed selected command to be cleared")
	}
	if result.phase != PhaseCommand {
		t.Fatalf("expected palette to return to command phase, got %v", result.phase)
	}
	if result.draft.Present() {
		t.Fatalf("expected edit draft to be cleared, got %q", result.draft.Text(nil))
	}
}

func TestReloadMsgRefreshesSelectedCommandPrompt(t *testing.T) {
	m := New("test-session", testCommands)
	m.selected = &m.commands[0]
	m.phase = PhaseTarget

	reloaded := append([]PaletteCmd(nil), testCommands...)
	reloaded[0].Prompt = "Fix the bug with updated context"

	updated, _ := m.Update(ReloadMsg{Commands: reloaded})
	result := updated.(Model)

	if result.selected == nil {
		t.Fatal("expected selected command to remain available after reload")
	}
	if result.selected.Prompt != "Fix the bug with updated context" {
		t.Fatalf("expected selected prompt to refresh, got %q", result.selected.Prompt)
	}
	if result.phase != PhaseTarget {
		t.Fatalf("expected phase to stay on target selection, got %v", result.phase)
	}
}

func TestRenderPreviewUsesAgentBadgeForNewerAgentCategories(t *testing.T) {
	m := New("test-session", []PaletteCmd{
		{Key: "cursor", Label: "Cursor Task", Category: "cursor", Prompt: "Do the thing"},
		{Key: "ops", Label: "Ops Task", Category: "Quick Actions", Prompt: "Do the other thing"},
	})
	m.width = 80
	m.filtered = append([]PaletteCmd(nil), m.commands...)
	m.cursor = 0

	preview := stripANSI(m.renderPreview(80))
	if !strings.Contains(preview, "CURSOR") {
		t.Fatalf("expected newer agent category to render an agent badge label, got:\n%s", preview)
	}
	if strings.Contains(preview, "Quick Actions") {
		t.Fatalf("preview should be showing the selected cursor command, got:\n%s", preview)
	}
}

func TestUpdateAnimationTick(t *testing.T) {
	t.Setenv("NTM_ANIMATIONS", "1")
	t.Setenv("TMUX", "")
	t.Setenv("STY", "")
	t.Setenv("CI", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")

	m := New("test-session", testCommands)
	initialTick := m.animTick

	msg := AnimationTickMsg{}
	newModel, cmd := m.Update(msg)
	m = newModel.(Model)

	if m.animTick != initialTick+1 {
		t.Errorf("Expected animTick to increment from %d to %d, got %d", initialTick, initialTick+1, m.animTick)
	}

	// Should return a new tick command
	if cmd == nil {
		t.Error("Animation tick should return next tick command")
	}
}

func TestNew_DisablesAnimationInTmuxByDefault(t *testing.T) {
	t.Setenv("NTM_ANIMATIONS", "")
	t.Setenv("NTM_REDUCE_MOTION", "")
	t.Setenv("TMUX", "/tmp/tmux-123/default,1,0")
	t.Setenv("STY", "")
	t.Setenv("CI", "")
	t.Setenv("TERM", "tmux-256color")
	t.Setenv("COLORTERM", "truecolor")

	m := New("test-session", testCommands)
	if m.animate {
		t.Fatal("expected palette animations to auto-disable inside tmux")
	}

	newModel, cmd := m.Update(AnimationTickMsg{})
	updated := newModel.(Model)
	if updated.animTick != 0 {
		t.Fatalf("expected animation tick to remain stable, got %d", updated.animTick)
	}
	if cmd != nil {
		t.Fatal("expected no follow-up tick command when animation is disabled")
	}
}

func TestNavigationUp(t *testing.T) {
	m := New("test-session", testCommands)
	m.cursor = 2 // Start in middle

	msg := tea.KeyMsg{Type: tea.KeyUp}
	newModel, _ := m.Update(msg)
	m = newModel.(Model)

	if m.cursor != 1 {
		t.Errorf("Expected cursor to move up to 1, got %d", m.cursor)
	}
}

func TestNavigationUpAtTop(t *testing.T) {
	m := New("test-session", testCommands)
	m.cursor = 0 // At top

	msg := tea.KeyMsg{Type: tea.KeyUp}
	newModel, _ := m.Update(msg)
	m = newModel.(Model)

	if m.cursor != 0 {
		t.Errorf("Expected cursor to stay at 0, got %d", m.cursor)
	}
}

func TestNavigationDown(t *testing.T) {
	m := New("test-session", testCommands)
	m.cursor = 0

	msg := tea.KeyMsg{Type: tea.KeyDown}
	newModel, _ := m.Update(msg)
	m = newModel.(Model)

	if m.cursor != 1 {
		t.Errorf("Expected cursor to move down to 1, got %d", m.cursor)
	}
}

func TestNavigationDownAtBottom(t *testing.T) {
	m := New("test-session", testCommands)
	m.cursor = len(testCommands) - 1 // At bottom

	msg := tea.KeyMsg{Type: tea.KeyDown}
	newModel, _ := m.Update(msg)
	m = newModel.(Model)

	if m.cursor != len(testCommands)-1 {
		t.Errorf("Expected cursor to stay at bottom, got %d", m.cursor)
	}
}

func TestNavigationUsesVisualOrderWhenCategoriesInterleave(t *testing.T) {
	commands := []PaletteCmd{
		{Key: "a", Label: "Alpha One", Category: "Alpha", Prompt: "A"},
		{Key: "b", Label: "Beta One", Category: "Beta", Prompt: "B"},
		{Key: "c", Label: "Alpha Two", Category: "Alpha", Prompt: "C"},
	}

	m := New("test-session", commands)
	m.buildVisualOrder()

	// Visual order should be [0,2,1]. Pressing down from first item should land on index 2.
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = newModel.(Model)

	if m.cursor != 2 {
		t.Fatalf("Expected cursor to follow visual order to index 2, got %d", m.cursor)
	}

	// Selecting should pick the visually selected command (key 'c').
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = newModel.(Model)

	if m.selected == nil || m.selected.Key != "c" {
		got := "<nil>"
		if m.selected != nil {
			got = m.selected.Key
		}
		t.Fatalf("Expected selected key 'c', got %s", got)
	}
}

func TestNavigationWithK(t *testing.T) {
	m := New("test-session", testCommands)
	m.cursor = 2

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}
	newModel, _ := m.Update(msg)
	m = newModel.(Model)

	// With k/j, the filter input might capture the keystroke
	// Just verify no crash
	if m.cursor < 0 {
		t.Error("Cursor should not be negative")
	}
}

func TestSelectWithEnter(t *testing.T) {
	m := New("test-session", testCommands)
	m.cursor = 1

	msg := tea.KeyMsg{Type: tea.KeyEnter}
	newModel, _ := m.Update(msg)
	m = newModel.(Model)

	if m.phase != PhaseTarget {
		t.Errorf("Expected phase to change to PhaseTarget, got %v", m.phase)
	}

	if m.selected == nil {
		t.Error("Expected selected to be set")
	}

	if m.selected.Key != testCommands[1].Key {
		t.Errorf("Expected selected command key %s, got %s", testCommands[1].Key, m.selected.Key)
	}
}

func TestSelectWithEmptyList(t *testing.T) {
	m := New("test-session", nil)

	msg := tea.KeyMsg{Type: tea.KeyEnter}
	newModel, _ := m.Update(msg)
	m = newModel.(Model)

	// Should stay in command phase since nothing to select
	if m.phase != PhaseCommand {
		t.Errorf("Expected phase to stay PhaseCommand with empty list, got %v", m.phase)
	}

	if m.selected != nil {
		t.Error("Expected selected to be nil with empty list")
	}
}

func TestQuitWithQ(t *testing.T) {
	m := New("test-session", testCommands)

	// When filter is focused (the default), 'q' should type into the filter,
	// not quit. This prevents the search bar from swallowing queries that
	// contain 'q'.
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}
	newModel, _ := m.Update(msg)
	m = newModel.(Model)

	if m.quitting {
		t.Error("Expected quitting to be false when filter is focused — 'q' should type into filter")
	}
	if m.filter.Value() != "q" {
		t.Errorf("Expected filter value 'q', got %q", m.filter.Value())
	}

	// Ctrl+C should still quit regardless of filter focus.
	m2 := New("test-session", testCommands)
	msg2 := tea.KeyMsg{Type: tea.KeyCtrlC}
	newModel2, cmd2 := m2.Update(msg2)
	m2 = newModel2.(Model)

	if !m2.quitting {
		t.Error("Expected quitting to be true after ctrl+c")
	}
	if cmd2 == nil {
		t.Error("Expected quit command from ctrl+c")
	}
}

func TestQuitWithEsc(t *testing.T) {
	m := New("test-session", testCommands)

	msg := tea.KeyMsg{Type: tea.KeyEsc}
	newModel, cmd := m.Update(msg)
	m = newModel.(Model)

	if !m.quitting {
		t.Error("Expected quitting to be true after Esc")
	}

	if cmd == nil {
		t.Error("Expected quit command")
	}
}

func TestUpdateFiltered(t *testing.T) {
	m := New("test-session", testCommands)

	// Set filter value manually
	m.filter.SetValue("fix")
	m.updateFiltered()

	if len(m.filtered) != 1 {
		t.Errorf("Expected 1 filtered command for 'fix', got %d", len(m.filtered))
	}

	if m.filtered[0].Key != "fix" {
		t.Errorf("Expected filtered command 'fix', got %s", m.filtered[0].Key)
	}
}

func TestUpdateFilteredByCategory(t *testing.T) {
	m := New("test-session", testCommands)

	m.filter.SetValue("quality")
	m.updateFiltered()

	// Should match "Code Quality" category
	if len(m.filtered) != 2 {
		t.Errorf("Expected 2 filtered commands for 'quality', got %d", len(m.filtered))
	}
}

func TestUpdateFilteredClearFilter(t *testing.T) {
	m := New("test-session", testCommands)

	// Set then clear filter
	m.filter.SetValue("fix")
	m.updateFiltered()
	m.filter.SetValue("")
	m.updateFiltered()

	if len(m.filtered) != len(testCommands) {
		t.Errorf("Expected all commands after clearing filter, got %d", len(m.filtered))
	}
}

func TestUpdateFilteredPreservesSelectionByKey(t *testing.T) {
	commands := []PaletteCmd{
		{Key: "foo", Label: "Foo", Category: "", Prompt: "Foo"},
		{Key: "bar", Label: "Bar", Category: "", Prompt: "Bar"},
		{Key: "baz", Label: "Baz", Category: "", Prompt: "Baz"},
	}

	m := New("test-session", commands)
	m.cursor = 1 // "bar"

	m.filter.SetValue("ba") // Matches "bar" and "baz"
	m.updateFiltered()

	if len(m.filtered) != 2 {
		t.Fatalf("Expected 2 filtered commands for 'ba', got %d", len(m.filtered))
	}
	if m.filtered[0].Key != "bar" || m.filtered[1].Key != "baz" {
		t.Fatalf("Unexpected filtered order: got [%s, %s]", m.filtered[0].Key, m.filtered[1].Key)
	}
	if m.cursor != 0 {
		t.Fatalf("Expected cursor to remain on 'bar' (index 0), got %d", m.cursor)
	}
}

func TestUpdateFilteredNoMatches(t *testing.T) {
	m := New("test-session", testCommands)

	m.filter.SetValue("zzzznonexistent")
	m.updateFiltered()

	if len(m.filtered) != 0 {
		t.Errorf("Expected 0 filtered for non-matching query, got %d", len(m.filtered))
	}

	// Cursor should be reset to 0 (or stay in bounds)
	if m.cursor != 0 {
		t.Errorf("Expected cursor reset to 0 with no matches, got %d", m.cursor)
	}
}

func TestBuildVisualOrder(t *testing.T) {
	m := New("test-session", testCommands)
	m.buildVisualOrder()

	// Visual order should map visual position to filtered index
	if len(m.visualOrder) != len(testCommands) {
		t.Errorf("Expected visualOrder length %d, got %d", len(testCommands), len(m.visualOrder))
	}

	// All indices should be valid
	for _, idx := range m.visualOrder {
		if idx < 0 || idx >= len(m.filtered) {
			t.Errorf("Invalid visual order index: %d", idx)
		}
	}
}

func TestBuildVisualOrderEmpty(t *testing.T) {
	m := New("test-session", nil)
	m.buildVisualOrder()

	if len(m.visualOrder) != 0 {
		t.Errorf("Expected empty visualOrder for empty commands, got %d", len(m.visualOrder))
	}
}

// TestDigitsTypeIntoFilterNotQuickSelect: 1-9 quick-select was removed (H3,
// bd-ws7-docs-ux-truth-tqh3l.3 — REJECTED decision). The filter input is
// always focused in the command phase, so digits must type into the search
// box rather than selecting items.
func TestDigitsTypeIntoFilterNotQuickSelect(t *testing.T) {
	for _, digit := range []rune{'1', '5', '9'} {
		m := New("test-session", testCommands)

		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{digit}}
		newModel, _ := m.Update(msg)
		m = newModel.(Model)

		if m.phase != PhaseCommand {
			t.Errorf("digit %q: expected to stay in PhaseCommand, got %v", digit, m.phase)
		}
		if m.selected != nil {
			t.Errorf("digit %q: expected no selection (quick-select is rejected), got %q", digit, m.selected.Key)
		}
		if m.filter.Value() != string(digit) {
			t.Errorf("digit %q: expected filter value %q, got %q", digit, string(digit), m.filter.Value())
		}
	}
}

func TestTargetPhaseBack(t *testing.T) {
	m := New("test-session", testCommands)
	m.phase = PhaseTarget
	m.selected = &testCommands[0]

	msg := tea.KeyMsg{Type: tea.KeyEsc}
	newModel, _ := m.updateTargetPhase(msg)
	m = newModel.(Model)

	if m.phase != PhaseCommand {
		t.Errorf("Expected phase to return to PhaseCommand, got %v", m.phase)
	}

	if m.selected != nil {
		t.Error("Expected selected to be cleared")
	}
}

func TestTargetPhaseQuit(t *testing.T) {
	m := New("test-session", testCommands)
	m.phase = PhaseTarget
	m.selected = &testCommands[0]

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}
	newModel, cmd := m.updateTargetPhase(msg)
	m = newModel.(Model)

	if !m.quitting {
		t.Error("Expected quitting to be true")
	}

	if cmd == nil {
		t.Error("Expected quit command")
	}
}

func TestViewCommandPhase(t *testing.T) {
	m := New("test-session", testCommands)
	m.width = 80
	m.height = 24

	view := stripANSI(m.View())

	// View should contain key elements (check for either title variation)
	if !strings.Contains(view, "nhm Command Palette") && !strings.Contains(view, "Palette") {
		t.Error("View should contain title")
	}
}

func TestHelpOverlayToggle(t *testing.T) {
	m := New("test-session", testCommands)

	// Move to PhaseTarget first (? only opens help in non-text-input phases)
	m.phase = PhaseTarget
	m.selected = &testCommands[0]

	// Open help overlay with '?'
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = newModel.(Model)
	if !m.showHelp {
		t.Fatal("Expected showHelp to be true after pressing '?'")
	}

	view := stripANSI(m.View())
	if !strings.Contains(view, "Palette Shortcuts") && !strings.Contains(view, "Keyboard Shortcuts") {
		t.Fatalf("Expected help overlay view to include title, got: %q", view)
	}

	// While help is open, other keys should not quit.
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = newModel.(Model)
	if m.quitting {
		t.Fatal("Expected palette not to quit when help overlay is open")
	}
	if !m.showHelp {
		t.Fatal("Expected help overlay to remain open after non-close key")
	}

	// Close help overlay with Esc
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = newModel.(Model)
	if m.showHelp {
		t.Fatal("Expected showHelp to be false after pressing Esc")
	}
}

func TestPreviewShowsTargetSummaryAndPromptMetadata(t *testing.T) {
	m := NewWithOptions("test-session", testCommands, Options{KindKeys: KindKeys(kinds.NewRegistry().All())})
	m.paneCountsKnown = true
	m.paneCounts = paneCounts{totalAgents: 7, byKind: map[string]int{"claude": 3, "codex": 2, "agy": 2}}

	out := stripANSI(m.renderPreview(80))
	if !strings.Contains(out, "Targets:") {
		t.Fatalf("Expected preview to include target summary, got: %q", out)
	}
	if !strings.Contains(out, "all 7") || !strings.Contains(out, "claude 3") || !strings.Contains(out, "codex 2") || !strings.Contains(out, "agy 2") {
		t.Fatalf("Expected target badges with counts, got: %q", out)
	}
	if strings.Contains(out, "grok") {
		t.Fatalf("Expected no badge for a kind with no agents, got: %q", out)
	}
	if !strings.Contains(out, "lines") || !strings.Contains(out, "chars") {
		t.Fatalf("Expected preview to include prompt metadata (lines/chars), got: %q", out)
	}
	if !strings.Contains(out, "key:") {
		t.Fatalf("Expected preview to include key metadata, got: %q", out)
	}
}

func TestPreviewSafetyNudgesIncludeDestructive(t *testing.T) {
	commands := []PaletteCmd{
		{Key: "danger", Label: "Danger", Category: "Quick Actions", Prompt: "Run `git reset --hard` then `rm -rf`"},
	}
	m := New("test-session", commands)
	m.paneCountsKnown = true
	m.paneCounts = paneCounts{totalAgents: 1}

	out := stripANSI(m.renderPreview(80))
	if !strings.Contains(strings.ToLower(out), "destructive") {
		t.Fatalf("Expected destructive nudge badge, got: %q", out)
	}
}

func TestViewTargetPhase(t *testing.T) {
	m := New("test-session", testCommands)
	m.phase = PhaseTarget
	m.selected = &testCommands[0]
	m.width = 80
	m.height = 24

	view := m.View()

	// View should contain target options
	if !strings.Contains(view, "Target") && !strings.Contains(view, "All Agents") {
		t.Log("Target phase view:", view)
		// May not contain text if rendering is complex
	}
}

func TestTargetPhaseShowsSamplePanesWhenWide(t *testing.T) {
	m := NewWithOptions("test-session", testCommands, Options{KindKeys: KindKeys(kinds.NewRegistry().All())})
	m.phase = PhaseTarget
	m.selected = &testCommands[0]
	m.paneCountsKnown = true
	m.paneCounts = countPanes([]Pane{
		{ID: "w1:p1", Index: 1, Title: "IcyKnoll", Type: "claude"},
		{ID: "w1:p2", Index: 2, Title: "BlueLake", Type: "codex"},
		{ID: "w1:p3", Index: 3, Title: "GreenOwl", Type: "agy"},
	})

	// Wide terminal enables sample pane rendering.
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 220, Height: 60})
	m = newModel.(Model)

	view := stripANSI(m.View())
	if !strings.Contains(view, "All Agents") || !strings.Contains(view, "(3)") {
		t.Fatalf("Expected target selector to include counts for All Agents, got: %q", view)
	}
	if !strings.Contains(view, "e.g.") || !strings.Contains(view, "IcyKnoll") {
		t.Fatalf("Expected target selector to include sample pane titles, got: %q", view)
	}
}

func TestViewQuitting(t *testing.T) {
	m := New("test-session", testCommands)
	m.quitting = true

	view := m.View()

	// Quitting view should be relatively empty or show exit message
	if len(view) > 1000 {
		t.Error("Quitting view should be concise")
	}
}

func TestViewQuittingWithError(t *testing.T) {
	m := New("test-session", testCommands)
	m.quitting = true
	m.err = errTestError

	view := m.View()

	if !strings.Contains(view, "Error") {
		t.Error("Quitting with error should show error message")
	}
}

func TestResult(t *testing.T) {
	m := New("test-session", testCommands)
	m.sub.answer = &SubmitMsg{Result: send.Result{Targets: []send.Receipt{{Status: send.StatusRefused}, {Status: send.StatusSubmitted}}}}
	m.err = nil

	sent, err := m.Result()

	if !sent {
		t.Error("Expected sent to be true")
	}

	if err != nil {
		t.Errorf("Expected nil error, got %v", err)
	}
}

func TestResultWithError(t *testing.T) {
	m := New("test-session", testCommands)
	m.sub.answer = &SubmitMsg{Result: send.Result{Targets: []send.Receipt{{Status: send.StatusRefused}}}}
	m.err = errTestError

	sent, err := m.Result()

	if sent {
		t.Error("Expected sent to be false")
	}

	if err != errTestError {
		t.Errorf("Expected test error, got %v", err)
	}
}

var errTestError = tea.ErrProgramKilled

// ═══════════════════════════════════════════════════════════════
// SessionSelector Tests
// ═══════════════════════════════════════════════════════════════

var testSessions = []Session{
	{ID: "w1", Name: "project1", Windows: 2, Attached: false},
	{ID: "w2", Name: "project2", Windows: 3, Attached: true},
	{ID: "w3", Name: "project3", Windows: 1, Attached: false},
}

func TestNewSessionSelector(t *testing.T) {
	s := NewSessionSelector(testSessions)

	if len(s.sessions) != len(testSessions) {
		t.Errorf("Expected %d sessions, got %d", len(testSessions), len(s.sessions))
	}

	if s.cursor != 0 {
		t.Errorf("Expected cursor at 0, got %d", s.cursor)
	}

	if s.selected != "" {
		t.Error("Expected selected to be empty initially")
	}
}

func TestNewSessionSelectorEmpty(t *testing.T) {
	s := NewSessionSelector(nil)

	if len(s.sessions) != 0 {
		t.Errorf("Expected 0 sessions, got %d", len(s.sessions))
	}
}

func TestSessionSelectorInit(t *testing.T) {
	t.Setenv("NTM_ANIMATIONS", "1")
	t.Setenv("TMUX", "")
	t.Setenv("STY", "")
	t.Setenv("CI", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")

	s := NewSessionSelector(testSessions)
	cmd := s.Init()

	if cmd == nil {
		t.Error("Init should return a command")
	}
}

func TestSessionSelectorWindowSize(t *testing.T) {
	s := NewSessionSelector(testSessions)

	msg := tea.WindowSizeMsg{Width: 100, Height: 50}
	newModel, _ := s.Update(msg)
	s = newModel.(SessionSelector)

	if s.width != 100 {
		t.Errorf("Expected width 100, got %d", s.width)
	}
}

func TestSessionSelectorNavigationUp(t *testing.T) {
	s := NewSessionSelector(testSessions)
	s.cursor = 1

	msg := tea.KeyMsg{Type: tea.KeyUp}
	newModel, _ := s.Update(msg)
	s = newModel.(SessionSelector)

	if s.cursor != 0 {
		t.Errorf("Expected cursor to move to 0, got %d", s.cursor)
	}
}

func TestSessionSelectorNavigationDown(t *testing.T) {
	s := NewSessionSelector(testSessions)
	s.cursor = 0

	msg := tea.KeyMsg{Type: tea.KeyDown}
	newModel, _ := s.Update(msg)
	s = newModel.(SessionSelector)

	if s.cursor != 1 {
		t.Errorf("Expected cursor to move to 1, got %d", s.cursor)
	}
}

func TestSessionSelectorSelect(t *testing.T) {
	s := NewSessionSelector(testSessions)
	s.cursor = 1

	msg := tea.KeyMsg{Type: tea.KeyEnter}
	newModel, cmd := s.Update(msg)
	s = newModel.(SessionSelector)

	if s.selected != "w2" {
		t.Errorf("Expected selected 'w2', got '%s'", s.selected)
	}

	if cmd == nil {
		t.Error("Expected quit command after selection")
	}
}

func TestSessionSelectorSelectEmpty(t *testing.T) {
	s := NewSessionSelector(nil)

	msg := tea.KeyMsg{Type: tea.KeyEnter}
	newModel, _ := s.Update(msg)
	s = newModel.(SessionSelector)

	if s.selected != "" {
		t.Error("Expected selected to be empty with no sessions")
	}
}

func TestSessionSelectorQuit(t *testing.T) {
	s := NewSessionSelector(testSessions)

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}
	newModel, cmd := s.Update(msg)
	s = newModel.(SessionSelector)

	if !s.quitting {
		t.Error("Expected quitting to be true")
	}

	if cmd == nil {
		t.Error("Expected quit command")
	}
}

func TestSessionSelectorSelectByNumber(t *testing.T) {
	s := NewSessionSelector(testSessions)

	if !s.selectByNumber(2) {
		t.Error("selectByNumber(2) should return true")
	}

	if s.selected != "w2" {
		t.Errorf("Expected selected 'w2', got '%s'", s.selected)
	}

	if s.cursor != 1 {
		t.Errorf("Expected cursor at 1, got %d", s.cursor)
	}
}

func TestSessionSelectorSelectByNumberOutOfRange(t *testing.T) {
	s := NewSessionSelector(testSessions)

	if s.selectByNumber(99) {
		t.Error("selectByNumber(99) should return false")
	}
}

func TestSessionSelectorView(t *testing.T) {
	s := NewSessionSelector(testSessions)
	s.width = 80
	s.height = 24

	view := stripANSI(s.View())

	// View should contain session names
	if !strings.Contains(view, "project1") {
		t.Logf("View output: %q", view)
		t.Error("View should contain session name 'project1'")
	}

	if !strings.Contains(view, "project2") {
		t.Error("View should contain session name 'project2'")
	}
}

func TestSessionSelectorViewEmpty(t *testing.T) {
	s := NewSessionSelector(nil)
	s.width = 80
	s.height = 24

	view := s.View()

	// Should show empty state message
	if !strings.Contains(view, "No herdr workspaces") {
		t.Error("View should show 'No herdr workspaces' for empty list")
	}
}

func TestSessionSelectorSelected(t *testing.T) {
	s := NewSessionSelector(testSessions)
	s.selected = "test-session"

	if s.Selected() != "test-session" {
		t.Errorf("Expected Selected() to return 'test-session', got '%s'", s.Selected())
	}
}

func TestRunSessionSelectorEmpty(t *testing.T) {
	_, err := RunSessionSelector(nil)

	if err == nil {
		t.Error("Expected error for empty sessions")
	}
}

func TestRunSessionSelectorSingleSession(t *testing.T) {
	sessions := []Session{{ID: "w9", Name: "only-one"}}

	id, err := RunSessionSelector(sessions)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if id != "w9" {
		t.Errorf("Expected 'w9', got '%s'", id)
	}
}

func TestVisualOrderPinnedThenRecents(t *testing.T) {
	m := NewWithOptions("test-session", testCommands, Options{
		PaletteState: PaletteState{
			Pinned:    []string{"docs"},
			Favorites: []string{"review"},
		},
	})

	// Simulate loaded recents (most-recent-first)
	m.recents = []string{"test", "fix"}
	m.buildVisualOrder()

	// Expected order: pinned (docs), recents (test, fix), then remaining categories (refactor, review)
	want := []int{3, 1, 0, 2, 4}
	if !reflect.DeepEqual(m.visualOrder, want) {
		t.Fatalf("Unexpected visualOrder:\nwant %v\ngot  %v", want, m.visualOrder)
	}

	out := stripANSI(m.renderCommandList(80))
	if !strings.Contains(out, "Pinned") {
		t.Fatalf("Expected command list to include 'Pinned' header:\n%s", out)
	}
	if !strings.Contains(out, "Recent") {
		t.Fatalf("Expected command list to include 'Recent' header:\n%s", out)
	}
}

func TestTogglePinAndFavoritePersistsPaletteState(t *testing.T) {
	dir := t.TempDir()

	m := NewWithOptions("test-session", testCommands, Options{
		Store: Store{Dir: dir},
	})

	// Pin the first command ("fix")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if cmd == nil {
		t.Fatalf("Expected save cmd for Ctrl+P when the store has a directory")
	}

	msg := cmd()
	if saved, ok := msg.(paletteStateSavedMsg); ok && saved.err != nil {
		t.Fatalf("Unexpected save error: %v", saved.err)
	}

	for _, name := range []string{PinsFile, FavoritesFile} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(data) != "fix\n" {
			t.Fatalf("Expected the pinned key to be persisted in %s, got %q", name, data)
		}
	}
}

func TestFetchRecentsFromTheStore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, RecentsFile), []byte("fix\ntest\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := NewWithOptions("test-session", testCommands, Options{Store: Store{Dir: dir}})
	cmd := m.fetchRecents()
	if cmd == nil {
		t.Fatalf("Expected fetchRecents cmd")
	}
	msg := cmd()
	newModel, _ := m.Update(msg)
	m = newModel.(Model)

	// Most-recent-first unique keys.
	want := []string{"fix", "test"}
	if !reflect.DeepEqual(m.recents, want) {
		t.Fatalf("Unexpected recents:\nwant %v\ngot  %v", want, m.recents)
	}
}

// ═══════════════════════════════════════════════════════════════
// KeyMap Tests
// ═══════════════════════════════════════════════════════════════

func TestKeyMapBindings(t *testing.T) {
	// Test that key bindings are properly configured
	if !key.Matches(tea.KeyMsg{Type: tea.KeyUp}, commandKeys.Up) {
		t.Error("Up key should match")
	}

	if !key.Matches(tea.KeyMsg{Type: tea.KeyDown}, commandKeys.Down) {
		t.Error("Down key should match")
	}

	if !key.Matches(tea.KeyMsg{Type: tea.KeyEnter}, commandKeys.Select) {
		t.Error("Enter key should match Select")
	}

	if !key.Matches(tea.KeyMsg{Type: tea.KeyEsc}, commandKeys.Back) {
		t.Error("Esc key should match Back")
	}

	if !key.Matches(tea.KeyMsg{Type: tea.KeyCtrlP}, commandKeys.TogglePin) {
		t.Error("Ctrl+P should match TogglePin")
	}

	if !key.Matches(tea.KeyMsg{Type: tea.KeyCtrlF}, commandKeys.ToggleFavorite) {
		t.Error("Ctrl+F should match ToggleFavorite")
	}
}

func TestSelectorKeyMapBindings(t *testing.T) {
	if !key.Matches(tea.KeyMsg{Type: tea.KeyUp}, selectorKeys.Up) {
		t.Error("Up key should match in selector")
	}

	if !key.Matches(tea.KeyMsg{Type: tea.KeyEnter}, selectorKeys.Select) {
		t.Error("Enter key should match Select in selector")
	}
}

// --- #204: command list box fits its content instead of a fixed oversized box ---

func TestCommandListBoxFitsContentWhenShort(t *testing.T) {
	m := New("test-session", testCommands)
	// Tall terminal: the list box must NOT expand to fill it when there are only
	// a handful of commands - it should size to its content (#204).
	m.width = 100
	m.height = 100
	m.syncListViewport()

	maxHeight := m.height - 14
	got := m.commandListBoxHeight()
	if got >= maxHeight {
		t.Fatalf("expected list box to fit content (< maxHeight %d), got %d", maxHeight, got)
	}
	if got < 5 {
		t.Fatalf("expected a sensible minimum height, got %d", got)
	}
}

func TestCommandListBoxCapsAndScrollsWhenLong(t *testing.T) {
	var many []PaletteCmd
	for i := 0; i < 100; i++ {
		many = append(many, PaletteCmd{
			Key:      fmtKey(i),
			Label:    fmtKey(i),
			Category: "Quick Actions",
			Prompt:   "do a thing",
		})
	}
	m := New("test-session", many)
	m.width = 100
	m.height = 40
	m.syncListViewport()

	maxHeight := m.height - 14
	if got := m.commandListBoxHeight(); got != maxHeight {
		t.Fatalf("expected long list to cap at maxHeight %d, got %d", maxHeight, got)
	}
	if m.listViewport.TotalLineCount() <= m.listViewport.Height {
		t.Fatalf("expected content to overflow the viewport so it scrolls")
	}
}

func fmtKey(i int) string {
	return "cmd-" + string(rune('a'+(i%26))) + string(rune('0'+(i/26)))
}

// --- #206: compose a free-text custom message from scratch ---

func TestComposeCustomMessageShortcut(t *testing.T) {
	m := New("test-session", testCommands)

	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m = newModel.(Model)

	if m.phase != PhaseEdit {
		t.Fatalf("expected ctrl+n to open the editor (PhaseEdit), got phase %v", m.phase)
	}
	if m.selected == nil || m.selected.Key != customMessageKey {
		t.Fatalf("expected a synthetic custom-message selection, got %+v", m.selected)
	}
	if strings.TrimSpace(m.editInput.Value()) != "" {
		t.Fatalf("expected an empty editor for a from-scratch message, got %q", m.editInput.Value())
	}

	// Type a message and confirm; it should land in the target picker with the
	// composed text captured as the draft.
	m.editInput.SetValue("please run the tests")
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = newModel.(Model)
	if m.phase != PhaseTarget {
		t.Fatalf("expected ctrl+s to advance to PhaseTarget, got %v", m.phase)
	}
	if !m.draft.Present() || m.draft.Text(m.selected) != "please run the tests" {
		t.Fatalf("expected composed text to be captured, got %q", m.draft.Text(m.selected))
	}
}

// TestSendRefusesEmptyMessage guards the #206 regression: a compose (or an
// edited-to-empty command) confirmed with nothing typed must NOT be dispatched.
// send() would otherwise fire a double-Enter with no text at every target pane,
// submitting whatever half-typed input already sits in each agent's prompt.
func TestSendRefusesEmptyMessage(t *testing.T) {
	rec := &sendRecorder{}
	m := sendModel(t, testPanes(), rec)

	// Enter the compose flow (empty prompt) and confirm without typing.
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m = newModel.(Model)
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS}) // ConfirmEdit with empty draft
	m = newModel.(Model)
	if m.phase != PhaseTarget {
		t.Fatalf("expected PhaseTarget after confirming, got %v", m.phase)
	}

	// Attempt to send. It must refuse: bounce back to the editor with a notice,
	// and NOT start a submission.
	model, _ := m.send()
	m = model.(Model)
	if m.sub.opID != "" || len(rec.requests()) != 0 {
		t.Fatalf("empty message must not be sent")
	}
	if m.phase != PhaseEdit {
		t.Fatalf("expected send() to bounce empty message back to PhaseEdit, got %v", m.phase)
	}
	if strings.TrimSpace(m.editNotice) == "" {
		t.Fatalf("expected an editNotice explaining the empty message was refused")
	}

	// A whitespace-only draft is likewise refused.
	m.draft.Set("   \n\t ")
	m.phase = PhaseTarget
	model, _ = m.send()
	m = model.(Model)
	if m.sub.opID != "" || m.phase != PhaseEdit || len(rec.requests()) != 0 {
		t.Fatalf("whitespace-only message must be refused too (op=%q phase=%v)", m.sub.opID, m.phase)
	}
}

// GH#251 phase 2: grok panes are valid targets, so a mixed claude+grok batch
// goes to every pane like claude/codex. In nhm the request names every pane.
func TestSendDeliversMixedGrokBatchToEveryPane(t *testing.T) {
	rec := &sendRecorder{}
	m := sendModel(t, testPanes()[:2], rec)
	m.phase = PhaseTarget
	m.selected = &testCommands[0]

	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	got := model.(Model)
	if got.phase != PhaseResult || cmd == nil {
		t.Fatalf("send() phase = %v, want the result screen", got.phase)
	}
	reqs := rec.waitRequests(t, 1)
	if want := []string{"w1:p1", "w1:p2"}; !reflect.DeepEqual(reqs[0].Selector.Panes, want) {
		t.Fatalf("send() targets = %v, want %v (grok pane delivered like claude)", reqs[0].Selector.Panes, want)
	}
	if reqs[0].Message != "Fix the bug" || reqs[0].Template != "" {
		t.Fatalf("send() payload = %+v", reqs[0])
	}
}

// TestOmpQuickTarget pins the palette's omp quick-select: key 7 targets only omp
// panes (the documented "6 = pick agents" binding is unchanged), the target
// rows advertise omp with its pane count and samples, and help lists the key.
func TestOmpQuickTarget(t *testing.T) {
	rec := &sendRecorder{}
	m := sendModel(t, testPanes(), rec)
	m.phase = PhaseTarget
	m.selected = &testCommands[0]
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 220, Height: 60})
	m = newModel.(Model)
	view := stripANSI(m.View())
	for _, want := range []string{"Oh My Pi (omp) (2)", "BlueLake", "7"} {
		if !strings.Contains(view, want) {
			t.Fatalf("target phase view missing %q:\n%s", want, view)
		}
	}
	if !key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'7'}}, targetKeys.TargetKind) ||
		key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'6'}}, targetKeys.TargetKind) {
		t.Fatal("key 7 must select a kind; 6 stays pick-agents")
	}
	found := false
	for _, e := range targetOverlayEntries() {
		if e.label == "2-9" {
			found = true
		}
	}
	if !found {
		t.Fatal("help overlay must list the kind quick-select keys")
	}

	pressed, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'7'}})
	got := pressed.(Model)
	if got.target != TargetKind || got.targetKind != "omp" {
		t.Fatalf("pressing 7 in the target phase set target %v %q, want omp", got.target, got.targetKind)
	}
	reqs := rec.waitRequests(t, 1)
	if want := []string{"w1:p3", "w1:p4"}; !reflect.DeepEqual(reqs[0].Selector.Panes, want) {
		t.Fatalf("omp quick target sent to %v, want the two omp panes", reqs[0].Selector.Panes)
	}
}

// --- #205: granular per-agent multi-select ---

func newAgentSelectModel() Model {
	m := New("test-session", testCommands)
	m.phase = PhaseSelectAgents
	m.selected = &testCommands[0]
	m.agentPanes = []Pane{
		{ID: "%1", Index: 1, Title: "test-session__cc_1", Type: "claude"},
		{ID: "%2", Index: 2, Title: "test-session__cod_1", Type: "codex"},
	}
	m.agentChecked = map[string]bool{"%1": true, "%2": true}
	m.agentCursor = 0
	return m
}

func TestSelectAgentsToggleAllNone(t *testing.T) {
	m := newAgentSelectModel()

	// Space toggles the pane under the cursor off.
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = newModel.(Model)
	if m.agentChecked["%1"] {
		t.Fatalf("expected space to uncheck pane under cursor")
	}
	if !m.agentChecked["%2"] {
		t.Fatalf("expected other pane to stay checked")
	}

	// 'n' clears all.
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = newModel.(Model)
	if m.agentChecked["%1"] || m.agentChecked["%2"] {
		t.Fatalf("expected 'n' to deselect all")
	}

	// 'a' selects all.
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = newModel.(Model)
	if !m.agentChecked["%1"] || !m.agentChecked["%2"] {
		t.Fatalf("expected 'a' to select all")
	}
}

func TestSelectAgentsNavigation(t *testing.T) {
	m := newAgentSelectModel()

	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = newModel.(Model)
	if m.agentCursor != 1 {
		t.Fatalf("expected cursor to move down to 1, got %d", m.agentCursor)
	}
	// Down at the bottom should clamp.
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = newModel.(Model)
	if m.agentCursor != 1 {
		t.Fatalf("expected cursor to clamp at last index, got %d", m.agentCursor)
	}
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = newModel.(Model)
	if m.agentCursor != 0 {
		t.Fatalf("expected cursor to move back to 0, got %d", m.agentCursor)
	}
}

func TestSelectAgentsBackReturnsToTarget(t *testing.T) {
	m := newAgentSelectModel()
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = newModel.(Model)
	if m.phase != PhaseTarget {
		t.Fatalf("expected esc to return to PhaseTarget, got %v", m.phase)
	}
}

func TestSelectAgentsEnterWithNoSelectionStays(t *testing.T) {
	m := newAgentSelectModel()
	m.agentChecked = map[string]bool{"%1": false, "%2": false}
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = newModel.(Model)
	if m.phase != PhaseSelectAgents {
		t.Fatalf("expected enter with nothing checked to stay in PhaseSelectAgents, got %v", m.phase)
	}
	if m.quitting {
		t.Fatalf("expected no send/quit when nothing is checked")
	}
}

func TestSelectAgentsView(t *testing.T) {
	m := newAgentSelectModel()
	m.width = 100
	m.height = 40
	view := stripANSI(m.viewSelectAgentsPhase())
	if !strings.Contains(view, "Select Agents") {
		t.Fatalf("expected title, got: %q", view)
	}
	if !strings.Contains(view, "test-session__cc_1") || !strings.Contains(view, "test-session__cod_1") {
		t.Fatalf("expected pane titles in view, got: %q", view)
	}
	if !strings.Contains(view, "[x]") {
		t.Fatalf("expected checked markers in view, got: %q", view)
	}
	if !strings.Contains(view, "2 of 2 selected") {
		t.Fatalf("expected selection count, got: %q", view)
	}
}

func TestTargetPhaseOffersSelectAgents(t *testing.T) {
	m := New("test-session", testCommands)
	m.phase = PhaseTarget
	m.selected = &testCommands[0]
	m.width = 100
	m.height = 40
	view := stripANSI(m.View())
	if !strings.Contains(view, "Select agents") {
		t.Fatalf("expected target phase to offer granular selection, got: %q", view)
	}
}

// ═══════════════════════════════════════════════════════════════
// nhm: the submission boundary and plan-v4 section 10's cases
// ═══════════════════════════════════════════════════════════════

// sendRecorder is a Sender that records requests; answer, when set,
// gives the result, else every pane is submitted.
type sendRecorder struct {
	mu     sync.Mutex
	reqs   []send.Request
	answer func(n int, req send.Request) (send.Result, error)
	block  chan struct{}
}

func (r *sendRecorder) send(_ context.Context, req send.Request) (send.Result, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	n := len(r.reqs)
	answer, block := r.answer, r.block
	r.mu.Unlock()
	if block != nil {
		<-block
	}
	if answer != nil {
		return answer(n, req)
	}
	return submittedAll(req), nil
}

func (r *sendRecorder) requests() []send.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]send.Request(nil), r.reqs...)
}

// waitRequests waits for the async submission to reach the sender.
func (r *sendRecorder) waitRequests(t *testing.T, n int) []send.Request {
	t.Helper()
	for i := 0; i < 500; i++ {
		if reqs := r.requests(); len(reqs) >= n {
			return reqs
		}
		waitABit()
	}
	t.Fatalf("the sender got %d requests, want %d", len(r.requests()), n)
	return nil
}

func waitABit() { time.Sleep(10 * time.Millisecond) }

func submittedAll(req send.Request) send.Result {
	res := send.Result{OperationID: req.OperationID}
	for i, p := range req.Selector.Panes {
		res.Targets = append(res.Targets, send.Receipt{TargetIndex: i, PaneID: p, MailName: "agent-" + p,
			Status: send.StatusSubmitted})
	}
	return res
}

func testPanes() []Pane {
	return []Pane{
		{ID: "w1:p1", Index: 1, Title: "IcyKnoll", Type: "claude", State: herdr.StatusIdle, BindingID: "b1", Generation: 1},
		{ID: "w1:p2", Index: 2, Title: "RedFox", Type: "grok", State: herdr.StatusWorking, BindingID: "b2", Generation: 4},
		{ID: "w1:p3", Index: 3, Title: "BlueLake", Type: "omp", State: herdr.StatusIdle, BindingID: "b3", Generation: 2},
		{ID: "w1:p4", Index: 4, Title: "GreenOwl", Type: "omp", State: herdr.StatusIdle, BindingID: "b4", Generation: 1},
	}
}

// sendModel is a palette wired to rec, with panes read.
func sendModel(t *testing.T, panes []Pane, rec *sendRecorder) Model {
	t.Helper()
	read := func(_ context.Context, id string) (send.Result, error) {
		return send.Result{OperationID: id, Targets: []send.Receipt{{MailName: "IcyKnoll", Status: send.StatusSubmitted}}}, nil
	}
	sub := NewSubmitter(rec.send, read, nil, func() (string, error) { return "op-test", nil })
	m := NewWithOptions("swarm", testCommands, Options{Submitter: sub, Bodies: Bodies{Dir: t.TempDir()},
		Base: send.Request{Session: "swarm", WorkspaceID: "w1"}, KindKeys: KindKeys(kinds.NewRegistry().All()),
		Store: Store{Dir: t.TempDir()}})
	m.panes, m.paneCountsKnown, m.paneCounts = panes, true, countPanes(panes)
	m.width, m.height = 120, 50
	return m
}

func press(t *testing.T, m Model, k tea.KeyMsg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(k)
	return next.(Model), cmd
}

func runeKey(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

// The targets are frozen at the keypress, with their generations, and the
// request names them: never "all", never a selector the core widens.
func TestTargetsAreFrozenAtTheKeypress(t *testing.T) {
	rec := &sendRecorder{}
	m := sendModel(t, testPanes(), rec)
	m.phase, m.selected = PhaseTarget, &testCommands[0]
	m, cmd := press(t, m, runeKey('1'))
	// A newer read arrives after the keypress: it changes nothing sent.
	next, _ := m.Update(paneCountsMsg{seq: paneReads.Add(1), panes: testPanes()[:1], counts: countPanes(testPanes()[:1])})
	m = next.(Model)
	reqs := rec.waitRequests(t, 1)
	r := reqs[0]
	if r.Selector.All || len(r.Selector.Kinds) != 0 || len(r.Selector.Panes) != 4 {
		t.Fatalf("selector = %+v", r.Selector)
	}
	want := map[string]int64{"b1": 1, "b2": 4, "b3": 2, "b4": 1}
	if !reflect.DeepEqual(r.Expect, want) {
		t.Fatalf("expect = %v, want %v", r.Expect, want)
	}
	if r.OperationID != "op-test" || r.DryRun || r.Session != "swarm" || r.WorkspaceID != "w1" {
		t.Fatalf("request = %+v", r)
	}
	ans := cmd().(SubmitMsg)
	next, _ = m.Update(ans)
	m = next.(Model)
	if m.sub.inFlight || len(m.sub.targets) != 4 {
		t.Fatalf("submission = %+v", m.sub)
	}
}

// ntm's bug: an emptied draft fell back to the template. Here the digit
// bounces to the editor and nothing is sent.
func TestAnEmptiedCommandDraftIsNeverReplacedByTheTemplate(t *testing.T) {
	rec := &sendRecorder{}
	m := sendModel(t, testPanes(), rec)
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = press(t, m, runeKey('e'))
	if m.phase != PhaseEdit || m.editInput.Value() != "Fix the bug" {
		t.Fatalf("editor = %v %q", m.phase, m.editInput.Value())
	}
	m.editInput.SetValue("")
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if !m.draft.Present() || m.draft.Text(m.selected) != "" {
		t.Fatal("the emptied draft must stay empty")
	}
	m, _ = press(t, m, runeKey('1'))
	if m.phase != PhaseEdit || m.editNotice == "" || m.editInput.Value() != "" {
		t.Fatalf("phase %v notice %q editor %q", m.phase, m.editNotice, m.editInput.Value())
	}
	if len(rec.requests()) != 0 {
		t.Fatal("the template was sent in place of the emptied draft")
	}
}

// A catalog prompt goes as a template file with its key; one submission
// per keypress, however often the key is pressed.
func TestRepeatedSubmitSendsOnce(t *testing.T) {
	rec := &sendRecorder{block: make(chan struct{})}
	m := sendModel(t, testPanes(), rec)
	m.commands[0].Source = "/catalog.md"
	m.updateFiltered()
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m, cmd := press(t, m, runeKey('7'))
	if m.phase != PhaseResult || !m.sub.inFlight {
		t.Fatalf("phase %v, in flight %v", m.phase, m.sub.inFlight)
	}
	for _, k := range []tea.KeyMsg{runeKey('7'), runeKey('1'), {Type: tea.KeyEnter}, runeKey('r')} {
		m, _ = press(t, m, k)
	}
	if m.phase != PhaseResult || !m.sub.inFlight {
		t.Fatal("a key in flight left the result screen")
	}
	close(rec.block)
	next, _ := m.Update(cmd().(SubmitMsg))
	m = next.(Model)
	if n := len(rec.requests()); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
	r := rec.requests()[0]
	if !strings.HasSuffix(r.Template, "/fix.md") || r.Message != "" {
		t.Fatalf("template request = %+v", r)
	}
	data, err := os.ReadFile(r.Template)
	if err != nil || strings.TrimSpace(string(data)) != "Fix the bug" {
		t.Fatalf("template file = %q %v", data, err)
	}
	if !reflect.DeepEqual(r.Selector.Panes, []string{"w1:p3", "w1:p4"}) {
		t.Fatalf("panes = %v", r.Selector.Panes)
	}
}

// An answer for another submission is dropped.
func TestStaleSubmitAnswerIsDropped(t *testing.T) {
	m := New("swarm", testCommands)
	m.phase = PhaseResult
	m.sub = submission{seq: 5, opID: "op-5", inFlight: true}
	next, _ := m.Update(SubmitMsg{Seq: 4, OperationID: "op-4"})
	m = next.(Model)
	if !m.sub.inFlight || m.sub.answer != nil {
		t.Fatalf("a stale answer was taken: %+v", m.sub)
	}
	next, _ = m.Update(ReconcileMsg{OperationID: "op-4"})
	if next.(Model).sub.reconciled != nil {
		t.Fatal("a reconcile for another operation was taken")
	}
}

// The result screen: each target in the core's words, the memory that was
// attached, and esc leaves.
func TestResultScreenShowsReceipts(t *testing.T) {
	rec := &sendRecorder{answer: func(_ int, req send.Request) (send.Result, error) {
		res := send.Result{OperationID: req.OperationID, MemoryInjection: enrich.Report{Enabled: true}}
		res.Targets = []send.Receipt{
			{MailName: "IcyKnoll", Kind: "claude", Status: send.StatusSubmitted},
			{MailName: "RedFox", Kind: "grok", Status: send.StatusRefused, ErrorCode: "gate_busy",
				Result: send.ReceiptResult{Reason: "the agent is working"}},
			{MailName: "BlueLake", Kind: "omp", Status: send.StatusPossiblySubmitted, Result: send.ReceiptResult{Reason: "lost_reply"}},
		}
		res.Injections = []enrich.TargetInjection{{TargetIndex: 0, Source: enrich.SourceCM,
			Items: []enrich.Item{{ID: "r1", Status: enrich.Included}, {ID: "r2", Status: enrich.Omitted}}}}
		return res, nil
	}}
	m := sendModel(t, testPanes(), rec)
	m.phase, m.selected = PhaseTarget, &testCommands[0]
	m, cmd := press(t, m, runeKey('1'))
	next, _ := m.Update(cmd().(SubmitMsg))
	m = next.(Model)
	view := stripANSI(m.View())
	for _, want := range []string{"operation op-test", "1 submitted, 1 refused or failed, 1 unknown", "IcyKnoll", "submitted",
		"RedFox", "refused: the agent is working", "BlueLake", "unknown, maybe sent", "memory: 1 CM rule(s), 0 CASS item(s) attached, for 1 target(s)"} {
		if !strings.Contains(view, want) {
			t.Fatalf("result view missing %q:\n%s", want, view)
		}
	}
	if sent, _ := m.Result(); !sent {
		t.Fatal("Result() should report a submitted target")
	}
	m, quit := press(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if !m.quitting || quit == nil {
		t.Fatal("esc must leave the result screen")
	}
}

// A lost answer is unknown, never resent; r reads what the core recorded.
func TestLostAnswerIsUnknownAndReadBack(t *testing.T) {
	rec := &sendRecorder{answer: func(_ int, req send.Request) (send.Result, error) {
		return send.Result{OperationID: req.OperationID}, errors.New("receipt write failed")
	}}
	m := sendModel(t, testPanes(), rec)
	m.phase, m.selected = PhaseTarget, &testCommands[0]
	m, cmd := press(t, m, runeKey('1'))
	next, _ := m.Update(cmd().(SubmitMsg))
	m = next.(Model)
	if !strings.Contains(stripANSI(m.View()), "The answer was lost") {
		t.Fatalf("view:\n%s", stripANSI(m.View()))
	}
	m, rcmd := press(t, m, runeKey('r'))
	if rcmd == nil || !m.sub.reconciling {
		t.Fatal("r should read the record")
	}
	next, _ = m.Update(rcmd())
	m = next.(Model)
	view := stripANSI(m.View())
	if !strings.Contains(view, "As the core recorded it") || !strings.Contains(view, "IcyKnoll") {
		t.Fatalf("reconciled view:\n%s", view)
	}
	if len(rec.requests()) != 1 {
		t.Fatal("a lost answer was sent again")
	}
}

// A refusal before the claim says nothing was sent.
func TestRefusedSendSaysNothingWasSent(t *testing.T) {
	rec := &sendRecorder{answer: func(int, send.Request) (send.Result, error) {
		return send.Result{}, errors.New("no agent matches the selection")
	}}
	m := sendModel(t, testPanes(), rec)
	m.phase, m.selected = PhaseTarget, &testCommands[0]
	m, cmd := press(t, m, runeKey('1'))
	next, _ := m.Update(cmd().(SubmitMsg))
	view := stripANSI(next.(Model).View())
	if !strings.Contains(view, "Nothing was sent: no agent matches the selection") {
		t.Fatalf("view:\n%s", view)
	}
}

// A template with a variable nothing fills: the core refuses before the
// claim; the palette asks for the value and sends again to the same
// frozen targets.
func TestUnfilledVariablesAskAndSendAgain(t *testing.T) {
	rec := &sendRecorder{answer: func(n int, req send.Request) (send.Result, error) {
		if n == 1 {
			return send.Result{}, &templates.UnresolvedVariablesError{Template: "fix", Names: []string{"thread"}}
		}
		return submittedAll(req), nil
	}}
	m := sendModel(t, testPanes(), rec)
	m.commands[0].Source, m.commands[0].Prompt = "/catalog.md", "Fix {{thread}}"
	m.updateFiltered()
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m, cmd := press(t, m, runeKey('7'))
	next, vcmd := m.Update(cmd().(SubmitMsg))
	m = next.(Model)
	if m.phase != PhaseVars || vcmd == nil {
		t.Fatalf("phase = %v, want the variables screen", m.phase)
	}
	if !strings.Contains(stripANSI(m.View()), "thread:") {
		t.Fatalf("view:\n%s", stripANSI(m.View()))
	}
	// An empty value is refused.
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.vars.notice == "" || m.phase != PhaseVars {
		t.Fatal("an empty value was accepted")
	}
	for _, r := range "nhm-1" {
		m, _ = press(t, m, runeKey(r))
	}
	m, cmd = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != PhaseResult || cmd == nil {
		t.Fatalf("phase = %v after the values", m.phase)
	}
	reqs := rec.waitRequests(t, 2)
	if reqs[1].Vars["thread"] != "nhm-1" || !reflect.DeepEqual(reqs[1].Expect, reqs[0].Expect) ||
		!reflect.DeepEqual(reqs[1].Selector.Panes, []string{"w1:p3", "w1:p4"}) {
		t.Fatalf("second request = %+v", reqs[1])
	}
	// The value belongs to that prompt: a new selection starts without it.
	next, _ = m.Update(cmd().(SubmitMsg))
	m = next.(Model)
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != PhaseTarget || len(m.vars.values) != 0 {
		t.Fatalf("phase %v, vars %v", m.phase, m.vars.values)
	}
}

// The unfilled names come from the core's error, or from its text when
// it crossed a process.
func TestUnfilledNamesFromTheError(t *testing.T) {
	typed := &templates.UnresolvedVariablesError{Template: "fix", Names: []string{"bead_id", "thread"}}
	cases := []struct {
		err  error
		want []string
	}{
		{typed, []string{"bead_id", "thread"}},
		{errors.New("send: " + typed.Error()), []string{"bead_id", "thread"}},
		{errors.New(`IcyKnoll (1.1): template "x" has unresolved variable(s): thread` + "\nmore"), []string{"thread"}},
		{errors.New("no agent matches the selection"), nil},
		{nil, nil},
	}
	for _, c := range cases {
		if got := unfilled(c.err); !reflect.DeepEqual(got, c.want) {
			t.Errorf("unfilled(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// A digit whose kind has no agent here, or no kind at all, sends nothing.
func TestKindDigitWithoutAgentsSendsNothing(t *testing.T) {
	rec := &sendRecorder{}
	m := sendModel(t, testPanes(), rec)
	m.phase, m.selected = PhaseTarget, &testCommands[0]
	m, _ = press(t, m, runeKey('8')) // opencode: none here
	if m.phase != PhaseTarget || !strings.Contains(m.targetNotice, "No bound agent") {
		t.Fatalf("phase %v notice %q", m.phase, m.targetNotice)
	}
	if len(rec.requests()) != 0 {
		t.Fatal("a digit with no agents sent")
	}
	m.kindKeys = nil
	m, _ = press(t, m, runeKey('2'))
	if !strings.Contains(m.targetNotice, "No kind has the key 2") {
		t.Fatalf("notice = %q", m.targetNotice)
	}
}

// Picking by hand sends to the checked agents only, as frozen when the
// picker opened.
func TestPickAgentsByHand(t *testing.T) {
	rec := &sendRecorder{}
	m := sendModel(t, testPanes(), rec)
	m.phase, m.selected = PhaseTarget, &testCommands[0]
	m, _ = press(t, m, runeKey('6'))
	if m.phase != PhaseSelectAgents || len(m.agentPanes) != 4 {
		t.Fatalf("picker = %v %d", m.phase, len(m.agentPanes))
	}
	m, _ = press(t, m, runeKey('n'))
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	reqs := rec.waitRequests(t, 1)
	if !reflect.DeepEqual(reqs[0].Selector.Panes, []string{"w1:p2"}) || reqs[0].Expect["b2"] != 4 || len(reqs[0].Expect) != 1 {
		t.Fatalf("request = %+v", reqs[0])
	}
	if m.phase != PhaseResult {
		t.Fatalf("phase = %v", m.phase)
	}
}

// The picker opens over the last read only; with none it says so.
func TestPickerWithoutAReadSaysSo(t *testing.T) {
	m := New("swarm", testCommands)
	m.paneCountsKnown = false
	m.phase, m.selected = PhaseTarget, &testCommands[0]
	m, cmd := press(t, m, runeKey('6'))
	if m.phase != PhaseTarget || !strings.Contains(m.targetNotice, "not read yet") || cmd == nil {
		t.Fatalf("phase %v notice %q", m.phase, m.targetNotice)
	}
}

// A pane read older than the one shown is dropped.
func TestStalePaneReadIsDropped(t *testing.T) {
	m := New("swarm", testCommands)
	old, newer := paneReads.Add(1), paneReads.Add(1)
	next, _ := m.Update(paneCountsMsg{seq: newer, panes: testPanes(), counts: countPanes(testPanes())})
	next, _ = next.(Model).Update(paneCountsMsg{seq: old, panes: nil, counts: countPanes(nil)})
	m = next.(Model)
	if len(m.panes) != 4 || m.paneCounts.totalAgents != 4 {
		t.Fatalf("an older read replaced a newer one: %d panes", len(m.panes))
	}
	next, _ = m.Update(paneCountsMsg{seq: paneReads.Add(1), err: errors.New("herdr: timeout")})
	m = next.(Model)
	m.phase, m.selected = PhaseTarget, &testCommands[0]
	if !strings.Contains(stripANSI(m.View()), "could not be read: herdr: timeout") {
		t.Fatalf("view:\n%s", stripANSI(m.View()))
	}
}

// After a send, enter goes back to the palette with a fresh draft; a
// catalog send is recorded as recent.
func TestBackToThePaletteAndRecents(t *testing.T) {
	rec := &sendRecorder{}
	m := sendModel(t, testPanes(), rec)
	m.commands[1].Source = "/catalog.md"
	m.updateFiltered()
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = press(t, m, runeKey('e'))
	m.editInput.SetValue("Run all tests twice")
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyCtrlS})
	m, cmd := press(t, m, runeKey('1'))
	next, rcmd := m.Update(cmd().(SubmitMsg))
	m = next.(Model)
	if rcmd == nil {
		t.Fatal("a catalog send should record a recent")
	}
	saved := rcmd().(recentSavedMsg)
	if saved.err != nil {
		t.Fatal(saved.err)
	}
	if recents, _ := m.store.Recents(); !reflect.DeepEqual(recents, []string{"test"}) {
		t.Fatalf("recents = %v", recents)
	}
	data, _ := os.ReadFile(rec.requests()[0].Template)
	if strings.TrimSpace(string(data)) != "Run all tests twice" {
		t.Fatalf("the edited draft was not what was sent: %q", data)
	}
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != PhaseCommand || m.selected != nil || m.draft.Present() || m.sub.opID != "" {
		t.Fatalf("after enter: phase %v selected %v", m.phase, m.selected)
	}
	if sent, _ := m.Result(); !sent {
		t.Fatal("Result() forgot the send after going back to the palette")
	}
}

// The memory preview follows the text in focus; an answer for an older
// revision is dropped.
func TestMemoryPreviewFollowsTheDraftRevision(t *testing.T) {
	var asked []string
	var mu sync.Mutex
	memory := func(_ context.Context, text string, _ MemoryModes) (Memory, error) {
		mu.Lock()
		asked = append(asked, text)
		mu.Unlock()
		return Memory{Query: text, Block: "[memory for " + text + "]", CM: enrich.Report{Enabled: true},
			Items: []enrich.Item{{Source: enrich.SourceCM, ID: "r1", Status: enrich.Included}}}, nil
	}
	sub := NewSubmitter(nil, nil, memory, nil)
	m := NewWithOptions("swarm", testCommands, Options{Submitter: sub})
	m.width, m.height = 200, 60

	m, cmd := press(t, m, tea.KeyMsg{Type: tea.KeyCtrlO})
	if !m.memory.shown || cmd == nil || !m.memory.loading {
		t.Fatal("ctrl+o should show the preview and schedule a retrieval")
	}
	first := m.memory.rev
	// The cursor moves before the retrieval starts: a new revision.
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if m.memory.rev == first || m.memory.text != "Run all tests" {
		t.Fatalf("revision %d text %q", m.memory.rev, m.memory.text)
	}
	// The old revision's tick does nothing; the current one retrieves.
	next, none := m.Update(memoryDueMsg{rev: first})
	m = next.(Model)
	if none != nil {
		t.Fatal("a stale tick started a retrieval")
	}
	next, get := m.Update(memoryDueMsg{rev: m.memory.rev})
	m = next.(Model)
	ans := get().(MemoryMsg)
	// An answer for the first revision is dropped.
	next, _ = m.Update(MemoryMsg{Rev: first, Memory: Memory{Block: "[stale]"}})
	m = next.(Model)
	if m.memory.answer != nil {
		t.Fatal("a stale answer was taken")
	}
	next, _ = m.Update(ans)
	m = next.(Model)
	preview := stripANSI(m.renderPreview(80))
	if !strings.Contains(preview, "[memory for Run all tests]") || !strings.Contains(preview, "CM: 1 rules") {
		t.Fatalf("preview:\n%s", preview)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(asked, []string{"Run all tests"}) {
		t.Fatalf("retrievals = %v", asked)
	}
	// The target phase previews the draft; an edit is a new revision.
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m.draft.Set("a new draft")
	next, _ = m.Update(tea.KeyMsg{})
	m = next.(Model)
	if m.memory.text != "a new draft" || m.memory.answer != nil {
		t.Fatalf("draft revision: text %q", m.memory.text)
	}
}

// Catalog, pane and core text reaches the screen without terminal
// controls.
func TestHostileTerminalTextIsCleaned(t *testing.T) {
	evil := "\x1b]0;owned\x07Bad\x1b[2J\x9b31m\x07"
	m := NewWithOptions("ws\x1b[5m", []PaletteCmd{{Key: "x", Label: "Label" + evil, Category: "Cat" + evil, Prompt: "Prompt" + evil}},
		Options{KindKeys: KindKeys(kinds.NewRegistry().All())})
	m.width, m.height = 200, 60
	m.paneCountsKnown, m.panes = true, []Pane{{ID: "w1:p1", Title: "Agent" + evil, Type: "claude" + evil}}
	m.paneCounts = countPanes(m.panes)
	views := []string{m.View(), m.renderPreview(80)}
	m.phase, m.selected = PhaseTarget, &m.commands[0]
	views = append(views, m.View())
	m.sub = submission{label: "L" + evil, answer: &SubmitMsg{Result: send.Result{Targets: []send.Receipt{
		{MailName: "N" + evil, Status: send.StatusRefused, Result: send.ReceiptResult{Reason: "R" + evil}}}}}}
	m.phase = PhaseResult
	views = append(views, m.View())
	for i, v := range views {
		for _, bad := range []string{"\x1b]", "\x1b[2J", "\x1b[5m", "\x07", "\x9b"} {
			if strings.Contains(v, bad) {
				t.Fatalf("view %d keeps %q", i, bad)
			}
		}
	}
}

// Small terminals render every phase without a panic.
func TestSmallTerminalSizes(t *testing.T) {
	for _, size := range [][2]int{{1, 1}, {20, 5}, {40, 10}} {
		m := sendModel(t, testPanes(), &sendRecorder{})
		next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = next.(Model)
		m.selected = &m.commands[0]
		for _, p := range []Phase{PhaseCommand, PhaseTarget, PhaseEdit, PhaseXFSearch, PhaseXFResults, PhaseSelectAgents, PhaseResult, PhaseVars} {
			m.phase = p
			if p == PhaseEdit {
				_ = m.enterEditPhase()
			}
			if p == PhaseVars {
				_ = m.enterVarsPhase([]string{"thread"})
			}
			_ = m.View()
		}
	}
}

// A selection the board handed on is the only target: 1 sends to exactly
// it, at the generations the board froze; the kind digits and the picker
// never widen it; an unbound pick is named and gets no prompt.
func TestHandedSelectionIsTheOnlyTarget(t *testing.T) {
	rec := &sendRecorder{}
	sub := NewSubmitter(rec.send, nil, nil, func() (string, error) { return "op-h", nil })
	picked := []ui.Target{{BindingID: "b3", Generation: 7, PaneID: "w1:p3", Kind: "omp"}, {PaneID: "w1:p9", Session: "id:x"}}
	m := NewWithOptions("swarm", testCommands, Options{Submitter: sub, Bodies: Bodies{Dir: t.TempDir()},
		Base: send.Request{Session: "swarm", WorkspaceID: "w1"}, KindKeys: KindKeys(kinds.NewRegistry().All()),
		Handed: picked, HandedBy: "board"})
	m.panes, m.paneCountsKnown, m.paneCounts = testPanes(), true, countPanes(testPanes())
	m.width, m.height = 140, 60
	m.phase, m.selected = PhaseTarget, &m.commands[0]
	view := stripANSI(m.View())
	for _, want := range []string{"Picked (1)", "the agents picked on the board", "To: BlueLake", "1 picked agent(s) have no binding"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Oh My Pi") || strings.Contains(view, "Select agents") {
		t.Fatalf("a handed selection offers other targets:\n%s", view)
	}
	for _, k := range []rune{'7', '2', '6'} {
		m, _ = press(t, m, runeKey(k))
		if m.phase != PhaseTarget || !strings.Contains(m.targetNotice, "The board picked the agents") {
			t.Fatalf("key %c: phase %v notice %q", k, m.phase, m.targetNotice)
		}
	}
	if len(rec.requests()) != 0 {
		t.Fatal("a key widened the handed selection")
	}
	m, _ = press(t, m, runeKey('1'))
	reqs := rec.waitRequests(t, 1)
	if !reflect.DeepEqual(reqs[0].Selector.Panes, []string{"w1:p3"}) || len(reqs[0].Expect) != 1 || reqs[0].Expect["b3"] != 7 {
		t.Fatalf("request = %+v", reqs[0])
	}
	if m.phase != PhaseResult {
		t.Fatalf("phase = %v", m.phase)
	}
}

// c and s switch CM's rules and CASS's hits for the palette's sends
// (nhm-t63): the target screen shows the state, the request carries the
// choice as --with-memory and --no-cass would, an untouched choice
// leaves the config's, and the memory preview follows the modes.
func TestMemoryChoiceForTheSends(t *testing.T) {
	rec := &sendRecorder{}
	var asked []MemoryModes
	var mu sync.Mutex
	memory := func(_ context.Context, _ string, modes MemoryModes) (Memory, error) {
		mu.Lock()
		asked = append(asked, modes)
		mu.Unlock()
		return Memory{}, nil
	}
	sub := NewSubmitter(rec.send, nil, memory, func() (string, error) { return "op-m", nil })
	base := send.Request{Session: "swarm", WorkspaceID: "w1",
		Enrich: send.Enrichment{DefaultCM: enrich.BestEffort, DefaultCASS: enrich.Off, Check: true}}
	m := NewWithOptions("swarm", testCommands, Options{Submitter: sub, Base: base, Bodies: Bodies{Dir: t.TempDir()},
		KindKeys: KindKeys(kinds.NewRegistry().All())})
	m.panes, m.paneCountsKnown, m.paneCounts = testPanes(), true, countPanes(testPanes())
	m.width, m.height = 140, 60
	m.phase, m.selected = PhaseTarget, &m.commands[0]
	if got := m.withMemoryChoice(base); !reflect.DeepEqual(got, base) {
		t.Fatalf("an untouched choice changed the request: %+v", got.Enrich)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "Memory: CM on · CASS off") {
		t.Fatalf("view:\n%s", view)
	}
	// The preview, shown, follows a switch with a new revision.
	m, _ = press(t, m, runeKey('m'))
	first := m.memory.rev
	m, _ = press(t, m, runeKey('c'))
	m, _ = press(t, m, runeKey('s'))
	if m.memory.rev == first || m.memory.modes != (MemoryModes{CM: enrich.Off, CASS: enrich.BestEffort}) {
		t.Fatalf("preview rev %d modes %+v", m.memory.rev, m.memory.modes)
	}
	next, get := m.Update(memoryDueMsg{rev: m.memory.rev})
	m = next.(Model)
	get()
	mu.Lock()
	if len(asked) != 1 || asked[0] != (MemoryModes{CM: enrich.Off, CASS: enrich.BestEffort}) {
		t.Fatalf("retrieved with %+v", asked)
	}
	mu.Unlock()
	if view := stripANSI(m.View()); !strings.Contains(view, "Memory: CM off · CASS on") {
		t.Fatalf("view:\n%s", view)
	}
	m, _ = press(t, m, runeKey('1'))
	r := rec.waitRequests(t, 1)[0]
	if r.Enrich.CM != enrich.Off || r.Enrich.CASS != enrich.BestEffort || !r.Enrich.Check ||
		r.Enrich.DefaultCM != enrich.BestEffort {
		t.Fatalf("request enrichment = %+v", r.Enrich)
	}
	// CASS off also skips the duplicate check, as --no-cass does.
	m.memChoice.CASS = enrich.Off
	if got := m.withMemoryChoice(base); got.Enrich.CASS != enrich.Off || got.Enrich.Check {
		t.Fatalf("CASS off = %+v", got.Enrich)
	}
	// A config with CASS required turns it back on as required.
	m.env.base.Enrich.DefaultCASS, m.memChoice.CASS = enrich.Required, enrich.Off
	m.toggleCASS()
	if m.memChoice.CASS != enrich.Required {
		t.Fatalf("CASS back on = %q", m.memChoice.CASS)
	}
}

// Agents with no binding are named on the target screen.
func TestUnboundAgentsAreNamed(t *testing.T) {
	m := New("swarm", testCommands)
	m.width, m.height = 120, 50
	m.phase, m.selected = PhaseTarget, &testCommands[0]
	m.paneCountsKnown, m.panesUnbound = true, 2
	if !strings.Contains(stripANSI(m.View()), "2 agent(s) here have no binding") {
		t.Fatalf("view:\n%s", stripANSI(m.View()))
	}
}
