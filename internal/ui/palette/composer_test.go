package palette

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func typeText(c Composer, s string) Composer {
	for _, r := range s {
		c, _ = c.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return c
}

// The composer is the palette's editor: runes and enter go into the text,
// an empty draft is refused, ctrl+s gives the text, esc cancels.
func TestComposerIsThePalettesEditor(t *testing.T) {
	c := NewComposer("to IcyKnoll and BlueLake", "", 100, 40, nil)
	if c.Init() == nil {
		t.Fatal("Init should blink the cursor")
	}
	c, cmd := c.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd != nil || !strings.Contains(c.notice, "empty") {
		t.Fatalf("an empty draft was not refused: %q", c.notice)
	}
	c = typeText(c, "quit q")
	c, _ = c.Update(tea.KeyMsg{Type: tea.KeyEnter})
	c = typeText(c, "line two")
	if c.Text() != "quit q\nline two" {
		t.Fatalf("text = %q", c.Text())
	}
	c, cmd = c.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	done, ok := cmd().(ComposedMsg)
	if !ok || done.Text != "quit q\nline two" || done.Rev != c.draft.Rev() || c.notice != "" {
		t.Fatalf("done = %+v", done)
	}
	// An emptied draft stays empty and is refused.
	c.editor.SetValue("")
	c, cmd = c.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd != nil || !c.draft.Present() || c.draft.Text(nil) != "" {
		t.Fatal("an emptied draft was sent")
	}
	_, cmd = c.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if _, ok := cmd().(ComposeCanceledMsg); !ok {
		t.Fatal("esc should cancel")
	}
	view := stripANSI(c.View())
	for _, want := range []string{"Compose", "to IcyKnoll and BlueLake", "ctrl+s", "esc", "ctrl+o"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
}

// The composer's memory preview follows the draft's revision.
func TestComposerMemoryPreview(t *testing.T) {
	sub := NewSubmitter(nil, nil, func(_ context.Context, text string, modes MemoryModes) (Memory, error) {
		if modes != (MemoryModes{}) {
			t.Errorf("the composer's preview should use the config's modes, got %+v", modes)
		}
		return Memory{Query: text, Block: "[for " + text + "]"}, nil
	}, nil)
	c := NewComposer("", "fix the tests", 100, 40, sub)
	c, tick := c.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if tick == nil || !c.memory.shown {
		t.Fatal("ctrl+o should show the preview")
	}
	first := c.memory.rev
	c = typeText(c, "!")
	if c.memory.rev == first {
		t.Fatal("typing should start a new revision")
	}
	c, get := c.Update(memoryDueMsg{rev: c.memory.rev})
	ans := get().(MemoryMsg)
	c, _ = c.Update(MemoryMsg{Rev: first, Memory: Memory{Block: "[stale]"}})
	if c.memory.answer != nil {
		t.Fatal("a stale answer was taken")
	}
	c, _ = c.Update(ans)
	if !strings.Contains(stripANSI(c.View()), "[for fix the tests!]") {
		t.Fatalf("view:\n%s", stripANSI(c.View()))
	}
}
