package dashboard

import (
	"strings"

	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/ui/board"
	"github.com/arosl/nhm/internal/ui/dashboard/panels"
)

// PanelID names a panel of the board (focus.go's ring).
type PanelID int

// The board's panels (D5, plan-v4 section 7).
const (
	// PanelPaneList is the agent table, with the preview under it.
	PanelPaneList PanelID = iota
	// PanelAttention is ntm's attention panel on snapshot.Attention.
	PanelAttention
	// PanelHistory is ntm's history panel on the send history.
	PanelHistory
)

// wideBoard is the width from which the side panels show beside the
// agent table; tallBoard is the height from which history shows under
// attention.
const (
	wideBoard = 110
	tallBoard = 30
)

// visiblePanels are the panels the layout shows now, in focus order.
func (m *Model) visiblePanels() []PanelID {
	out := []PanelID{PanelPaneList}
	if m.width >= wideBoard {
		out = append(out, PanelAttention)
		if m.history != nil && m.height >= tallBoard {
			out = append(out, PanelHistory)
		}
	}
	return out
}

// severityActionability is nhm's attention severity in ntm's terms.
func severityActionability(sev string) panels.Actionability {
	switch sev {
	case snapshot.SevCritical:
		return panels.ActionabilityActionRequired
	case snapshot.SevWarning:
		return panels.ActionabilityInteresting
	default:
		return panels.ActionabilityBackground
	}
}

// attentionItems are the board's attention items as ntm's panel shows
// them, and the pane each item names (for zoom), by item key.
func attentionItems(m *snapshot.Model) ([]panels.AttentionItem, map[string]string) {
	if m == nil {
		return nil, nil
	}
	kinds := map[string]string{}
	for _, a := range m.Agents {
		kinds[a.PaneID] = a.Kind
	}
	var items []panels.AttentionItem
	panes := map[string]string{}
	for _, it := range snapshot.Attention(m) {
		key := strings.Join([]string{it.Kind, it.BindingID, it.PaneID, it.OperationID}, "|")
		src := it.Slot
		if src == "" {
			src = it.PaneID
		}
		summary := it.Detail
		if it.MailName != "" && !strings.Contains(summary, it.MailName) {
			summary = it.MailName + ": " + summary
		}
		items = append(items, panels.AttentionItem{Summary: board.Clean(summary), Actionability: severityActionability(it.Severity),
			SourcePane: board.Clean(src), SourceAgent: board.Clean(kinds[it.PaneID]), Cursor: key})
		if it.PaneID != "" {
			panes[key] = it.PaneID
		}
	}
	return items, panes
}

// attentionCounts sums attention up for the narrow header.
func attentionCounts(items []panels.AttentionItem) (action, interesting int) {
	for _, it := range items {
		switch it.Actionability {
		case panels.ActionabilityActionRequired:
			action++
		case panels.ActionabilityInteresting:
			interesting++
		}
	}
	return action, interesting
}
