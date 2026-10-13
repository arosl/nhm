package menu

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/actions"
	"github.com/arosl/nhm/internal/ui/forms"
)

// idleStep is how far ←/→ move reap's idle limit.
const idleStep = 15 * time.Minute

// running is the line a form shows while its job runs.
const running = "esc closes this view; the operation runs on, and recent operations shows how it ends."

// resultText is an action's whole answer (its status, lines, targets
// and warnings), each line clipped to width.
func resultText(r actions.Result, width int) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimRight(forms.ResultText(r), "\n"), "\n") {
		if l != "" {
			b.WriteString(clip(l, width) + "\n")
		}
	}
	return b.String()
}

// targetsText lists a review's or a result's targets in the core's words.
func targetsText(r actions.Result, width int) string {
	var b strings.Builder
	for _, l := range r.Lines {
		b.WriteString(clip("  "+l, width) + "\n")
	}
	for _, t := range r.Targets {
		line := "  " + t.Name + ": " + t.Status
		if t.Reason != "" {
			line += ": " + t.Reason
		}
		b.WriteString(clip(line, width) + "\n")
	}
	return b.String()
}

// head is a form's title, with a blank line under it when the popup has
// room for one.
func head(height int, title ...string) []string {
	if height >= compactHeight {
		return append(title, "")
	}
	return title
}

// withErr is body with err's line under it.
func withErr(body []string, err error, width int) []string {
	if err == nil {
		return body
	}
	return append(body, "", clip(err.Error(), width))
}

// reapScreen is reap: a dry run, then exactly that set.
type reapScreen struct {
	f  *forms.ReapForm
	pg page
}

func newReapScreen(run forms.Runner, c ui.Context) *reapScreen {
	return &reapScreen{f: forms.NewReapForm(run, c, actions.DefaultIdle)}
}

func (s *reapScreen) Init() tea.Cmd { return s.review() }

func (s *reapScreen) review() tea.Cmd {
	s.pg.reset()
	return s.f.StartReview()
}

func (s *reapScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		s.pg.size(m)
		return nil, false
	case tea.KeyMsg:
		return s.key(m)
	}
	before := s.f.Phase()
	cmd := s.f.Update(msg)
	if s.f.Phase() != before {
		s.pg.reset()
	}
	return cmd, false
}

func (s *reapScreen) key(k tea.KeyMsg) (tea.Cmd, bool) {
	if s.pg.key(k, s.layout(s.pg.width, s.pg.height)) {
		return nil, false
	}
	switch k.String() {
	case "esc", "q":
		return nil, true
	case "left", "-":
		if s.f.CanReview() {
			s.f.SetIdle(max(0, s.f.Idle()-idleStep))
			return s.review(), false
		}
	case "right", "+":
		if s.f.CanReview() {
			s.f.SetIdle(s.f.Idle() + idleStep)
			return s.review(), false
		}
	case "r":
		if s.f.CanReview() {
			return s.review(), false
		}
	case "enter":
		switch s.f.Phase() {
		case forms.Reviewed:
			// Only a set the operator saw whole is retired.
			if !s.pg.seenAll(s.layout(s.pg.width, s.pg.height)) {
				return nil, false
			}
			s.pg.reset()
			return s.f.Confirm(), false
		case forms.Done, forms.Failed, forms.Refused:
			return nil, true
		}
	}
	return nil, false
}

func (s *reapScreen) layout(width, height int) layout {
	l := layout{head: head(height, clip(fmt.Sprintf("reap: retire the agents idle for %s or more, whose work is done", s.f.Idle()), width))}
	keys := "esc: close"
	switch s.f.Phase() {
	case forms.Reviewing:
		l.body = []string{"dry run…"}
	case forms.Reviewed:
		l.body = lines(targetsText(s.f.Review(), width))
		keys = fmt.Sprintf("enter: retire these %d agent(s) · ←/→ idle −/+%s · r: dry run again · esc: close", len(s.f.Set()), idleStep)
	case forms.Refused, forms.Failed, forms.Done:
		if s.f.Phase() == forms.Done || len(s.f.Result().Targets) > 0 {
			l.body = lines(resultText(s.f.Result(), width))
		} else {
			l.body = lines(targetsText(s.f.Review(), width))
		}
		l.body = withErr(l.body, s.f.Err(), width)
		keys = "enter or esc: close"
		if s.f.CanReview() {
			keys = fmt.Sprintf("enter or esc: close · ←/→ idle −/+%s · r: dry run again", idleStep)
		}
	case forms.Running:
		l.body = []string{"retiring one agent at a time, through retire's checks…", clip(running, width)}
	}
	l.foot = []string{clip(keys, width)}
	return l
}

func (s *reapScreen) View(width, height int) string {
	return s.pg.render(s.layout(width, height), width, height)
}

// killScreen is kill: the checks, the typed label, the kill.
type killScreen struct {
	f     *forms.KillForm
	typed *field
	pg    page
}

func newKillScreen(run forms.Runner, c ui.Context) *killScreen {
	return &killScreen{f: forms.NewKillForm(run, c), typed: newField("label", "")}
}

func (s *killScreen) Init() tea.Cmd { return s.review() }

func (s *killScreen) review() tea.Cmd {
	s.pg.reset()
	s.typed.set("")
	return s.f.StartReview()
}

func (s *killScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		s.pg.size(m)
		return nil, false
	case tea.KeyMsg:
		return s.key(m)
	}
	before := s.f.Phase()
	cmd := s.f.Update(msg)
	if s.f.Phase() != before {
		s.pg.reset()
	}
	return cmd, false
}

func (s *killScreen) key(k tea.KeyMsg) (tea.Cmd, bool) {
	if s.pg.key(k, s.layout(s.pg.width, s.pg.height)) {
		return nil, false
	}
	ph := s.f.Phase()
	switch k.String() {
	case "esc":
		return nil, true
	case "ctrl+b":
		if s.f.CanReview() {
			s.f.SetNoBeads(!s.f.NoBeads())
			return s.review(), false
		}
		return nil, false
	case "r":
		// While the label is typed, r is a letter of it.
		if ph != forms.Reviewed && s.f.CanReview() {
			return s.review(), false
		}
	case "enter":
		switch ph {
		case forms.Reviewed:
			// Only a review the operator saw whole is confirmed.
			if !s.pg.seenAll(s.layout(s.pg.width, s.pg.height)) {
				return nil, false
			}
			s.f.Type(s.typed.String())
			cmd := s.f.Confirm()
			if cmd != nil {
				s.pg.reset()
			}
			return cmd, false
		case forms.Done, forms.Failed, forms.Refused:
			return nil, true
		}
		return nil, false
	}
	if ph == forms.Reviewed && s.typed.key(k) {
		s.f.Type(s.typed.String())
	}
	return nil, false
}

func (s *killScreen) layout(width, height int) layout {
	title := []string{clip(fmt.Sprintf("kill: retire every agent of workspace %s, then close it", s.f.Label()), width)}
	if s.f.NoBeads() {
		title = append(title, "(no beads: the agents worked none)")
	}
	l := layout{head: head(height, title...)}
	keys := "esc: close"
	switch s.f.Phase() {
	case forms.Reviewing:
		l.body = []string{"checking every agent with retire's checks…"}
	case forms.Reviewed:
		r := s.f.Review()
		l.body = lines(targetsText(r, width))
		l.foot = []string{clip(fmt.Sprintf("Type the workspace's label, %q, and press enter to kill it.", s.f.Label()), width),
			clip(s.typed.view(true), width)}
		if s.f.Mismatch() {
			l.foot = append(l.foot, "the label does not match: nothing was killed")
		}
		keys = fmt.Sprintf("enter: kill (%d agent(s)) · ctrl+b: no beads · esc: close", len(r.Targets))
	case forms.Refused:
		r := s.f.Review()
		l.body = append([]string{"kill refuses; nothing was closed:"}, lines(targetsText(r, width))...)
		// The error repeats the review's refusals; it is the reason only
		// when the review has none (a label that cannot be typed).
		if err := s.f.Err(); err != nil && len(r.Lines) == 0 && len(r.Targets) == 0 {
			l.body = append(l.body, lines(wrap(err.Error(), width-2))...)
		}
		keys = "r: check again · ctrl+b: no beads · enter or esc: close"
	case forms.Running:
		l.body = []string{"killing…"}
		if p := s.f.Progress(); p != nil {
			l.body = append(l.body, clip(fmt.Sprintf("  record %s: %s", p.OperationID, p.Status), width))
			for _, t := range p.Targets {
				l.body = append(l.body, clip(fmt.Sprintf("  agent %d: %s", t.Index, t.Status), width))
			}
		}
		l.body = append(l.body, clip(running, width))
	case forms.Done, forms.Failed:
		l.body = withErr(lines(resultText(s.f.Result(), width)), s.f.Err(), width)
		keys = "enter or esc: close"
		if s.f.CanReview() {
			keys = "r: check again · enter or esc: close"
		}
	}
	l.foot = append(l.foot, clip(keys, width))
	return l
}

func (s *killScreen) View(width, height int) string {
	return s.pg.render(s.layout(width, height), width, height)
}

// newScreen is new: a label and a project directory.
type newScreen struct {
	f      *forms.NewForm
	fields []*field
	focus  int
	pg     page
}

func newNewScreen(run forms.Runner, c ui.Context) *newScreen {
	f := forms.NewNewForm(run, c)
	return &newScreen{f: f, fields: []*field{newField("label", ""), newField("project", f.Project())}}
}

func (s *newScreen) Init() tea.Cmd { return nil }

func (s *newScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		s.pg.size(m)
		return nil, false
	case tea.KeyMsg:
		return s.key(m)
	}
	before := s.f.Phase()
	cmd := s.f.Update(msg)
	if s.f.Phase() != before {
		s.pg.reset()
	}
	return cmd, false
}

func (s *newScreen) key(k tea.KeyMsg) (tea.Cmd, bool) {
	ph := s.f.Phase()
	if ph != forms.Editing && s.pg.key(k, s.layout(s.pg.width, s.pg.height)) {
		return nil, false
	}
	switch k.String() {
	case "esc":
		return nil, true
	case "tab", "down":
		// The focused field is the body's line on screen.
		s.focus = (s.focus + 1) % len(s.fields)
		s.pg.scroll = s.focus
		return nil, false
	case "shift+tab", "up":
		s.focus = (s.focus + len(s.fields) - 1) % len(s.fields)
		s.pg.scroll = s.focus
		return nil, false
	case "enter":
		if ph >= forms.Done {
			return nil, true
		}
		if ph == forms.Editing {
			s.f.SetLabel(s.fields[0].String())
			s.f.SetProject(s.fields[1].String())
			return s.f.Confirm(), false
		}
		return nil, false
	}
	if ph == forms.Editing {
		s.fields[s.focus].key(k)
	}
	return nil, false
}

func (s *newScreen) layout(width, height int) layout {
	l := layout{head: head(height, clip("new: a workspace with this label, its cwd the project; no agent", width))}
	keys := "esc: close"
	switch s.f.Phase() {
	case forms.Editing:
		for i, f := range s.fields {
			l.body = append(l.body, clip(f.view(i == s.focus), width))
		}
		l.body = withErr(l.body, s.f.Err(), width)
		keys = "tab: next field · enter: create · esc: close"
	case forms.Running:
		l.body = []string{"creating…", clip(running, width)}
	default:
		l.body = withErr(lines(resultText(s.f.Result(), width)), s.f.Err(), width)
		keys = "enter or esc: close"
	}
	l.foot = []string{clip(keys, width)}
	return l
}

func (s *newScreen) View(width, height int) string {
	return s.pg.render(s.layout(width, height), width, height)
}

// wakeScreen is wake for the picked agents.
type wakeScreen struct {
	f  *forms.WakeForm
	pg page
}

func newWakeScreen(run forms.Runner, c ui.Context, ts []ui.Target) *wakeScreen {
	f := forms.NewWakeForm(run, c)
	f.SetRecipients(ts)
	return &wakeScreen{f: f}
}

func (s *wakeScreen) Init() tea.Cmd { return nil }

func (s *wakeScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		s.pg.size(m)
		return nil, false
	case tea.KeyMsg:
		return s.key(m)
	}
	before := s.f.Phase()
	cmd := s.f.Update(msg)
	if s.f.Phase() != before {
		s.pg.reset()
	}
	return cmd, false
}

func (s *wakeScreen) key(k tea.KeyMsg) (tea.Cmd, bool) {
	if s.pg.key(k, s.layout(s.pg.width, s.pg.height)) {
		return nil, false
	}
	switch k.String() {
	case "esc":
		return nil, true
	case "enter":
		switch s.f.Phase() {
		case forms.Editing:
			// Only recipients the operator saw are woken.
			if !s.pg.seenAll(s.layout(s.pg.width, s.pg.height)) {
				return nil, false
			}
			return s.f.Confirm(), false
		case forms.Done, forms.Failed:
			return nil, true
		}
	}
	return nil, false
}

func (s *wakeScreen) layout(width, height int) layout {
	l := layout{head: head(height, clip("wake: tell these agents about their new mail, once", width))}
	keys := "esc: close"
	switch s.f.Phase() {
	case forms.Editing:
		for _, t := range s.f.Recipients() {
			l.body = append(l.body, clip("  "+describe(t), width))
		}
		l.body = withErr(l.body, s.f.Err(), width)
		keys = fmt.Sprintf("enter: wake these %d · esc: close", len(s.f.Recipients()))
	case forms.Running:
		l.body = []string{"waking…", clip(running, width)}
	default:
		l.body = withErr(lines(resultText(s.f.Result(), width)), s.f.Err(), width)
		keys = "enter or esc: close"
	}
	l.foot = []string{clip(keys, width)}
	return l
}

func (s *wakeScreen) View(width, height int) string {
	return s.pg.render(s.layout(width, height), width, height)
}

// describe names a frozen target.
func describe(t ui.Target) string {
	if t.Bound() {
		return fmt.Sprintf("%s (binding %s, generation %d)", t.PaneID, t.BindingID, t.Generation)
	}
	return t.PaneID + " (no binding)"
}
