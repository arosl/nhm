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
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/board"
	"github.com/arosl/nhm/internal/ui/tui/theme"
)

// Config is what the board program needs. The CLI builds it: the board
// model on the snapshot service, the captured context and herdr's calls.
type Config struct {
	Board   *board.Board
	Context ui.Context
	// Zoom focuses the workspace and zooms a pane, as nhm zoom does; nil
	// leaves enter and z out.
	Zoom func(ctx context.Context, workspaceID, paneID string) error
	// Inbox peeks at an agent's unread mail; it never marks it read. nil
	// leaves i out.
	Inbox func(ctx context.Context, projectKey, mailName string) ([]agentmail.Message, error)
	// Binary watches the nhm binary: after an upgrade on disk the board
	// takes no new mutations. nil watches nothing.
	Binary *board.Binary
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

	status     string
	statusErr  bool
	upgraded   bool
	lastBinary time.Time

	inbox inboxState
	seq   uint64
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
	t := theme.Current()
	m := &Model{cfg: cfg, b: cfg.Board, keys: dashKeys, help: newHelpModel(t), theme: t}
	// Keys whose action this board does not have yet are not shown and do
	// nothing (plan-v4 section 2: no dead keys).
	m.keys.Zoom.SetEnabled(cfg.Zoom != nil)
	m.keys.InboxToggle.SetEnabled(cfg.Inbox != nil)
	m.keys.MailRefresh.SetEnabled(cfg.Board.Mail != nil)
	for _, k := range []*key.Binding{&m.keys.Actions, &m.keys.SpawnWizard, &m.keys.NextPanel, &m.keys.PrevPanel,
		&m.keys.Tab, &m.keys.ShiftTab, &m.keys.Num1, &m.keys.Num2, &m.keys.Num3, &m.keys.Num4, &m.keys.Num5,
		&m.keys.Num6, &m.keys.Num7, &m.keys.Num8, &m.keys.Num9} {
		k.SetEnabled(false)
	}
	return m, nil
}

// Init starts the first reads and the loops.
func (m *Model) Init() tea.Cmd {
	m.b.Refresh()
	return tea.Batch(m.tick(), m.waitChanged())
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
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		now := time.Time(msg)
		m.b.Tick(now)
		if m.cfg.Binary != nil && !m.upgraded && now.Sub(m.lastBinary) >= binaryEvery {
			m.lastBinary = now
			m.upgraded = m.cfg.Binary.Check() != nil
		}
		return m, m.tick()
	case changedMsg:
		m.b.Sync()
		return m, m.waitChanged()
	case zoomMsg:
		if msg.err != nil {
			m.say(true, "zoom "+msg.name+": "+msg.err.Error())
		} else {
			m.say(false, "zoomed "+msg.name)
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
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
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

// zoom zooms the cursor's agent, in a command.
func (m *Model) zoom() tea.Cmd {
	r, ok := m.b.Sel.Cursor()
	if !ok || r.Agent == nil {
		m.say(true, "no agent pane under the cursor")
		return nil
	}
	zoom, ws, pane, name, d := m.cfg.Zoom, m.b.WorkspaceID(), r.Agent.PaneID, rowName(r), m.cfg.CallTimeout
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

// Upgraded reports whether the binary changed on disk: the board then
// refuses new mutations until it is reopened.
func (m *Model) Upgraded() bool { return m.upgraded }

// Frozen returns board.ErrUpgraded after an upgrade on disk, else nil.
// Every mutation the board starts checks it first.
func (m *Model) Frozen() error {
	if m.upgraded {
		return board.ErrUpgraded
	}
	return nil
}

func rowName(r board.Row) string {
	if r.Name != "" {
		return r.Name
	}
	return r.PaneID()
}
