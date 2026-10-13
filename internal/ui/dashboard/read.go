package dashboard

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/arosl/nhm/internal/ui/actions"
	"github.com/arosl/nhm/internal/ui/board"
)

// The read view (space r; nhm read and watch, plan-v4 section 3): one
// agent's pane, its last ReadLines lines, at most ReadBytes bytes, with
// the controls removed. It follows the pane every second until p pauses
// it; the arrows scroll.
const (
	ReadLines    = 400
	ReadBytes    = 64 << 10
	readEvery    = time.Second
	readActionID = "agent.read"
)

// readState is the read view's agent, its last read and its position.
type readState struct {
	name, pane string
	seq        uint64
	reading    bool
	lastRead   time.Time
	text       string
	err        error
	follow     bool
	// back is how many lines the view is scrolled up from the bottom.
	back int
}

// readMsg is one read of the pane; seq drops an older one.
type readMsg struct {
	seq  uint64
	text string
	err  error
}

// readAction is the table's read row, when the board can read panes.
func (m *Model) readAction() (actions.Action, bool) {
	if m.cfg.Read == nil {
		return actions.Action{}, false
	}
	return actions.ByID(readActionID)
}

// openRead opens the read view on the one agent the action menu froze.
func (m *Model) openRead(tg board.Targets) tea.Cmd {
	switch {
	case len(tg.Invalid) > 0:
		m.view = viewMain
		m.say(true, "a marked agent is no longer on the board; esc clears the marks")
		return nil
	case len(tg.Rows) != 1 || tg.Rows[0].Agent == nil:
		m.view = viewMain
		m.say(true, "read takes one agent with a pane: move the cursor to it, or mark only it")
		return nil
	}
	r := tg.Rows[0]
	// The sequence goes on: a read of the agent read before, still on its
	// way, is dropped, never shown under this agent's title.
	m.read = readState{seq: m.read.seq, name: rowName(r), pane: r.Agent.PaneID, follow: true}
	m.view = viewRead
	return m.readPane()
}

// readPane reads the read view's pane in a command.
func (m *Model) readPane() tea.Cmd {
	if m.read.reading {
		return nil
	}
	m.read.seq++
	m.read.reading, m.read.lastRead = true, m.cfg.Now()
	read, pane, seq, d := m.cfg.Read, m.read.pane, m.read.seq, m.cfg.CallTimeout
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		text, err := read(ctx, pane, ReadLines)
		return readMsg{seq: seq, text: text, err: err}
	}
}

// readTick reads again while the view follows the pane.
func (m *Model) readTick(now time.Time) tea.Cmd {
	if m.view != viewRead || !m.read.follow || m.read.reading || now.Sub(m.read.lastRead) < readEvery {
		return nil
	}
	return m.readPane()
}

// readUpdate takes a read.
func (m *Model) readUpdate(msg readMsg) {
	if msg.seq != m.read.seq {
		return
	}
	m.read.reading = false
	if msg.err != nil {
		m.read.err = msg.err
		return
	}
	text := board.Clean(msg.text)
	if len(text) > ReadBytes {
		text = text[len(text)-ReadBytes:]
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
		text = strings.ToValidUTF8(text, "")
	}
	m.read.text, m.read.err = strings.TrimRight(text, "\n"), nil
}

// readKey handles a key in the read view.
func (m *Model) readKey(msg tea.KeyMsg) tea.Cmd {
	page := max(m.height-6, 1)
	switch {
	case msg.Type == tea.KeyEsc, msg.String() == "q":
		m.view = viewMain
	case msg.String() == "p":
		m.read.follow = !m.read.follow
		if m.read.follow {
			m.read.back = 0
			return m.readPane()
		}
	case msg.String() == "r":
		return m.readPane()
	case msg.Type == tea.KeyUp, msg.String() == "k":
		m.read.back++
	case msg.Type == tea.KeyDown, msg.String() == "j":
		m.read.back = max(m.read.back-1, 0)
	case msg.Type == tea.KeyPgUp:
		m.read.back += page
	case msg.Type == tea.KeyPgDown:
		m.read.back = max(m.read.back-page, 0)
	case msg.Type == tea.KeyHome:
		m.read.back = strings.Count(m.read.text, "\n")
	case msg.Type == tea.KeyEnd:
		m.read.back = 0
	}
	if m.read.back > 0 {
		// Reading back pauses the follow, so the text stays put.
		m.read.follow = false
	}
	return nil
}

// readView renders the read view over the whole board.
func (m *Model) readView(w, h int) string {
	title := lipgloss.NewStyle().Bold(true).Foreground(m.theme.Primary)
	dim := lipgloss.NewStyle().Foreground(m.theme.Overlay)
	state := "following"
	if !m.read.follow {
		state = "paused"
	}
	head := title.Render(ansi.Truncate("read "+board.Clean(m.read.name)+" · "+m.read.pane+" · "+state, w, "…"))
	foot := dim.Render(ansi.Truncate("↑/↓ pgup/pgdn home/end scroll · p pause/follow · r read now · esc back", w, "…"))
	room := max(h-2, 1)
	var body []string
	switch {
	case m.read.err != nil:
		body = []string{lipgloss.NewStyle().Foreground(m.theme.Error).Render(ansi.Truncate("herdr: "+board.Clean(m.read.err.Error()), w, "…"))}
	case m.read.text == "" && m.read.reading:
		body = []string{dim.Render("reading the pane…")}
	case m.read.text == "":
		body = []string{dim.Render("(the pane shows nothing)")}
	default:
		lines := strings.Split(m.read.text, "\n")
		m.read.back = min(m.read.back, max(len(lines)-room, 0))
		end := len(lines) - m.read.back
		start := max(end-room, 0)
		for _, l := range lines[start:end] {
			body = append(body, ansi.Truncate(l, w, "…"))
		}
	}
	for len(body) < room {
		body = append(body, "")
	}
	return strings.Join(append(append([]string{head}, body...), foot), "\n")
}
