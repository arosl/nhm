package palette

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/arosl/nhm/internal/ui/tui/icons"
	"github.com/arosl/nhm/internal/ui/tui/styles"
	"github.com/arosl/nhm/internal/ui/tui/theme"
)

// Composer is the palette's editor as a component, for the menu and the
// board (plan-v4 section 5: "the composer used by the board and the menu
// is this palette's editor, not a second one"). It is the palette's edit
// phase: the same editor (newEditor), enter as a new line, a Draft whose
// "present" is apart from its text, the refusal of an empty draft, and
// the memory preview (ctrl+o) bound to the draft's revision. It sends
// nothing itself: its host sends ComposedMsg's text through the action
// table's send, to targets the host froze before.
type Composer struct {
	label  string
	editor textarea.Model
	draft  Draft
	notice string
	memory memoryState
	sub    *Submitter
	width  int
	height int
	theme  theme.Theme
	icons  icons.IconSet
}

// ComposedMsg is a finished draft that is not empty, and its revision.
type ComposedMsg struct {
	Text string
	Rev  uint64
}

// ComposeCanceledMsg: the user left with esc; nothing is sent.
type ComposeCanceledMsg struct{}

// NewComposer returns a composer for text, sized for the host's width and
// height. label says what it writes for ("to IcyKnoll and BlueLake"); sub,
// when set, serves the memory preview.
func NewComposer(label, text string, width, height int, sub *Submitter) Composer {
	t := theme.Current()
	c := Composer{label: label, sub: sub, width: width, height: height, theme: t, icons: icons.Current()}
	c.editor = newEditor(t, width, height, text)
	c.editor.Focus()
	c.draft.Set(text)
	return c
}

// Init starts the cursor's blink.
func (c Composer) Init() tea.Cmd { return textarea.Blink }

// Text is the draft's text now.
func (c Composer) Text() string { return c.editor.Value() }

// Update handles the editor's keys and the memory preview's messages.
func (c Composer) Update(msg tea.Msg) (Composer, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		c.width, c.height = msg.Width, msg.Height
		c.editor.SetWidth(max(msg.Width-8, 30))
		return c, nil
	case memoryDueMsg, MemoryMsg:
		return c, c.memory.update(msg, c.sub)
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, composerKeys.Done):
			text := c.editor.Value()
			c.draft.Set(text)
			if strings.TrimSpace(text) == "" {
				c.notice = "Message is empty — type something to send, or Esc to cancel."
				return c, nil
			}
			c.notice = ""
			rev := c.draft.Rev()
			return c, func() tea.Msg { return ComposedMsg{Text: text, Rev: rev} }
		case key.Matches(msg, composerKeys.Cancel):
			return c, func() tea.Msg { return ComposeCanceledMsg{} }
		case key.Matches(msg, composerKeys.Memory):
			c.memory.toggle()
			return c, c.memory.follow(c.editor.Value(), true)
		}
	}
	var cmd tea.Cmd
	c.editor, cmd = c.editor.Update(msg)
	c.draft.Set(c.editor.Value())
	return c, tea.Batch(cmd, c.memory.follow(c.editor.Value(), true))
}

// View is the editor with its label, notice, memory preview and keys.
func (c Composer) View() string {
	t := c.theme
	ic := c.icons
	var b strings.Builder
	boxWidth := max(c.width-6, 40)

	b.WriteString("\n")
	b.WriteString("  " + styles.GradientText(ic.Send+"  Compose", string(t.Yellow), string(t.Peach)) + "\n")
	b.WriteString("  " + styles.GradientDivider(boxWidth, string(t.Yellow), string(t.Peach)) + "\n\n")
	if c.label != "" {
		b.WriteString("  " + lipgloss.NewStyle().Foreground(t.Subtext).Render(clean(c.label)) + "\n\n")
	}
	if c.notice != "" {
		b.WriteString("  " + lipgloss.NewStyle().Foreground(t.Peach).Render(ic.Warning+"  "+c.notice) + "\n\n")
	}
	b.WriteString("  " + c.editor.View() + "\n\n")
	if block := c.memory.render(t, ic, boxWidth); block != "" {
		b.WriteString(lipgloss.NewStyle().MarginLeft(2).Render(block) + "\n")
	}
	b.WriteString("  " + styles.GradientDivider(boxWidth, string(t.Surface2), string(t.Surface1)) + "\n\n")
	keyStyle := lipgloss.NewStyle().Background(t.Surface0).Foreground(t.Text).Bold(true).Padding(0, 1)
	descStyle := lipgloss.NewStyle().Foreground(t.Overlay)
	var parts []string
	for _, e := range composerHelpEntries() {
		parts = append(parts, keyStyle.Render(e.label)+" "+descStyle.Render(e.desc))
	}
	b.WriteString("  " + strings.Join(parts, "  ") + "\n")
	return b.String()
}
