package dashboard

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/actions"
	"github.com/arosl/nhm/internal/ui/board"
	"github.com/arosl/nhm/internal/ui/forms"
	"github.com/arosl/nhm/internal/ui/palette"
)

// sendActionID is the table's free-text send (space s).
const sendActionID = "send"

// sendState is the board's send: the frozen targets, the palette's
// composer (the one editor, plan-v4 section 5), and the submission.
type sendState struct {
	targets  []ui.Target
	names    map[string]string
	label    string
	composer palette.Composer
	opID     string
	running  bool
	done     bool
	result   actions.Result
	err      error
}

// sentMsg is the core's answer to the board's send.
type sentMsg struct {
	opID string
	res  actions.Result
	err  error
}

// openSend freezes the targets of the action menu and opens the
// composer. An invalid mark, an unbound agent or no agent refuses it.
func (m *Model) openSend(a actions.Action) tea.Cmd {
	tg := m.b.Sel.Targets()
	switch {
	case tg.From == board.FromNone:
		m.view = viewMain
		m.say(true, "no agent: move the cursor to one, or mark some with x")
		return nil
	case len(tg.Invalid) > 0:
		m.view = viewMain
		m.say(true, fmt.Sprintf("%d marks no longer name their agent; esc clears the marks", len(tg.Invalid)))
		return nil
	}
	if why := a.Unavailable(m.cfg.Context, m.cfg.Core); why != "" {
		m.view = viewMain
		m.say(true, a.Label+": "+why)
		return nil
	}
	ts := make([]ui.Target, len(tg.Rows))
	names := map[string]string{}
	var who []string
	for i, r := range tg.Rows {
		ts[i] = r.Key
		names[r.Key.Key()] = rowName(r)
		who = append(who, board.Clean(rowName(r)))
	}
	frozen, _, err := a.Targets(m.cfg.Context.WithTargets("board", ts))
	if err != nil {
		m.view = viewMain
		m.say(true, a.Label+": "+err.Error())
		return nil
	}
	label := "to " + strings.Join(who, ", ")
	m.send = sendState{targets: frozen, names: names, label: label,
		composer: palette.NewComposer(label, "", max(m.width-4, 40), max(m.height-12, 5), m.cfg.Memory)}
	m.view = viewSend
	return m.send.composer.Init()
}

// sendUpdate hands a message to the composer, and submits what it
// composed: once, through the board's guard, as the table's send.
func (m *Model) sendUpdate(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case palette.ComposeCanceledMsg:
		m.view = viewMain
		return nil
	case palette.ComposedMsg:
		return m.submitSend(msg.Text)
	case sentMsg:
		if msg.opID == m.send.opID {
			m.send.running, m.send.done, m.send.result, m.send.err = false, true, msg.res, msg.err
			m.trackSend()
		}
		return nil
	case tea.KeyMsg:
		if m.send.done || m.send.running {
			// The result is read: any key closes it. While the job runs,
			// esc leaves it running.
			if m.send.done || msg.Type == tea.KeyEsc {
				if m.send.running {
					m.say(false, "send "+m.send.opID+" runs on; recent operations shows it")
				}
				m.view = viewMain
			}
			return nil
		}
	}
	if m.send.running || m.send.done {
		return nil
	}
	var cmd tea.Cmd
	m.send.composer, cmd = m.send.composer.Update(msg)
	return cmd
}

// submitSend submits the composed text to the frozen targets.
func (m *Model) submitSend(text string) tea.Cmd {
	if err := m.Frozen(); err != nil {
		m.say(true, err.Error())
		return nil
	}
	a, ok := actions.ByID(sendActionID)
	if !ok || m.guard == nil {
		m.say(true, "send is not available here")
		return nil
	}
	req := actions.Request{Context: m.cfg.Context.WithTargets("board", m.send.targets), Targets: m.send.targets,
		Params: map[string]string{actions.ParamText: text}}
	key := actions.ReviewKey(a.ID, req)
	if _, err := m.guard.Review(key); err != nil {
		m.say(true, err.Error())
		return nil
	}
	id, err := m.guard.Submit(key)
	if err != nil {
		m.say(true, err.Error())
		return nil
	}
	req.OperationID = id
	m.send.opID, m.send.running = id, true
	_ = m.b.Results.Begin(id, a.ID, m.send.targets, m.send.names, m.cfg.Now())
	core := m.cfg.Core
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), forms.RunTimeout)
		defer cancel()
		res, err := a.Do(ctx, core, req)
		return sentMsg{opID: id, res: res, err: err}
	}
}

// trackSend records the send's answer in the board's results.
func (m *Model) trackSend() {
	res, err := m.send.result, m.send.err
	outcome := res.Status
	if outcome == "" {
		outcome = board.OutcomeUnknown
	}
	detail := strings.Join(res.Lines, "; ")
	if err != nil {
		detail = strings.TrimPrefix(detail+"; "+err.Error(), "; ")
	}
	var per []board.TargetResult
	for _, t := range res.Targets {
		per = append(per, board.TargetResult{Key: t.Target, Name: t.Name, Status: t.Status, Detail: t.Reason})
	}
	if m.b.Results.End(m.send.opID, outcome, detail, per, m.cfg.Now()) {
		m.say(outcome == board.OutcomeUnknown || err != nil, fmt.Sprintf("send %s: %s", m.send.opID, outcome))
	}
}

// sendView is the composer, or the submission's state and result.
func (m *Model) sendView(w int) string {
	if !m.send.running && !m.send.done {
		return m.send.composer.View()
	}
	var b strings.Builder
	b.WriteString("send " + board.Clean(m.send.label) + "\n\n")
	if m.send.running {
		fmt.Fprintf(&b, "submitted as %s; waiting for the core…\nesc leaves it running; recent operations shows it.\n", m.send.opID)
		return b.String()
	}
	res := m.send.result
	if m.send.err != nil && len(res.Targets) == 0 {
		fmt.Fprintf(&b, "unknown: %s\nThe core's record says what happened: recent operations, %s.\nThe board does not send it again.\n",
			board.Clean(m.send.err.Error()), m.send.opID)
	} else {
		fmt.Fprintf(&b, "%s (%s)\n", board.Clean(res.Status), m.send.opID)
		for _, t := range res.Targets {
			name := t.Name
			if name == "" {
				name = m.send.names[t.Target.Key()]
			}
			line := "  " + board.Clean(name) + ": " + board.Clean(t.Status)
			if t.Reason != "" {
				line += " — " + board.Clean(t.Reason)
			}
			b.WriteString(line + "\n")
		}
		for _, l := range res.Lines {
			b.WriteString("  " + board.Clean(l) + "\n")
		}
		for _, wn := range res.Warnings {
			b.WriteString("  warning: " + board.Clean(wn) + "\n")
		}
	}
	b.WriteString("\nany key closes\n")
	return b.String()
}
