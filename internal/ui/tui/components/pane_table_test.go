// Taken from ntm (github.com/Dicklesworthstone/ntm) internal/tui/components/pane_table_test.go
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: this header; the import path.

package components

import (
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/ui/tui/theme"
)

func TestPaneTableViewIncludesColumnsAndRows(t *testing.T) {
	table := NewPaneTable(theme.Current())
	table.SetSize(80, 10) // wider/taller to ensure all columns and rows visible
	table.SetRows([]PaneTableRow{
		{PaneIndex: 1, Type: "cc", Status: "working", ContextPct: 72, Model: "sonnet", Command: "go test ./..."},
		{PaneIndex: 2, Type: "cod", Status: "idle", ContextPct: 15, Model: "gpt-5.4", Command: "rg TODO"},
	})
	table.Select(1)

	rendered := table.View()
	for _, want := range []string{"Type", "Status", "Ctx%", "Command", "go test", "gpt-5.4"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("pane table view missing %q in %q", want, rendered)
		}
	}
}

func TestPaneTableTruncatesLongCommands(t *testing.T) {
	table := NewPaneTable(theme.Current())
	table.SetSize(60, 6) // wide enough for columns, narrow enough to truncate long command
	table.SetRows([]PaneTableRow{
		{
			PaneIndex:  1,
			Type:       "cc",
			Status:     "rate_limited",
			ContextPct: 91,
			Model:      "claude-sonnet-4.5",
			Command:    "this-is-a-very-long-command-that-should-not-fit-verbatim",
		},
	})

	rendered := table.View()
	if !strings.Contains(rendered, "RATE") {
		t.Fatalf("expected abbreviated status label in %q", rendered)
	}
	if strings.Contains(rendered, "this-is-a-very-long-command-that-should-not-fit-verbatim") {
		t.Fatalf("expected command truncation in %q", rendered)
	}
	if !strings.Contains(rendered, "…") {
		t.Fatalf("expected ellipsis in %q", rendered)
	}
}

func TestPaneTableRendersMultiWindowAddressesForSameLocalIndex(t *testing.T) {
	table := NewPaneTable(theme.Current())
	table.SetSize(80, 10)
	table.SetRows([]PaneTableRow{
		{PaneIndex: 0, PaneAddress: "0.0", Type: "cc", Status: "working", ContextPct: 70, Model: "sonnet"},
		{PaneIndex: 0, PaneAddress: "1.0", Type: "cod", Status: "idle", ContextPct: 20, Model: "gpt-5.4"},
	})

	rendered := table.View()
	for _, address := range []string{"0.0", "1.0"} {
		if !strings.Contains(rendered, address) {
			t.Fatalf("pane table omitted physical address %q in %q", address, rendered)
		}
	}
}
