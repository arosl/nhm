// Package dashboard is the board's program (plan-v4 section 7): ntm's
// dashboard presentation on nhm's board model (internal/ui/board). The
// files taken from ntm say so in their headers; this one is nhm's own.
//
// The program never waits in its update loop. The board's feeds read in
// their own goroutines and signal Changed; zoom and the inbox peek run as
// commands that answer with a message carrying the request it answers,
// and a stale answer is dropped.
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/core/zoom"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/store/history"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/actions"
	"github.com/arosl/nhm/internal/ui/board"
	"github.com/arosl/nhm/internal/ui/dashboard/panels"
	"github.com/arosl/nhm/internal/ui/forms"
	"github.com/arosl/nhm/internal/ui/palette"
	"github.com/arosl/nhm/internal/ui/tui/agent"
	"github.com/arosl/nhm/internal/ui/tui/components"
	"github.com/arosl/nhm/internal/ui/tui/theme"
)

// Config is what the board program needs. The CLI builds it: the board
// model on the snapshot service, the captured context and herdr's calls.
type Config struct {
	Board   *board.Board
	Context ui.Context
	// Zoom focuses the workspace and zooms a pane, as nhm zoom does
	// (zoom.Pane); nil leaves enter and z out. A *zoom.Unshown error is
	// a zoom that worked, with the pane's tab behind; a *zoom.Alone, a
	// pane alone in its tab, which is in front.
	Zoom func(ctx context.Context, workspaceID, paneID string) error
	// Inbox peeks at an agent's unread mail; it never marks it read. nil
	// leaves i out.
	Inbox func(ctx context.Context, projectKey, mailName string) ([]agentmail.Message, error)
	// Binary watches the nhm binary: after an upgrade on disk the board
	// takes no new mutations. nil watches nothing.
	Binary *board.Binary
	// Core runs the action menu's actions (space) through the action
	// table; a missing seam makes its actions unavailable, with the reason.
	Core actions.Core
	// NewOperationID makes the submission guard's operation ids
	// (store.NewOperationID); nil leaves space out.
	NewOperationID func() (string, error)
	// OperationEnded reports whether an operation's runner job has ended
	// (runner.Client.Ended): the guard then releases a request it held for
	// it, such as one whose answer was lost. nil holds it for the board's
	// life.
	OperationEnded func(opID string) bool
	// History reads the recent send history (history.ReadRecent); nil
	// leaves the history panel out.
	History func() ([]history.HistoryEntry, error)
	// Read reads a pane's last lines for the read view (space r); nil
	// leaves the read view out.
	Read func(ctx context.Context, paneID string, lines int) (string, error)
	// Memory serves the composer's memory preview (the palette's
	// Submitter); the send itself goes through the action table. nil: no
	// preview.
	Memory *palette.Submitter
	// OpenPalette opens the palette popup on a context that carries the
	// board's frozen selection (space a); nil leaves space a out.
	OpenPalette func(ctx context.Context, c ui.Context) error
	// Kinds are the kinds ctrl+w's spawn wizard offers (kinds.All()); none
	// leaves ctrl+w out.
	Kinds []kinds.Adapter
	// Now is time.Now in production.
	Now func() time.Time
	// CallTimeout bounds each call a key starts (default 5 s).
	CallTimeout time.Duration
}

// view is what the board shows over its table.
type view int

const (
	viewMain view = iota
	viewHelp
	viewInbox
	viewDiagnostics
	viewUsage
	viewActions
	viewForm
	viewRead
	viewSend
	viewWizard
)

// tickEvery is how often the program lets the feeds start the reads that
// are due; the feeds keep their own schedules.
const tickEvery = 250 * time.Millisecond

// binaryEvery is how often the program checks the binary on disk.
const binaryEvery = time.Second

// Model is the board's bubbletea model.
type Model struct {
	cfg   Config
	b     *board.Board
	keys  KeyMap
	help  help.Model
	theme theme.Theme

	width, height int
	view          view

	// filtering: / is open; the typed text filters as it is typed, and
	// esc puts back filterPrev.
	filtering  bool
	filterText string
	filterPrev string

	status    string
	statusErr bool
	// frozen is why the board takes no mutations (an upgrade on disk, a
	// binary it cannot watch); once set, it stays until the board ends.
	frozen     error
	lastBinary time.Time

	inbox inboxState
	seq   uint64

	// The action menu (space): its rows, the submission guard and the
	// form shown. menuTargets are the menu's targets, frozen when space
	// opens it.
	menu        []actions.Action
	menuTargets board.Targets
	guard       *actions.Guard
	form        *forms.AgentForm
	// wizard is ctrl+w's spawn wizard while it is open.
	wizard *forms.SpawnWizard

	// The side panels and the focus between them (focus.go). The board
	// runs no animations: dashboardSprings stays nil.
	focusedPanel     PanelID
	focusRing        FocusRing
	dashboardSprings *components.SpringManager
	attention        *panels.AttentionPanel
	attnItems        []panels.AttentionItem
	attnPanes        map[string]string
	history          *panels.HistoryPanel
	historySeq       uint64
	historyAt        time.Time

	read readState
	send sendState
}

// inboxState is the inbox view's request and its answer.
type inboxState struct {
	seq     uint64
	key     board.Key
	name    string
	loading bool
	msgs    []agentmail.Message
	err     error
}

// Messages.
type (
	tickMsg    time.Time
	changedMsg struct{}
	zoomMsg    struct {
		name string
		err  error
	}
	inboxMsg struct {
		seq  uint64
		msgs []agentmail.Message
		err  error
	}
)

// New makes the board's model.
func New(cfg Config) (*Model, error) {
	if cfg.Board == nil {
		return nil, errors.New("dashboard: no board")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = 5 * time.Second
	}
	// Every spawn the board starts (the wizard's) checks the binary at the
	// call, so a wizard opened before an upgrade spawns nothing after it.
	if cfg.Core.Spawn != nil {
		cfg.Core.Spawn = frozenSpawn(cfg.Binary, cfg.Core.Spawn)
	}
	t := theme.Current()
	m := &Model{cfg: cfg, b: cfg.Board, keys: dashKeys, help: newHelpModel(t), theme: t,
		attention: panels.NewAttentionPanel()}
	if cfg.History != nil {
		m.history = panels.NewHistoryPanel()
	}
	// Keys whose action this board does not have yet are not shown and do
	// nothing (plan-v4 section 2: no dead keys).
	m.keys.Zoom.SetEnabled(cfg.Zoom != nil)
	m.keys.InboxToggle.SetEnabled(cfg.Inbox != nil)
	m.keys.MailRefresh.SetEnabled(cfg.Board.Mail != nil)
	if cfg.NewOperationID != nil {
		m.guard = actions.NewGuard(cfg.NewOperationID).ReleaseEnded(cfg.OperationEnded)
	}
	for _, a := range actions.Space() {
		// The forms and the send need the guard; the read view is the
		// board's own; the palette is a surface the board opens.
		if a.ID == readActionID && cfg.Read != nil || a.ID == paletteActionID && cfg.OpenPalette != nil ||
			m.guard != nil && a.Visible() && (forms.IsAgentAction(a.ID) || a.ID == sendActionID) {
			m.menu = append(m.menu, a)
		}
	}
	m.keys.Actions.SetEnabled(len(m.menu) > 0)
	// ctrl+w: ntm's spawn wizard, for nhm add into this workspace.
	spawnAdd, ok := actions.ByID(spawnActionID)
	m.keys.SpawnWizard.SetEnabled(ok && spawnAdd.Visible() && m.guard != nil && cfg.Core.Spawn != nil && len(cfg.Kinds) > 0)
	for _, k := range []*key.Binding{&m.keys.Num1, &m.keys.Num2, &m.keys.Num3, &m.keys.Num4,
		&m.keys.Num5, &m.keys.Num6, &m.keys.Num7, &m.keys.Num8, &m.keys.Num9} {
		k.SetEnabled(false)
	}
	m.layoutChanged()
	return m, nil
}

// layoutChanged rebuilds the focus ring for the panels the size shows;
// tab moves between panels only when there is more than one.
func (m *Model) layoutChanged() {
	m.syncFocusRing()
	many := len(m.visiblePanels()) > 1
	for _, k := range []*key.Binding{&m.keys.NextPanel, &m.keys.PrevPanel, &m.keys.Tab, &m.keys.ShiftTab} {
		k.SetEnabled(many)
	}
	m.focusPanels()
}

// focusPanels tells each side panel whether it has the focus.
func (m *Model) focusPanels() {
	if m.focusedPanel == PanelAttention {
		m.attention.Focus()
	} else {
		m.attention.Blur()
	}
	if m.history != nil {
		if m.focusedPanel == PanelHistory {
			m.history.Focus()
		} else {
			m.history.Blur()
		}
	}
}

// historyEvery is how often the board reads the send history again.
const historyEvery = 30 * time.Second

// historyMsg is a read of the send history; seq drops an older read.
type historyMsg struct {
	seq     uint64
	entries []history.HistoryEntry
	err     error
}

// readHistory reads the send history in a command.
func (m *Model) readHistory() tea.Cmd {
	if m.history == nil {
		return nil
	}
	m.historySeq++
	m.historyAt = m.cfg.Now()
	read, seq := m.cfg.History, m.historySeq
	return func() tea.Msg {
		entries, err := read()
		return historyMsg{seq: seq, entries: entries, err: err}
	}
}

// workspaceHistory keeps the entries of the board's workspace: a send
// records its session as the caller named it, a label or an id.
func (m *Model) workspaceHistory(entries []history.HistoryEntry) []history.HistoryEntry {
	c := m.cfg.Context
	var out []history.HistoryEntry
	for _, e := range entries {
		if e.Session != "" && (e.Session == c.WorkspaceID || e.Session == c.WorkspaceLabel) {
			out = append(out, e)
		}
	}
	// ntm's panel lists the newest first.
	slices.Reverse(out)
	return out
}

// historyPanes names the board's rows for the history panel: the target
// keys nhm's send records (the slot, else the pane id). Every row, not
// only those the filter shows.
func (m *Model) historyPanes() []panels.HistoryPane {
	var out []panels.HistoryPane
	for _, r := range m.b.Rows() {
		k := r.Slot()
		if k == "" {
			k = r.PaneID()
		}
		if k == "" {
			continue
		}
		out = append(out, panels.HistoryPane{Key: k, Label: board.Clean(rowName(r)), Type: agent.AgentType(r.Kind()).Canonical()})
	}
	return out
}

// moveFocus moves the focus to the next (+1) or the previous panel.
func (m *Model) moveFocus(dir int) {
	if dir > 0 {
		m.focusRing.Next()
	} else {
		m.focusRing.Prev()
	}
	m.setFocusedPanel(m.focusRing.Current().Panel)
	m.focusPanels()
}

// syncAttention gives the attention panel the merged state's items.
func (m *Model) syncAttention() {
	merged := m.b.Merged()
	m.attnItems, m.attnPanes = attentionItems(merged)
	m.attention.SetData(m.attnItems, merged != nil)
}

// Init starts the first reads and the loops.
func (m *Model) Init() tea.Cmd {
	m.b.Refresh()
	return tea.Batch(m.tick(), m.waitChanged(), m.readHistory())
}

func (m *Model) tick() tea.Cmd {
	return tea.Tick(tickEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// waitChanged waits, in a command's goroutine, for the board to change.
func (m *Model) waitChanged() tea.Cmd {
	ch := m.b.Changed()
	return func() tea.Msg {
		<-ch
		return changedMsg{}
	}
}

// Update handles one message; it never blocks.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, isKey := msg.(tea.KeyMsg); !isKey {
		// The open form's own answers (its review, its result) go to it.
		m.toForms(msg)
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layoutChanged()
		switch m.view {
		case viewSend:
			return m, m.sendUpdate(msg)
		case viewWizard:
			return m, m.wizardUpdate(msg)
		}
		return m, nil
	case sentMsg, palette.ComposedMsg, palette.ComposeCanceledMsg:
		return m, m.sendUpdate(msg)
	case forms.SpawnWizardDoneMsg:
		return m, m.wizardUpdate(msg)
	case tickMsg:
		now := time.Time(msg)
		m.b.Tick(now)
		if now.Sub(m.lastBinary) >= binaryEvery {
			// The banner's check; every mutation checks again itself.
			m.lastBinary = now
			_ = m.Frozen()
		}
		cmds := []tea.Cmd{m.tick(), m.readTick(now)}
		if m.history != nil && !m.b.Paused() && now.Sub(m.historyAt) >= historyEvery {
			cmds = append(cmds, m.readHistory())
		}
		return m, tea.Batch(cmds...)
	case readMsg:
		m.readUpdate(msg)
		return m, nil
	case historyMsg:
		if msg.seq == m.historySeq && m.history != nil {
			m.history.SetEntries(m.workspaceHistory(msg.entries), msg.err)
		}
		return m, nil
	case changedMsg:
		m.b.Sync()
		m.syncAttention()
		if m.history != nil {
			m.history.SetPanes(m.historyPanes())
		}
		return m, m.waitChanged()
	case zoomMsg:
		var unshown *zoom.Unshown
		var alone *zoom.Alone
		switch {
		case errors.As(msg.err, &unshown):
			// The pane is zoomed; only its tab did not come in front.
			m.say(true, "zoomed "+msg.name+", but herdr did not bring its tab in front: "+unshown.Reason)
		case errors.As(msg.err, &alone):
			// Nothing to zoom; the pane's tab is in front.
			m.say(false, msg.name+" is alone in its tab: nothing to zoom")
		case msg.err != nil:
			m.say(true, "zoom "+msg.name+": "+msg.err.Error())
		default:
			m.say(false, "zoomed "+msg.name)
		}
		return m, nil
	case paletteMsg:
		if msg.err != nil {
			m.say(true, "palette: "+msg.err.Error())
		} else {
			m.say(false, fmt.Sprintf("the palette opened for %d agents", msg.n))
		}
		return m, nil
	case inboxMsg:
		if msg.seq != m.inbox.seq {
			return m, nil // an answer to an older request
		}
		m.inbox.loading, m.inbox.msgs, m.inbox.err = false, msg.msgs, msg.err
		return m, nil
	case tea.KeyMsg:
		return m.key(msg)
	}
	switch m.view {
	case viewSend:
		// The composer's own messages (its cursor, its memory preview).
		return m, m.sendUpdate(msg)
	case viewWizard:
		// The wizard's own messages (its fields, its review and spawn).
		return m, m.wizardUpdate(msg)
	}
	return m, nil
}

func (m *Model) say(isErr bool, s string) { m.status, m.statusErr = board.Clean(s), isErr }

// key handles a key. Text for the filter comes before every shortcut.
func (m *Model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	if m.filtering {
		m.filterKey(msg)
		return m, nil
	}
	switch m.view {
	case viewRead:
		return m, m.readKey(msg)
	case viewSend:
		return m, m.sendUpdate(msg)
	case viewWizard:
		return m, m.wizardUpdate(msg)
	case viewForm:
		cmd := m.form.Update(msg)
		m.track(m.form)
		if m.form.Done() {
			if m.form.Running() {
				m.say(false, m.form.Action().Label+" "+m.form.OperationID()+" runs on; recent operations shows it")
			}
			m.form, m.view = nil, viewMain
		}
		return m, cmd
	case viewActions:
		if key.Matches(msg, m.keys.Back) || key.Matches(msg, m.keys.Quit) || key.Matches(msg, m.keys.Actions) {
			m.view = viewMain
			return m, nil
		}
		for _, a := range m.menu {
			if msg.String() != a.Board.Space {
				continue
			}
			tg, ok := m.menuSelection()
			if !ok {
				return m, nil
			}
			switch a.ID {
			case readActionID:
				return m, m.openRead(tg)
			case sendActionID:
				return m, m.openSend(a, tg)
			case paletteActionID:
				return m, m.openPalette(tg)
			}
			return m, m.openForm(a, tg)
		}
		return m, nil
	}
	if m.view != viewMain {
		switch {
		case key.Matches(msg, m.keys.Back), key.Matches(msg, m.keys.Quit),
			m.view == viewHelp && key.Matches(msg, m.keys.Help),
			m.view == viewInbox && key.Matches(msg, m.keys.InboxToggle),
			m.view == viewDiagnostics && key.Matches(msg, m.keys.Diagnostics),
			m.view == viewUsage && key.Matches(msg, m.keys.QuotaToggle):
			m.view = viewMain
		}
		return m, nil
	}
	if m.focusedPanel == PanelHistory && m.history != nil && historyKey(msg, m.history.PreviewOpen()) {
		// The history panel's own keys come first while it has the focus:
		// moves, its preview (v; esc and q close it) and its filters (f,
		// a, t). Its replay and copy are not the board's yet.
		m.history.Update(msg)
		return m, nil
	}
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.NextPanel):
		m.moveFocus(1)
	case key.Matches(msg, m.keys.PrevPanel):
		m.moveFocus(-1)
	case m.focusedPanel == PanelAttention && (key.Matches(msg, m.keys.Up) || key.Matches(msg, m.keys.Down) ||
		msg.Type == tea.KeyHome || msg.Type == tea.KeyEnd):
		// The focused side panel moves its own selection.
		m.attention.Update(msg)
	case key.Matches(msg, m.keys.Up):
		m.b.Sel.Move(-1)
		m.b.FollowCursor()
	case key.Matches(msg, m.keys.Down):
		m.b.Sel.Move(1)
		m.b.FollowCursor()
	case msg.Type == tea.KeyHome:
		m.b.Sel.Top()
		m.b.FollowCursor()
	case msg.Type == tea.KeyEnd:
		m.b.Sel.Bottom()
		m.b.FollowCursor()
	case msg.Type == tea.KeyPgUp:
		m.b.Sel.Move(-m.tableHeight())
		m.b.FollowCursor()
	case msg.Type == tea.KeyPgDown:
		m.b.Sel.Move(m.tableHeight())
		m.b.FollowCursor()
	case key.Matches(msg, m.keys.Zoom):
		return m, m.zoom()
	case key.Matches(msg, m.keys.Refresh):
		m.b.Refresh()
		m.say(false, "refreshing")
		return m, m.readHistory()
	case key.Matches(msg, m.keys.Pause):
		m.b.SetPaused(!m.b.Paused())
		if m.b.Paused() {
			m.say(false, "auto-refresh paused (r still refreshes)")
		} else {
			m.say(false, "auto-refresh resumed")
		}
	case key.Matches(msg, m.keys.MailRefresh):
		m.b.RefreshMail()
		m.say(false, "refreshing mail")
	case key.Matches(msg, m.keys.InboxToggle):
		return m, m.openInbox()
	case key.Matches(msg, m.keys.Diagnostics):
		m.view = viewDiagnostics
	case key.Matches(msg, m.keys.QuotaToggle):
		m.view = viewUsage
	case key.Matches(msg, m.keys.Help):
		m.view = viewHelp
	case key.Matches(msg, m.keys.Actions):
		// The menu's targets freeze now: a refresh while it is open never
		// swaps the cursor's agent for the row now in its place.
		m.menuTargets, m.view = m.b.Sel.Targets(), viewActions
	case key.Matches(msg, m.keys.SpawnWizard):
		return m, m.openWizard()
	case key.Matches(msg, m.keys.Mark):
		m.b.Sel.Toggle()
	case key.Matches(msg, m.keys.Filter):
		m.filtering, m.filterPrev, m.filterText = true, m.b.Sel.Filter(), m.b.Sel.Filter()
	case key.Matches(msg, m.keys.Back):
		switch {
		case m.b.Sel.Filter() != "":
			m.b.Sel.SetFilter("")
			m.b.FollowCursor()
		case len(m.b.Sel.Marks()) > 0:
			m.b.Sel.ClearMarks()
			m.say(false, "marks cleared")
		}
	}
	return m, nil
}

// filterKey edits the filter: runes and paste add text, backspace takes
// a rune away, enter keeps it, esc puts the old one back.
func (m *Model) filterKey(msg tea.KeyMsg) {
	switch msg.Type {
	case tea.KeyEnter:
		m.filtering = false
		return
	case tea.KeyEsc:
		m.filtering = false
		m.filterText = m.filterPrev
	case tea.KeyBackspace:
		if _, size := utf8.DecodeLastRuneInString(m.filterText); size > 0 {
			m.filterText = m.filterText[:len(m.filterText)-size]
		}
	case tea.KeyRunes, tea.KeySpace:
		// A paste is one line of filter text.
		m.filterText += strings.Map(func(r rune) rune {
			if r == '\n' || r == '\t' {
				return ' '
			}
			return r
		}, board.Clean(string(msg.Runes)))
		if msg.Type == tea.KeySpace && len(msg.Runes) == 0 {
			m.filterText += " "
		}
	default:
		return
	}
	m.b.Sel.SetFilter(m.filterText)
	m.b.FollowCursor()
}

// zoom zooms the cursor's agent, or the pane of the attention item the
// attention panel selects, in a command.
func (m *Model) zoom() tea.Cmd {
	var pane, name string
	if m.focusedPanel == PanelAttention {
		it := m.attention.SelectedItem()
		if it == nil || m.attnPanes[it.Cursor] == "" {
			m.say(true, "this attention item names no pane")
			return nil
		}
		pane, name = m.attnPanes[it.Cursor], it.SourcePane
	} else {
		r, ok := m.b.Sel.Cursor()
		if !ok || r.Agent == nil {
			m.say(true, "no agent pane under the cursor")
			return nil
		}
		pane, name = r.Agent.PaneID, rowName(r)
	}
	zoom, ws, d := m.cfg.Zoom, m.b.WorkspaceID(), m.cfg.CallTimeout
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		return zoomMsg{name: name, err: zoom(ctx, ws, pane)}
	}
}

// openInbox opens the inbox view for the cursor's bound agent and peeks
// at its mail, in a command.
func (m *Model) openInbox() tea.Cmd {
	r, ok := m.b.Sel.Cursor()
	if !ok || r.Agent == nil || r.Agent.Binding == nil {
		m.say(true, "the inbox needs a bound agent under the cursor")
		return nil
	}
	m.seq++
	m.inbox = inboxState{seq: m.seq, key: r.Key, name: r.Name, loading: true}
	m.view = viewInbox
	inbox, seq, project, name, d := m.cfg.Inbox, m.seq, r.Agent.Binding.ProjectKey, r.Agent.Binding.MailName, m.cfg.CallTimeout
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		msgs, err := inbox(ctx, project, name)
		return inboxMsg{seq: seq, msgs: msgs, err: err}
	}
}

// historyKey reports whether a key is the history panel's while it has
// the focus.
func historyKey(msg tea.KeyMsg, preview bool) bool {
	switch msg.String() {
	case "up", "k", "down", "j", "pgup", "pgdown", "ctrl+u", "home", "g", "end", "G", "v", "f", "a", "t":
		return true
	case "esc", "q":
		return preview
	}
	return false
}

// toForms hands a message to the open form; a form ignores another
// form's messages.
func (m *Model) toForms(msg tea.Msg) {
	if m.form != nil {
		m.form.Update(msg)
		m.track(m.form)
	}
}

// track records a form's submission in the board's results, and its
// answer when it comes. A form closed while its job runs takes no answer:
// the job runs on, and recent operations has its outcome. The review
// gives the form its id, but the result begins at the submission, so a
// form reviewed and then closed leaves no result pending.
func (m *Model) track(f *forms.AgentForm) {
	id := f.OperationID()
	if id == "" {
		return
	}
	if _, ok := m.b.Results.Get(id); !ok {
		if !f.Running() {
			return
		}
		names := map[string]string{}
		for _, r := range m.b.Rows() {
			names[r.Key.Key()] = rowName(r)
		}
		_ = m.b.Results.Begin(id, f.Action().ID, f.Targets(), names, m.cfg.Now())
	}
	if p := f.Phase(); p != forms.Done && p != forms.Failed {
		return
	}
	res, err := f.Result(), f.Err()
	outcome, detail := res.Status, strings.Join(res.Lines, "; ")
	if outcome == "" {
		outcome = board.OutcomeUnknown
	}
	if err != nil {
		detail = strings.TrimPrefix(detail+"; "+err.Error(), "; ")
	}
	var per []board.TargetResult
	for _, t := range res.Targets {
		per = append(per, board.TargetResult{Key: t.Target, Name: t.Name, Status: t.Status, Detail: t.Reason})
	}
	if m.b.Results.End(id, outcome, detail, per, m.cfg.Now()) {
		m.say(outcome == board.OutcomeUnknown || err != nil, fmt.Sprintf("%s %s: %s", f.Action().Label, id, outcome))
	}
}

// menuSelection is the menu's frozen targets as the board has them now:
// the same keys (binding and generation, or pane and session), with
// today's rows. A target whose key is no longer on the board (retired,
// recycled, gone) refuses the action and closes the menu; the action
// never falls back to the row in its place or to the cursor.
func (m *Model) menuSelection() (board.Targets, bool) {
	tg := m.menuTargets
	tg.Rows = slices.Clone(tg.Rows)
	now := m.b.Rows()
	for i, r := range tg.Rows {
		j := slices.IndexFunc(now, func(x board.Row) bool { return x.Key.Key() == r.Key.Key() })
		if j < 0 {
			m.view = viewMain
			m.say(true, board.Clean(rowName(r))+" is no longer on the board as it was (retired, recycled or gone): "+
				"nothing was done; space shows the board as it is now")
			return tg, false
		}
		tg.Rows[i] = now[j]
	}
	return tg, true
}

// targetsWhy is why the menu's frozen targets do not fit action a, or "":
// adopt on a bound agent, or on one herdr reports no agent session for;
// a bound-only action on an unbound agent. The menu says it before the
// pick; the form would refuse after it. Invalid marks have their own
// lines.
func (m *Model) targetsWhy(a actions.Action) string {
	tg := m.menuTargets
	if tg.From == board.FromNone || len(tg.Invalid) > 0 {
		return ""
	}
	ts := make([]ui.Target, len(tg.Rows))
	for i, r := range tg.Rows {
		ts[i] = r.Key
	}
	if _, _, err := a.Targets(m.cfg.Context.WithTargets("board", ts)); err != nil {
		return strings.TrimPrefix(err.Error(), actions.ErrTargets.Error()+": ")
	}
	return ""
}

// openForm opens the action's form on the targets the action menu froze:
// the marks, else the cursor's row. An invalid mark refuses it: it is
// never dropped or swapped for the row in its place.
func (m *Model) openForm(a actions.Action, tg board.Targets) tea.Cmd {
	switch err := m.Frozen(); {
	case err != nil:
		// The form refuses at its confirm too (WithFrozen).
		m.view = viewMain
		m.say(true, err.Error())
		return nil
	case tg.From == board.FromNone:
		m.view = viewMain
		m.say(true, "no agent: move the cursor to one, or mark some with x")
		return nil
	case len(tg.Invalid) > 0:
		m.view = viewMain
		names := make([]string, len(tg.Invalid))
		for i, mk := range tg.Invalid {
			names[i] = mk.Name + " (" + mk.Reason + ")"
		}
		m.say(true, fmt.Sprintf("%d marks no longer name their agent: %s; esc clears the marks",
			len(tg.Invalid), strings.Join(names, ", ")))
		return nil
	}
	ts := make([]ui.Target, len(tg.Rows))
	names := map[string]string{}
	for i, r := range tg.Rows {
		ts[i] = r.Key
		names[r.Key.Key()] = rowName(r)
	}
	f, err := forms.NewAgentForm(a, m.cfg.Core, m.guard, m.cfg.Context, ts, names,
		forms.WithFrozen(m.Frozen), forms.WithOrigin("board"))
	if err != nil {
		m.view = viewMain
		m.say(true, a.Label+": "+err.Error())
		return nil
	}
	m.form, m.view = f, viewForm
	if tg.Hidden > 0 {
		m.say(false, fmt.Sprintf("%d of the marked agents are hidden by the filter", tg.Hidden))
	}
	return f.Init()
}

// Frozen returns why the board takes no new mutations, else nil:
// board.ErrUpgraded after an upgrade on disk, board.ErrUnwatched when it
// cannot watch its binary, board.ErrUnchecked when a check failed for
// another reason. It checks the binary each time (a stat), so a confirm
// right after an upgrade is refused too. Every mutation the board
// starts, and every surface it hands a selection to, checks it first.
func (m *Model) Frozen() error {
	if m.frozen == nil && m.cfg.Binary != nil {
		m.frozen = m.cfg.Binary.Check()
	}
	return m.frozen
}

// rowName is a row's name: the Mail name, else herdr's name, else the
// kind and the pane, as attention names an unbound agent ("claude in
// w1:p3").
func rowName(r board.Row) string {
	switch {
	case r.Name != "":
		return r.Name
	case r.Kind() != "" && r.PaneID() != "":
		return r.Kind() + " in " + r.PaneID()
	}
	return r.PaneID()
}
