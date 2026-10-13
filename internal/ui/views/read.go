package views

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/tui/components"
	"github.com/arosl/nhm/internal/ui/tui/theme"
)

// readView is one agent's screen (nhm read, with watch's follow). It
// follows at first: it reads again every Env.FollowEvery and keeps the
// end in sight. p or space pauses, and so does any scroll up; the text
// then holds still.
type readView struct {
	env       Env
	target    ui.Target
	name      string
	pane      string
	req       uint64
	loading   bool
	text      string
	err       error
	at        time.Time
	following bool
	tick      uint64
	panel     *components.ScrollablePanel
	width     int
	high      int
}

// followTick asks a following view to read again; id drops a tick that a
// pause or a newer read made stale.
type followTick struct{ id uint64 }

func newRead(env Env, t ui.Target) *readView {
	r := &readView{env: env, target: t, pane: t.PaneID, following: true, width: 80, high: 24}
	r.panel = components.NewScrollablePanel(r.width, r.panelHeight())
	return r
}

func (r *readView) Title() string {
	if r.name != "" {
		return "read " + r.name
	}
	return "read " + r.target.Kind
}

func (r *readView) Init() tea.Cmd { return r.read() }

func (r *readView) every() time.Duration {
	if r.env.FollowEvery > 0 {
		return r.env.FollowEvery
	}
	return time.Second
}

func (r *readView) read() tea.Cmd {
	if r.env.Read == nil {
		r.err = errNotAvailable
		r.layout()
		return nil
	}
	r.req, r.loading = nextRequest(), true
	read, t := r.env.Read, r.target
	return load(r.req, r.env.timeout(), func(ctx context.Context) (any, error) { return read(ctx, t) })
}

func (r *readView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		r.width, r.high = msg.Width, msg.Height
		r.layout()
	case loaded:
		if msg.id != r.req {
			return r, nil
		}
		r.loading, r.at, r.err = false, r.env.now(), msg.err
		if s, ok := msg.value.(Screen); ok && (msg.err == nil || s.Text != "") {
			r.name, r.pane, r.text = clean(s.Name), clean(s.Pane), clean(s.Text)
		}
		r.layout()
		if r.following && r.err == nil {
			r.tick = nextRequest()
			id := r.tick
			return r, tea.Tick(r.every(), func(time.Time) tea.Msg { return followTick{id} })
		}
	case followTick:
		if msg.id == r.tick && r.following {
			return r, r.read()
		}
	case tea.KeyMsg:
		switch k := msg.String(); {
		case isBack(msg):
			return r, backCmd
		case k == "p" || k == " ":
			r.following = !r.following
			if r.following {
				r.layout()
				return r, r.read()
			}
		case k == "r":
			return r, r.read()
		default:
			if k == "up" || k == "k" || k == "pgup" || k == "home" || k == "ctrl+u" {
				r.following = false
			}
			var cmd tea.Cmd
			r.panel, cmd = r.panel.Update(msg)
			return r, cmd
		}
	}
	return r, nil
}

func (r *readView) panelHeight() int { return max(1, r.high-3) }

func (r *readView) layout() {
	r.panel.SetSize(r.width, r.panelHeight())
	t := theme.Current()
	switch {
	case r.text != "":
		r.panel.SetContent(wrap(r.text, r.width))
	case r.err != nil:
		r.panel.SetContent(wrap(lipgloss.NewStyle().Foreground(t.Error).Render(clean(r.err.Error())), r.width))
	default:
		r.panel.SetContent(lipgloss.NewStyle().Foreground(t.Overlay).Render("loading…"))
	}
	if r.following {
		r.panel.GotoBottom()
	}
}

// note is the header's note: the pane, following or paused, and the
// error of a read that kept the text shown before.
func (r *readView) note() string {
	t := theme.Current()
	dim := lipgloss.NewStyle().Foreground(t.Overlay)
	state := "paused"
	if r.following {
		state = "following"
	}
	if r.err != nil && r.text != "" {
		return dim.Render(r.pane+"  "+state+"  ") + lipgloss.NewStyle().Foreground(t.Error).Render(clean(r.err.Error()))
	}
	if !r.at.IsZero() {
		state += ", as of " + r.at.UTC().Format("15:04:05Z")
	}
	return dim.Render(r.pane + "  " + state)
}

func (r *readView) View() string {
	pause := components.KeyHint{Key: "p", Desc: "pause"}
	if !r.following {
		pause.Desc = "follow"
	}
	return frame(r.Title(), r.note(), r.panel.View(), []components.KeyHint{
		{Key: "↑/↓", Desc: "scroll"}, pause, {Key: "r", Desc: "read again"}, {Key: "esc", Desc: "back"},
	}, r.width)
}
