package views

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/arosl/nhm/internal/ui/tui/theme"
)

// list is a cursor over rows that keeps the cursor in sight.
type list struct {
	cursor int
	offset int
}

// key moves the cursor for a navigation key over n rows, height of them
// in sight; it reports whether the key was one.
func (l *list) key(k string, n, height int) bool {
	page := max(1, height-1)
	switch k {
	case "up", "k":
		l.cursor--
	case "down", "j":
		l.cursor++
	case "pgup", "ctrl+u":
		l.cursor -= page
	case "pgdown", "ctrl+d":
		l.cursor += page
	case "home", "g":
		l.cursor = 0
	case "end", "G":
		l.cursor = n - 1
	default:
		return false
	}
	l.fit(n, height)
	return true
}

// fit keeps the cursor on a row and in sight.
func (l *list) fit(n, height int) {
	l.cursor = max(0, min(l.cursor, n-1))
	height = max(1, height)
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+height {
		l.offset = l.cursor - height + 1
	}
	l.offset = max(0, min(l.offset, n-height))
}

// render shows the rows in sight, each cut at width, the cursor's
// marked.
func (l *list) render(rows []string, width, height int) string {
	t := theme.Current()
	selected := lipgloss.NewStyle().Foreground(t.Primary).Bold(true)
	var out []string
	for i := l.offset; i < len(rows) && i < l.offset+max(1, height); i++ {
		line := ansi.Truncate(rows[i], max(1, width-2), "…")
		if i == l.cursor {
			out = append(out, selected.Render("▸ "+line))
		} else {
			out = append(out, "  "+line)
		}
	}
	return strings.Join(out, "\n")
}
