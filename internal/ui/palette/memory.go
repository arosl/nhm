package palette

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wordwrap"

	"github.com/arosl/nhm/internal/core/enrich"
	"github.com/arosl/nhm/internal/ui/tui/icons"
	"github.com/arosl/nhm/internal/ui/tui/theme"
)

// memoryDelay lets the text in focus settle before a memory preview
// starts, so moving through the list or typing does not run cm and cass
// per step.
const memoryDelay = 300 * time.Millisecond

// memoryBlockLines is how many lines of the block the preview shows.
const memoryBlockLines = 12

// memoryRevs numbers the memory previews' revisions across every preview
// in the process (the palette's and a composer's), so an answer never
// lands in the wrong one.
var memoryRevs atomic.Uint64

// memoryState is a memory preview (plan-v4 section 5, decided
// 2026-10-13: a toggle, bound to the draft's revision only): whether it
// is shown, the text in focus and that text's revision, and the answer
// for that revision. An answer for another revision is dropped.
type memoryState struct {
	shown   bool
	rev     uint64
	text    string
	loading bool
	answer  *MemoryMsg
}

// memoryDueMsg starts the preview of revision rev if it is still current.
type memoryDueMsg struct {
	rev uint64
}

// toggle shows or hides the preview. A hidden preview keeps its last
// answer.
func (s *memoryState) toggle() {
	s.shown = !s.shown
}

// follow starts a new revision when the preview is shown and the text in
// focus (ok: there is one) changed. The retrieval starts after
// memoryDelay.
func (s *memoryState) follow(text string, ok bool) tea.Cmd {
	if !s.shown || !ok || (s.rev != 0 && text == s.text) {
		return nil
	}
	s.rev = memoryRevs.Add(1)
	s.text = text
	s.answer = nil
	s.loading = strings.TrimSpace(text) != ""
	if !s.loading {
		return nil
	}
	rev := s.rev
	return tea.Tick(memoryDelay, func(time.Time) tea.Msg { return memoryDueMsg{rev: rev} })
}

// update handles the preview's messages: it starts the retrieval of the
// current revision through sub, and takes the answer for it.
func (s *memoryState) update(msg tea.Msg, sub *Submitter) tea.Cmd {
	switch msg := msg.(type) {
	case memoryDueMsg:
		if msg.rev != s.rev || !s.loading {
			return nil
		}
		if sub == nil {
			s.loading = false
			s.answer = &MemoryMsg{Rev: msg.rev, Err: fmt.Errorf("no memory retrieval here")}
			return nil
		}
		_, cmd := sub.Memory(s.text, msg.rev)
		return cmd
	case MemoryMsg:
		if msg.Rev != s.rev {
			// Made for another revision of the text.
			return nil
		}
		s.loading = false
		s.answer = &msg
	}
	return nil
}

// render is the preview, or "" when it is hidden.
func (s memoryState) render(t theme.Theme, ic icons.IconSet, width int) string {
	if !s.shown {
		return ""
	}
	if width < 20 {
		width = 20
	}
	title := lipgloss.NewStyle().Foreground(t.Lavender).Bold(true).Render(ic.Search + " Memory: what CM and CASS add")
	dim := lipgloss.NewStyle().Foreground(t.Subtext)
	var b strings.Builder
	b.WriteString(title + "\n")
	switch {
	case strings.TrimSpace(s.text) == "":
		b.WriteString(dim.Render("No text yet.") + "\n")
		return b.String()
	case s.loading || s.answer == nil:
		b.WriteString(dim.Render("Reading cm and cass…") + "\n")
		return b.String()
	case s.answer.Err != nil:
		b.WriteString(lipgloss.NewStyle().Foreground(t.Warning).Render(clean(s.answer.Err.Error())) + "\n")
		return b.String()
	}
	mem := s.answer.Memory
	for _, r := range []struct {
		name, source, unit string
		rep                enrich.Report
	}{{"CM", enrich.SourceCM, "rules", mem.CM}, {"CASS", enrich.SourceCASS, "items", mem.CASS}} {
		b.WriteString(dim.Render(r.name+": "+sourceLine(r.rep, mem.Items, r.source, r.unit)) + "\n")
	}
	if strings.TrimSpace(mem.Block) == "" {
		b.WriteString(dim.Render("Nothing would be added.") + "\n")
		return b.String()
	}
	lines := strings.Split(wordwrap.String(clean(mem.Block), width), "\n")
	more := 0
	if len(lines) > memoryBlockLines {
		lines, more = lines[:memoryBlockLines], len(lines)-memoryBlockLines
	}
	text := lipgloss.NewStyle().Foreground(t.Text)
	for _, l := range lines {
		b.WriteString(text.Render(l) + "\n")
	}
	if more > 0 {
		b.WriteString(dim.Render(fmt.Sprintf("… %d more lines", more)) + "\n")
	}
	b.WriteString(dim.Render("A send leaves out what an agent's context already has.") + "\n")
	return b.String()
}

// sourceLine says what one source gives: its included items, or why it
// gives none.
func sourceLine(rep enrich.Report, items []enrich.Item, source, unit string) string {
	switch {
	case rep.Error != "":
		return "failed: " + clean(rep.Error)
	case !rep.Enabled:
		reason := rep.SkippedReason
		if reason == "" {
			reason = "off"
		}
		return clean(reason)
	}
	n := 0
	for _, it := range items {
		if it.Source == source && it.Status == enrich.Included {
			n++
		}
	}
	return fmt.Sprintf("%d %s", n, unit)
}

// focusText is the text the palette's memory preview is for: the
// highlighted command's prompt in the command phase, the draft in the
// target phase.
func (m Model) focusText() (string, bool) {
	switch m.phase {
	case PhaseCommand:
		if len(m.filtered) == 0 || m.cursor < 0 || m.cursor >= len(m.filtered) {
			return "", false
		}
		return m.filtered[m.cursor].Prompt, true
	case PhaseTarget:
		if m.selected == nil {
			return "", false
		}
		return m.draft.Text(m.selected), true
	}
	return "", false
}

// toggleMemory shows or hides the palette's memory preview.
func (m *Model) toggleMemory() { m.memory.toggle() }

// memoryFollow follows the palette's text in focus.
func (m *Model) memoryFollow() tea.Cmd { return m.memory.follow(m.focusText()) }

// updateMemory handles the palette's memory messages.
func (m Model) updateMemory(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.memory.update(msg, m.env.submitter)
	return m, cmd
}

// renderMemory is the palette's memory preview, or "" when it is hidden.
func (m Model) renderMemory(width int) string {
	return m.memory.render(m.theme, m.icons, width)
}
