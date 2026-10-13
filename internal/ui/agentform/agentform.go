// Package agentform is the one review-and-confirm form for nhm's
// per-agent actions (interrupt, retire, recycle): the board's action
// menu and the menu's agent group embed it, so both run an action the
// same way (plan-v4 sections 2 and 4).
//
// A form freezes its targets when it opens. It shows them, the action's
// options and, for retire, retire's own checks (a dry run). Confirm
// starts on cancel. A confirmed request is submitted once through the
// surface's actions.Guard and runs through actions.Action.Do in a
// command, so the update loop never waits; the core's answer is shown
// per target in its words. No answer is "unknown": the core's record
// says what happened, and the form never submits again.
package agentform

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/actions"
)

// RunTimeout bounds the wait for a submitted action's answer; the job
// itself runs on (the core's runner), and recent operations shows it.
const RunTimeout = 15 * time.Minute

// CheckTimeout bounds retire's dry run.
const CheckTimeout = 30 * time.Second

type phase int

const (
	phaseReview phase = iota
	phaseRunning
	phaseResult
)

// option is one of the action's fields: a toggle or one line of text.
type option struct {
	param  string
	label  string
	toggle bool
	value  string // "1"/"" for a toggle
}

// Form is one action's review, confirm and result.
type Form struct {
	id      uint64
	a       actions.Action
	core    actions.Core
	guard   *actions.Guard
	c       ui.Context
	targets []ui.Target
	names   map[string]string
	opts    []option
	// focus: 0..len(opts)-1 an option, len(opts) cancel, len(opts)+1 confirm.
	focus int
	phase phase
	// frozen, when set, refuses the submission (the board's upgrade freeze).
	frozen func() error
	origin string

	checkSeq  uint64
	checking  bool
	check     *actions.Result
	checkErr  error
	checkedAs string // the review key the check ran for

	opID   string
	result *actions.Result
	err    error
	note   string
	done   bool
}

var forms atomic.Uint64

// Option sets up a form.
type Option func(*Form)

// WithFrozen makes the form refuse its submission while frozen returns
// an error.
func WithFrozen(frozen func() error) Option { return func(f *Form) { f.frozen = frozen } }

// WithOrigin names the surface the targets come from (default "board").
func WithOrigin(origin string) Option { return func(f *Form) { f.origin = origin } }

// New opens a form for action a on the frozen targets ts, in context c.
// names maps a target's Key() to its name, for the review. It refuses an
// action that is not built or cannot run here, and targets that do not
// fit the action (two for retire, an unbound agent for a bound-only
// action, a target given twice).
func New(a actions.Action, core actions.Core, guard *actions.Guard, c ui.Context, ts []ui.Target,
	names map[string]string, opts ...Option) (*Form, error) {
	if !a.Visible() || a.Kind != actions.KindForm {
		return nil, fmt.Errorf("agentform: %s is not a form that runs here", a.ID)
	}
	if why := a.Unavailable(c, core); why != "" {
		return nil, fmt.Errorf("%w: %s: %s", actions.ErrUnavailable, a.Label, why)
	}
	if guard == nil {
		return nil, errors.New("agentform: no submission guard")
	}
	f := &Form{id: forms.Add(1), a: a, core: core, guard: guard, c: c, names: names, origin: "board"}
	for _, o := range opts {
		o(f)
	}
	targets, pick, err := a.Targets(c.WithTargets(f.origin, ts))
	switch {
	case err != nil:
		return nil, err
	case pick:
		return nil, errors.New("agentform: no targets")
	}
	f.targets = targets
	switch a.ID {
	case "agent.retire":
		f.opts = []option{
			{param: actions.ParamKeepIdentity, label: "keep the Agent Mail identity active", toggle: true},
			{param: actions.ParamNoBeads, label: "the agent worked no beads (a reviewer)", toggle: true},
		}
	case "agent.recycle":
		f.opts = []option{
			{param: actions.ParamTemplate, label: "kickoff: template or palette key (empty: none)"},
			{param: actions.ParamNoHandoff, label: "no handoff", toggle: true},
		}
	}
	f.focus = len(f.opts) // cancel
	return f, nil
}

// Messages carry the form's id and the request's sequence: an answer to
// another form, or to an older check, is dropped.
type (
	checkedMsg struct {
		form, seq uint64
		key       string
		res       actions.Result
		err       error
	}
	ranMsg struct {
		form uint64
		res  actions.Result
		err  error
	}
)

// Init starts retire's checks.
func (f *Form) Init() tea.Cmd { return f.recheck() }

// Done reports whether the form is closed: canceled, or its result read.
func (f *Form) Done() bool { return f.done }

// Result is the core's answer, once the action ran; nil before.
func (f *Form) Result() (*actions.Result, error) { return f.result, f.err }

// Running reports whether a submission waits for its answer.
func (f *Form) Running() bool { return f.phase == phaseRunning }

func (f *Form) params() map[string]string {
	p := map[string]string{}
	for _, o := range f.opts {
		if v := strings.TrimSpace(o.value); v != "" {
			p[o.param] = v
		}
	}
	return p
}

func (f *Form) request() actions.Request {
	return actions.Request{Context: f.c.WithTargets(f.origin, f.targets), Targets: f.targets, Params: f.params()}
}

// recheck runs retire's checks for the current options: the request's
// own review key and operation id, with ParamDryRun, so nothing is
// claimed and the later submit matches the review.
func (f *Form) recheck() tea.Cmd {
	if f.a.ID != "agent.retire" {
		return nil
	}
	req := f.request()
	key := actions.ReviewKey(f.a.ID, req)
	id, err := f.guard.Review(key)
	if err != nil {
		f.checkErr = err
		return nil
	}
	f.checkSeq++
	f.checking, f.check, f.checkErr = true, nil, nil
	req.OperationID = id
	req.Params[actions.ParamDryRun] = "1"
	a, core, form, seq := f.a, f.core, f.id, f.checkSeq
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), CheckTimeout)
		defer cancel()
		res, err := a.Do(ctx, core, req)
		return checkedMsg{form: form, seq: seq, key: key, res: res, err: err}
	}
}

// Update handles a message for the form; the parent passes it every
// message while the form is open.
func (f *Form) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case checkedMsg:
		if msg.form != f.id || msg.seq != f.checkSeq {
			return nil
		}
		f.checking, f.check, f.checkErr, f.checkedAs = false, &msg.res, msg.err, msg.key
		return nil
	case ranMsg:
		if msg.form != f.id {
			return nil
		}
		f.phase, f.result, f.err = phaseResult, &msg.res, msg.err
		return nil
	case tea.KeyMsg:
		return f.key(msg)
	}
	return nil
}

func (f *Form) key(msg tea.KeyMsg) tea.Cmd {
	switch f.phase {
	case phaseRunning:
		// Closing the form does not cancel the job: it runs on, and
		// recent operations shows it.
		if msg.Type == tea.KeyEsc {
			f.done = true
		}
		return nil
	case phaseResult:
		f.done = true
		return nil
	}
	last := len(f.opts) + 1
	switch msg.Type {
	case tea.KeyEsc:
		f.done = true
	case tea.KeyUp, tea.KeyShiftTab:
		f.focus = max(f.focus-1, 0)
	case tea.KeyDown, tea.KeyTab:
		f.focus = min(f.focus+1, last)
	case tea.KeyLeft:
		if f.focus == last {
			f.focus--
		}
	case tea.KeyRight:
		if f.focus == last-1 {
			f.focus++
		}
	case tea.KeyEnter:
		switch {
		case f.focus == last:
			return f.submit()
		case f.focus == last-1:
			f.done = true
		default:
			f.focus++
		}
	case tea.KeySpace, tea.KeyRunes, tea.KeyBackspace:
		if f.focus >= len(f.opts) {
			return nil
		}
		o := &f.opts[f.focus]
		switch {
		case o.toggle && msg.Type == tea.KeySpace:
			if o.value == "" {
				o.value = "1"
			} else {
				o.value = ""
			}
		case o.toggle:
			return nil
		case msg.Type == tea.KeyBackspace:
			r := []rune(o.value)
			if len(r) > 0 {
				o.value = string(r[:len(r)-1])
			}
		default:
			o.value += strings.Map(func(r rune) rune {
				if r < 0x20 || r == 0x7f {
					return -1
				}
				return r
			}, string(msg.Runes))
		}
		f.note = ""
		return f.recheck()
	}
	return nil
}

// submit hands the reviewed request to the core, once.
func (f *Form) submit() tea.Cmd {
	if f.frozen != nil {
		if err := f.frozen(); err != nil {
			f.note = err.Error()
			return nil
		}
	}
	req := f.request()
	key := actions.ReviewKey(f.a.ID, req)
	if f.a.ID == "agent.retire" {
		switch {
		case f.checking:
			f.note = "retire's checks are still running"
			return nil
		case f.checkedAs != key:
			f.note = "the options changed: retire's checks run again"
			return f.recheck()
		case f.checkErr != nil || f.check != nil && f.check.Status == "refused":
			f.note = "retire's checks refuse this agent"
			return nil
		}
	}
	if _, err := f.guard.Review(key); err != nil {
		f.note = err.Error()
		return nil
	}
	id, err := f.guard.Submit(key)
	if err != nil {
		f.note = err.Error()
		return nil
	}
	req.OperationID = id
	f.opID, f.phase, f.note = id, phaseRunning, ""
	a, core, form := f.a, f.core, f.id
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), RunTimeout)
		defer cancel()
		res, err := a.Do(ctx, core, req)
		return ranMsg{form: form, res: res, err: err}
	}
}

// name is a target's name for the review.
func (f *Form) name(t ui.Target) string {
	if n := f.names[t.Key()]; n != "" {
		return n
	}
	if t.PaneID != "" {
		return "pane " + t.PaneID
	}
	return t.BindingID
}

// View renders the form within width columns.
func (f *Form) View(width int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n\n", f.a.Label, f.a.Help)
	b.WriteString("targets:\n")
	for _, t := range f.targets {
		line := "  " + f.name(t)
		if t.Bound() {
			line += fmt.Sprintf("  (generation %d)", t.Generation)
		}
		b.WriteString(clip(line, width) + "\n")
	}
	switch f.phase {
	case phaseRunning:
		fmt.Fprintf(&b, "\nsubmitted as %s; waiting for the core…\nesc closes this view; the operation runs on, and recent operations shows it.\n", f.opID)
		return b.String()
	case phaseResult:
		b.WriteString("\n" + f.resultText(width))
		b.WriteString("\nany key closes\n")
		return b.String()
	}
	if len(f.opts) > 0 {
		b.WriteString("\noptions:\n")
		for i, o := range f.opts {
			mark := "  "
			if f.focus == i {
				mark = "▶ "
			}
			if o.toggle {
				box := "[ ]"
				if o.value != "" {
					box = "[x]"
				}
				b.WriteString(clip(fmt.Sprintf("%s%s %s", mark, box, o.label), width) + "\n")
			} else {
				b.WriteString(clip(fmt.Sprintf("%s%s: %s▏", mark, o.label, o.value), width) + "\n")
			}
		}
	}
	if f.a.ID == "agent.retire" {
		b.WriteString("\nretire's checks:\n")
		switch {
		case f.checking:
			b.WriteString("  running…\n")
		case f.check != nil && len(f.check.Targets) > 0:
			// A refusal comes with the reasons, in retire's words.
			for _, t := range f.check.Targets {
				b.WriteString(clip("  "+t.Status+": "+t.Reason, width) + "\n")
			}
			for _, l := range f.check.Lines {
				b.WriteString(clip("  would "+l, width) + "\n")
			}
		case f.checkErr != nil:
			b.WriteString(clip("  "+f.checkErr.Error(), width) + "\n")
		}
	}
	cancel, confirm := "  cancel  ", "  "+f.a.Label+"  "
	last := len(f.opts) + 1
	if f.focus == last-1 {
		cancel = "[ cancel ]"
	}
	if f.focus == last {
		confirm = "[ " + f.a.Label + " ]"
	}
	fmt.Fprintf(&b, "\n%s   %s\n", cancel, confirm)
	if f.note != "" {
		b.WriteString(clip(f.note, width) + "\n")
	}
	b.WriteString("tab/←/→ move · space toggles · enter picks · esc cancels\n")
	return b.String()
}

// resultText is the core's answer in its words; with no answer, it says
// so and names the operation.
func (f *Form) resultText(width int) string {
	var b strings.Builder
	if f.err != nil && (f.result == nil || len(f.result.Targets) == 0) {
		fmt.Fprintf(&b, "unknown: %s\nThe core's record says what happened: recent operations, %s.\nThe form does not submit it again.\n",
			f.err.Error(), f.opID)
		return clipLines(b.String(), width)
	}
	fmt.Fprintf(&b, "%s (%s)\n", f.result.Status, f.result.OperationID)
	for _, t := range f.result.Targets {
		name := t.Name
		if name == "" {
			name = f.name(t.Target)
		}
		line := "  " + name + ": " + t.Status
		if t.Reason != "" {
			line += " — " + t.Reason
		}
		b.WriteString(line + "\n")
	}
	for _, l := range f.result.Lines {
		b.WriteString("  " + l + "\n")
	}
	for _, w := range f.result.Warnings {
		b.WriteString("  warning: " + w + "\n")
	}
	if f.err != nil {
		b.WriteString("  error: " + f.err.Error() + "\n")
	}
	return clipLines(b.String(), width)
}

// clip cuts a line to width runes, with terminal controls removed.
func clip(s string, width int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' || r == 0x7f || r >= 0x80 && r <= 0x9f {
			return -1
		}
		return r
	}, ansi.Strip(s))
	r := []rune(s)
	if width > 1 && len(r) > width {
		return string(r[:width-1]) + "…"
	}
	return s
}

func clipLines(s string, width int) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = clip(l, width)
	}
	return strings.Join(lines, "\n")
}
