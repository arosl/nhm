// Taken from ntm (github.com/Dicklesworthstone/ntm) internal/palette/model.go
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
//
// Local changes, mechanical: this header; import paths; config.PaletteCmd
// and config.PaletteState are this package's; tmux.Pane is Pane; the title
// names nhm.
//
// Local changes, behaviour (plan-v4 section 5):
//   - Panes come from Options.Panes (the captured workspace's bound agents,
//     read from nhm's snapshot) off the update loop, and the model keeps
//     them: the per-agent picker no longer reads tmux synchronously. A stale
//     read is dropped.
//   - Targets: TargetKind with a kind replaces ntm's fixed per-kind targets;
//     the digits come from nhm's kinds (KindKeys); counts and samples are
//     per kind.
//   - The draft is a Draft, "present" kept apart from its text: an emptied
//     draft is refused and never falls back to the template (ntm read
//     editDraft == "" as "no edit").
//   - sendWith (tmux send-keys, then quit) is replaced by send: the targets
//     are frozen at the keypress and go to nhm's send operation as one
//     asynchronous submission; the receipts show on a result screen
//     (result.go). ntm's history record and delivery preflight are the
//     core's now.
//   - Pins and favorites are saved through Store; recents come from the
//     Store, not from ntm's history.
//   - Update runs ntm's handler, then starts a memory preview when it is on
//     and the text in focus changed (memory.go).
//   - The editor's construction is newEditor, which the Composer (the
//     menu's and the board's editor) shares.
//   - A selection another surface handed on (Options.Handed, the board's
//     marks) is the only target: 1 sends to exactly it; the kind digits
//     and the picker do not widen it (handed.go).
//   - c and s in the target phase switch CM's rules and CASS's hits for
//     the sends, shown on the target screen (memory.go, nhm-t63).
//   - Catalog text is shown without terminal controls (clean).

package palette

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wordwrap"

	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/integrations/xf"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/tui/agent"
	"github.com/arosl/nhm/internal/ui/tui/components"
	"github.com/arosl/nhm/internal/ui/tui/icons"
	"github.com/arosl/nhm/internal/ui/tui/layout"
	"github.com/arosl/nhm/internal/ui/tui/styles"
	"github.com/arosl/nhm/internal/ui/tui/theme"
)

// AnimationTickMsg is used to trigger animation updates
type AnimationTickMsg time.Time

// Phase represents the current UI phase
type Phase int

const (
	PhaseCommand Phase = iota
	PhaseTarget
	PhaseEdit // edit prompt text before sending
	PhaseXFSearch
	PhaseXFResults
	PhaseSelectAgents // granular per-agent multi-select (#205)
	// PhaseResult shows a submission's receipts (nhm).
	PhaseResult
	// PhaseVars asks for the template variables the core found unfilled (nhm).
	PhaseVars
)

// Target represents the send target
type Target int

const (
	TargetAll Target = iota
	// TargetKind is every agent of one kind (Model.targetKind).
	TargetKind
	TargetSelected // explicit per-pane selection (#205)
)

// ReloadMsg is emitted when palette commands are reloaded from config changes.
type ReloadMsg struct {
	Commands []PaletteCmd
}

type paneCounts struct {
	totalAgents int
	byKind      map[string]int

	// Representative pane titles per target (best-effort, used for UI clarity).
	allSamples  []string
	kindSamples map[string][]string
}

type paneCountsMsg struct {
	seq     uint64
	counts  paneCounts
	panes   []Pane
	unbound int
	err     error
}

// paneReads numbers the pane reads, so an older read never replaces a
// newer one.
var paneReads atomic.Uint64

type recentsMsg struct {
	keys []string
	err  error
}

type paletteStateSavedMsg struct {
	err error
}

// Model is the Bubble Tea model for the palette
type Model struct {
	session  string
	commands []PaletteCmd
	filtered []PaletteCmd
	cursor   int
	selected *PaletteCmd
	phase    Phase
	target   Target
	// targetKind is the kind of TargetKind.
	targetKind string
	filter     textinput.Model
	width      int
	height     int
	// sent: a submission of this session reached at least one agent.
	sent     bool
	quitting bool
	err      error

	// Animation state
	animTick    int
	animate     bool
	showPreview bool
	showHelp    bool

	// Recents/favorites/pins
	recents         []string
	paletteState    PaletteState
	store           Store
	paletteStateErr error

	// Cached target counts for target summary preview (best-effort).
	paneCounts      paneCounts
	paneCountsKnown bool
	paneCountsErr   error
	paneSeen        uint64

	// The captured workspace's bound agents, from the last read, and how
	// many of its agents have no binding.
	panes        []Pane
	panesUnbound int
	loadPanes    func(ctx context.Context) ([]Pane, int, error)
	kindKeys     []KindKey
	// handed is a selection another surface handed on (handed.go).
	handed handed
	// targetNotice, when set, is shown in the target phase (a digit with no
	// agent behind it).
	targetNotice string

	// visualOrder maps visual display position (0-indexed) to index in filtered slice.
	// This is needed because items are grouped by category, so visual order differs from slice order.
	visualOrder []int

	// Edit phase state
	editInput textarea.Model
	// draft is the text being sent when the user has one of their own; it
	// keeps "present" apart from the text.
	draft Draft
	// editNotice, when set, is shown in the edit phase (e.g. after refusing to
	// send an empty message). Cleared each time the editor is (re)entered.
	editNotice string

	// composeCmd backs the synthetic "Custom message" selection so m.selected can
	// point at a stable address when composing free text from scratch (#206).
	composeCmd PaletteCmd

	// XF search state
	xf          *xf.Adapter
	xfQuery     textinput.Model
	xfResults   []xf.SearchResult
	xfCursor    int
	xfSearching bool
	xfErr       error

	// Submission, result and memory state (nhm; result.go, memory.go).
	sub    submission
	vars   varsState
	memory memoryState
	env    sendEnv
	// memChoice is the user's CM and CASS choice for the sends (c, s);
	// an empty mode leaves the config's.
	memChoice MemoryModes

	// Theme and styles
	theme  theme.Theme
	styles theme.Styles
	icons  icons.IconSet

	// Computed gradient colors
	headerGradient []string
	listGradient   []string

	// Layout tier (narrow/split/wide/ultra)
	tier layout.Tier

	// Viewport for scrollable command list
	listViewport viewport.Model

	// listContentLines is the number of rendered lines in the command list.
	// Used to size the list box to its content instead of a fixed oversized
	// height that fills the whole popup (#204).
	listContentLines int

	// Granular per-agent selection state (#205). Populated when the user opens
	// the "Select agents" sub-dialog from the target phase.
	agentPanes   []Pane          // the bound agents when the dialog opened
	agentCursor  int             // cursor position within agentPanes
	agentChecked map[string]bool // pane ID -> selected for send
}

// The palette keybindings live in keymap.go as per-phase keymaps
// (commandKeys, targetKeys, selectAgentsKeys, editKeys, xfSearchKeys,
// xfResultsKeys). Both the Update handlers and every help surface consume the
// same binding values, so help cannot advertise keys Update does not handle.

// Options wires the palette to nhm.
type Options struct {
	PaletteState PaletteState
	// Store keeps the pins, favorites and recents.
	Store Store
	// Panes reads the captured workspace's bound agents and how many of
	// its agents have no binding; nil reads none.
	Panes func(ctx context.Context) ([]Pane, int, error)
	// KindKeys are the target digits of nhm's kinds.
	KindKeys []KindKey
	// Submitter runs the sends and memory previews; Base is the request
	// every send starts from (session, workspace, project, scope,
	// enrichment); Bodies holds the template drafts.
	Submitter *Submitter
	Base      send.Request
	Bodies    Bodies
	// XF searches the xf archive; nil uses xf on PATH.
	XF *xf.Adapter
	// Handed is a selection another surface (HandedBy, "board") handed
	// on, frozen there: 1 sends to exactly it, and nothing widens it.
	Handed   []ui.Target
	HandedBy string
}

// customMessageKey is the reserved key for the synthetic "Custom message"
// selection that lets the user compose a free-text prompt from scratch (#206)
// instead of only sending pre-baked commands. It is never stored in the command
// list; it is created on demand when the user presses the compose shortcut.
const customMessageKey = "custom-message"

// paneReadTimeout bounds one read of the panes.
const paneReadTimeout = 10 * time.Second

// NewWithOptions creates a new palette model with optional persisted state wiring.
func NewWithOptions(session string, commands []PaletteCmd, opts Options) Model {
	ti := textinput.New()
	ti.Placeholder = "Search commands..."
	ti.Focus()
	ti.CharLimit = 50
	ti.Width = 40

	t := theme.Current()

	// Style the input with theme colors
	ti.PromptStyle = lipgloss.NewStyle().Foreground(t.Mauve)
	ti.TextStyle = lipgloss.NewStyle().Foreground(t.Text)
	ti.PlaceholderStyle = lipgloss.NewStyle().Foreground(t.Overlay)
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(t.Pink)

	s := theme.NewStyles(t)
	ic := icons.Current()

	// Initialize viewport for scrollable command list
	vp := viewport.New(80, 10) // Will be resized on WindowSizeMsg
	vp.Style = lipgloss.NewStyle()

	m := Model{
		session:      session,
		commands:     commands,
		filtered:     commands,
		filter:       ti,
		phase:        PhaseCommand,
		width:        80,
		height:       24,
		animate:      styles.AnimationsEnabled(),
		showPreview:  true,
		theme:        t,
		styles:       s,
		icons:        ic,
		tier:         layout.TierForWidth(80),
		listViewport: vp,
		headerGradient: []string{
			string(t.Blue),
			string(t.Lavender),
			string(t.Mauve),
		},
		listGradient: []string{
			string(t.Mauve),
			string(t.Pink),
		},
	}

	m.paletteState = opts.PaletteState
	m.store = opts.Store
	m.loadPanes = opts.Panes
	m.kindKeys = opts.KindKeys
	m.handed = handed{by: opts.HandedBy, targets: append([]ui.Target(nil), opts.Handed...)}
	m.env = sendEnv{submitter: opts.Submitter, base: opts.Base, bodies: opts.Bodies}
	m.xf = opts.XF
	if m.xf == nil {
		m.xf = xf.NewAdapter()
	}
	m.xfQuery = initXFQuery(t)

	// Build initial visual order mapping
	m.buildVisualOrder()
	m.syncListViewport()

	return m
}

// Init implements tea.Model
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		textinput.Blink,
		m.fetchPaneCounts(),
		m.fetchRecents(),
	}
	if m.animate {
		cmds = append(cmds, m.tick())
	}
	return tea.Batch(cmds...)
}

func (m Model) tick() tea.Cmd {
	// Use 250ms tick interval to minimize flicker on terminals that
	// struggle with rapid ANSI color escape re-rendering.
	return tea.Tick(time.Millisecond*250, func(t time.Time) tea.Msg {
		return AnimationTickMsg(t)
	})
}

func (m Model) fetchRecents() tea.Cmd {
	store := m.store
	return func() tea.Msg {
		keys, err := store.Recents()
		if err != nil {
			return recentsMsg{err: err}
		}
		return recentsMsg{keys: keys}
	}
}

func (m Model) savePaletteState() tea.Cmd {
	store := m.store
	state := m.paletteState
	if store.Dir == "" {
		return nil
	}

	return func() tea.Msg {
		return paletteStateSavedMsg{err: store.Save(state)}
	}
}

func (m Model) fetchPaneCounts() tea.Cmd {
	load := m.loadPanes
	seq := paneReads.Add(1)
	return func() tea.Msg {
		if load == nil {
			return paneCountsMsg{seq: seq}
		}

		ctx, cancel := context.WithTimeout(context.Background(), paneReadTimeout)
		defer cancel()
		panes, unbound, err := load(ctx)
		if err != nil {
			return paneCountsMsg{seq: seq, err: err}
		}

		return paneCountsMsg{seq: seq, counts: countPanes(panes), panes: panes, unbound: unbound}
	}
}

// countPanes counts the panes per kind, with a few titles as samples.
func countPanes(panes []Pane) paneCounts {
	counts := paneCounts{byKind: map[string]int{}, kindSamples: map[string][]string{}}
	const (
		maxAllSamples  = 3
		maxTypeSamples = 2
	)
	addSample := func(dst *[]string, value string, max int) {
		if value == "" || len(*dst) >= max {
			return
		}
		*dst = append(*dst, value)
	}

	for _, p := range panes {
		title := clean(strings.TrimSpace(p.Title))
		if title == "" {
			title = fmt.Sprintf("pane %d", p.Index)
		}

		counts.totalAgents++
		addSample(&counts.allSamples, title, maxAllSamples)
		counts.byKind[p.Type]++
		samples := counts.kindSamples[p.Type]
		addSample(&samples, title, maxTypeSamples)
		counts.kindSamples[p.Type] = samples
	}

	return counts
}

func (m Model) commandPhaseLayout() (listWidth, previewWidth int, showSplitView bool) {
	const (
		minColumnWidth  = 35
		maxListWidth    = 70
		maxPreviewWidth = 100
	)

	showSplitView = m.tier >= layout.TierSplit
	if !showSplitView {
		listWidth = m.width - 4
		previewWidth = 0
	} else {
		left, right := layout.SplitProportions(m.width)
		listWidth = left - 2
		previewWidth = right - 2

		if listWidth > maxListWidth {
			listWidth = maxListWidth
		}
		if previewWidth > maxPreviewWidth {
			previewWidth = maxPreviewWidth
		}
	}

	if listWidth < minColumnWidth {
		listWidth = minColumnWidth
	}
	if showSplitView && previewWidth < minColumnWidth {
		previewWidth = minColumnWidth
	}

	return listWidth, previewWidth, showSplitView
}

func (m Model) commandListBoxHeight() int {
	// Maximum height the list box may occupy given surrounding chrome (header,
	// filter, help bar). This is the scroll ceiling for long command lists.
	maxHeight := m.height - 14
	if maxHeight < 5 {
		maxHeight = 5
	}

	// Fit the box to its content so a short list renders a compact dialog
	// instead of a fixed, oversized box that fills the whole popup (#204).
	// The viewport is (box height - 2) tall, so a box of contentLines+2 shows
	// every line with no wasted space; taller lists are capped and scroll.
	fit := m.listContentLines + 2
	if fit < 5 {
		fit = 5
	}
	if fit > maxHeight {
		return maxHeight
	}
	return fit
}

func (m *Model) syncListViewport() {
	if m == nil {
		return
	}

	listWidth, _, _ := m.commandPhaseLayout()

	viewportWidth := listWidth - 6
	if viewportWidth < 1 {
		viewportWidth = 1
	}

	// Render the list first so we know how many lines it occupies, then size the
	// box to that content (capped) rather than to the full terminal height (#204).
	content := m.renderCommandList(max(viewportWidth+2, 1))
	m.listContentLines = strings.Count(content, "\n") + 1

	listBoxHeight := m.commandListBoxHeight()
	viewportHeight := listBoxHeight - 2
	if viewportHeight < 1 {
		viewportHeight = 1
	}

	m.listViewport.Width = viewportWidth
	m.listViewport.Height = viewportHeight
	m.listViewport.SetContent(content)
	m.ensureCursorVisible()
}

// Update implements tea.Model: ntm's handler (update), then a memory
// preview when it is on and the text in focus changed.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	nm, ok := next.(Model)
	if !ok {
		return next, cmd
	}
	if mcmd := nm.memoryFollow(); mcmd != nil {
		return nm, tea.Batch(cmd, mcmd)
	}
	return nm, cmd
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.tier = layout.TierForWidth(msg.Width)
		m.filter.Width = m.width/2 - 10
		if m.filter.Width < 20 {
			m.filter.Width = 20
		}
		m.syncListViewport()
		// Resize textarea if currently in edit phase
		if m.phase == PhaseEdit {
			editWidth := msg.Width - 8
			if editWidth < 30 {
				editWidth = 30
			}
			m.editInput.SetWidth(editWidth)

			// Recalculate height to match available vertical space.
			lines := strings.Count(m.editInput.Value(), "\n") + 3
			maxHeight := msg.Height - 14
			if maxHeight < 4 {
				maxHeight = 4
			}
			if lines > maxHeight {
				lines = maxHeight
			}
			if lines < 4 {
				lines = 4
			}
			m.editInput.SetHeight(lines)
		}
		// Force a full clear + repaint on resize (#186). In alt-screen mode the
		// renderer only repaints the new frame's lines and does not reliably
		// erase cells left by a previous, differently sized frame, which can
		// leave stale glyphs ("scrambled" UI) - especially when growing the
		// window. tea.ClearScreen guarantees a clean repaint.
		return m, tea.ClearScreen

	case AnimationTickMsg:
		if !m.animate {
			return m, nil
		}
		m.animTick++
		m.syncListViewport()
		return m, m.tick()

	case paneCountsMsg:
		if msg.seq < m.paneSeen {
			// An older read than the one shown.
			return m, nil
		}
		m.paneSeen = msg.seq
		if msg.err != nil {
			m.paneCountsErr = msg.err
			m.paneCountsKnown = false
			return m, nil
		}
		m.paneCounts = msg.counts
		m.panes = msg.panes
		m.panesUnbound = msg.unbound
		m.paneCountsKnown = true
		m.paneCountsErr = nil
		return m, nil

	case recentsMsg:
		if msg.err == nil {
			m.recents = msg.keys
			m.buildVisualOrder()
			m.listViewport.GotoTop()
			m.syncListViewport()
		}
		return m, nil

	case paletteStateSavedMsg:
		m.paletteStateErr = msg.err
		return m, nil

	case ReloadMsg:
		m.commands = msg.Commands
		m.updateFiltered()
		m.reconcileSelectionAfterReload()
		return m, nil

	case XFSearchResultsMsg:
		m.xfSearching = false
		if msg.Err != nil {
			m.xfErr = msg.Err
			return m, nil
		}
		m.xfErr = nil
		m.xfResults = msg.Results
		m.xfCursor = 0
		if len(msg.Results) > 0 {
			m.phase = PhaseXFResults
		} else {
			m.xfErr = fmt.Errorf("no results found for %q", msg.Query)
		}
		return m, nil

	case SubmitMsg, ReconcileMsg, MemoryMsg, memoryDueMsg, recentSavedMsg:
		return m.updateNhm(msg)

	case tea.MouseMsg:
		// Handle mouse wheel scrolling in command phase
		if m.phase == PhaseCommand && !m.showHelp {
			var cmd tea.Cmd
			m.listViewport, cmd = m.listViewport.Update(msg)
			return m, cmd
		}

	case tea.KeyMsg:
		// Help overlay: Esc or ?/F1 closes it; otherwise ignore input.
		if m.showHelp {
			if msg.String() == "esc" || key.Matches(msg, targetKeys.Help) {
				m.showHelp = false
			}
			return m, nil
		}

		// Open the help overlay with each phase's Help binding. The command
		// phase keeps the filter focused, so its binding is f1 only ('?' must
		// type into the search box); target/xf-results also accept '?'.
		switch {
		case m.phase == PhaseCommand && key.Matches(msg, commandKeys.Help):
			m.showHelp = true
			return m, nil
		case m.phase == PhaseTarget && key.Matches(msg, targetKeys.Help):
			m.showHelp = true
			return m, nil
		case m.phase == PhaseXFResults && key.Matches(msg, xfResultsKeys.Help):
			m.showHelp = true
			return m, nil
		}

		switch m.phase {
		case PhaseCommand:
			return m.updateCommandPhase(msg)
		case PhaseTarget:
			return m.updateTargetPhase(msg)
		case PhaseSelectAgents:
			return m.updateSelectAgentsPhase(msg)
		case PhaseEdit:
			return m.updateEditPhase(msg)
		case PhaseXFSearch:
			return m.updateXFSearchPhase(msg)
		case PhaseXFResults:
			return m.updateXFResultsPhase(msg)
		case PhaseResult:
			return m.updateResultPhase(msg)
		case PhaseVars:
			return m.updateVarsPhase(msg)
		}
	}

	// Update filter input
	if m.phase == PhaseCommand {
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		m.updateFiltered()
		return m, cmd
	}

	// Update edit textarea (handles cursor blink and other non-key messages)
	if m.phase == PhaseEdit {
		var cmd tea.Cmd
		m.editInput, cmd = m.editInput.Update(msg)
		return m, cmd
	}

	// Update xf query input
	if m.phase == PhaseXFSearch {
		var cmd tea.Cmd
		m.xfQuery, cmd = m.xfQuery.Update(msg)
		return m, cmd
	}

	// Update the variable inputs
	if m.phase == PhaseVars {
		return m.updateVarsInput(msg)
	}

	return m, nil
}

func (m *Model) updateCommandPhase(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// When the filter input is focused, printable characters (runes and space)
	// must go to the text input first. Without this guard, single-char shortcuts
	// like 'q' (quit) and 'g' (home) swallow keystrokes that belong to the
	// search query.  Only non-printable keys (ctrl combos, arrows, esc, enter,
	// function keys, etc.) fall through to the shortcut switch below.
	if m.filter.Focused() && (msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace) {
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		m.updateFiltered()
		return *m, cmd
	}

	switch {
	case key.Matches(msg, commandKeys.Quit):
		m.quitting = true
		return *m, tea.Quit

	case key.Matches(msg, commandKeys.Back):
		m.quitting = true
		return *m, tea.Quit

	case key.Matches(msg, commandKeys.Up):
		if len(m.visualOrder) > 0 {
			if pos := m.cursorVisualPos(); pos > 0 {
				m.cursor = m.visualOrder[pos-1]
				m.ensureCursorVisible()
			}
		}

	case key.Matches(msg, commandKeys.Down):
		if len(m.visualOrder) > 0 {
			if pos := m.cursorVisualPos(); pos < len(m.visualOrder)-1 {
				m.cursor = m.visualOrder[pos+1]
				m.ensureCursorVisible()
			}
		}

	case key.Matches(msg, commandKeys.PageUp):
		// Move cursor up by approximately one page worth of items
		if len(m.visualOrder) > 0 {
			pos := m.cursorVisualPos()
			// Estimate items per page (viewport height minus some header overhead)
			itemsPerPage := m.listViewport.Height - 4
			if itemsPerPage < 3 {
				itemsPerPage = 3
			}
			newPos := pos - itemsPerPage
			if newPos < 0 {
				newPos = 0
			}
			m.cursor = m.visualOrder[newPos]
			m.ensureCursorVisible()
		}

	case key.Matches(msg, commandKeys.PageDown):
		// Move cursor down by approximately one page worth of items
		if len(m.visualOrder) > 0 {
			pos := m.cursorVisualPos()
			itemsPerPage := m.listViewport.Height - 4
			if itemsPerPage < 3 {
				itemsPerPage = 3
			}
			newPos := pos + itemsPerPage
			if newPos >= len(m.visualOrder) {
				newPos = len(m.visualOrder) - 1
			}
			m.cursor = m.visualOrder[newPos]
			m.ensureCursorVisible()
		}

	case key.Matches(msg, commandKeys.HalfPageUp):
		// Move cursor up by half a page worth of items
		if len(m.visualOrder) > 0 {
			pos := m.cursorVisualPos()
			itemsPerHalfPage := (m.listViewport.Height - 4) / 2
			if itemsPerHalfPage < 2 {
				itemsPerHalfPage = 2
			}
			newPos := pos - itemsPerHalfPage
			if newPos < 0 {
				newPos = 0
			}
			m.cursor = m.visualOrder[newPos]
			m.ensureCursorVisible()
		}

	case key.Matches(msg, commandKeys.HalfPageDown):
		// Move cursor down by half a page worth of items
		if len(m.visualOrder) > 0 {
			pos := m.cursorVisualPos()
			itemsPerHalfPage := (m.listViewport.Height - 4) / 2
			if itemsPerHalfPage < 2 {
				itemsPerHalfPage = 2
			}
			newPos := pos + itemsPerHalfPage
			if newPos >= len(m.visualOrder) {
				newPos = len(m.visualOrder) - 1
			}
			m.cursor = m.visualOrder[newPos]
			m.ensureCursorVisible()
		}

	case key.Matches(msg, commandKeys.Home):
		// Jump to first item
		if len(m.visualOrder) > 0 {
			m.cursor = m.visualOrder[0]
			m.listViewport.GotoTop()
		}

	case key.Matches(msg, commandKeys.End):
		// Jump to last item
		if len(m.visualOrder) > 0 {
			m.cursor = m.visualOrder[len(m.visualOrder)-1]
			m.listViewport.GotoBottom()
		}

	case key.Matches(msg, commandKeys.TogglePin):
		if len(m.filtered) > 0 {
			selectedKey := strings.TrimSpace(m.filtered[m.cursor].Key)
			if selectedKey != "" {
				var added bool
				m.paletteState.Pinned, added = toggleListKey(m.paletteState.Pinned, selectedKey, true)
				if added {
					m.paletteState.Favorites = ensureListKey(m.paletteState.Favorites, selectedKey, true)
				}
				m.buildVisualOrder()
				m.syncListViewport()
				return *m, m.savePaletteState()
			}
		}

	case key.Matches(msg, commandKeys.ToggleFavorite):
		if len(m.filtered) > 0 {
			selectedKey := strings.TrimSpace(m.filtered[m.cursor].Key)
			if selectedKey != "" {
				var added bool
				m.paletteState.Favorites, added = toggleListKey(m.paletteState.Favorites, selectedKey, true)
				if !added {
					// Pinned is a subset of favorites.
					m.paletteState.Pinned = removeListKey(m.paletteState.Pinned, selectedKey)
				}
				m.buildVisualOrder()
				m.syncListViewport()
				return *m, m.savePaletteState()
			}
		}

	case key.Matches(msg, commandKeys.Memory):
		m.toggleMemory()
		return *m, nil

	case key.Matches(msg, commandKeys.XFSearch):
		m.enterXFSearch()
		return *m, nil

	case key.Matches(msg, commandKeys.Compose):
		// Compose a free-text message from scratch (#206) without needing to pick
		// a pre-baked command first. Uses a synthetic selection with an empty
		// prompt and jumps straight into the editor.
		m.composeCmd = PaletteCmd{Key: customMessageKey, Label: "Custom message", Prompt: ""}
		m.selected = &m.composeCmd
		m.draft.Reset()
		m.vars = varsState{}
		editCmd := m.enterEditPhase()
		return *m, editCmd

	case key.Matches(msg, commandKeys.Select):
		if len(m.filtered) > 0 {
			cmd := m.filtered[m.cursor]
			if cmd.Key == "xf-search" {
				m.enterXFSearch()
				return *m, nil
			}
			m.selected = &m.filtered[m.cursor]
			m.draft.Reset()
			m.vars = varsState{}
			m.targetNotice = ""
			m.phase = PhaseTarget
			// Read the agents again: the target counts and the targets
			// frozen by the next digit come from the newest read.
			m.syncListViewport()
			return *m, m.fetchPaneCounts()
		}

	// NOTE: there is deliberately no 1-9 quick-select here. The filter input is
	// always focused, so digits type into the search box; quick-select was
	// REJECTED per the recorded H3 decision (bd-ws7-docs-ux-truth-tqh3l.3).

	default:
		// Let the textinput handle it
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		m.updateFiltered()
		return *m, cmd
	}

	m.syncListViewport()
	return *m, nil
}

func (m *Model) updateTargetPhase(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, targetKeys.Back):
		m.phase = PhaseCommand
		m.selected = nil
		m.draft.Reset()
		m.vars = varsState{}
		m.targetNotice = ""

	case key.Matches(msg, targetKeys.Quit):
		m.quitting = true
		return *m, tea.Quit

	case key.Matches(msg, targetKeys.Edit):
		cmd := m.enterEditPhase()
		return *m, cmd

	case key.Matches(msg, targetKeys.Memory):
		m.toggleMemory()
		return *m, nil

	case key.Matches(msg, targetKeys.MemoryCM):
		m.toggleCM()
		return *m, nil

	case key.Matches(msg, targetKeys.MemoryCASS):
		m.toggleCASS()
		return *m, nil

	case key.Matches(msg, targetKeys.Target1):
		m.target = TargetAll
		return m.send()

	case key.Matches(msg, targetKeys.TargetKind):
		if m.handed.present() {
			m.targetNotice = m.handedNotice()
			return *m, nil
		}
		kind := m.kindForKey(msg.String())
		if kind == "" {
			m.targetNotice = fmt.Sprintf("No kind has the key %s.", msg.String())
			return *m, nil
		}
		m.target = TargetKind
		m.targetKind = kind
		return m.send()

	case key.Matches(msg, targetKeys.SelectAgents):
		if m.handed.present() {
			m.targetNotice = m.handedNotice()
			return *m, nil
		}
		return m.enterSelectAgentsPhase()
	}

	return *m, nil
}

// kindForKey is the kind whose target digit is k, or "".
func (m Model) kindForKey(k string) string {
	for _, kk := range m.kindKeys {
		if kk.Key == k {
			return kk.Kind
		}
	}
	return ""
}

// enterSelectAgentsPhase opens the granular per-agent multi-select dialog
// (#205) over the agents of the last read. All agents start checked so the
// user can quickly deselect the few they want to exclude.
func (m *Model) enterSelectAgentsPhase() (tea.Model, tea.Cmd) {
	if !m.paneCountsKnown {
		m.targetNotice = "The agents are not read yet."
		if m.paneCountsErr != nil {
			m.targetNotice = "The agents could not be read: " + clean(m.paneCountsErr.Error())
		}
		return *m, m.fetchPaneCounts()
	}

	agents := make([]Pane, len(m.panes))
	copy(agents, m.panes)

	m.agentPanes = agents
	m.agentCursor = 0
	m.agentChecked = make(map[string]bool, len(agents))
	for _, p := range agents {
		m.agentChecked[p.ID] = true
	}
	m.phase = PhaseSelectAgents
	return *m, nil
}

// updateSelectAgentsPhase handles key events in the granular agent selector.
func (m *Model) updateSelectAgentsPhase(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, selectAgentsKeys.Back):
		m.phase = PhaseTarget
		return *m, nil

	case key.Matches(msg, selectAgentsKeys.Quit):
		m.quitting = true
		return *m, tea.Quit

	case key.Matches(msg, selectAgentsKeys.Up):
		if m.agentCursor > 0 {
			m.agentCursor--
		}
		return *m, nil

	case key.Matches(msg, selectAgentsKeys.Down):
		if m.agentCursor < len(m.agentPanes)-1 {
			m.agentCursor++
		}
		return *m, nil

	case key.Matches(msg, selectAgentsKeys.Toggle):
		if m.agentCursor >= 0 && m.agentCursor < len(m.agentPanes) {
			id := m.agentPanes[m.agentCursor].ID
			m.agentChecked[id] = !m.agentChecked[id]
		}
		return *m, nil

	case key.Matches(msg, selectAgentsKeys.All):
		for _, p := range m.agentPanes {
			m.agentChecked[p.ID] = true
		}
		return *m, nil

	case key.Matches(msg, selectAgentsKeys.None):
		for _, p := range m.agentPanes {
			m.agentChecked[p.ID] = false
		}
		return *m, nil

	case key.Matches(msg, selectAgentsKeys.Select):
		// Only send if at least one pane is checked; otherwise stay put.
		for _, p := range m.agentPanes {
			if m.agentChecked[p.ID] {
				m.target = TargetSelected
				return m.send()
			}
		}
		return *m, nil
	}

	return *m, nil
}

// enterEditPhase opens the prompt editor, pre-populating with any existing draft.
func (m *Model) enterEditPhase() tea.Cmd {
	if m.selected == nil {
		return nil
	}
	// Clear any stale notice; the send() empty-guard re-sets it after this call.
	m.editNotice = ""
	prompt := m.draft.Text(m.selected)

	m.editInput = newEditor(m.theme, m.width, m.height, prompt)
	m.phase = PhaseEdit
	return m.editInput.Focus()
}

// newEditor is the palette's prompt editor, sized for width and height
// and themed: the edit phase's and the Composer's.
func newEditor(t theme.Theme, width, height int, prompt string) textarea.Model {
	ta := textarea.New()
	ta.SetValue(prompt)
	if strings.TrimSpace(prompt) == "" {
		ta.Placeholder = "Type your custom message…"
	}
	ta.ShowLineNumbers = false
	ta.CharLimit = 0

	editWidth := width - 8
	if editWidth < 30 {
		editWidth = 30
	}
	ta.SetWidth(editWidth)

	// Height: fit prompt lines, capped at available vertical space.
	lines := strings.Count(prompt, "\n") + 3
	maxHeight := height - 14
	if maxHeight < 4 {
		maxHeight = 4
	}
	if lines > maxHeight {
		lines = maxHeight
	}
	if lines < 4 {
		lines = 4
	}
	ta.SetHeight(lines)

	// Theme the textarea to match the palette
	ta.FocusedStyle.Base = lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(t.Blue)
	ta.FocusedStyle.Text = lipgloss.NewStyle().Foreground(t.Text)
	ta.FocusedStyle.Placeholder = lipgloss.NewStyle().Foreground(t.Overlay)
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle().Background(t.Surface0)
	ta.Cursor.Style = lipgloss.NewStyle().Foreground(t.Pink)

	return ta
}

// updateEditPhase handles key events in the prompt-edit phase.
func (m *Model) updateEditPhase(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, editKeys.ConfirmEdit):
		// Save the draft and return to target selection. An emptied draft
		// stays empty: send refuses it.
		m.draft.Set(m.editInput.Value())
		m.phase = PhaseTarget
		return *m, nil

	case key.Matches(msg, editKeys.Back):
		// Discard unsaved changes and return to target selection.
		m.phase = PhaseTarget
		return *m, nil

	// ctrl+c only: the textarea is focused, so a 'q' binding here would quit
	// the palette mid-prompt whenever the text contains the letter q.
	case key.Matches(msg, editKeys.Quit):
		m.quitting = true
		return *m, tea.Quit
	}

	var cmd tea.Cmd
	m.editInput, cmd = m.editInput.Update(msg)
	return *m, cmd
}

func (m *Model) reconcileSelectionAfterReload() {
	if m.selected == nil {
		return
	}

	// XF results and free-text compose selections are ephemeral rather than
	// config-backed palette items. Keep them stable across palette config reloads.
	if m.selected.Key == "xf-result" || m.selected.Key == customMessageKey {
		return
	}

	selectedKey := strings.TrimSpace(m.selected.Key)
	if selectedKey != "" {
		for i := range m.commands {
			if strings.TrimSpace(m.commands[i].Key) == selectedKey {
				m.selected = &m.commands[i]
				return
			}
		}
	}

	m.selected = nil
	m.draft.Reset()
	if m.phase == PhaseTarget || m.phase == PhaseEdit {
		m.phase = PhaseCommand
	}
}

func (m *Model) updateFiltered() {
	prevKey := ""
	if m.cursor >= 0 && m.cursor < len(m.filtered) {
		prevKey = m.filtered[m.cursor].Key
	}

	query := strings.ToLower(m.filter.Value())
	if query == "" {
		m.filtered = m.commands
	} else {
		m.filtered = nil
		for _, cmd := range m.commands {
			if strings.Contains(strings.ToLower(cmd.Label), query) ||
				strings.Contains(strings.ToLower(cmd.Key), query) ||
				strings.Contains(strings.ToLower(cmd.Category), query) {
				m.filtered = append(m.filtered, cmd)
			}
		}
	}

	// Build visual order mapping (items grouped by category)
	m.buildVisualOrder()

	// Preserve selection when filtering, if possible.
	if prevKey != "" {
		for i, cmd := range m.filtered {
			if cmd.Key == prevKey {
				m.cursor = i
				break
			}
		}
	}

	// Keep cursor in bounds
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	m.syncListViewport()
}

func (m Model) cursorVisualPos() int {
	for pos, idx := range m.visualOrder {
		if idx == m.cursor {
			return pos
		}
	}
	return 0
}

// visualPosToLineNum converts a visual position (index in visualOrder) to an
// approximate line number in the rendered list, accounting for section headers
// and blank lines between sections.
func (m *Model) visualPosToLineNum(pos int) int {
	if pos < 0 || len(m.visualOrder) == 0 {
		return 0
	}
	if pos >= len(m.visualOrder) {
		pos = len(m.visualOrder) - 1
	}

	// Count how many items are in pinned and recents sections
	pinnedCount := 0
	recentsCount := 0

	idxByKey := make(map[string]int, len(m.filtered))
	for i, cmd := range m.filtered {
		if cmd.Key != "" {
			idxByKey[cmd.Key] = i
		}
	}

	used := make(map[int]bool)
	for _, k := range m.paletteState.Pinned {
		if idx, ok := idxByKey[k]; ok && !used[idx] {
			used[idx] = true
			pinnedCount++
		}
	}
	for _, k := range m.recents {
		if idx, ok := idxByKey[k]; ok && !used[idx] {
			used[idx] = true
			recentsCount++
		}
	}

	// Calculate which section the position falls into and the line offset
	lineNum := 0
	itemsBeforePos := 0

	// Pinned section: header + items + blank
	if pinnedCount > 0 {
		lineNum++ // header
		if pos < pinnedCount {
			return lineNum + pos
		}
		lineNum += pinnedCount + 1 // items + blank
		itemsBeforePos = pinnedCount
	}

	// Recents section: header + items + blank
	if recentsCount > 0 {
		lineNum++ // header
		if pos < itemsBeforePos+recentsCount {
			return lineNum + (pos - itemsBeforePos)
		}
		lineNum += recentsCount + 1 // items + blank
		itemsBeforePos += recentsCount
	}

	// Category sections: exact iteration mirroring the actual render structure.
	// Each category has 1 header line + N items + 1 blank separator line.
	if pos >= itemsBeforePos {
		// Rebuild category order exactly as buildVisualOrder / renderCommandList do.
		catItems := make(map[string][]int)
		catOrder := []string{}
		for i, cmd := range m.filtered {
			if used[i] {
				continue
			}
			cat := cmd.Category
			if cat == "" {
				cat = "General"
			}
			if _, exists := catItems[cat]; !exists {
				catOrder = append(catOrder, cat)
			}
			catItems[cat] = append(catItems[cat], i)
		}

		for _, cat := range catOrder {
			count := len(catItems[cat])
			lineNum++ // category header
			if pos < itemsBeforePos+count {
				return lineNum + (pos - itemsBeforePos)
			}
			lineNum += count + 1 // items + blank separator
			itemsBeforePos += count
		}
	}

	return lineNum
}

// ensureCursorVisible scrolls the viewport if necessary to keep the cursor visible.
func (m *Model) ensureCursorVisible() {
	pos := m.cursorVisualPos()
	linePos := m.visualPosToLineNum(pos)

	if linePos < m.listViewport.YOffset {
		m.listViewport.SetYOffset(linePos)
	}

	visibleBottom := m.listViewport.YOffset + m.listViewport.Height - 2
	if linePos > visibleBottom {
		newOffset := linePos - m.listViewport.Height + 3
		if newOffset < 0 {
			newOffset = 0
		}
		m.listViewport.SetYOffset(newOffset)
	}
}

// buildVisualOrder creates a mapping from visual position to filtered index.
// Items are grouped by category, so the visual order differs from the slice order.
func (m *Model) buildVisualOrder() {
	m.visualOrder = nil
	if len(m.filtered) == 0 {
		return
	}

	// Key → index for filtered commands (keys are unique after config merge/dedupe).
	idxByKey := make(map[string]int, len(m.filtered))
	for i, cmd := range m.filtered {
		if cmd.Key != "" {
			idxByKey[cmd.Key] = i
		}
	}

	used := make(map[int]bool, len(m.filtered))
	appendKeyOrder := func(keys []string) {
		for _, k := range keys {
			idx, ok := idxByKey[k]
			if !ok || used[idx] {
				continue
			}
			used[idx] = true
			m.visualOrder = append(m.visualOrder, idx)
		}
	}

	// Pinned first, then recents, then the remaining categories.
	appendKeyOrder(m.paletteState.Pinned)
	appendKeyOrder(m.recents)

	// Group remaining by category (same logic as renderCommandList)
	categories := make(map[string][]int)
	categoryOrder := []string{}
	for i, cmd := range m.filtered {
		if used[i] {
			continue
		}
		cat := cmd.Category
		if cat == "" {
			cat = "General"
		}
		if _, exists := categories[cat]; !exists {
			categoryOrder = append(categoryOrder, cat)
		}
		categories[cat] = append(categories[cat], i)
	}

	for _, cat := range categoryOrder {
		m.visualOrder = append(m.visualOrder, categories[cat]...)
	}
}

// send freezes the targets of m.target at this keypress and submits the
// draft to them through nhm's send operation (result.go). An empty draft
// goes back to the editor; it is never sent, and never replaced by the
// command's prompt.
func (m *Model) send() (tea.Model, tea.Cmd) {
	if m.selected == nil {
		return *m, nil
	}

	payload, err := m.draft.Payload(m.selected, m.vars.values)
	// Never dispatch an empty/whitespace message. Reachable via the ctrl+n
	// compose flow (#206) confirmed with nothing typed, or a pre-baked
	// command edited to empty. Bounce back to the editor instead of sending.
	if err != nil {
		editCmd := m.enterEditPhase()
		m.editNotice = "Message is empty — type something to send, or Esc to cancel."
		return *m, editCmd
	}

	targets := m.frozenTargets()
	if len(targets) == 0 {
		m.targetNotice = "No bound agent for that target here."
		if !m.paneCountsKnown {
			m.targetNotice = "The agents are not read yet; try again."
		}
		if m.phase == PhaseSelectAgents {
			m.phase = PhaseTarget
		}
		return *m, m.fetchPaneCounts()
	}
	m.targetNotice = ""
	return m.submit(payload, targets)
}

// frozenTargets are the targets of m.target now: every agent of the last
// read, one kind's, or the ones checked in the picker. With a handed
// selection, 1 is exactly that selection, as it was frozen.
func (m Model) frozenTargets() Targets {
	if m.handed.present() {
		if m.target != TargetAll {
			return nil
		}
		ts, _ := m.handedTargets()
		return ts
	}
	var ts Targets
	if m.target == TargetSelected {
		for _, p := range m.agentPanes {
			if m.agentChecked[p.ID] {
				ts = append(ts, p.Frozen())
			}
		}
		return ts
	}
	for _, p := range m.panes {
		if m.target == TargetAll || p.Type == m.targetKind {
			ts = append(ts, p.Frozen())
		}
	}
	return ts
}

func toggleListKey(list []string, key string, prepend bool) ([]string, bool) {
	if key == "" {
		return list, false
	}
	for i, v := range list {
		if v == key {
			// Remove
			out := make([]string, 0, len(list)-1)
			out = append(out, list[:i]...)
			out = append(out, list[i+1:]...)
			return out, false
		}
	}

	// Add
	if prepend {
		return append([]string{key}, list...), true
	}
	return append(list, key), true
}

func removeListKey(list []string, key string) []string {
	if key == "" || len(list) == 0 {
		return list
	}
	out := list[:0]
	for _, v := range list {
		if v == key {
			continue
		}
		out = append(out, v)
	}
	// out aliases list's backing array; copy to avoid surprising retention if callers append.
	return append([]string(nil), out...)
}

func ensureListKey(list []string, key string, prepend bool) []string {
	if key == "" {
		return list
	}
	for _, v := range list {
		if v == key {
			return list
		}
	}
	if prepend {
		return append([]string{key}, list...)
	}
	return append(list, key)
}

// View implements tea.Model
func (m Model) View() string {
	if m.showHelp {
		maxWidth := 70
		if m.width > 0 && m.width-4 < maxWidth {
			maxWidth = m.width - 4
		}
		if maxWidth < 20 {
			maxWidth = 20
		}

		helpOverlay := components.HelpOverlay(components.HelpOverlayOptions{
			Title:    "Palette Shortcuts",
			Sections: paletteHelpSections(),
			MaxWidth: maxWidth,
		})
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, helpOverlay)
	}

	if m.quitting {
		return m.viewQuitting()
	}

	switch m.phase {
	case PhaseCommand:
		return m.viewCommandPhase()
	case PhaseTarget:
		return m.viewTargetPhase()
	case PhaseSelectAgents:
		return m.viewSelectAgentsPhase()
	case PhaseEdit:
		return m.viewEditPhase()
	case PhaseXFSearch:
		return m.viewXFSearchPhase()
	case PhaseXFResults:
		return m.viewXFResultsPhase()
	case PhaseResult:
		return m.viewResultPhase()
	case PhaseVars:
		return m.viewVarsPhase()
	}

	return ""
}

// viewQuitting is the last frame: an error, or nothing (the receipts were
// shown on the result screen before).
func (m Model) viewQuitting() string {
	t := m.theme
	ic := m.icons

	if m.err != nil {
		errorStyle := lipgloss.NewStyle().Foreground(t.Error)
		return errorStyle.Render(fmt.Sprintf("\n  %s Error: %v\n\n", ic.Cross, clean(m.err.Error())))
	}

	return ""
}

func (m Model) viewCommandPhase() string {
	t := m.theme
	ic := m.icons

	var b strings.Builder

	listWidth, previewWidth, showSplitView := m.commandPhaseLayout()

	// ═══════════════════════════════════════════════════════════════
	// HEADER with animated gradient
	// ═══════════════════════════════════════════════════════════════
	b.WriteString("\n")

	// Animated title with shimmer effect
	titleText := ic.Palette + "  nhm Command Palette"
	animatedTitle := styles.Shimmer(titleText, m.animTick, m.headerGradient...)

	sessionBadge := lipgloss.NewStyle().
		Background(t.Surface0).
		Foreground(t.Text).
		Padding(0, 1).
		Render(ic.Session + " " + clean(m.session))

	headerLine := "  " + animatedTitle + "  " + sessionBadge
	b.WriteString(headerLine + "\n")

	// Gradient divider
	b.WriteString("  " + styles.GradientDivider(m.width-4, m.headerGradient...) + "\n\n")

	// ═══════════════════════════════════════════════════════════════
	// FILTER INPUT with glow effect
	// ═══════════════════════════════════════════════════════════════
	filterBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Mauve).
		Padding(0, 1).
		Width(listWidth - 4)

	searchIcon := lipgloss.NewStyle().Foreground(t.Mauve).Render(ic.Search + " ")
	filterRendered := lipgloss.NewStyle().MarginLeft(2).Render(filterBox.Render(searchIcon + m.filter.View()))
	b.WriteString(filterRendered + "\n\n")

	// ═══════════════════════════════════════════════════════════════
	// RESPONSIVE LAYOUT: Adapts to terminal width
	// ═══════════════════════════════════════════════════════════════
	listBoxHeight := m.commandListBoxHeight()

	// Render scroll indicator if content overflows
	scrollIndicator := ""
	if m.listViewport.TotalLineCount() > m.listViewport.Height {
		pct := m.listViewport.ScrollPercent() * 100
		scrollStyle := lipgloss.NewStyle().Foreground(t.Overlay)
		if pct < 1 {
			scrollIndicator = scrollStyle.Render(" ↓ scroll")
		} else if pct > 99 {
			scrollIndicator = scrollStyle.Render(" ↑ scroll")
		} else {
			scrollIndicator = scrollStyle.Render(fmt.Sprintf(" %.0f%%", pct))
		}
	}

	// List box with subtle glow - use viewport content
	listBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Surface2).
		Width(listWidth-2).
		Padding(1, 1)

	var columns string
	if showSplitView {
		// Show preview alongside list on wider displays
		previewContent := m.renderPreview(previewWidth - 4)

		// Preview box with accent border
		previewBox := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Blue).
			Width(previewWidth-2).
			Height(listBoxHeight).
			Padding(1, 1)

		// Join columns horizontally
		columns = lipgloss.JoinHorizontal(
			lipgloss.Top,
			listBox.Render(m.listViewport.View()+scrollIndicator),
			"  ",
			previewBox.Render(previewContent),
		)
	} else {
		// Narrow display: list only (preview shown on selection)
		columns = listBox.Render(m.listViewport.View() + scrollIndicator)
	}

	b.WriteString(columns + "\n\n")

	// ═══════════════════════════════════════════════════════════════
	// HELP BAR with styled keys
	// ═══════════════════════════════════════════════════════════════
	b.WriteString("  " + m.renderHelpBar() + "\n")

	// Center the (now content-sized) palette vertically so it reads as a compact
	// floating dialog instead of a block anchored to the top edge (#204).
	return lipgloss.Place(m.width, m.height, lipgloss.Left, lipgloss.Center, b.String())
}

func (m Model) renderCommandList(width int) string {
	t := m.theme
	ic := m.icons

	if len(m.filtered) == 0 {
		return components.EmptyState("No commands match your filter", width)
	}

	var lines []string
	query := strings.TrimSpace(m.filter.Value())

	// Index commands by key (keys are unique after config merge/dedupe).
	idxByKey := make(map[string]int, len(m.filtered))
	for i, cmd := range m.filtered {
		if cmd.Key != "" {
			idxByKey[cmd.Key] = i
		}
	}

	used := make(map[int]bool, len(m.filtered))
	resolveKeyOrder := func(keys []string) []int {
		out := make([]int, 0, len(keys))
		for _, k := range keys {
			idx, ok := idxByKey[k]
			if !ok || used[idx] {
				continue
			}
			used[idx] = true
			out = append(out, idx)
		}
		return out
	}

	pinned := resolveKeyOrder(m.paletteState.Pinned)
	recents := resolveKeyOrder(m.recents)

	// Remaining grouped by category.
	categories := make(map[string][]int)
	categoryOrder := []string{}
	for i, cmd := range m.filtered {
		if used[i] {
			continue
		}
		cat := cmd.Category
		if cat == "" {
			cat = "General"
		}
		if _, exists := categories[cat]; !exists {
			categoryOrder = append(categoryOrder, cat)
		}
		categories[cat] = append(categories[cat], i)
	}

	renderHeader := func(icon, title string) {
		header := strings.TrimSpace(icon + " " + clean(title))
		lines = append(lines, styles.GradientText(header, string(t.Lavender), string(t.Mauve)))
	}

	isFavorite := func(key string) bool {
		for _, v := range m.paletteState.Favorites {
			if v == key {
				return true
			}
		}
		return false
	}
	isPinned := func(key string) bool {
		for _, v := range m.paletteState.Pinned {
			if v == key {
				return true
			}
		}
		return false
	}

	pinStyle := lipgloss.NewStyle().Foreground(t.Mauve).Bold(true)
	favStyle := lipgloss.NewStyle().Foreground(t.Yellow).Bold(true)

	renderItem := func(idx int, itemNum int) {
		cmd := m.filtered[idx]
		isSelected := idx == m.cursor

		var line strings.Builder

		// Selection indicator with animation
		if isSelected {
			pointer := styles.Shimmer(ic.Pointer, m.animTick, string(t.Pink), string(t.Mauve))
			line.WriteString(pointer + " ")
		} else {
			line.WriteString("  ")
		}

		// Number (1-9) with subtle styling
		if itemNum <= 9 {
			numStyle := lipgloss.NewStyle().
				Foreground(t.Surface2).
				Background(t.Surface0).
				Padding(0, 0)
			line.WriteString(numStyle.Render(fmt.Sprintf("%d", itemNum)) + " ")
		} else {
			line.WriteString("  ")
		}

		// Marker column (pinned or favorite)
		marker := " "
		if isPinned(cmd.Key) {
			marker = pinStyle.Render(ic.Target)
		} else if isFavorite(cmd.Key) {
			marker = favStyle.Render(ic.Star)
		}
		line.WriteString(marker + " ")

		// Item label with selection highlight
		labelBudget := width - 10
		if labelBudget < 10 {
			labelBudget = 10
		}
		label := layout.TruncateWidthDefault(clean(cmd.Label), labelBudget)

		if isSelected {
			line.WriteString(styles.GradientText(label, string(t.Pink), string(t.Rosewater)))
		} else {
			labelStyle := lipgloss.NewStyle().Foreground(t.Text)
			matchStyle := lipgloss.NewStyle().Foreground(t.Mauve).Bold(true)
			line.WriteString(renderMatchHighlighted(label, query, labelStyle, matchStyle))
		}

		lines = append(lines, line.String())
	}

	itemNum := 0

	if len(pinned) > 0 {
		renderHeader(ic.Target, "Pinned")
		for _, idx := range pinned {
			itemNum++
			renderItem(idx, itemNum)
		}
		lines = append(lines, "")
	}

	if len(recents) > 0 {
		renderHeader(ic.Circle, "Recent")
		for _, idx := range recents {
			itemNum++
			renderItem(idx, itemNum)
		}
		lines = append(lines, "")
	}

	for _, cat := range categoryOrder {
		indices := categories[cat]
		catIcon := ic.CategoryIcon(cat)
		renderHeader(catIcon, cat)
		for _, idx := range indices {
			itemNum++
			renderItem(idx, itemNum)
		}
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

func renderMatchHighlighted(text, query string, baseStyle, matchStyle lipgloss.Style) string {
	query = strings.TrimSpace(query)
	if query == "" || text == "" {
		return baseStyle.Render(text)
	}

	runes := []rune(text)
	needle := []rune(query)
	if len(needle) > len(runes) {
		return baseStyle.Render(text)
	}

	for i := 0; i <= len(runes)-len(needle); i++ {
		if strings.EqualFold(string(runes[i:i+len(needle)]), query) {
			return baseStyle.Render(string(runes[:i])) +
				matchStyle.Render(string(runes[i:i+len(needle)])) +
				baseStyle.Render(string(runes[i+len(needle):]))
		}
	}

	return baseStyle.Render(text)
}

func (m Model) renderPreview(width int) string {
	t := m.theme
	ic := m.icons

	if len(m.filtered) == 0 || m.cursor >= len(m.filtered) {
		return components.RenderState(components.StateOptions{
			Kind:    components.StateEmpty,
			Message: "Select a command to preview",
			Width:   width,
			Align:   lipgloss.Center,
		})
	}

	cmd := m.filtered[m.cursor]
	prompt := clean(cmd.Prompt)

	var b strings.Builder

	// Title with gradient
	titleText := ic.Send + " " + clean(cmd.Label)
	b.WriteString(styles.GradientText(titleText, string(t.Blue), string(t.Sapphire)) + "\n")
	b.WriteString(styles.GradientDivider(width, string(t.Surface2), string(t.Surface1)) + "\n\n")

	// Key + Category badges
	var badges []string
	if cmd.Key != "" {
		badges = append(badges, styles.TextBadge("key: "+cmd.Key, t.Surface0, t.Text))
	}
	if cmd.Category != "" {
		// Use the shared agent canonicalizer so newer agent categories keep their
		// dedicated badge styling instead of falling back to the generic category badge.
		canonicalCategory := agent.AgentType(cmd.Category).Canonical()
		var badge string
		if isKnownPaletteAgentCategory(canonicalCategory) {
			badge = components.RenderAgentBadge(string(canonicalCategory))
		} else {
			badge = lipgloss.NewStyle().
				Background(t.Mauve).
				Foreground(t.Base).
				Bold(true).
				Padding(0, 1).
				Render(clean(cmd.Category))
		}
		badges = append(badges, badge)
	}
	if len(badges) > 0 {
		b.WriteString(styles.BadgeGroup(badges...) + "\n")
	}

	// Target summary + prompt metadata (reduce misfires)
	if targets := m.renderTargetSummaryBadges(); targets != "" {
		labelStyle := lipgloss.NewStyle().Foreground(t.Subtext)
		b.WriteString(labelStyle.Render("Targets: ") + targets + "\n")
	}

	lineCount := 0
	if strings.TrimSpace(prompt) != "" {
		lineCount = strings.Count(prompt, "\n") + 1
	}
	charCount := utf8.RuneCountInString(prompt)
	meta := styles.BadgeGroup(
		styles.TextBadge(fmt.Sprintf("%d lines", lineCount), t.Surface0, t.Text),
		styles.TextBadge(fmt.Sprintf("%d chars", charCount), t.Surface0, t.Text),
	)
	b.WriteString(meta + "\n")

	if warn := m.renderSafetyNudges(prompt, lineCount, charCount); warn != "" {
		b.WriteString(warn + "\n")
	}

	b.WriteString("\n")

	// Prompt content with wrapping
	promptStyle := lipgloss.NewStyle().Foreground(t.Text)
	wrapped := wordwrap.String(prompt, width)

	// Add subtle line highlighting on the left
	lines := strings.Split(wrapped, "\n")
	for i, line := range lines {
		if i < len(lines)-1 || line != "" {
			b.WriteString(promptStyle.Render(line) + "\n")
		}
	}

	// What memory would add to this prompt, when shown (memory.go).
	if block := m.renderMemory(width); block != "" {
		b.WriteString("\n" + block)
	}

	return b.String()
}

func isKnownPaletteAgentCategory(category agent.AgentType) bool {
	switch category {
	case agent.AgentTypeClaudeCode,
		agent.AgentTypeCodex,
		agent.AgentTypeGemini,
		agent.AgentTypeOmp,
		agent.AgentTypeCursor,
		agent.AgentTypeWindsurf,
		agent.AgentTypeAider,
		agent.AgentTypeOllama,
		agent.AgentTypeUser:
		return true
	default:
		return false
	}
}

// kindLook is how the target phase shows a kind: ntm's icon, colour,
// label and description for the kinds ntm knows, a generic one for the
// others.
func (m Model) kindLook(k KindKey) (icon, label, desc string, color lipgloss.Color) {
	t := m.theme
	ic := m.icons
	switch k.NTMType {
	case "claude":
		return ic.Claude, "Claude (cc)", "Anthropic agents", t.Claude
	case "codex":
		return ic.Codex, "Codex (cod)", "OpenAI agents", t.Codex
	case "gemini":
		return ic.Gemini, "Gemini (gmi)", "Google agents (legacy)", t.Gemini
	case "antigravity":
		return ic.Gemini, "Antigravity (agy)", "Google agents", t.Lavender
	case "omp":
		return ic.Robot, "Oh My Pi (omp)", "omp agents", t.Sky
	}
	return ic.Robot, clean(k.Kind), clean(k.Kind) + " agents", t.Teal
}

func (m Model) renderTargetSummaryBadges() string {
	t := m.theme
	ic := m.icons

	labelWithIcon := func(icon, fallback, label string, count *int) string {
		prefix := strings.TrimSpace(icon)
		if prefix == "" {
			prefix = fallback
		}
		if prefix != "" {
			prefix = prefix + " "
		}
		if count == nil {
			return prefix + label
		}
		return fmt.Sprintf("%s%s %d", prefix, label, *count)
	}

	var all *int
	if m.paneCountsKnown {
		all = &m.paneCounts.totalAgents
	}

	badges := []string{
		styles.TextBadge(labelWithIcon(ic.All, "", "all", all), t.Green, t.Base),
	}
	// One badge per kind that has agents here, in digit order.
	if m.paneCountsKnown {
		for _, k := range m.kindKeys {
			n := m.paneCounts.byKind[k.Kind]
			if n == 0 {
				continue
			}
			icon, _, _, color := m.kindLook(k)
			badges = append(badges, styles.TextBadge(labelWithIcon(icon, "", clean(k.Kind), &n), color, t.Base))
		}
	}

	return styles.BadgeGroup(badges...)
}

func (m Model) samplePaneTitlesForTargetKey(key string, max int) []string {
	if !m.paneCountsKnown || max <= 0 {
		return nil
	}

	var src []string
	switch key {
	case KeyAll:
		src = m.paneCounts.allSamples
	default:
		src = m.paneCounts.kindSamples[m.kindForKey(key)]
	}

	if len(src) > max {
		return src[:max]
	}
	return src
}

func (m Model) renderSafetyNudges(prompt string, lineCount, charCount int) string {
	t := m.theme
	ic := m.icons
	lower := strings.ToLower(prompt)

	warnIcon := strings.TrimSpace(ic.Warning)
	if warnIcon == "" {
		warnIcon = "!"
	}

	type warn struct {
		label string
		bg    lipgloss.Color
		fg    lipgloss.Color
	}

	var warns []warn
	add := func(label string, bg, fg lipgloss.Color) {
		warns = append(warns, warn{label: label, bg: bg, fg: fg})
	}

	// Highest-signal warnings first.
	if strings.Contains(lower, "rm -rf") ||
		strings.Contains(lower, "git reset --hard") ||
		strings.Contains(lower, "git clean -fd") ||
		strings.Contains(lower, "delete all") ||
		strings.Contains(lower, "drop table") ||
		strings.Contains(lower, "truncate") {
		add(warnIcon+" destructive", t.Error, t.Base)
	}

	if strings.Contains(lower, "sudo ") || strings.HasPrefix(lower, "sudo") {
		add(warnIcon+" sudo", t.Warning, t.Base)
	}

	if strings.Contains(lower, "curl ") ||
		strings.Contains(lower, "wget ") ||
		strings.Contains(lower, "go get ") ||
		strings.Contains(lower, "npm install") ||
		strings.Contains(lower, "pip install") ||
		strings.Contains(lower, "brew install") {
		add(warnIcon+" network", t.Warning, t.Base)
	}

	if strings.Contains(lower, "git push") ||
		strings.Contains(lower, "git commit") ||
		strings.Contains(lower, "commit and push") ||
		strings.Contains(lower, "push.") ||
		strings.Contains(lower, "push ") {
		add(warnIcon+" git", t.Warning, t.Base)
	}

	if lineCount >= 40 || charCount >= 4000 {
		add(warnIcon+" long prompt", t.Surface1, t.Text)
	}

	if len(warns) == 0 {
		return ""
	}
	if len(warns) > 3 {
		warns = warns[:3]
	}

	var badges []string
	for _, w := range warns {
		badges = append(badges, styles.TextBadge(w.label, w.bg, w.fg))
	}
	return styles.BadgeGroup(badges...)
}

// renderHelpEntries renders a keymap-generated help bar in the palette's
// standard badge style. All phase help bars go through this single renderer so
// their content stays keymap-derived (see keymap.go).
func (m Model) renderHelpEntries(entries []helpEntry) string {
	t := m.theme

	keyStyle := lipgloss.NewStyle().
		Background(t.Surface0).
		Foreground(t.Text).
		Bold(true).
		Padding(0, 1)

	descStyle := lipgloss.NewStyle().
		Foreground(t.Overlay)

	var parts []string
	for _, e := range entries {
		parts = append(parts, keyStyle.Render(e.label)+" "+descStyle.Render(e.desc))
	}

	return strings.Join(parts, "  ")
}

func (m Model) renderHelpBar() string {
	canScroll := m.tier >= layout.TierSplit && m.listViewport.TotalLineCount() > m.listViewport.Height
	return m.renderHelpEntries(commandHelpEntries(m.tier, canScroll))
}

// targetBoxWidth is the target-phase dialogs' width for the layout mode.
func (m Model) targetBoxWidth() int {
	switch styles.GetLayoutMode(m.width) {
	case styles.LayoutUltraWide:
		return 80
	case styles.LayoutSpacious:
		return 70
	case styles.LayoutDefault:
		return 60
	}
	boxWidth := m.width - 10
	if boxWidth < 40 {
		boxWidth = 40
	}
	return boxWidth
}

func (m Model) viewTargetPhase() string {
	t := m.theme
	ic := m.icons

	var b strings.Builder

	// Responsive box dimensions based on layout mode
	boxWidth := m.targetBoxWidth()

	b.WriteString("\n")

	// ═══════════════════════════════════════════════════════════════
	// HEADER with animated gradient
	// ═══════════════════════════════════════════════════════════════
	titleText := ic.Target + "  Select Target"
	animatedTitle := styles.Shimmer(titleText, m.animTick, string(t.Blue), string(t.Mauve), string(t.Pink))
	b.WriteString("  " + animatedTitle + "\n")
	b.WriteString("  " + styles.GradientDivider(boxWidth, string(t.Blue), string(t.Mauve)) + "\n\n")

	// Selected command info
	b.WriteString(m.renderSendingLine())
	// nhm: what memory the send adds, switched with c and s (nhm-t63).
	b.WriteString(m.renderMemoryChoice())

	// ═══════════════════════════════════════════════════════════════
	// TARGET OPTIONS with visual styling
	// ═══════════════════════════════════════════════════════════════
	type targetRow struct {
		key     string
		icon    string
		label   string
		desc    string
		color   lipgloss.Color
		bgColor lipgloss.Color
		count   int
	}
	targets := []targetRow{
		{KeyAll, ic.All, "All Agents", "broadcast to all", t.Green, t.Surface0, m.paneCounts.totalAgents},
	}
	for _, k := range m.kindKeys {
		icon, label, desc, color := m.kindLook(k)
		targets = append(targets, targetRow{k.Key, icon, label, desc, color, t.Surface0, m.paneCounts.byKind[k.Kind]})
	}
	targets = append(targets, targetRow{KeySelect, ic.Target, "Select agents…", "pick specific agents", t.Mauve, t.Surface0,
		m.paneCounts.totalAgents})
	if m.handed.present() {
		// nhm: a handed selection is the one target; nothing widens it.
		ts, _ := m.handedTargets()
		targets = []targetRow{{KeyAll, ic.Target, "Picked", "the agents picked on " + m.handed.surface(), t.Mauve, t.Surface0, len(ts)}}
	}

	for _, target := range targets {
		// Count suffixes (best-effort)
		countSuffix := ""
		if m.paneCountsKnown || m.handed.present() {
			countSuffix = fmt.Sprintf(" (%d)", target.count)
		}

		labelText := target.label
		if target.key == KeyAll {
			star := strings.TrimSpace(ic.Star)
			if star == "" {
				star = "*"
			}
			labelText = star + " " + labelText
		}

		// Key badge
		keyBadge := lipgloss.NewStyle().
			Background(target.color).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render(target.key)

		// Icon with color
		iconStyled := lipgloss.NewStyle().
			Foreground(target.color).
			Bold(true).
			Render(target.icon)

		// Label
		labelStyle := lipgloss.NewStyle().
			Foreground(t.Text).
			Bold(true).
			Width(18)

		// Description
		descStyle := lipgloss.NewStyle().
			Foreground(t.Overlay).
			Italic(true)

		line := fmt.Sprintf("  %s  %s  %s %s",
			keyBadge,
			iconStyled,
			labelStyle.Render(labelText+countSuffix),
			descStyle.Render(target.desc))

		b.WriteString(line + "\n")

		// Representative pane samples (width-tier aware, avoid wrapping).
		maxSamples := 0
		switch {
		case m.tier >= layout.TierWide:
			if target.key == KeyAll {
				maxSamples = 3
			} else {
				maxSamples = 2
			}
		case m.tier >= layout.TierSplit:
			maxSamples = 1
		}

		samples := []string(nil)
		if target.key != KeySelect && !m.handed.present() {
			samples = m.samplePaneTitlesForTargetKey(target.key, maxSamples)
		}
		if len(samples) > 0 {
			sampleText := "e.g. " + strings.Join(samples, ", ")
			sampleIndent := "      "
			maxWidth := boxWidth - 2 - len(sampleIndent)
			if maxWidth < 10 {
				maxWidth = 10
			}
			sampleText = layout.TruncateWidthDefault(sampleText, maxWidth)
			sampleStyle := lipgloss.NewStyle().Foreground(t.Subtext).Italic(true)
			b.WriteString("  " + sampleIndent + sampleStyle.Render(sampleText) + "\n")
		}

		b.WriteString("\n")
	}

	// nhm: the handed agents, what the targets leave out, and why a key
	// did nothing.
	b.WriteString(m.renderHanded())
	b.WriteString(m.renderTargetNotes())

	// What memory would add to the draft, when shown (memory.go).
	if block := m.renderMemory(boxWidth); block != "" {
		b.WriteString(lipgloss.NewStyle().MarginLeft(2).Render(block) + "\n")
	}

	// Divider
	b.WriteString("  " + styles.GradientDivider(boxWidth, string(t.Surface2), string(t.Surface1)) + "\n\n")

	// Help bar
	b.WriteString("  " + m.renderTargetHelpBar() + "\n")

	// Center the compact target picker so it reads as a floating dialog rather
	// than a block pinned to the top-left of the popup (#204).
	return lipgloss.Place(m.width, m.height, lipgloss.Left, lipgloss.Center, b.String())
}

// renderSendingLine is the "Sending: <label> (edited)" line of the target
// and select-agents phases.
func (m Model) renderSendingLine() string {
	t := m.theme
	dimStyle := lipgloss.NewStyle().Foreground(t.Subtext)
	label := ""
	if m.selected != nil {
		label = clean(m.selected.Label)
	}
	cmdBadge := lipgloss.NewStyle().
		Background(t.Surface0).
		Foreground(t.Text).
		Padding(0, 1).
		Render(label)

	if m.draft.Present() {
		editedBadge := lipgloss.NewStyle().
			Foreground(t.Yellow).
			Italic(true).
			Render("(edited)")
		return "  " + dimStyle.Render("Sending:") + " " + cmdBadge + " " + editedBadge + "\n\n"
	}
	return "  " + dimStyle.Render("Sending:") + " " + cmdBadge + "\n\n"
}

func (m Model) renderTargetHelpBar() string {
	return m.renderHelpEntries(targetHelpEntries())
}

// viewSelectAgentsPhase renders the granular per-agent multi-select dialog (#205).
func (m Model) viewSelectAgentsPhase() string {
	t := m.theme
	ic := m.icons

	var b strings.Builder

	// Responsive box width, mirroring the target phase.
	boxWidth := m.targetBoxWidth()

	b.WriteString("\n")

	titleText := ic.Target + "  Select Agents"
	animatedTitle := styles.Shimmer(titleText, m.animTick, string(t.Blue), string(t.Mauve), string(t.Pink))
	b.WriteString("  " + animatedTitle + "\n")
	b.WriteString("  " + styles.GradientDivider(boxWidth, string(t.Blue), string(t.Mauve)) + "\n\n")

	// Selected command info (with edited marker).
	dimStyle := lipgloss.NewStyle().Foreground(t.Subtext)
	b.WriteString(m.renderSendingLine())

	if len(m.agentPanes) == 0 {
		b.WriteString("  " + dimStyle.Render("No bound agents in this workspace.") + "\n\n")
		b.WriteString(m.renderTargetNotes())
		b.WriteString("  " + styles.GradientDivider(boxWidth, string(t.Surface2), string(t.Surface1)) + "\n\n")
		b.WriteString("  " + m.renderSelectAgentsHelpBar() + "\n")
		return lipgloss.Place(m.width, m.height, lipgloss.Left, lipgloss.Center, b.String())
	}

	checkStyle := lipgloss.NewStyle().Foreground(t.Green).Bold(true)
	uncheckStyle := lipgloss.NewStyle().Foreground(t.Overlay)
	labelStyle := lipgloss.NewStyle().Foreground(t.Text)
	typeStyle := lipgloss.NewStyle().Foreground(t.Subtext).Italic(true)
	selectedCount := 0

	for i, p := range m.agentPanes {
		checked := m.agentChecked[p.ID]
		if checked {
			selectedCount++
		}

		// Cursor pointer.
		pointer := "  "
		if i == m.agentCursor {
			pointer = styles.Shimmer(ic.Pointer, m.animTick, string(t.Pink), string(t.Mauve)) + " "
		}

		box := uncheckStyle.Render("[ ]")
		if checked {
			box = checkStyle.Render("[x]")
		}

		title := clean(strings.TrimSpace(p.Title))
		if title == "" {
			title = fmt.Sprintf("pane %d", p.Index)
		}
		nameBudget := boxWidth - 20
		if nameBudget < 10 {
			nameBudget = 10
		}
		title = layout.TruncateWidthDefault(title, nameBudget)

		line := fmt.Sprintf("  %s%s  %s %s",
			pointer,
			box,
			labelStyle.Render(title),
			typeStyle.Render("("+clean(p.Type)+", "+clean(string(p.State))+")"))
		b.WriteString(line + "\n")
	}

	b.WriteString("\n")
	countStyle := lipgloss.NewStyle().Foreground(t.Subtext)
	b.WriteString("  " + countStyle.Render(fmt.Sprintf("%d of %d selected", selectedCount, len(m.agentPanes))) + "\n\n")
	b.WriteString(m.renderTargetNotes())

	b.WriteString("  " + styles.GradientDivider(boxWidth, string(t.Surface2), string(t.Surface1)) + "\n\n")
	b.WriteString("  " + m.renderSelectAgentsHelpBar() + "\n")

	return lipgloss.Place(m.width, m.height, lipgloss.Left, lipgloss.Center, b.String())
}

func (m Model) renderSelectAgentsHelpBar() string {
	return m.renderHelpEntries(selectAgentsHelpEntries())
}

// viewEditPhase renders the prompt-editing screen.
func (m Model) viewEditPhase() string {
	t := m.theme
	ic := m.icons

	var b strings.Builder

	boxWidth := m.width - 6
	if boxWidth < 40 {
		boxWidth = 40
	}

	b.WriteString("\n")

	titleText := ic.Save + "  Edit Prompt"
	animatedTitle := styles.Shimmer(titleText, m.animTick, string(t.Yellow), string(t.Peach), string(t.Maroon))
	b.WriteString("  " + animatedTitle + "\n")
	b.WriteString("  " + styles.GradientDivider(boxWidth, string(t.Yellow), string(t.Peach)) + "\n\n")

	dimStyle := lipgloss.NewStyle().Foreground(t.Subtext)
	label := ""
	if m.selected != nil {
		label = clean(m.selected.Label)
	}
	cmdBadge := lipgloss.NewStyle().
		Background(t.Surface0).
		Foreground(t.Text).
		Padding(0, 1).
		Render(label)
	b.WriteString("  " + dimStyle.Render("Editing:") + " " + cmdBadge + "\n\n")

	if m.editNotice != "" {
		noticeStyle := lipgloss.NewStyle().Foreground(t.Peach)
		b.WriteString("  " + noticeStyle.Render(ic.Warning+"  "+m.editNotice) + "\n\n")
	}

	b.WriteString("  " + m.editInput.View() + "\n\n")

	b.WriteString("  " + styles.GradientDivider(boxWidth, string(t.Surface2), string(t.Surface1)) + "\n\n")
	b.WriteString("  " + m.renderEditHelpBar() + "\n")

	return b.String()
}

func (m Model) renderEditHelpBar() string {
	return m.renderHelpEntries(editHelpEntries())
}

// Result returns the send result after the program exits: sent is true
// when a submission reached at least one agent.
func (m Model) Result() (sent bool, err error) {
	return m.sent || m.sub.sentAny(), m.err
}
