package views

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/arosl/nhm/internal/ui/tui/components"
	"github.com/arosl/nhm/internal/ui/tui/theme"
)

// errNotAvailable is a view whose loader the menu did not give.
var errNotAvailable = errors.New("not available in this menu")

type pageState int

const (
	pageLoading pageState = iota
	pageReady
	pageFailed
)

// page is a view of one text that scrolls: what a CLI verb prints, or a
// detail of the modes and ensembles views. Its loader returns text that
// is safe to show (cleaned, or styled by nhm itself).
type page struct {
	title string
	env   Env
	fn    Text
	req   uint64
	state pageState
	text  string
	err   error
	at    time.Time
	panel *components.ScrollablePanel
	width int
	high  int
	// empty is what an empty answer means; "" is "nothing to show".
	empty string
}

func newPage(title string, env Env, fn Text) *page {
	p := &page{title: title, env: env, fn: fn, width: 80, high: 24}
	p.panel = components.NewScrollablePanel(p.width, p.panelHeight())
	return p
}

// cleaned wraps a loader of outside text so that its text is safe to
// show.
func cleaned(fn Text) Text {
	if fn == nil {
		return nil
	}
	return func(ctx context.Context) (string, error) {
		s, err := fn(ctx)
		return clean(s), err
	}
}

func (p *page) Title() string { return p.title }

func (p *page) Init() tea.Cmd { return p.refresh() }

// refresh starts a load; the text shown so far stays until its answer.
func (p *page) refresh() tea.Cmd {
	if p.fn == nil {
		p.state, p.err = pageFailed, errNotAvailable
		p.layout()
		return nil
	}
	p.req = nextRequest()
	p.state = pageLoading
	p.layout()
	fn := p.fn
	return load(p.req, p.env.timeout(), func(ctx context.Context) (any, error) { return fn(ctx) })
}

func (p *page) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width, p.high = msg.Width, msg.Height
		p.layout()
	case loaded:
		if msg.id != p.req {
			return p, nil
		}
		p.at = p.env.now()
		p.state, p.err = pageReady, msg.err
		if msg.err != nil {
			p.state = pageFailed
		}
		// A verb that fails a check still prints what it read; a failed
		// refresh with nothing new keeps the text shown before.
		if s, _ := msg.value.(string); msg.err == nil || s != "" {
			p.text = s
		}
		p.layout()
	case tea.KeyMsg:
		switch {
		case isBack(msg):
			return p, backCmd
		case msg.String() == "r":
			return p, p.refresh()
		}
		var cmd tea.Cmd
		p.panel, cmd = p.panel.Update(msg)
		return p, cmd
	}
	return p, nil
}

func (p *page) panelHeight() int { return max(1, p.high-3) }

func (p *page) layout() {
	p.panel.SetSize(p.width, p.panelHeight())
	p.panel.SetContent(p.body())
}

func (p *page) body() string {
	t := theme.Current()
	switch {
	case p.text != "":
		return wrap(p.text, p.width)
	case p.state == pageLoading:
		return lipgloss.NewStyle().Foreground(t.Overlay).Render("loading…")
	case p.state == pageFailed:
		return wrap(lipgloss.NewStyle().Foreground(t.Error).Render(clean(p.err.Error())), p.width)
	}
	empty := p.empty
	if empty == "" {
		empty = "nothing to show"
	}
	return wrap(lipgloss.NewStyle().Foreground(t.Overlay).Render(empty), p.width)
}

// status is the header's note: loading, the time of the answer, or the
// error of a refresh that kept the text shown before.
func (p *page) status() string {
	t := theme.Current()
	dim := lipgloss.NewStyle().Foreground(t.Overlay)
	switch {
	case p.state == pageLoading:
		return dim.Render("loading…")
	case p.state == pageFailed && p.text != "":
		return lipgloss.NewStyle().Foreground(t.Error).Render(clean(p.err.Error()))
	case p.at.IsZero():
		return ""
	}
	return dim.Render("as of " + p.at.UTC().Format("15:04:05Z"))
}

func (p *page) View() string {
	return frame(p.title, p.status(), p.panel.View(), []components.KeyHint{
		{Key: "↑/↓", Desc: "scroll"}, {Key: "r", Desc: "refresh"}, {Key: "esc", Desc: "back"},
	}, p.width)
}

// frame lays out a view: the title and its note, a blank line, the body
// and the key hints.
func frame(title, note, body string, hints []components.KeyHint, width int) string {
	t := theme.Current()
	head := lipgloss.NewStyle().Bold(true).Foreground(t.Primary).Render(title)
	if note != "" {
		head += "  " + note
	}
	help := components.RenderHelpBar(components.HelpBarOptions{Hints: hints, Width: width})
	return strings.Join([]string{ansi.Truncate(head, width, "…"), "", body, help}, "\n")
}

// renderChecks is nhm doctor's text, with the results in colour. The
// checks' text comes from outside nhm, so it is cleaned first.
func renderChecks(checks []Check) string {
	if len(checks) == 0 {
		return ""
	}
	t := theme.Current()
	var b strings.Builder
	failed := 0
	for _, c := range checks {
		style := lipgloss.NewStyle()
		switch c.Result {
		case "ok":
			style = style.Foreground(t.Success)
		case "warn":
			style = style.Foreground(t.Warning)
		case "fail":
			style = style.Foreground(t.Error).Bold(true)
			failed++
		}
		fmt.Fprintf(&b, "%s %-16s %s\n", style.Render(fmt.Sprintf("%-4s", clean(c.Result))), clean(c.Name), clean(c.Detail))
		if c.Fix != "" && c.Result != "ok" {
			fmt.Fprintf(&b, "     %-16s fix: %s\n", "", clean(c.Fix))
		}
	}
	if failed > 0 {
		fmt.Fprintf(&b, "\n%s\n", lipgloss.NewStyle().Foreground(t.Error).Render(fmt.Sprintf("%d checks failed", failed)))
	}
	return b.String()
}
