package menu

import (
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

// field is one line of text a form asks for. Typed and pasted text reach
// it before any shortcut (plan-v4 section 4); control characters never
// enter it.
type field struct {
	label string
	value []rune
	pos   int
}

func newField(label, value string) *field {
	f := &field{label: label}
	f.set(value)
	return f
}

func (f *field) set(s string) {
	f.value = []rune(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s))
	f.pos = len(f.value)
}

func (f *field) String() string { return string(f.value) }

// key edits the field; it reports whether it took the key.
func (f *field) key(k tea.KeyMsg) bool {
	switch k.Type {
	case tea.KeyRunes, tea.KeySpace:
		rs := k.Runes
		if k.Type == tea.KeySpace {
			rs = []rune{' '}
		}
		var in []rune
		for _, r := range rs {
			if !unicode.IsControl(r) {
				in = append(in, r)
			}
		}
		f.value = append(f.value[:f.pos], append(in, f.value[f.pos:]...)...)
		f.pos += len(in)
	case tea.KeyBackspace:
		if f.pos > 0 {
			f.value = append(f.value[:f.pos-1], f.value[f.pos:]...)
			f.pos--
		}
	case tea.KeyDelete:
		if f.pos < len(f.value) {
			f.value = append(f.value[:f.pos], f.value[f.pos+1:]...)
		}
	case tea.KeyLeft:
		f.pos = max(0, f.pos-1)
	case tea.KeyRight:
		f.pos = min(len(f.value), f.pos+1)
	case tea.KeyHome, tea.KeyCtrlA:
		f.pos = 0
	case tea.KeyEnd, tea.KeyCtrlE:
		f.pos = len(f.value)
	case tea.KeyCtrlU:
		f.value, f.pos = append([]rune{}, f.value[f.pos:]...), 0
	default:
		return false
	}
	return true
}

// view is the field on one line; focused shows the cursor.
func (f *field) view(focused bool) string {
	mark := "  "
	if focused {
		mark = "▶ "
	}
	text := string(f.value)
	if focused {
		text = string(f.value[:f.pos]) + "▌" + string(f.value[f.pos:])
	}
	return mark + f.label + ": " + text
}
