package palette

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/arosl/nhm/internal/core/enrich"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/templates"
	"github.com/arosl/nhm/internal/ui/tui/layout"
	"github.com/arosl/nhm/internal/ui/tui/styles"
)

// sendEnv is what a submission needs from the caller.
type sendEnv struct {
	submitter *Submitter
	base      send.Request
	bodies    Bodies
}

// submission is the palette's one submission (plan-v4 section 4: one
// submission, durable results): what it sent to whom, and its answer.
type submission struct {
	seq      uint64
	opID     string
	inFlight bool
	label    string
	payload  Payload
	targets  Targets
	// err refused the submission before it reached the core.
	err    error
	answer *SubmitMsg
	// reconciled is what the core recorded, read again with r.
	reconciled  *ReconcileMsg
	reconciling bool
}

// result is the newest known outcome: the reconciled one, else the
// answer.
func (s submission) result() (send.Result, bool) {
	if s.reconciled != nil && s.reconciled.Err == nil {
		return s.reconciled.Result, true
	}
	if s.answer != nil && s.answer.Err == nil {
		return s.answer.Result, true
	}
	return send.Result{}, false
}

// sentAny reports whether at least one target was submitted.
func (s submission) sentAny() bool {
	res, ok := s.result()
	if !ok {
		return false
	}
	for _, rc := range res.Targets {
		if rc.Status == send.StatusSubmitted {
			return true
		}
	}
	return false
}

// Pending is the operation id of a submission whose answer has not come
// back, or "": the caller says, after the program exits, that it runs on.
func (m Model) Pending() string {
	if m.sub.inFlight {
		return m.sub.opID
	}
	return ""
}

// recentSavedMsg follows a write of the recents.
type recentSavedMsg struct {
	err error
}

// submit starts the submission of p to the frozen targets ts and shows
// the result screen. The send runs in the core; the screen waits for
// its answer.
func (m *Model) submit(p Payload, ts Targets) (tea.Model, tea.Cmd) {
	label := ""
	if m.selected != nil {
		label = m.selected.Label
	}
	m.sub = submission{label: label, payload: p, targets: ts}
	m.phase = PhaseResult
	if m.env.submitter == nil {
		m.sub.err = errors.New("this palette has no way to send")
		return *m, nil
	}
	path := ""
	if p.Template {
		var err error
		if path, err = m.env.bodies.Path(p); err != nil {
			m.sub.err = err
			return *m, nil
		}
	}
	req, err := BuildRequest(m.withMemoryChoice(m.env.base), p, ts, path)
	if err != nil {
		m.sub.err = err
		return *m, nil
	}
	seq, opID, cmd, err := m.env.submitter.Submit(req)
	if err != nil {
		m.sub.err = err
		return *m, nil
	}
	m.sub.seq, m.sub.opID, m.sub.inFlight = seq, opID, true
	return *m, cmd
}

// updateNhm handles the answers of nhm's async calls.
func (m Model) updateNhm(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case SubmitMsg:
		if !m.sub.inFlight || msg.Seq != m.sub.seq {
			return m, nil
		}
		m.sub.inFlight = false
		m.sub.answer = &msg
		m.sent = m.sent || m.sub.sentAny()
		if names := unfilled(msg.Err); msg.Outcome() == Refused && len(names) > 0 {
			// Nothing was sent: ask for the values and send again.
			return m, m.enterVarsPhase(names)
		}
		if m.sub.sentAny() && m.selected != nil && m.selected.Source != "" {
			store, k := m.store, m.selected.Key
			return m, func() tea.Msg { return recentSavedMsg{err: store.RecordRecent(k)} }
		}
		return m, nil
	case ReconcileMsg:
		if msg.OperationID != m.sub.opID {
			return m, nil
		}
		m.sub.reconciling = false
		m.sub.reconciled = &msg
		m.sent = m.sent || m.sub.sentAny()
		return m, nil
	case recentSavedMsg:
		if msg.err == nil {
			return m, m.fetchRecents()
		}
		return m, nil
	case MemoryMsg, memoryDueMsg:
		return m.updateMemory(msg)
	}
	return m, nil
}

// unfilledText is how templates.UnresolvedVariablesError lists the names.
const unfilledText = "has unresolved variable(s): "

// varName is a template variable's name.
var varName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// unfilled returns the template variables a refusal names: from the
// core's error, or from its text when the error crossed a process (a
// runner job).
func unfilled(err error) []string {
	if err == nil {
		return nil
	}
	var uv *templates.UnresolvedVariablesError
	if errors.As(err, &uv) {
		return uv.Names
	}
	s := err.Error()
	i := strings.Index(s, unfilledText)
	if i < 0 {
		return nil
	}
	rest := s[i+len(unfilledText):]
	if j := strings.IndexAny(rest, "\n;"); j >= 0 {
		rest = rest[:j]
	}
	var names []string
	for _, n := range strings.Split(rest, ",") {
		if n = strings.TrimSpace(n); varName.MatchString(n) {
			names = append(names, n)
		}
	}
	return names
}

// updateResultPhase handles the result screen's keys.
func (m *Model) updateResultPhase(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, resultKeys.Quit):
		// The core finishes a send in flight; the caller waits for it.
		m.quitting = true
		return *m, tea.Quit
	case key.Matches(msg, resultKeys.Again):
		if m.sub.inFlight {
			return *m, nil
		}
		m.backToCommands()
		return *m, m.fetchPaneCounts()
	case key.Matches(msg, resultKeys.Reconcile):
		if m.sub.inFlight || m.sub.opID == "" || m.sub.reconciling || m.env.submitter == nil {
			return *m, nil
		}
		m.sub.reconciling = true
		return *m, m.env.submitter.Reconcile(m.sub.opID)
	}
	return *m, nil
}

// backToCommands starts over at the command list, with a fresh draft.
func (m *Model) backToCommands() {
	m.phase = PhaseCommand
	m.selected = nil
	m.draft.Reset()
	m.vars = varsState{}
	m.sub = submission{}
	m.targetNotice = ""
	m.filter.Focus()
}

// varsState asks for the template variables the core found unfilled.
type varsState struct {
	names  []string
	inputs []textinput.Model
	focus  int
	values map[string]string
	notice string
}

// enterVarsPhase asks for names' values; it keeps values given before.
func (m *Model) enterVarsPhase(names []string) tea.Cmd {
	t := m.theme
	inputs := make([]textinput.Model, len(names))
	for i, n := range names {
		ti := textinput.New()
		ti.Prompt = n + ": "
		ti.CharLimit = 500
		ti.Width = 40
		ti.PromptStyle = lipgloss.NewStyle().Foreground(t.Mauve)
		ti.TextStyle = lipgloss.NewStyle().Foreground(t.Text)
		ti.Cursor.Style = lipgloss.NewStyle().Foreground(t.Pink)
		ti.SetValue(m.vars.values[n])
		inputs[i] = ti
	}
	values := m.vars.values
	if values == nil {
		values = map[string]string{}
	}
	m.vars = varsState{names: names, inputs: inputs, values: values}
	m.phase = PhaseVars
	return m.vars.inputs[0].Focus()
}

// updateVarsPhase handles the variables screen's keys.
func (m *Model) updateVarsPhase(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := &m.vars
	switch {
	case key.Matches(msg, varsKeys.Quit):
		m.quitting = true
		return *m, tea.Quit
	case key.Matches(msg, varsKeys.Back):
		m.keepVarValues()
		m.phase = PhaseTarget
		return *m, nil
	case key.Matches(msg, varsKeys.Prev):
		if v.focus > 0 {
			v.inputs[v.focus].Blur()
			v.focus--
			return *m, v.inputs[v.focus].Focus()
		}
		return *m, nil
	case key.Matches(msg, varsKeys.Next):
		if strings.TrimSpace(v.inputs[v.focus].Value()) == "" {
			v.notice = "Give " + v.names[v.focus] + " a value."
			return *m, nil
		}
		v.notice = ""
		if v.focus < len(v.inputs)-1 {
			v.inputs[v.focus].Blur()
			v.focus++
			return *m, v.inputs[v.focus].Focus()
		}
		m.keepVarValues()
		// Send again to the targets frozen at the first keypress: the
		// core refused before it claimed anything.
		payload, err := m.draft.Payload(m.selected, v.values)
		if err != nil {
			cmd := m.enterEditPhase()
			m.editNotice = "Message is empty — type something to send, or Esc to cancel."
			return *m, cmd
		}
		return m.submit(payload, m.sub.targets)
	}
	return m.updateVarsInput(msg)
}

// updateVarsInput passes a message to the focused input.
func (m Model) updateVarsInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	if len(m.vars.inputs) == 0 {
		return m, nil
	}
	var cmd tea.Cmd
	m.vars.inputs[m.vars.focus], cmd = m.vars.inputs[m.vars.focus].Update(msg)
	return m, cmd
}

// keepVarValues stores the inputs' values for the next send.
func (m *Model) keepVarValues() {
	for i, n := range m.vars.names {
		if val := strings.TrimSpace(m.vars.inputs[i].Value()); val != "" {
			m.vars.values[n] = val
		}
	}
}

// renderTargetNotes says what the targets leave out, and why the last
// key did nothing.
func (m Model) renderTargetNotes() string {
	t := m.theme
	ic := m.icons
	var b strings.Builder
	dim := lipgloss.NewStyle().Foreground(t.Subtext)
	if m.paneCountsErr != nil {
		b.WriteString("  " + lipgloss.NewStyle().Foreground(t.Warning).Render(ic.Warning+"  The agents could not be read: "+
			clean(m.paneCountsErr.Error())) + "\n\n")
	}
	if m.panesUnbound > 0 {
		b.WriteString("  " + dim.Render(fmt.Sprintf("%d agent(s) here have no binding and get no prompt (nhm adopt binds one).",
			m.panesUnbound)) + "\n\n")
	}
	if m.targetNotice != "" {
		b.WriteString("  " + lipgloss.NewStyle().Foreground(t.Peach).Render(ic.Warning+"  "+m.targetNotice) + "\n\n")
	}
	return b.String()
}

// viewResultPhase shows the submission and its receipts.
func (m Model) viewResultPhase() string {
	t := m.theme
	ic := m.icons
	var b strings.Builder
	boxWidth := m.targetBoxWidth()
	dim := lipgloss.NewStyle().Foreground(t.Subtext)
	bad := lipgloss.NewStyle().Foreground(t.Error)
	warn := lipgloss.NewStyle().Foreground(t.Warning)

	b.WriteString("\n")
	title := ic.Send + "  Send"
	if label := clean(m.sub.label); label != "" {
		title += ": " + label
	}
	b.WriteString("  " + styles.GradientText(title, string(t.Blue), string(t.Mauve)) + "\n")
	b.WriteString("  " + styles.GradientDivider(boxWidth, string(t.Blue), string(t.Mauve)) + "\n\n")
	if m.sub.opID != "" {
		b.WriteString("  " + dim.Render("operation "+m.sub.opID) + "\n\n")
	}

	answer := m.sub.answer
	switch {
	case m.sub.err != nil:
		b.WriteString("  " + bad.Render(ic.Cross+"  Nothing was sent: "+clean(m.sub.err.Error())) + "\n")
	case m.sub.inFlight:
		b.WriteString("  " + dim.Render(fmt.Sprintf("Sending to %d agent(s)…", len(m.sub.targets))) + "\n")
		for _, tg := range m.sub.targets {
			b.WriteString("    " + dim.Render(clean(tg.MailName)+"  "+clean(tg.Kind)) + "\n")
		}
	case answer != nil && answer.Outcome() == Refused:
		b.WriteString("  " + bad.Render(ic.Cross+"  Nothing was sent: "+clean(answer.Err.Error())) + "\n")
	default:
		if answer != nil && answer.Outcome() == Unknown && m.sub.reconciled == nil {
			b.WriteString("  " + warn.Render(ic.Warning+"  The answer was lost: "+clean(answer.Err.Error())) + "\n")
			b.WriteString("  " + dim.Render("The core recorded the send before any prompt. Press r to read what it recorded;") + "\n")
			b.WriteString("  " + dim.Render("the palette never sends it again on its own.") + "\n")
		}
		b.WriteString(m.renderReconcile())
		if res, ok := m.sub.result(); ok {
			b.WriteString(m.renderReceipts(res, boxWidth))
		}
	}

	b.WriteString("\n  " + styles.GradientDivider(boxWidth, string(t.Surface2), string(t.Surface1)) + "\n\n")
	b.WriteString("  " + m.renderHelpEntries(resultHelpEntries()) + "\n")
	return lipgloss.Place(m.width, m.height, lipgloss.Left, lipgloss.Center, b.String())
}

// renderReconcile says how the last read of the record went.
func (m Model) renderReconcile() string {
	t := m.theme
	dim := lipgloss.NewStyle().Foreground(t.Subtext)
	switch {
	case m.sub.reconciling:
		return "  " + dim.Render("Reading the record…") + "\n"
	case m.sub.reconciled == nil:
		return ""
	case errors.Is(m.sub.reconciled.Err, sql.ErrNoRows):
		return "  " + dim.Render("The core has no record of this operation: nothing was sent.") + "\n"
	case m.sub.reconciled.Err != nil:
		return "  " + lipgloss.NewStyle().Foreground(t.Warning).Render("The record could not be read: "+
			clean(m.sub.reconciled.Err.Error())) + "\n"
	case m.sub.reconciled.Result.InProgress:
		return "  " + dim.Render("The core still runs this send; press r again later.") + "\n"
	}
	return "  " + dim.Render("As the core recorded it:") + "\n"
}

// renderReceipts shows one line per target in the core's words, then
// what memory was attached and the warnings.
func (m Model) renderReceipts(res send.Result, width int) string {
	t := m.theme
	ic := m.icons
	var b strings.Builder
	dim := lipgloss.NewStyle().Foreground(t.Subtext)
	ok := lipgloss.NewStyle().Foreground(t.Success).Bold(true)
	bad := lipgloss.NewStyle().Foreground(t.Error).Bold(true)
	unknown := lipgloss.NewStyle().Foreground(t.Warning).Bold(true)
	name := lipgloss.NewStyle().Foreground(t.Text).Bold(true)

	counts := res.Counts()
	b.WriteString("  " + dim.Render(fmt.Sprintf("%d submitted, %d refused or failed, %d unknown",
		counts[send.StatusSubmitted], counts[send.StatusRefused]+counts[send.StatusFailed],
		counts[send.StatusPossiblySubmitted]+counts[send.StatusAttemptStarted]+counts[send.StatusPending])) + "\n\n")
	for _, rc := range res.Targets {
		who := clean(rc.MailName)
		if who == "" {
			who = clean(rc.Key)
		}
		var mark, what string
		switch {
		case rc.Status == send.StatusSubmitted:
			mark, what = ok.Render(ic.Check), "submitted"
		case send.Unknown(rc.Status):
			mark, what = unknown.Render("?"), "unknown, maybe sent ("+rc.Status+")"
		default:
			mark, what = bad.Render(ic.Cross), rc.Status
		}
		if reason := rc.Result.Reason; reason != "" {
			what += ": " + reason
		} else if rc.ErrorCode != "" && rc.Status != send.StatusSubmitted {
			what += ": " + rc.ErrorCode
		}
		line := fmt.Sprintf("%s %s %s", mark, name.Render(who), dim.Render(clean(rc.Kind)))
		b.WriteString("  " + line + "\n")
		b.WriteString("      " + dim.Render(layout.TruncateWidthDefault(clean(what), max(width-8, 20))) + "\n")
	}
	if line := memoryAttached(res); line != "" {
		b.WriteString("\n  " + dim.Render(line) + "\n")
	}
	for _, w := range res.Warnings {
		b.WriteString("  " + lipgloss.NewStyle().Foreground(t.Warning).Render(ic.Warning+"  "+clean(w)) + "\n")
	}
	return b.String()
}

// memoryAttached says what memory the send put before the prompts: the
// distinct CM rules and CASS items included, and how many targets got
// any. "" when both sources were off.
func memoryAttached(res send.Result) string {
	if !res.MemoryInjection.Enabled && !res.CASSInjection.Enabled {
		return ""
	}
	ids := map[string]map[string]bool{enrich.SourceCM: {}, enrich.SourceCASS: {}}
	targets := map[int]bool{}
	for _, ti := range res.Injections {
		for _, it := range ti.Items {
			if it.Status != enrich.Included {
				continue
			}
			if ids[ti.Source] == nil {
				ids[ti.Source] = map[string]bool{}
			}
			ids[ti.Source][it.ID] = true
			targets[ti.TargetIndex] = true
		}
	}
	return fmt.Sprintf("memory: %d CM rule(s), %d CASS item(s) attached, for %d target(s)",
		len(ids[enrich.SourceCM]), len(ids[enrich.SourceCASS]), len(targets))
}

// viewVarsPhase asks for the template's unfilled variables.
func (m Model) viewVarsPhase() string {
	t := m.theme
	ic := m.icons
	var b strings.Builder
	boxWidth := m.targetBoxWidth()
	dim := lipgloss.NewStyle().Foreground(t.Subtext)

	b.WriteString("\n")
	b.WriteString("  " + styles.GradientText(ic.Save+"  Fill the template", string(t.Yellow), string(t.Peach)) + "\n")
	b.WriteString("  " + styles.GradientDivider(boxWidth, string(t.Yellow), string(t.Peach)) + "\n\n")
	b.WriteString("  " + dim.Render(fmt.Sprintf("Nothing was sent. The prompt needs these values for its %d agent(s):",
		len(m.sub.targets))) + "\n\n")
	for _, in := range m.vars.inputs {
		b.WriteString("  " + in.View() + "\n")
	}
	if m.vars.notice != "" {
		b.WriteString("\n  " + lipgloss.NewStyle().Foreground(t.Peach).Render(ic.Warning+"  "+m.vars.notice) + "\n")
	}
	b.WriteString("\n  " + styles.GradientDivider(boxWidth, string(t.Surface2), string(t.Surface1)) + "\n\n")
	b.WriteString("  " + m.renderHelpEntries(varsHelpEntries()) + "\n")
	return lipgloss.Place(m.width, m.height, lipgloss.Left, lipgloss.Center, b.String())
}
