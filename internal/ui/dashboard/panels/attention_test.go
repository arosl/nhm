// Taken from ntm (github.com/Dicklesworthstone/ntm) internal/tui/dashboard/panels/attention_test.go
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: this header; the local Actionability for robot's; string
// cursors and source panes (attention.go's boundary edits). The tests of
// SelectNearestCursor are gone with it; nhm's
// TestAttentionPanelSelectionKeepsItsPlaceWhenItsItemGoes takes the place
// of TestAttentionPanelSetDataFallsBackToNearestCursorWhenSelectionRemoved.

package panels

import (
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestNewAttentionPanel(t *testing.T) {
	panel := NewAttentionPanel()
	if panel == nil {
		t.Fatal("NewAttentionPanel returned nil")
	}

	cfg := panel.Config()
	if cfg.ID != "attention" {
		t.Fatalf("Config().ID = %q, want %q", cfg.ID, "attention")
	}
	if cfg.Title != "Attention" {
		t.Fatalf("Config().Title = %q, want %q", cfg.Title, "Attention")
	}
}

func TestAttentionPanelViewFeedInactive(t *testing.T) {
	panel := NewAttentionPanel()
	panel.SetSize(80, 18)
	panel.SetData(nil, false)

	view := panel.View()
	if !strings.Contains(view, "Feed not active") {
		t.Fatalf("expected inactive attention panel title, got %q", view)
	}
	if !strings.Contains(view, "Attention Feed not active") {
		t.Fatalf("expected inactive attention panel description, got %q", view)
	}
}

func TestAttentionPanelViewAllClear(t *testing.T) {
	panel := NewAttentionPanel()
	panel.SetSize(80, 18)
	panel.SetData(nil, true)

	view := panel.View()
	if !strings.Contains(view, "All clear") {
		t.Fatalf("expected all-clear title, got %q", view)
	}
	if !strings.Contains(view, "No attention items") {
		t.Fatalf("expected empty-state description, got %q", view)
	}
}

func TestAttentionPanelSelectedItemAndCursorClamp(t *testing.T) {
	panel := NewAttentionPanel()
	panel.SetData([]AttentionItem{
		{Summary: "first", Actionability: ActionabilityInteresting, Timestamp: time.Now()},
	}, true)
	panel.cursor = 5
	panel.SetData(panel.items, true)

	item := panel.SelectedItem()
	if item == nil || item.Summary != "first" {
		t.Fatalf("SelectedItem() = %+v, want first item", item)
	}
}

func TestAttentionPanelSetDataSortsActionabilityAndRecency(t *testing.T) {
	panel := NewAttentionPanel()
	base := time.Date(2026, 3, 21, 12, 0, 0, 0, time.UTC)

	panel.SetData([]AttentionItem{
		{Summary: "interesting-old", Actionability: ActionabilityInteresting, Timestamp: base},
		{Summary: "action-new", Actionability: ActionabilityActionRequired, Timestamp: base.Add(2 * time.Minute)},
		{Summary: "interesting-new", Actionability: ActionabilityInteresting, Timestamp: base.Add(3 * time.Minute)},
	}, true)

	if got := panel.items[0].Summary; got != "action-new" {
		t.Fatalf("expected action_required item first, got %q", got)
	}
	if got := panel.items[1].Summary; got != "interesting-new" {
		t.Fatalf("expected newer interesting item second, got %q", got)
	}
}

func TestAttentionPanelNavigationMovesSelection(t *testing.T) {
	panel := NewAttentionPanel()
	panel.SetSize(80, 18)
	panel.SetData([]AttentionItem{
		{Summary: "first", Actionability: ActionabilityInteresting, Timestamp: time.Now()},
		{Summary: "second", Actionability: ActionabilityInteresting, Timestamp: time.Now().Add(-2 * time.Minute)},
	}, true)
	panel.Focus()

	updated, cmd := panel.Update(tea.KeyMsg{Type: tea.KeyDown})
	if cmd != nil {
		t.Fatalf("expected nil command from navigation update, got %v", cmd)
	}

	got := updated.(*AttentionPanel).SelectedItem()
	if got == nil || got.Summary != "second" {
		t.Fatalf("selected item after down key = %+v, want second", got)
	}
}

func TestAttentionPanelKeybindingsIncludeZoomToSource(t *testing.T) {
	panel := NewAttentionPanel()
	bindings := panel.Keybindings()

	found := false
	for _, binding := range bindings {
		if binding.Action == "zoom_to_source" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected zoom_to_source keybinding")
	}
}

func TestAttentionPanelSelectCursor(t *testing.T) {
	panel := NewAttentionPanel()
	panel.SetData([]AttentionItem{
		{Summary: "older", Cursor: "10", Timestamp: time.Now().Add(-2 * time.Minute)},
		{Summary: "newer", Cursor: "25", Timestamp: time.Now()},
	}, true)

	if !panel.SelectCursor("25") {
		t.Fatal("expected SelectCursor to find cursor 25")
	}
	if got := panel.SelectedItem(); got == nil || got.Cursor != "25" {
		t.Fatalf("selected item = %+v, want cursor 25", got)
	}
}

func TestAttentionPanelScrollsSelectedItemIntoView(t *testing.T) {
	panel := NewAttentionPanel()
	panel.SetSize(40, 10)
	panel.Focus()

	items := make([]AttentionItem, 0, 8)
	now := time.Now()
	for i := 0; i < 8; i++ {
		items = append(items, AttentionItem{
			Summary:       "item",
			Actionability: ActionabilityInteresting,
			Timestamp:     now.Add(time.Duration(i) * time.Minute),
			SourcePane:    "0." + strconv.Itoa(i),
			SourceAgent:   "codex",
			Cursor:        strconv.Itoa(i + 1),
		})
	}
	panel.SetData(items, true)

	for i := 0; i < len(items)-1; i++ {
		panel.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	view := panel.View()

	if panel.scroll == nil {
		t.Fatal("expected scroll panel to be initialized")
	}
	if got := panel.scroll.YOffset(); got == 0 {
		t.Fatalf("expected scroll offset to move for deep selection, got %d", got)
	}
	if !strings.Contains(view, "%") {
		t.Fatalf("expected attention view to include scroll percent badge, got %q", view)
	}
}

// =============================================================================
// Overlay-Feed Integration Tests (br-rb9oj)
// =============================================================================

func TestAttentionPanelTruncationAwareness(t *testing.T) {
	t.Parallel()

	panel := NewAttentionPanel()
	panel.SetSize(80, 10) // Small height to force truncation awareness

	// Create more items than can display
	items := make([]AttentionItem, 20)
	now := time.Now()
	for i := range items {
		items[i] = AttentionItem{
			Summary:       "item " + string(rune('A'+i)),
			Actionability: ActionabilityInteresting,
			Timestamp:     now.Add(time.Duration(i) * time.Minute),
			Cursor:        strconv.Itoa(i + 1),
		}
	}
	panel.SetData(items, true)

	// Panel should be aware it has more items than visible
	if len(panel.items) != 20 {
		t.Errorf("expected 20 items in panel data, got %d", len(panel.items))
	}

	// Scroll should be initialized for overflow handling
	if panel.scroll == nil {
		t.Fatal("expected scroll panel to be initialized for overflow")
	}

	t.Logf("TRUNCATION_AWARENESS items=%d scroll_initialized=%v", len(panel.items), panel.scroll != nil)
}

func TestAttentionPanelCursorContinuityAcrossUpdates(t *testing.T) {
	t.Parallel()

	panel := NewAttentionPanel()
	panel.SetSize(80, 18)

	// Initial data with cursor 50 selected
	items1 := []AttentionItem{
		{Summary: "item-a", Cursor: "40", Timestamp: time.Now().Add(-2 * time.Minute)},
		{Summary: "item-b", Cursor: "50", Timestamp: time.Now()},
		{Summary: "item-c", Cursor: "60", Timestamp: time.Now().Add(time.Minute)},
	}
	panel.SetData(items1, true)
	panel.SelectCursor("50")

	selected1 := panel.SelectedItem()
	if selected1 == nil || selected1.Cursor != "50" {
		t.Fatalf("initial selection = %+v, want cursor 50", selected1)
	}

	// Update with same cursor still present
	items2 := []AttentionItem{
		{Summary: "item-b", Cursor: "50", Timestamp: time.Now()},
		{Summary: "item-d", Cursor: "70", Timestamp: time.Now().Add(2 * time.Minute)},
	}
	panel.SetData(items2, true)

	// Selection should persist across update (cursor 50 still exists)
	selected2 := panel.SelectedItem()
	if selected2 == nil || selected2.Cursor != "50" {
		t.Fatalf("expected selection to remain on cursor 50 after update, got %+v", selected2)
	}
}

func TestAttentionPanelSetDataPreservesSelectionAcrossInsertedItems(t *testing.T) {
	t.Parallel()

	panel := NewAttentionPanel()
	panel.SetSize(80, 18)

	items1 := []AttentionItem{
		{Summary: "oldest", Cursor: "10", Timestamp: time.Now().Add(-3 * time.Minute)},
		{Summary: "target", Cursor: "20", Timestamp: time.Now().Add(-2 * time.Minute)},
	}
	panel.SetData(items1, true)
	if !panel.SelectCursor("20") {
		t.Fatal("expected initial selection on cursor 20")
	}

	items2 := []AttentionItem{
		{Summary: "newest", Cursor: "40", Timestamp: time.Now()},
		{Summary: "newer", Cursor: "30", Timestamp: time.Now().Add(-time.Minute)},
		{Summary: "target", Cursor: "20", Timestamp: time.Now().Add(-2 * time.Minute)},
	}
	panel.SetData(items2, true)

	selected := panel.SelectedItem()
	if selected == nil || selected.Cursor != "20" {
		t.Fatalf("expected selection to stay on cursor 20 after insertions, got %+v", selected)
	}
}

// nhm: a key has no nearest key; when the selected item goes, the
// selection keeps its place in the list.
func TestAttentionPanelSelectionKeepsItsPlaceWhenItsItemGoes(t *testing.T) {
	t.Parallel()

	panel := NewAttentionPanel()
	panel.SetSize(80, 18)

	items1 := []AttentionItem{
		{Summary: "older", Cursor: "a", Timestamp: time.Now().Add(-3 * time.Minute)},
		{Summary: "target", Cursor: "b", Timestamp: time.Now().Add(-2 * time.Minute)},
		{Summary: "newer", Cursor: "c", Timestamp: time.Now().Add(-time.Minute)},
	}
	panel.SetData(items1, true)
	if !panel.SelectCursor("b") {
		t.Fatal("expected initial selection on cursor b")
	}

	items2 := []AttentionItem{
		{Summary: "older", Cursor: "a", Timestamp: time.Now().Add(-3 * time.Minute)},
		{Summary: "newer", Cursor: "c", Timestamp: time.Now()},
	}
	panel.SetData(items2, true)

	selected := panel.SelectedItem()
	if selected == nil || selected.Summary != "older" {
		t.Fatalf("expected the selection to stay second (older), got %+v", selected)
	}
}

func TestAttentionPanelGracefulDegradationWhenFeedUnavailable(t *testing.T) {
	t.Parallel()

	panel := NewAttentionPanel()
	panel.SetSize(80, 18)

	// Set feed as unavailable
	panel.SetData(nil, false)

	view := panel.View()

	// Should show user-friendly unavailable state
	if !strings.Contains(view, "not active") && !strings.Contains(view, "unavailable") {
		t.Logf("WARN: view doesn't clearly indicate feed unavailability: %s", view)
	}

	// Should not panic or show broken state
	if strings.Contains(view, "nil") || strings.Contains(view, "panic") {
		t.Fatalf("view shows broken state: %s", view)
	}

	// SelectedItem should return nil gracefully
	if item := panel.SelectedItem(); item != nil {
		t.Fatalf("expected nil selected item when feed unavailable, got %+v", item)
	}

	t.Logf("GRACEFUL_DEGRADATION feed_available=false view_ok=true")
}

func TestAttentionPanelItemCountTracking(t *testing.T) {
	t.Parallel()

	panel := NewAttentionPanel()
	panel.SetSize(80, 18)

	// Test empty state
	panel.SetData([]AttentionItem{}, true)
	if len(panel.items) != 0 {
		t.Errorf("expected 0 items after setting empty data, got %d", len(panel.items))
	}

	// Test with items
	items := []AttentionItem{
		{Summary: "item-1", Actionability: ActionabilityActionRequired, Timestamp: time.Now()},
		{Summary: "item-2", Actionability: ActionabilityInteresting, Timestamp: time.Now()},
		{Summary: "item-3", Actionability: ActionabilityInteresting, Timestamp: time.Now()},
	}
	panel.SetData(items, true)

	// Item count should be tracked correctly
	if len(panel.items) != 3 {
		t.Errorf("expected 3 items after SetData, got %d", len(panel.items))
	}

	t.Logf("ITEM_COUNT_TRACKING empty=%d with_items=%d", 0, len(panel.items))
}

func TestAttentionPanelFocusTransitionsPreserveSelection(t *testing.T) {
	t.Parallel()

	panel := NewAttentionPanel()
	panel.SetSize(80, 18)

	items := []AttentionItem{
		{Summary: "first", Cursor: "10", Timestamp: time.Now()},
		{Summary: "second", Cursor: "20", Timestamp: time.Now().Add(-time.Minute)},
	}
	panel.SetData(items, true)
	panel.SelectCursor("20")

	// Get initial selection
	before := panel.SelectedItem()
	if before == nil || before.Cursor != "20" {
		t.Fatalf("expected cursor 20 selected, got %+v", before)
	}

	// Blur and refocus
	panel.Blur()
	panel.Focus()

	// Selection should persist
	after := panel.SelectedItem()
	if after == nil || after.Cursor != before.Cursor {
		t.Fatalf("selection changed after focus transition: before=%+v after=%+v", before, after)
	}

	t.Logf("FOCUS_TRANSITION before=%s after=%s", before.Cursor, after.Cursor)
}

func TestAttentionPanelActionabilityPriorityInSorting(t *testing.T) {
	t.Parallel()

	panel := NewAttentionPanel()
	base := time.Date(2026, 3, 21, 12, 0, 0, 0, time.UTC)

	// Mix of actionability levels with varying timestamps
	items := []AttentionItem{
		{Summary: "interesting-newest", Actionability: ActionabilityInteresting, Timestamp: base.Add(10 * time.Minute)},
		{Summary: "action-oldest", Actionability: ActionabilityActionRequired, Timestamp: base},
		{Summary: "interesting-oldest", Actionability: ActionabilityInteresting, Timestamp: base.Add(-10 * time.Minute)},
		{Summary: "action-newest", Actionability: ActionabilityActionRequired, Timestamp: base.Add(5 * time.Minute)},
	}

	panel.SetData(items, true)

	// ActionRequired items should come first regardless of timestamp
	if len(panel.items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(panel.items))
	}

	// First two should be ActionRequired
	for i := 0; i < 2; i++ {
		if panel.items[i].Actionability != ActionabilityActionRequired {
			t.Errorf("items[%d] actionability = %v, want ActionRequired", i, panel.items[i].Actionability)
		}
	}

	// Within ActionRequired, newest should come first
	if panel.items[0].Summary != "action-newest" {
		t.Errorf("items[0] = %q, want action-newest (newest ActionRequired first)", panel.items[0].Summary)
	}

	t.Logf("ACTIONABILITY_PRIORITY order=%v", []string{
		panel.items[0].Summary, panel.items[1].Summary, panel.items[2].Summary, panel.items[3].Summary,
	})
}

func TestAttentionPanelEmptyStateIsDistinctFromUnavailable(t *testing.T) {
	t.Parallel()

	panel := NewAttentionPanel()
	panel.SetSize(80, 18)

	// Empty but available (all clear)
	panel.SetData([]AttentionItem{}, true)
	emptyView := panel.View()

	// Unavailable (feed not running)
	panel.SetData(nil, false)
	unavailableView := panel.View()

	// Views should be different
	if emptyView == unavailableView {
		t.Fatalf("empty and unavailable views should be distinct:\nempty: %s\nunavailable: %s", emptyView, unavailableView)
	}

	// Empty should indicate "all clear" or similar
	if !strings.Contains(strings.ToLower(emptyView), "clear") && !strings.Contains(strings.ToLower(emptyView), "no ") {
		t.Logf("WARN: empty view doesn't clearly indicate all-clear state")
	}

	// Unavailable should indicate the feed is not running
	if !strings.Contains(strings.ToLower(unavailableView), "not") {
		t.Logf("WARN: unavailable view doesn't clearly indicate feed not running")
	}

	t.Logf("EMPTY_VS_UNAVAILABLE distinct=true")
}

// nhm: an item with no time shows no age (nhm's attention items have
// none), and a slot names the pane.
func TestAttentionPanelItemWithoutTimeHasNoAge(t *testing.T) {
	t.Parallel()

	panel := NewAttentionPanel()
	panel.SetSize(80, 18)
	panel.SetData([]AttentionItem{{Summary: "BlueLake is blocked", Actionability: ActionabilityActionRequired,
		SourcePane: "0.2", SourceAgent: "claude", Cursor: "agent_blocked|w1:p2"}}, true)
	view := panel.View()
	if strings.Contains(view, "ago") || strings.Contains(view, "just now") {
		t.Fatalf("an item with no time shows an age: %q", view)
	}
	if !strings.Contains(view, "pane 0.2 (claude)") {
		t.Fatalf("the source is missing: %q", view)
	}
}
