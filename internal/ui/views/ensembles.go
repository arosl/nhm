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

	"github.com/arosl/nhm/internal/core/ensemble"
	"github.com/arosl/nhm/internal/ui/tui/components"
	"github.com/arosl/nhm/internal/ui/tui/theme"
)

// The ensembles view's two lists.
const (
	tabPresets = iota
	tabRuns
)

// errNoInstance is the runs list of a menu with no nhm instance.
var errNoInstance = errors.New("no nhm instance: open the menu from a pane of an enrolled herdr server")

// ensembles is nhm ensemble presets, runs and status: ntm's presets, this
// instance's runs, and a run's status on enter. s on a run that is not
// stopped asks the menu for its stop form (Open); the view stops nothing.
type ensembles struct {
	env       Env
	tab       int
	presets   []ensemble.PresetDetail
	presetErr error
	runs      []ensemble.Run
	runsReq   uint64
	runsState pageState
	runsErr   error
	runsAt    time.Time
	lists     [2]list
	detail    *page
	detailRun string
	width     int
	high      int
}

func newEnsembles(env Env) *ensembles {
	e := &ensembles{env: env, width: 80, high: 24}
	cat, err := ensemble.Builtin()
	if err != nil {
		e.presetErr = err
	} else {
		e.presets = ensemble.PresetsList(cat, "", true, env.now()).Details
	}
	return e
}

func (e *ensembles) Title() string { return "ensembles" }

func (e *ensembles) Init() tea.Cmd { return e.refreshRuns() }

// refreshRuns reads the instance's runs; the runs shown so far stay until
// the answer.
func (e *ensembles) refreshRuns() tea.Cmd {
	if e.env.RunsDir == "" {
		e.runsState, e.runsErr = pageFailed, errNoInstance
		return nil
	}
	e.runsReq = nextRequest()
	e.runsState = pageLoading
	dir := e.env.RunsDir
	return load(e.runsReq, e.env.timeout(), func(context.Context) (any, error) { return ensemble.List(dir) })
}

// listHeight is the rows in sight: the frame takes three lines, the tabs
// and the column header two.
func (e *ensembles) listHeight() int { return max(1, e.high-5) }

func (e *ensembles) rowCount() int {
	if e.tab == tabRuns {
		return len(e.runs)
	}
	return len(e.presets)
}

// selectedRun is the run under the cursor, or under the open status page.
func (e *ensembles) selectedRun() (ensemble.Run, bool) {
	id := e.detailRun
	if e.detail == nil {
		if e.tab != tabRuns || e.lists[tabRuns].cursor >= len(e.runs) {
			return ensemble.Run{}, false
		}
		id = e.runs[e.lists[tabRuns].cursor].ID
	}
	for _, r := range e.runs {
		if r.ID == id {
			return r, true
		}
	}
	return ensemble.Run{}, false
}

// stoppable is a run whose stop form makes sense: ensemble.Stop changes
// nothing on a stopped run.
func stoppable(r ensemble.Run) bool { return r.Status != ensemble.RunStopped }

func (e *ensembles) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		e.width, e.high = msg.Width, msg.Height
		for i := range e.lists {
			n := len(e.presets)
			if i == tabRuns {
				n = len(e.runs)
			}
			e.lists[i].fit(n, e.listHeight())
		}
		if e.detail != nil {
			e.detail.Update(msg)
		}
	case loaded:
		if msg.id == e.runsReq {
			e.runsAt = e.env.now()
			e.runsState, e.runsErr = pageReady, msg.err
			if msg.err != nil {
				e.runsState = pageFailed
			} else {
				runs, _ := msg.value.([]ensemble.Run)
				e.setRuns(runs)
			}
			return e, nil
		}
		if e.detail != nil {
			_, cmd := e.detail.Update(msg)
			return e, cmd
		}
	case tea.KeyMsg:
		return e, e.key(msg)
	}
	return e, nil
}

// setRuns replaces the runs and keeps the cursor on the run it was on.
func (e *ensembles) setRuns(runs []ensemble.Run) {
	l := &e.lists[tabRuns]
	was := ""
	if l.cursor < len(e.runs) {
		was = e.runs[l.cursor].ID
	}
	e.runs = runs
	for i, r := range runs {
		if r.ID == was {
			l.cursor = i
		}
	}
	l.fit(len(runs), e.listHeight())
}

func (e *ensembles) key(msg tea.KeyMsg) tea.Cmd {
	k := msg.String()
	if k == "s" {
		if r, ok := e.selectedRun(); ok && stoppable(r) {
			id := r.ID
			return func() tea.Msg { return Open{Row: "view.ensembles", Action: "stop", Arg: id} }
		}
		return nil
	}
	if e.detail != nil {
		if isBack(msg) {
			e.detail, e.detailRun = nil, ""
			return nil
		}
		_, cmd := e.detail.Update(msg)
		return cmd
	}
	switch {
	case isBack(msg):
		return backCmd
	case k == "tab" || k == "shift+tab":
		e.tab = 1 - e.tab
	case k == "r" && e.tab == tabRuns:
		return e.refreshRuns()
	case k == "enter":
		return e.open()
	default:
		e.lists[e.tab].key(k, e.rowCount(), e.listHeight())
	}
	return nil
}

// open shows the selected preset's details, or the selected run's status.
func (e *ensembles) open() tea.Cmd {
	c := e.lists[e.tab].cursor
	if c >= e.rowCount() {
		return nil
	}
	if e.tab == tabPresets {
		d := e.presets[c]
		e.detail = newPage(d.Name, e.env, func(context.Context) (string, error) {
			var b strings.Builder
			err := ensemble.RenderPresets(&b, ensemble.PresetsOutput{Count: 1, Details: []ensemble.PresetDetail{d}}, true, "text")
			return clean(b.String()), err
		})
	} else {
		id := e.runs[c].ID
		var fn Text
		if e.env.RunStatus != nil {
			status := e.env.RunStatus
			fn = cleaned(func(ctx context.Context) (string, error) { return status(ctx, id) })
		}
		e.detail, e.detailRun = newPage("run "+id, e.env, fn), id
	}
	e.detail.Update(tea.WindowSizeMsg{Width: e.width, Height: e.high})
	return e.detail.Init()
}

func (e *ensembles) View() string {
	if e.detail != nil {
		hints := []components.KeyHint{{Key: "↑/↓", Desc: "scroll"}, {Key: "r", Desc: "refresh"}}
		if r, ok := e.selectedRun(); ok && stoppable(r) {
			hints = append(hints, components.KeyHint{Key: "s", Desc: "stop"})
		}
		hints = append(hints, components.KeyHint{Key: "esc", Desc: "back"})
		return frame(e.detail.title, e.detail.status(), e.detail.panel.View(), hints, e.width)
	}
	t := theme.Current()
	dim := lipgloss.NewStyle().Foreground(t.Overlay)
	on := lipgloss.NewStyle().Foreground(t.Primary).Bold(true).Underline(true)
	tabs := []string{fmt.Sprintf("presets (%d)", len(e.presets)), fmt.Sprintf("runs (%d)", len(e.runs))}
	for i := range tabs {
		if i == e.tab {
			tabs[i] = on.Render(tabs[i])
		} else {
			tabs[i] = dim.Render(tabs[i])
		}
	}
	var header, body, note string
	hints := []components.KeyHint{{Key: "tab", Desc: "presets/runs"}, {Key: "↑/↓", Desc: "move"}, {Key: "enter", Desc: "open"}}
	if e.tab == tabPresets {
		header = presetRow("NAME", "MODES", "STRATEGY", "DESCRIPTION")
		switch {
		case e.presetErr != nil:
			body = lipgloss.NewStyle().Foreground(t.Error).Render(clean(e.presetErr.Error()))
		case len(e.presets) == 0:
			body = dim.Render("no presets")
		default:
			rows := make([]string, len(e.presets))
			for i, d := range e.presets {
				rows[i] = presetRow(d.Name, fmt.Sprint(d.ModeCount), d.Synthesis.Strategy, d.Description)
			}
			body = e.lists[tabPresets].render(rows, e.width, e.listHeight())
		}
	} else {
		header = runRow("RUN", "PRESET", "STATUS", "ANGLES")
		note = e.runsNote()
		hints = append(hints, components.KeyHint{Key: "r", Desc: "refresh"})
		if r, ok := e.selectedRun(); ok && stoppable(r) {
			hints = append(hints, components.KeyHint{Key: "s", Desc: "stop"})
		}
		switch {
		case len(e.runs) > 0:
			rows := make([]string, len(e.runs))
			for i, r := range e.runs {
				rows[i] = runRow(r.ID, r.Preset, r.Status, fmt.Sprint(len(r.Angles)))
			}
			body = e.lists[tabRuns].render(rows, e.width, e.listHeight())
		case e.runsState == pageFailed:
			body = lipgloss.NewStyle().Foreground(t.Error).Render(clean(e.runsErr.Error()))
		case e.runsState == pageLoading:
			body = dim.Render("loading…")
		default:
			body = dim.Render("no runs")
		}
	}
	hints = append(hints, components.KeyHint{Key: "esc", Desc: "back"})
	body = strings.Join([]string{
		ansi.Truncate(strings.Join(tabs, "   "), e.width, "…"),
		dim.Render(ansi.Truncate("  "+header, e.width, "…")),
		body,
	}, "\n")
	return frame("ensembles", note, body, hints, e.width)
}

// runsNote is the runs list's note: loading, the time of the answer, or
// the error of a refresh that kept the runs shown before.
func (e *ensembles) runsNote() string {
	t := theme.Current()
	dim := lipgloss.NewStyle().Foreground(t.Overlay)
	switch {
	case e.runsState == pageLoading:
		return dim.Render("loading…")
	case e.runsState == pageFailed && len(e.runs) > 0:
		return lipgloss.NewStyle().Foreground(t.Error).Render(clean(e.runsErr.Error()))
	case e.runsAt.IsZero():
		return ""
	}
	return dim.Render("as of " + e.runsAt.UTC().Format("15:04:05Z"))
}

func presetRow(name, modes, strategy, desc string) string {
	return fmt.Sprintf("%-22s %-5s %-14s %s", clean(name), modes, clean(strategy), clean(desc))
}

// runRow is one run; its id holds its start time (ens-YYYYMMDDTHHMMSS-…,
// UTC).
func runRow(id, preset, status, angles string) string {
	return fmt.Sprintf("%-26s %-20s %-12s %s", clean(id), clean(preset), clean(status), angles)
}
