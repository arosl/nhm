package menu

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// layout is a form screen's text: its head (the title) and its foot (a
// field, a note and the key line, last) stay on screen; its body (the
// review, the result) scrolls.
type layout struct {
	head, body, foot []string
}

// page is a form screen's frame at the popup's size. The body scrolls with
// ↑/↓ and pgup/pgdn, and the page keeps how much of the body has been on
// screen: a screen confirms only a review that was on screen to its end.
type page struct {
	width, height int
	scroll        int
	// seen is how many body lines, from the top, have been on screen.
	seen int
	// warn is a line above the keys until the next key.
	warn string
}

// size takes the screen's size.
func (p *page) size(m tea.WindowSizeMsg) { p.width, p.height = m.Width, m.Height }

// reset starts the body over: a new review is a new body.
func (p *page) reset() { p.scroll, p.seen, p.warn = 0, 0, "" }

// arrange fits a layout in height lines. The body keeps at least one
// line: the head gives way first, then the foot's lines above the keys,
// then the keys.
func (p *page) arrange(l layout, height int) (head, body, foot []string, n int) {
	head, body, foot = l.head, l.body, l.foot
	if p.warn != "" && len(foot) > 0 {
		foot = append(append(append([]string{}, foot[:len(foot)-1]...), clip(p.warn, max(10, p.width))), foot[len(foot)-1])
	}
	for len(head) > 0 && height-len(head)-len(foot) < 1 {
		head = head[:len(head)-1]
	}
	for len(foot) > 1 && height-len(foot) < 1 {
		foot = foot[1:]
	}
	if height-len(foot) < 1 {
		foot = nil
	}
	return head, body, foot, max(1, height-len(head)-len(foot))
}

// start is the first body line on screen.
func (p *page) start(bodyLen, n int) int { return max(0, min(p.scroll, bodyLen-n)) }

// mark notes the body lines on screen now.
func (p *page) mark(l layout) {
	_, body, _, n := p.arrange(l, p.height)
	p.seen = max(p.seen, min(len(body), p.start(len(body), n)+n))
}

// key scrolls the body; false for a key that is not a scroll key.
func (p *page) key(k tea.KeyMsg, l layout) bool {
	_, body, _, n := p.arrange(l, p.height)
	last := max(0, len(body)-n)
	p.scroll = min(p.scroll, last)
	switch k.String() {
	case "up":
		p.scroll = max(0, p.scroll-1)
	case "down":
		p.scroll = min(last, p.scroll+1)
	case "pgup":
		p.scroll = max(0, p.scroll-n)
	case "pgdown":
		p.scroll = min(last, p.scroll+n)
	default:
		return false
	}
	p.warn = ""
	p.mark(l)
	return true
}

// seenAll reports whether the whole body has been on screen. When it has
// not, the page says so above the keys.
func (p *page) seenAll(l layout) bool {
	p.mark(l)
	if p.seen >= len(l.body) {
		return true
	}
	p.warn = "read the whole review first: ↓ shows the rest"
	return false
}

// render is the layout at a size: the head, the body from its scroll
// line, and the foot. A body that does not fit says so on the key line.
func (p *page) render(l layout, width, height int) string {
	head, body, foot, n := p.arrange(l, height)
	start := p.start(len(body), n)
	shown := body[start:min(len(body), start+n)]
	if len(body) > n && len(foot) > 0 {
		foot = append([]string{}, foot...)
		foot[len(foot)-1] = clip("↑/↓ more · "+foot[len(foot)-1], width)
	}
	return strings.Join(append(append(append([]string{}, head...), shown...), foot...), "\n")
}

// lines splits text into its lines, without the last newline's empty one.
func lines(text string) []string {
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
