// Taken from ntm (github.com/Dicklesworthstone/ntm) internal/tui/components/pane_table.go
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: this header; the import path.

package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"

	"github.com/arosl/nhm/internal/ui/tui/theme"
)

// PaneTableRow describes a single pane row for the dashboard table view.
type PaneTableRow struct {
	PaneIndex   int
	PaneAddress string
	Type        string
	Status      string
	ContextPct  float64
	Model       string
	Command     string
}

// PaneTable wraps bubbles/table.Model for the dashboard pane list.
type PaneTable struct {
	theme       theme.Theme
	table       table.Model
	rows        []PaneTableRow
	width       int
	height      int
	initialized bool
}

// NewPaneTable creates a new pane table with dashboard styling.
func NewPaneTable(t theme.Theme) *PaneTable {
	return &PaneTable{
		theme: t,
		width: 40,
	}
}

// SetSize updates the pane table dimensions.
func (p *PaneTable) SetSize(width, height int) {
	if width > 0 {
		p.width = width
	}
	if height > 0 {
		p.height = height
	}
	p.rebuild()
}

// SetRows replaces the table contents while preserving the current cursor when possible.
func (p *PaneTable) SetRows(rows []PaneTableRow) {
	nextRows := make([]PaneTableRow, len(rows))
	copy(nextRows, rows)
	p.rows = nextRows
	p.rebuild()
}

// Select moves the selected row to idx, clamping it to the available range.
func (p *PaneTable) Select(idx int) {
	if !p.initialized || len(p.rows) == 0 {
		return
	}
	p.table.SetCursor(clamp(idx, 0, len(p.rows)-1))
}

// View renders the pane table.
func (p *PaneTable) View() string {
	if !p.initialized {
		p.rebuild()
	}
	return p.table.View()
}

func (p *PaneTable) rebuild() {
	width := p.width
	if width < 35 {
		width = 35
	}

	cursor := 0
	if p.initialized {
		cursor = p.table.Cursor()
	}

	cols := p.columns(width)
	rows := p.renderRows(cols)

	styles := table.DefaultStyles()
	styles.Header = styles.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(p.theme.Surface1).
		BorderBottom(true).
		Bold(true).
		Foreground(p.theme.Lavender)
	styles.Selected = styles.Selected.
		Foreground(p.theme.Text).
		Background(p.theme.Surface0).
		Bold(false)
	styles.Cell = styles.Cell.Foreground(p.theme.Subtext)

	tableHeight := len(rows) + 1 // account for the header row in bubbles/table
	if p.height > 0 && tableHeight > p.height {
		tableHeight = p.height
	}

	p.table = table.New(
		table.WithColumns(cols),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithHeight(tableHeight),
		table.WithWidth(width),
		table.WithStyles(styles),
	)
	if len(p.rows) > 0 {
		p.table.SetCursor(clamp(cursor, 0, len(p.rows)-1))
	}
	p.initialized = true
}

func (p *PaneTable) columns(width int) []table.Column {
	const (
		indexWidth   = 3
		typeWidth    = 4
		statusWidth  = 7
		contextWidth = 5
		minModel     = 6
		maxModel     = 10
		minCommand   = 8
	)

	modelWidth := maxModel
	commandWidth := width - indexWidth - typeWidth - statusWidth - contextWidth - modelWidth
	if commandWidth < minCommand {
		modelWidth -= (minCommand - commandWidth)
	}
	if modelWidth < minModel {
		modelWidth = minModel
	}
	commandWidth = width - indexWidth - typeWidth - statusWidth - contextWidth - modelWidth
	if commandWidth < minCommand {
		commandWidth = minCommand
	}

	return []table.Column{
		{Title: "#", Width: indexWidth},
		{Title: "Type", Width: typeWidth},
		{Title: "Status", Width: statusWidth},
		{Title: "Ctx%", Width: contextWidth},
		{Title: "Model", Width: modelWidth},
		{Title: "Command", Width: commandWidth},
	}
}

func (p *PaneTable) renderRows(cols []table.Column) []table.Row {
	if len(p.rows) == 0 {
		return nil
	}

	rows := make([]table.Row, 0, len(p.rows))
	for _, row := range p.rows {
		address := row.PaneAddress
		if address == "" {
			address = fmt.Sprintf("%d", row.PaneIndex)
		}
		rows = append(rows, table.Row{
			address,
			truncateCell(row.Type, cols[1].Width),
			truncateCell(statusLabel(row.Status), cols[2].Width),
			fmt.Sprintf("%.0f%%", row.ContextPct),
			truncateCell(row.Model, cols[4].Width),
			truncateCell(row.Command, cols[5].Width),
		})
	}
	return rows
}

func statusLabel(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "working":
		return "WORK"
	case "idle":
		return "IDLE"
	case "error":
		return "ERROR"
	case "compacted":
		return "CMPCT"
	case "rate_limited":
		return "RATE"
	default:
		if status == "" {
			return "UNK"
		}
		return strings.ToUpper(status)
	}
}

func truncateCell(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}

	var b strings.Builder
	current := 0
	for _, r := range value {
		rw := lipgloss.Width(string(r))
		if current+rw+1 > width {
			break
		}
		b.WriteRune(r)
		current += rw
	}
	return b.String() + "…"
}

func clamp(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
