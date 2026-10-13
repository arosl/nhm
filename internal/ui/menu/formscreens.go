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

// reapScreen is reap: a dry run, then exactly that set.
type reapScreen struct{ f *forms.ReapForm }

func newReapScreen(run forms.Runner, c ui.Context) *reapScreen {
	return &reapScreen{f: forms.NewReapForm(run, c, actions.DefaultIdle)}
}

func (s *reapScreen) Init() tea.Cmd { return s.f.StartReview() }

func (s *reapScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return s.f.Update(msg), false
	}
	ph := s.f.Phase()
	switch k.String() {
	case "esc", "q":
		return nil, true
	case "left", "-":
		if s.f.CanReview() {
			s.f.SetIdle(max(0, s.f.Idle()-idleStep))
			return s.f.StartReview(), false
		}
	case "right", "+":
		if s.f.CanReview() {
			s.f.SetIdle(s.f.Idle() + idleStep)
			return s.f.StartReview(), false
		}
	case "r":
		if s.f.CanReview() {
			return s.f.StartReview(), false
		}
	case "enter":
		switch ph {
		case forms.Reviewed:
			return s.f.Confirm(), false
		case forms.Done, forms.Failed, forms.Refused:
			return nil, true
		}
	}
	return nil, false
}

func (s *reapScreen) View(width, _ int) string {
	var b strings.Builder
	b.WriteString(clip(fmt.Sprintf("reap: retire the agents idle for %s or more, whose work is done", s.f.Idle()), width) + "\n\n")
	switch s.f.Phase() {
	case forms.Reviewing:
		b.WriteString("dry run…\n")
	case forms.Reviewed:
		b.WriteString(targetsText(s.f.Review(), width))
		fmt.Fprintf(&b, "\nenter: retire exactly these %d agent(s) (would_retire) · ←/→ idle −/+%s · r: dry run again · esc: close\n",
			len(s.f.Set()), idleStep)
	case forms.Refused, forms.Failed, forms.Done:
		if s.f.Phase() == forms.Done || len(s.f.Result().Targets) > 0 {
			b.WriteString(resultText(s.f.Result(), width))
		} else {
			b.WriteString(targetsText(s.f.Review(), width))
		}
		if err := s.f.Err(); err != nil {
			b.WriteString("\n" + clip(err.Error(), width) + "\n")
		}
		if s.f.CanReview() {
			fmt.Fprintf(&b, "\nenter or esc: close · ←/→ idle −/+%s · r: dry run again\n", idleStep)
		} else {
			b.WriteString("\nenter or esc: close\n")
		}
	case forms.Running:
		b.WriteString("retiring one agent at a time, through retire's checks…\n" + running + "\n")
	}
	return b.String()
}

// killScreen is kill: the checks, the typed label, the kill.
type killScreen struct {
	f     *forms.KillForm
	typed *field
}

func newKillScreen(run forms.Runner, c ui.Context) *killScreen {
	return &killScreen{f: forms.NewKillForm(run, c), typed: newField("label", "")}
}

func (s *killScreen) Init() tea.Cmd { return s.f.StartReview() }

func (s *killScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return s.f.Update(msg), false
	}
	ph := s.f.Phase()
	switch k.String() {
	case "esc":
		return nil, true
	case "ctrl+b":
		if s.f.CanReview() {
			s.f.SetNoBeads(!s.f.NoBeads())
			s.typed.set("")
			return s.f.StartReview(), false
		}
		return nil, false
	case "r":
		// While the label is typed, r is a letter of it.
		if ph != forms.Reviewed && s.f.CanReview() {
			s.typed.set("")
			return s.f.StartReview(), false
		}
	case "enter":
		switch ph {
		case forms.Reviewed:
			s.f.Type(s.typed.String())
			return s.f.Confirm(), false
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

func (s *killScreen) View(width, _ int) string {
	var b strings.Builder
	b.WriteString(clip(fmt.Sprintf("kill: retire every agent of workspace %s, then close it", s.f.Label()), width) + "\n")
	if s.f.NoBeads() {
		b.WriteString("(no beads: the agents worked none)\n")
	}
	b.WriteString("\n")
	switch s.f.Phase() {
	case forms.Reviewing:
		b.WriteString("checking every agent with retire's checks…\n")
	case forms.Reviewed:
		b.WriteString(targetsText(s.f.Review(), width))
		b.WriteString("\n" + clip(fmt.Sprintf("Type the workspace's label, %q, and press enter to kill it.", s.f.Label()), width) + "\n")
		b.WriteString(s.typed.view(true) + "\n")
		if s.f.Mismatch() {
			b.WriteString("the label does not match: nothing was killed\n")
		}
		b.WriteString("\nctrl+b: the agents worked no beads · esc: close\n")
	case forms.Refused:
		r := s.f.Review()
		b.WriteString("kill refuses; nothing was closed:\n" + targetsText(r, width))
		// The error repeats the review's refusals; it is the reason only
		// when the review has none (a label that cannot be typed).
		if err := s.f.Err(); err != nil && len(r.Lines) == 0 && len(r.Targets) == 0 {
			b.WriteString(wrap(err.Error(), width-2) + "\n")
		}
		b.WriteString("\n" + clip("r: check again · ctrl+b: no beads · enter or esc: close", width) + "\n")
	case forms.Running:
		b.WriteString("killing…\n")
		if p := s.f.Progress(); p != nil {
			b.WriteString(clip(fmt.Sprintf("  record %s: %s", p.OperationID, p.Status), width) + "\n")
			for _, t := range p.Targets {
				b.WriteString(clip(fmt.Sprintf("  agent %d: %s", t.Index, t.Status), width) + "\n")
			}
		}
		b.WriteString(running + "\n")
	case forms.Done, forms.Failed:
		b.WriteString(resultText(s.f.Result(), width))
		if err := s.f.Err(); err != nil {
			b.WriteString("\n" + clip(err.Error(), width) + "\n")
		}
		if s.f.CanReview() {
			b.WriteString("\nr: check again · enter or esc: close\n")
		} else {
			b.WriteString("\nenter or esc: close\n")
		}
	}
	return b.String()
}

// newScreen is new: a label and a project directory.
type newScreen struct {
	f      *forms.NewForm
	fields []*field
	focus  int
}

func newNewScreen(run forms.Runner, c ui.Context) *newScreen {
	f := forms.NewNewForm(run, c)
	return &newScreen{f: f, fields: []*field{newField("label", ""), newField("project", f.Project())}}
}

func (s *newScreen) Init() tea.Cmd { return nil }

func (s *newScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return s.f.Update(msg), false
	}
	ph := s.f.Phase()
	switch k.String() {
	case "esc":
		return nil, true
	case "tab", "down":
		s.focus = (s.focus + 1) % len(s.fields)
		return nil, false
	case "shift+tab", "up":
		s.focus = (s.focus + len(s.fields) - 1) % len(s.fields)
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

func (s *newScreen) View(width, _ int) string {
	var b strings.Builder
	b.WriteString("new: a workspace with this label, its cwd the project; no agent\n\n")
	for i, f := range s.fields {
		b.WriteString(clip(f.view(i == s.focus && s.f.Phase() == forms.Editing), width) + "\n")
	}
	switch s.f.Phase() {
	case forms.Editing:
		if err := s.f.Err(); err != nil {
			b.WriteString("\n" + clip(err.Error(), width) + "\n")
		}
		b.WriteString("\ntab: next field · enter: create · esc: close\n")
	case forms.Running:
		b.WriteString("\ncreating…\n" + running + "\n")
	default:
		b.WriteString("\n" + resultText(s.f.Result(), width))
		if err := s.f.Err(); err != nil {
			b.WriteString(clip(err.Error(), width) + "\n")
		}
		b.WriteString("\nenter or esc: close\n")
	}
	return b.String()
}

// wakeScreen is wake for the picked agents.
type wakeScreen struct{ f *forms.WakeForm }

func newWakeScreen(run forms.Runner, c ui.Context, ts []ui.Target) *wakeScreen {
	f := forms.NewWakeForm(run, c)
	f.SetRecipients(ts)
	return &wakeScreen{f: f}
}

func (s *wakeScreen) Init() tea.Cmd { return nil }

func (s *wakeScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return s.f.Update(msg), false
	}
	switch k.String() {
	case "esc":
		return nil, true
	case "enter":
		switch s.f.Phase() {
		case forms.Editing:
			return s.f.Confirm(), false
		case forms.Done, forms.Failed:
			return nil, true
		}
	}
	return nil, false
}

func (s *wakeScreen) View(width, _ int) string {
	var b strings.Builder
	b.WriteString("wake: tell these agents about their new mail, once\n\n")
	switch s.f.Phase() {
	case forms.Editing:
		for _, t := range s.f.Recipients() {
			b.WriteString(clip("  "+describe(t), width) + "\n")
		}
		if err := s.f.Err(); err != nil {
			b.WriteString("\n" + clip(err.Error(), width) + "\n")
		}
		b.WriteString("\nenter: wake them · esc: close\n")
	case forms.Running:
		b.WriteString("waking…\n" + running + "\n")
	default:
		b.WriteString(resultText(s.f.Result(), width))
		if err := s.f.Err(); err != nil {
			b.WriteString("\n" + clip(err.Error(), width) + "\n")
		}
		b.WriteString("\nenter or esc: close\n")
	}
	return b.String()
}

// describe names a frozen target.
func describe(t ui.Target) string {
	if t.Bound() {
		return fmt.Sprintf("%s (binding %s, generation %d)", t.PaneID, t.BindingID, t.Generation)
	}
	return t.PaneID + " (no binding)"
}
