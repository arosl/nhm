package menu

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/actions"
	"github.com/arosl/nhm/internal/ui/forms"
	"github.com/arosl/nhm/internal/ui/palette"
	"github.com/arosl/nhm/internal/ui/picker"
	"github.com/arosl/nhm/internal/ui/views"
)

// screenFor is the screen of a row: a view, a form, or the picker that
// hands on to the form of its pick. A surface runs in the popup and has
// no screen.
func (m *Model) screenFor(a actions.Action) (screen, error) {
	c := m.env.Context
	switch a.Kind {
	case actions.KindSurface:
		cmd := m.surface(a.ID)
		id := a.ID
		return &execScreen{cmd: tea.ExecProcess(cmd, func(err error) tea.Msg { return surfaceEnded{id: id, err: err} })}, nil
	case actions.KindView:
		if a.ID == "operations" {
			return &opsScreen{v: forms.NewOperationsView(m.env.Operations, 0, nil)}, nil
		}
		if slices.Contains(views.ForIDs, a.ID) {
			return m.pickFor(a, func(ts []ui.Target) (screen, error) {
				v, ok := views.NewFor(a.ID, m.env.Views, ts[0])
				if !ok {
					return nil, errors.New("not built yet")
				}
				return &viewScreen{v: v, open: m.openFromView}, nil
			})
		}
		v, ok := views.New(a.ID, m.env.Views)
		if !ok {
			return nil, errors.New("not built yet")
		}
		return &viewScreen{v: v, open: m.openFromView}, nil
	}
	run := forms.Runner{Action: a, Core: m.env.Core, Guard: actions.NewGuard(m.env.NewID)}
	switch a.ID {
	case "spawn.add", "spawn.new":
		return &wizardScreen{w: forms.NewSpawnWizard(run, c, m.env.Kinds, m.width, m.height-2)}, nil
	case "workspace.reap":
		return newReapScreen(run, c), nil
	case "workspace.kill":
		return newKillScreen(run, c), nil
	case "workspace.new":
		return newNewScreen(run, c), nil
	case "workspace.attach":
		if m.env.Agents == nil {
			return nil, errors.New("no workspace list here")
		}
		return newWorkspacesScreen(a, m.env.Core, c, m.env.Agents), nil
	case "ensemble.stop":
		// The ensembles view picks the run: its s opens the stop form.
		v, ok := views.New("view.ensembles", m.env.Views)
		if !ok {
			return nil, errors.New("no ensembles view here")
		}
		return &viewScreen{v: v, open: m.openFromView}, nil
	}
	if a.Selection == actions.SelectNone || a.Selection == "" {
		return nil, errors.New("this menu has no form for it yet")
	}
	next, err := m.after(a, run)
	if err != nil {
		return nil, err
	}
	return m.pickFor(a, next)
}

// pickFor freezes a's targets: the context's explicit selection, or the
// agent picker's pick; then next opens with them. It never widens a
// selection.
func (m *Model) pickFor(a actions.Action, next func([]ui.Target) (screen, error)) (screen, error) {
	ts, pick, err := a.Targets(m.env.Context)
	switch {
	case err != nil:
		return nil, err
	case !pick:
		return next(ts)
	case m.env.Agents == nil:
		return nil, errors.New("no agent picker here")
	}
	return &pickerScreen{p: picker.ForAction(a, m.env.Context, m.env.Agents), next: next}, nil
}

// after is what opens once the targets are frozen.
func (m *Model) after(a actions.Action, run forms.Runner) (func([]ui.Target) (screen, error), error) {
	switch a.ID {
	case "workspace.wake":
		return func(ts []ui.Target) (screen, error) { return newWakeScreen(run, m.env.Context, ts), nil }, nil
	case "send":
		return func(ts []ui.Target) (screen, error) {
			return newSendScreen(a, m.env.Core, run.Guard, m.env.Context, ts, m.width, m.height-2, m.env.Memory), nil
		}, nil
	case "agent.zoom":
		return func(ts []ui.Target) (screen, error) {
			return newDoScreen(a, m.env.Core, actions.Request{Context: m.env.Context, Targets: ts}), nil
		}, nil
	}
	if forms.IsAgentAction(a.ID) {
		return func(ts []ui.Target) (screen, error) {
			f, err := forms.NewAgentForm(a, m.env.Core, run.Guard, m.env.Context, ts, nil, forms.WithOrigin("menu"))
			if err != nil {
				return nil, err
			}
			return &agentScreen{f: f}, nil
		}, nil
	}
	return nil, errors.New("this menu has no form for it yet")
}

// execScreen runs a surface in the popup; the menu closes it and comes
// back when the surface ends (surfaceEnded).
type execScreen struct{ cmd tea.Cmd }

func (s *execScreen) Init() tea.Cmd                  { return s.cmd }
func (s *execScreen) Update(tea.Msg) (tea.Cmd, bool) { return nil, false }
func (s *execScreen) View(int, int) string           { return "" }

// pickerScreen is the agent picker; its pick opens the next screen.
type pickerScreen struct {
	p    picker.Model
	next func([]ui.Target) (screen, error)
}

func (s *pickerScreen) Init() tea.Cmd { return s.p.Init() }

func (s *pickerScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case picker.Picked:
		if m.ID != s.p.ID() {
			return nil, false
		}
		nx, err := s.next(m.Targets)
		if err != nil {
			text := err.Error()
			return func() tea.Msg { return noteMsg{text: text} }, false
		}
		return func() tea.Msg { return openScreen{s: nx} }, false
	case picker.Canceled:
		return nil, m.ID == s.p.ID()
	}
	var cmd tea.Cmd
	s.p, cmd = s.p.Update(msg)
	return cmd, false
}

func (s *pickerScreen) View(int, int) string { return s.p.View() }

// viewScreen is a read-only view. A form it asks for (views.Open) opens
// over it, and the view comes back when the form closes.
type viewScreen struct {
	v    views.View
	open func(views.Open) (screen, error)
}

func (s *viewScreen) Init() tea.Cmd { return s.v.Init() }

func (s *viewScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case views.Back:
		return nil, true
	case views.Open:
		form, err := s.open(m)
		if err != nil {
			text := err.Error()
			return func() tea.Msg { return noteMsg{text: text} }, false
		}
		return func() tea.Msg { return pushScreen{s: form} }, false
	}
	model, cmd := s.v.Update(msg)
	if v, ok := model.(views.View); ok {
		s.v = v
	}
	return cmd, false
}

func (s *viewScreen) View(int, int) string { return s.v.View() }

// agentScreen is the shared per-agent form (forms.AgentForm).
type agentScreen struct{ f *forms.AgentForm }

func (s *agentScreen) Init() tea.Cmd { return s.f.Init() }

func (s *agentScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	cmd := s.f.Update(msg)
	return cmd, s.f.Done()
}

func (s *agentScreen) View(width, _ int) string { return s.f.View(width) }

// wizardScreen is the spawn wizard.
type wizardScreen struct{ w *forms.SpawnWizard }

func (s *wizardScreen) Init() tea.Cmd { return s.w.Init() }

func (s *wizardScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case forms.SpawnWizardDoneMsg:
		return nil, true
	case tea.WindowSizeMsg:
		s.w.SetSize(m.Width, m.Height)
		return nil, false
	}
	_, cmd := s.w.Update(msg)
	return cmd, false
}

func (s *wizardScreen) View(int, int) string { return s.w.View() }

// opsScreen is recent operations: the core's records and the UI's jobs,
// newest first; enter shows the selected one's targets.
type opsScreen struct {
	v      *forms.OperationsView
	detail bool
}

func (s *opsScreen) Init() tea.Cmd { return s.v.Init() }

func (s *opsScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc", "q", "backspace", "left":
			if s.detail {
				s.detail = false
				return nil, false
			}
			return nil, true
		case "up":
			s.v.Move(-1)
		case "down":
			s.v.Move(1)
		case "enter":
			s.detail = !s.detail
		case "r":
			return s.v.Refresh(), false
		}
		return nil, false
	}
	return s.v.Update(msg), false
}

func (s *opsScreen) View(width, height int) string {
	var b strings.Builder
	b.WriteString("recent operations\n\n")
	if err := s.v.Err(); err != nil {
		b.WriteString("read failed (the list is as last read): " + clip(err.Error(), width-2) + "\n")
	}
	rows := s.v.Rows()
	if len(rows) == 0 {
		b.WriteString("none yet\n")
	}
	for _, r := range rows {
		mark := "  "
		if r.ID == s.v.Selected() {
			mark = "▶ "
		}
		line := fmt.Sprintf("%s %-14s %-12s %-8s %s", r.Created.Local().Format("15:04:05"), r.Verb, r.Status, r.State, r.Workspace)
		b.WriteString(mark + clip(line, width-2) + "\n")
		if r.ID != s.v.Selected() || !s.detail {
			continue
		}
		fmt.Fprintf(&b, "    %s\n", r.ID)
		if r.Detail != "" {
			b.WriteString("    " + clip(r.Detail, width-4) + "\n")
		}
		if r.State == forms.OpUnknown || r.State == forms.OpQuiet {
			b.WriteString("    no final outcome: diagnostics › unfinished operations lists it; nothing replays it\n")
		}
		for _, t := range r.Targets {
			line := fmt.Sprintf("    %s: %s", t.Name, t.Status)
			if t.Detail != "" {
				line += ": " + t.Detail
			}
			b.WriteString(clip(line, width-2) + "\n")
		}
	}
	b.WriteString("\n↑/↓ · enter: targets · r: read again · esc: back")
	return b.String()
}

// sendScreen is "send text": the palette's composer for the frozen
// targets, then the send through the action table, then the receipts.
type sendScreen struct {
	a       actions.Action
	core    actions.Core
	guard   *actions.Guard
	ctx     ui.Context
	targets []ui.Target
	comp    palette.Composer
	running bool
	request uint64
	result  *actions.Result
	err     error
}

type sendDone struct {
	request uint64
	res     actions.Result
	err     error
}

func newSendScreen(a actions.Action, core actions.Core, guard *actions.Guard, c ui.Context, ts []ui.Target, w, h int,
	mem *palette.Submitter) *sendScreen {
	label := fmt.Sprintf("to %d agent(s)", len(ts))
	return &sendScreen{a: a, core: core, guard: guard, ctx: c, targets: ts, comp: palette.NewComposer(label, "", w, h, mem)}
}

func (s *sendScreen) Init() tea.Cmd { return s.comp.Init() }

func (s *sendScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case palette.ComposeCanceledMsg:
		return nil, true
	case palette.ComposedMsg:
		if s.running || s.result != nil {
			return nil, false
		}
		req := actions.Request{Context: s.ctx, Targets: s.targets, Params: map[string]string{actions.ParamText: m.Text}}
		key := actions.ReviewKey(s.a.ID, req)
		var err error
		if _, err = s.guard.Review(key); err == nil {
			req.OperationID, err = s.guard.Submit(key)
		}
		if err != nil {
			s.err = err
			return nil, false
		}
		s.running, s.request = true, nextRequest()
		a, core, rid := s.a, s.core, s.request
		return func() tea.Msg {
			res, err := a.Do(context.Background(), core, req)
			return sendDone{request: rid, res: res, err: err}
		}, false
	case sendDone:
		if m.request != s.request {
			return nil, false
		}
		s.running, s.result, s.err = false, &m.res, m.err
		return nil, false
	case tea.KeyMsg:
		if s.running || s.result != nil {
			// While it runs, esc closes the view; the send runs on.
			return nil, m.String() == "esc" || s.result != nil
		}
	}
	var cmd tea.Cmd
	s.comp, cmd = s.comp.Update(msg)
	return cmd, false
}

func (s *sendScreen) View(width, height int) string {
	switch {
	case s.running:
		return "sending…\nesc closes this view; the send runs on, and recent operations shows it.\n"
	case s.result != nil:
		out := forms.ResultText(*s.result)
		if s.err != nil {
			out += "error: " + s.err.Error() + "\n"
		}
		return out + "\nany key closes\n"
	}
	out := s.comp.View()
	if s.err != nil {
		out += "\n" + s.err.Error()
	}
	return out
}

// openFromView is the form a view asks for: the ensembles view's stop.
func (m *Model) openFromView(o views.Open) (screen, error) {
	if o.Row != "view.ensembles" || o.Action != "stop" {
		return nil, fmt.Errorf("%s %s: this menu has no form for it", o.Row, o.Action)
	}
	a, ok := actions.ByID("ensemble.stop")
	if !ok {
		return nil, errors.New("ensemble stop is not in the action table")
	}
	if why := a.Unavailable(m.env.Context, m.env.Core); why != "" {
		return nil, fmt.Errorf("%s: %s", a.Label, why)
	}
	run := forms.Runner{Action: a, Core: m.env.Core, Guard: actions.NewGuard(m.env.NewID)}
	req := actions.Request{Context: m.env.Context, Params: map[string]string{actions.ParamRun: o.Arg}}
	return &confirmScreen{f: forms.NewConfirmForm(run, req)}, nil
}

// confirmScreen is a review-and-confirm form (ensemble stop).
type confirmScreen struct{ f *forms.ConfirmForm }

func (s *confirmScreen) Init() tea.Cmd { return s.f.StartReview() }

func (s *confirmScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return s.f.Update(msg), false
	}
	switch k.String() {
	case "esc":
		return nil, true
	case "enter":
		switch s.f.Phase() {
		case forms.Reviewed:
			return s.f.Confirm(), false
		case forms.Done, forms.Failed, forms.Refused:
			return nil, true
		}
	}
	return nil, false
}

func (s *confirmScreen) View(width, _ int) string {
	var b strings.Builder
	a := s.f.Action()
	b.WriteString(a.Label + ": " + a.Help + "\n\n")
	switch s.f.Phase() {
	case forms.Reviewing:
		b.WriteString("reading it…\n")
	case forms.Reviewed:
		b.WriteString(targetsText(s.f.Review(), width))
		b.WriteString("\nenter: " + a.Label + " · esc: back\n")
	case forms.Running:
		b.WriteString(a.Label + "…\n" + running + "\n")
	default:
		r := s.f.Review()
		if s.f.Phase() == forms.Done {
			r = s.f.Result()
		}
		b.WriteString(targetsText(r, width))
		if r.Status != "" {
			b.WriteString("  " + r.Status + "\n")
		}
		if err := s.f.Err(); err != nil {
			b.WriteString("\n" + clip(err.Error(), width) + "\n")
		}
		b.WriteString("\nenter or esc: back\n")
	}
	return b.String()
}

// doScreen runs an action that changes no agent (zoom, focus) at once,
// and shows its answer.
type doScreen struct {
	a       actions.Action
	core    actions.Core
	req     actions.Request
	request uint64
	ended   bool
	res     actions.Result
	err     error
}

type doDone struct {
	request uint64
	res     actions.Result
	err     error
}

func newDoScreen(a actions.Action, core actions.Core, req actions.Request) *doScreen {
	return &doScreen{a: a, core: core, req: req}
}

func (s *doScreen) Init() tea.Cmd {
	s.request = nextRequest()
	a, core, req, rid := s.a, s.core, s.req, s.request
	return func() tea.Msg {
		res, err := a.Do(context.Background(), core, req)
		return doDone{request: rid, res: res, err: err}
	}
}

func (s *doScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case doDone:
		if m.request == s.request {
			s.ended, s.res, s.err = true, m.res, m.err
		}
	case tea.KeyMsg:
		return nil, s.ended || m.String() == "esc"
	}
	return nil, false
}

func (s *doScreen) View(width, _ int) string {
	if !s.ended {
		return s.a.Label + "…\n"
	}
	out := forms.ResultText(s.res)
	if s.err != nil {
		out += clip("error: "+s.err.Error(), width) + "\n"
	}
	return out + "\nany key closes\n"
}

// workspacesScreen lists the workspaces to focus one (workspace.attach).
type workspacesScreen struct {
	a       actions.Action
	core    actions.Core
	ctx     ui.Context
	src     picker.Source
	request uint64
	list    []snapshot.Workspace
	cursor  int
	err     error
	loaded  bool
}

type workspacesRead struct {
	request uint64
	list    []snapshot.Workspace
	err     error
}

func newWorkspacesScreen(a actions.Action, core actions.Core, c ui.Context, src picker.Source) *workspacesScreen {
	return &workspacesScreen{a: a, core: core, ctx: c, src: src}
}

func (s *workspacesScreen) Init() tea.Cmd {
	s.request = nextRequest()
	src, rid := s.src, s.request
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		m, err := src(ctx)
		if err != nil {
			return workspacesRead{request: rid, err: err}
		}
		return workspacesRead{request: rid, list: m.Workspaces}
	}
}

func (s *workspacesScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case workspacesRead:
		if m.request == s.request {
			s.loaded, s.list, s.err = true, m.list, m.err
		}
	case tea.KeyMsg:
		switch m.String() {
		case "esc", "q", "left", "backspace":
			return nil, true
		case "up":
			s.cursor = max(0, s.cursor-1)
		case "down":
			s.cursor = min(len(s.list)-1, s.cursor+1)
		case "enter":
			if s.cursor < len(s.list) {
				req := actions.Request{Context: s.ctx, Params: map[string]string{actions.ParamWorkspace: s.list[s.cursor].ID}}
				next := newDoScreen(s.a, s.core, req)
				return func() tea.Msg { return openScreen{s: next} }, false
			}
		}
	}
	return nil, false
}

func (s *workspacesScreen) View(width, _ int) string {
	var b strings.Builder
	b.WriteString("focus a workspace\n\n")
	switch {
	case !s.loaded:
		b.WriteString("reading the workspaces…\n")
	case s.err != nil:
		b.WriteString(clip("herdr did not answer: "+s.err.Error(), width) + "\n")
	}
	for i, w := range s.list {
		mark := "  "
		if i == s.cursor {
			mark = "▶ "
		}
		line := fmt.Sprintf("%s (%s), %d agent(s)", forms.Clean(w.Label), forms.Clean(w.ID), w.AgentCount)
		if w.Focused {
			line += ", focused"
		}
		b.WriteString(mark + clip(line, width-2) + "\n")
	}
	b.WriteString("\n↑/↓ · enter: focus · esc: back")
	return b.String()
}

// requests numbers the menu's own async requests, so an answer to a
// closed screen never matches a new one.
var requests atomic.Uint64

func nextRequest() uint64 { return requests.Add(1) }
