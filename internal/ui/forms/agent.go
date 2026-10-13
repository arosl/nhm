package forms

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/actions"
	"github.com/arosl/nhm/internal/ui/tui/theme"
)

// AgentForm is the one form of the per-agent actions of the action table
// (interrupt, retire, recycle, adopt), for the board's action menu and
// the menu's agent rows (nhm-qbi.9). It shows the frozen targets and the
// action's fields; the review fixes the operation id (actions.Guard), and
// retire's review runs its checks as a dry run that claims nothing; the
// confirm defaults to cancel; the confirm submits once, through
// actions.Action.Do, which runs the core as a job of its own (the CLI's
// Core); the result shows each target's outcome in the core's words.
// Closing the form while it runs does not cancel the job.
type AgentForm struct {
	run     Runner
	ctx     ui.Context
	targets []ui.Target
	names   map[string]string
	fields  []*field
	focus   int
	// frozen, when set, refuses the submission while it returns an error
	// (the board's upgrade freeze, plan-v4 section 7).
	frozen func() error

	phase   Phase
	request uint64
	key     string
	opID    string
	review  actions.Result
	yes     bool
	result  actions.Result
	err     error
	note    string
	closed  bool
}

// AgentOption sets up an agent form.
type AgentOption func(*agentSetup)

type agentSetup struct {
	frozen func() error
	origin string
}

// WithFrozen makes the form refuse its submission while frozen returns an
// error; the error is shown. The review still runs: it only reads.
func WithFrozen(frozen func() error) AgentOption { return func(s *agentSetup) { s.frozen = frozen } }

// WithOrigin names the surface the targets come from ("board", "menu");
// the default is the context's origin.
func WithOrigin(origin string) AgentOption { return func(s *agentSetup) { s.origin = origin } }

// field is one input of the form: text, or a toggle.
type field struct {
	param string
	label string
	text  bool
	input textinput.Model
	on    bool
}

// AgentFormActions are the actions the form takes.
var AgentFormActions = []string{"agent.interrupt", "agent.retire", "agent.recycle", "agent.adopt"}

// IsAgentAction reports whether the agent form runs the action with id.
func IsAgentAction(id string) bool {
	for _, a := range AgentFormActions {
		if a == id {
			return true
		}
	}
	return false
}

// RunTimeout bounds how long the form waits for the core's answer; the
// job itself runs on, and no answer in time is "unknown".
var RunTimeout = 15 * time.Minute

// doWithin is Runner.do with a deadline on the wait.
func doWithin(r Runner, request uint64, req actions.Request, d time.Duration) tea.Cmd {
	a, core := r.Action, r.Core
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		res, err := a.Do(ctx, core, req)
		return done{request: request, res: res, err: err}
	}
}

// NewAgentForm is action a on targets ts in context c: the marks, or the
// row under the cursor, or the picker's pick. names maps a target's Key()
// to the name to show. It refuses an action the form does not take, one
// that cannot run here, and targets that do not fit the action.
func NewAgentForm(a actions.Action, core actions.Core, guard *actions.Guard, c ui.Context, ts []ui.Target,
	names map[string]string, opts ...AgentOption) (*AgentForm, error) {
	if !IsAgentAction(a.ID) {
		return nil, fmt.Errorf("forms: %s is not an agent action of this form", a.ID)
	}
	if guard == nil {
		return nil, errors.New("forms: an agent form needs the surface's guard")
	}
	if why := a.Unavailable(c, core); why != "" {
		return nil, fmt.Errorf("%w: %s: %s", actions.ErrUnavailable, a.Label, why)
	}
	setup := agentSetup{origin: c.Origin}
	for _, o := range opts {
		o(&setup)
	}
	c = c.WithTargets(setup.origin, ts)
	targets, _, err := a.Targets(c)
	if err != nil {
		return nil, err
	}
	f := &AgentForm{run: Runner{Action: a, Core: core, Guard: guard}, ctx: c, targets: targets, names: names, frozen: setup.frozen}
	switch a.ID {
	case "agent.retire":
		f.fields = []*field{toggle(actions.ParamKeepIdentity, "keep the Agent Mail identity"),
			toggle(actions.ParamNoBeads, "it worked no beads (a reviewer)")}
	case "agent.recycle":
		f.fields = []*field{text(actions.ParamTemplate, "template or palette key"), text(actions.ParamPrompt, "or kickoff text"),
			toggle(actions.ParamNoHandoff, "no handoff")}
	case "agent.adopt":
		f.fields = []*field{text(actions.ParamMailName, "Agent Mail name"), text(actions.ParamHandoffFrom, "or hand over from binding")}
	}
	return f, nil
}

func toggle(param, label string) *field { return &field{param: param, label: label} }

func text(param, label string) *field {
	in := textinput.New()
	in.Prompt, in.CharLimit = "", 4096
	// A static cursor: no blink timer runs in the popup.
	in.Cursor.SetMode(cursor.CursorStatic)
	return &field{param: param, label: label, text: true, input: in}
}

// Phase is where the form is.
func (f *AgentForm) Phase() Phase { return f.phase }

// Action is the form's action.
func (f *AgentForm) Action() actions.Action { return f.run.Action }

// Targets are the form's frozen targets.
func (f *AgentForm) Targets() []ui.Target { return append([]ui.Target(nil), f.targets...) }

// Running reports whether a submission waits for its answer.
func (f *AgentForm) Running() bool { return f.phase == Running }

// Done reports whether the operator left the form.
func (f *AgentForm) Done() bool { return f.closed }

// OperationID is the review's operation id, once reviewed.
func (f *AgentForm) OperationID() string { return f.opID }

// Result is the core's answer, once it came.
func (f *AgentForm) Result() actions.Result { return f.result }

// Err is the error the review or the run ended with.
func (f *AgentForm) Err() error { return f.err }

// Init opens the form: the first field, or the review when there is none.
func (f *AgentForm) Init() tea.Cmd {
	if len(f.fields) == 0 {
		return f.startReview()
	}
	f.phase = Editing
	return f.setFocus(0)
}

func (f *AgentForm) setFocus(i int) tea.Cmd {
	if len(f.fields) == 0 {
		return nil
	}
	f.focus = (i + len(f.fields)) % len(f.fields)
	var cmd tea.Cmd
	for j, fl := range f.fields {
		if !fl.text {
			continue
		}
		if j == f.focus {
			cmd = fl.input.Focus()
		} else {
			fl.input.Blur()
		}
	}
	return cmd
}

// params are the form's values: the real request's, without a dry run.
func (f *AgentForm) params() map[string]string {
	p := map[string]string{}
	for _, fl := range f.fields {
		switch {
		case fl.text && strings.TrimSpace(fl.input.Value()) != "":
			p[fl.param] = strings.TrimSpace(fl.input.Value())
		case !fl.text && fl.on:
			p[fl.param] = "1"
		}
	}
	return p
}

// check refuses values the action cannot take.
func (f *AgentForm) check(p map[string]string) string {
	switch f.run.Action.ID {
	case "agent.recycle":
		if p[actions.ParamTemplate] != "" && p[actions.ParamPrompt] != "" {
			return "give a template or a kickoff text, not both"
		}
	case "agent.adopt":
		if p[actions.ParamMailName] == "" && p[actions.ParamHandoffFrom] == "" {
			return "give the Agent Mail name, or the binding it takes over"
		}
	}
	return ""
}

func (f *AgentForm) requestFor(p map[string]string, opID string) actions.Request {
	return actions.Request{Context: f.ctx, Targets: f.targets, Params: p, OperationID: opID}
}

// startReview fixes the request and its operation id; retire's review
// runs its checks as a dry run under that id, which claims nothing.
func (f *AgentForm) startReview() tea.Cmd {
	p := f.params()
	if why := f.check(p); why != "" {
		f.note = why
		return nil
	}
	key, id, err := f.run.review(f.requestFor(p, ""))
	if err != nil {
		f.phase, f.err = Failed, err
		return nil
	}
	f.key, f.opID, f.yes, f.review, f.err, f.note = key, id, false, actions.Result{}, nil, ""
	if f.run.Action.ID != "agent.retire" {
		f.phase = Reviewed
		return nil
	}
	f.phase, f.request = Reviewing, nextRequest()
	return doWithin(f.run, f.request, withParam(f.requestFor(p, id), actions.ParamDryRun, "1"), RunTimeout)
}

// confirm submits the reviewed request once; a frozen surface refuses it
// before the guard spends the submission.
func (f *AgentForm) confirm() tea.Cmd {
	if f.frozen != nil {
		if err := f.frozen(); err != nil {
			f.note = err.Error()
			return nil
		}
	}
	id, err := f.run.Guard.Submit(f.key)
	if err != nil {
		f.note = err.Error()
		return nil
	}
	f.phase, f.request = Running, nextRequest()
	run, guard, key := doWithin(f.run, f.request, f.requestFor(f.params(), id), RunTimeout), f.run.Guard, f.key
	return func() tea.Msg {
		msg := run()
		// The hold ends where the answer arrives, also when the form was
		// closed meanwhile; a lost answer keeps it (Guard.Settle).
		if d, ok := msg.(done); ok {
			guard.Settle(key, d.err)
		}
		return msg
	}
}

// Update takes keys and the core's answers.
func (f *AgentForm) Update(msg tea.Msg) tea.Cmd {
	if f.closed {
		return nil
	}
	switch m := msg.(type) {
	case done:
		if m.request != f.request {
			return nil
		}
		switch f.phase {
		case Reviewing:
			f.review = m.res
			switch {
			case m.res.Status == "refused":
				f.phase, f.err = Refused, m.err
			case m.err != nil:
				f.phase, f.err = Failed, m.err
			default:
				f.phase = Reviewed
			}
		case Running:
			f.result, f.err = m.res, m.err
			f.phase = Done
			if m.err != nil {
				f.phase = Failed
			}
		}
	case tea.KeyMsg:
		return f.onKey(m)
	}
	return nil
}

func (f *AgentForm) onKey(k tea.KeyMsg) tea.Cmd {
	switch f.phase {
	case Editing:
		return f.editKey(k)
	case Reviewed:
		switch k.String() {
		case "tab", "shift+tab", "left", "right", "h", "l":
			f.yes = !f.yes
		case "enter":
			if f.yes {
				return f.confirm()
			}
			f.closed = true
		case "esc", "q":
			f.back()
		}
	case Reviewing, Refused:
		if s := k.String(); s == "esc" || s == "q" || s == "enter" {
			f.back()
		}
	case Running:
		if s := k.String(); s == "esc" || s == "q" {
			// Closing is not cancellation: the job runs on.
			f.closed = true
		}
	case Done, Failed:
		if s := k.String(); s == "esc" || s == "q" || s == "enter" {
			f.closed = true
		}
	}
	return nil
}

// back leaves a review: to the fields, or out when there are none. The
// review's id stays with its request; an edit makes a new review.
func (f *AgentForm) back() {
	// A new request id drops a review answer still on its way.
	f.request = nextRequest()
	if len(f.fields) == 0 {
		f.closed = true
		return
	}
	f.phase, f.yes, f.err, f.note = Editing, false, nil, ""
}

func (f *AgentForm) editKey(k tea.KeyMsg) tea.Cmd {
	fl := f.fields[f.focus]
	switch k.String() {
	case "esc":
		f.closed = true
		return nil
	case "enter":
		return f.startReview()
	case "tab", "down":
		return f.setFocus(f.focus + 1)
	case "shift+tab", "up":
		return f.setFocus(f.focus - 1)
	case " ":
		if !fl.text {
			fl.on = !fl.on
			return nil
		}
	}
	if fl.text {
		f.note = ""
		var cmd tea.Cmd
		fl.input, cmd = fl.input.Update(k)
		return cmd
	}
	return nil
}

// View draws the form in width columns.
func (f *AgentForm) View(width int) string {
	st := theme.NewStyles(theme.Current())
	fit := func(s string) string {
		if width > 0 {
			return ansi.Truncate(s, width, "…")
		}
		return s
	}
	var b strings.Builder
	line := func(style func(...string) string, s string) { b.WriteString(style(fit(s)) + "\n") }
	n := len(f.targets)
	head := fmt.Sprintf("%s %d agent", f.run.Action.Label, n)
	if n != 1 {
		head += "s"
	}
	line(st.Title.Render, head+" · workspace "+cleanText(firstNonEmpty(f.ctx.WorkspaceLabel, f.ctx.WorkspaceID, "none")))
	for _, t := range f.targets {
		line(st.Normal.Render, "  "+f.describe(t))
	}
	if len(f.fields) > 0 {
		b.WriteString("\n")
		for i, fl := range f.fields {
			pointer := "  "
			if f.phase == Editing && i == f.focus {
				pointer = "> "
			}
			value := "[ ]"
			switch {
			case fl.text:
				value = fl.input.View()
				if f.phase != Editing || i != f.focus {
					value = cleanText(fl.input.Value())
				}
			case fl.on:
				value = "[x]"
			}
			line(st.Normal.Render, pointer+fl.label+": "+value)
		}
	}
	b.WriteString("\n")
	switch f.phase {
	case Reviewing:
		line(st.Dim.Render, "checking…")
	case Refused:
		line(st.Error.Render, f.run.Action.Label+" refuses:")
		f.writeTargets(&b, st, fit, f.review)
	case Reviewed:
		if f.run.Action.ID == "agent.retire" {
			line(st.Info.Render, "retire will:")
			for _, l := range f.review.Lines {
				line(st.Normal.Render, "  "+cleanText(l))
			}
			for _, w := range f.review.Warnings {
				line(st.Warning.Render, "  "+cleanText(w))
			}
		}
		cancel, ok := "[ cancel ]", "  confirm  "
		if f.yes {
			cancel, ok = "  cancel  ", "[ confirm ]"
		}
		line(st.Bold.Render, cancel+"  "+ok)
	case Running:
		line(st.Info.Render, "running operation "+f.opID+"; closing this leaves it running (recent operations)")
	case Done, Failed:
		f.writeResult(&b, st, fit)
	}
	if f.note != "" {
		line(st.Warning.Render, cleanText(f.note))
	}
	line(st.Help.Render, f.keys())
	return strings.TrimRight(b.String(), "\n")
}

func (f *AgentForm) describe(t ui.Target) string {
	name := cleanText(f.names[t.Key()])
	switch {
	case t.Bound() && name == "":
		name = t.BindingID
	case name == "":
		name = "pane " + t.PaneID
	}
	if t.Bound() {
		return fmt.Sprintf("%s  (generation %d, pane %s)", name, t.Generation, firstNonEmpty(t.PaneID, "gone"))
	}
	return fmt.Sprintf("%s  (no binding, %s agent in pane %s)", name, cleanText(t.Kind), t.PaneID)
}

func (f *AgentForm) writeTargets(b *strings.Builder, st theme.Styles, fit func(string) string, r actions.Result) {
	for _, tr := range r.Targets {
		name := cleanText(firstNonEmpty(tr.Name, f.names[tr.Target.Key()], tr.Target.BindingID, tr.Target.PaneID))
		s := "  " + name + ": " + cleanText(tr.Status)
		if tr.Reason != "" {
			s += ": " + cleanText(tr.Reason)
		}
		b.WriteString(st.Normal.Render(fit(s)) + "\n")
	}
}

func (f *AgentForm) writeResult(b *strings.Builder, st theme.Styles, fit func(string) string) {
	r := f.result
	if f.err != nil && len(r.Targets) == 0 && len(r.Lines) == 0 {
		// No result at all: a lost answer is unknown; any other error
		// (the job did not start, a refusal before the claim) ran nothing.
		if actions.NoAnswer(f.err) {
			b.WriteString(st.Warning.Render(fit("no answer: the outcome of operation "+f.opID+" is unknown; see recent operations")) + "\n")
		}
		b.WriteString(st.Error.Render(fit(cleanText(f.err.Error()))) + "\n")
		return
	}
	status := firstNonEmpty(r.Status, "done")
	b.WriteString(st.Title.Render(fit(f.run.Action.Label+": "+cleanText(status)+" · operation "+firstNonEmpty(r.OperationID, f.opID))) + "\n")
	f.writeTargets(b, st, fit, r)
	for _, l := range r.Lines {
		b.WriteString(st.Normal.Render(fit("  "+cleanText(l))) + "\n")
	}
	for _, w := range r.Warnings {
		b.WriteString(st.Warning.Render(fit("  "+cleanText(w))) + "\n")
	}
	if f.err != nil && len(r.Targets) == 0 {
		b.WriteString(st.Error.Render(fit(cleanText(f.err.Error()))) + "\n")
	}
}

func (f *AgentForm) keys() string {
	switch f.phase {
	case Editing:
		return "tab next · space toggle · enter review · esc close"
	case Reviewed:
		return "tab choose · enter · esc back"
	case Running:
		return "esc close (the job runs on)"
	case Done, Failed:
		return "enter close"
	}
	return "esc back"
}

// cleanText drops terminal controls from names, reasons and errors
// (plan-v4 section 4).
func cleanText(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
