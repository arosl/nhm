package herdr

// The types in this file mirror the objects of the pinned protocol-22
// schema that nhm reads. Their JSON names are checked against the schema
// in schema_check_test.go. A field the schema marks optional and whose
// absence means something (a sequence number, a flag) is a pointer.

// AgentStatus is herdr's agent state. herdr's own "unknown" is one of the
// five values the schema lists; a value outside those five, from a newer
// herdr, is kept as it came and Valid reports false.
type AgentStatus string

const (
	StatusIdle    AgentStatus = "idle"
	StatusWorking AgentStatus = "working"
	StatusBlocked AgentStatus = "blocked"
	StatusDone    AgentStatus = "done"
	StatusUnknown AgentStatus = "unknown"
)

// Valid reports whether s is one of the five states of protocol 22.
func (s AgentStatus) Valid() bool {
	switch s {
	case StatusIdle, StatusWorking, StatusBlocked, StatusDone, StatusUnknown:
		return true
	}
	return false
}

// ReadSource selects what pane.read and agent.read return.
type ReadSource string

const (
	ReadVisible         ReadSource = "visible"
	ReadRecent          ReadSource = "recent"
	ReadRecentUnwrapped ReadSource = "recent_unwrapped"
	ReadDetection       ReadSource = "detection"
)

// ReadFormat is "text" or "ansi".
type ReadFormat string

const (
	FormatText ReadFormat = "text"
	FormatANSI ReadFormat = "ansi"
)

// AgentSession is herdr's reference to an agent's native session, as the
// agent's integration (or nhm) reported it. Kind is "id" or "path".
type AgentSession struct {
	Source string `json:"source"`
	Agent  string `json:"agent"`
	Kind   string `json:"kind"`
	Value  string `json:"value"`
}

// AgentInfo is one agent: the occupant of a pane, with its alias.
type AgentInfo struct {
	PaneID      string `json:"pane_id"`
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	TerminalID  string `json:"terminal_id"`
	// Name is the agent alias; empty when the agent has none.
	Name string `json:"name,omitempty"`
	// Agent is the kind herdr detected ("claude").
	Agent        string        `json:"agent,omitempty"`
	DisplayAgent string        `json:"display_agent,omitempty"`
	AgentStatus  AgentStatus   `json:"agent_status"`
	AgentSession *AgentSession `json:"agent_session,omitempty"`
	Focused      bool          `json:"focused"`
	Revision     uint64        `json:"revision"`
	// StateChangeSeq counts state changes of the pane.
	StateChangeSeq *uint64 `json:"state_change_seq,omitempty"`
	// CompletionSeq is absent while the agent works and equals
	// StateChangeSeq at idle or done.
	CompletionSeq *uint64 `json:"completion_seq,omitempty"`
	// InteractiveReady is set once an agent.start launch has finished.
	// It is never set on a hand-started agent, so it is not turn
	// readiness.
	InteractiveReady *bool `json:"interactive_ready,omitempty"`
	// LaunchPending is true from agent.start until the launch finishes.
	// agent.prompt and agent.rename refuse the agent until then.
	LaunchPending          *bool             `json:"launch_pending,omitempty"`
	ScreenDetectionSkipped bool              `json:"screen_detection_skipped,omitempty"`
	Cwd                    string            `json:"cwd,omitempty"`
	ForegroundCwd          string            `json:"foreground_cwd,omitempty"`
	Title                  string            `json:"title,omitempty"`
	TerminalTitle          string            `json:"terminal_title,omitempty"`
	TerminalTitleStripped  string            `json:"terminal_title_stripped,omitempty"`
	StateLabels            map[string]string `json:"state_labels,omitempty"`
	Tokens                 map[string]string `json:"tokens,omitempty"`
}

// PaneInfo is one pane.
type PaneInfo struct {
	PaneID                string            `json:"pane_id"`
	TabID                 string            `json:"tab_id"`
	WorkspaceID           string            `json:"workspace_id"`
	TerminalID            string            `json:"terminal_id"`
	Label                 string            `json:"label,omitempty"`
	Agent                 string            `json:"agent,omitempty"`
	DisplayAgent          string            `json:"display_agent,omitempty"`
	AgentStatus           AgentStatus       `json:"agent_status"`
	AgentSession          *AgentSession     `json:"agent_session,omitempty"`
	Focused               bool              `json:"focused"`
	Revision              uint64            `json:"revision"`
	Cwd                   string            `json:"cwd,omitempty"`
	ForegroundCwd         string            `json:"foreground_cwd,omitempty"`
	RestoreError          string            `json:"restore_error,omitempty"`
	Title                 string            `json:"title,omitempty"`
	TerminalTitle         string            `json:"terminal_title,omitempty"`
	TerminalTitleStripped string            `json:"terminal_title_stripped,omitempty"`
	StateLabels           map[string]string `json:"state_labels,omitempty"`
	Tokens                map[string]string `json:"tokens,omitempty"`
}

// TabInfo is one tab.
type TabInfo struct {
	TabID       string      `json:"tab_id"`
	WorkspaceID string      `json:"workspace_id"`
	Number      uint        `json:"number"`
	Label       string      `json:"label"`
	Focused     bool        `json:"focused"`
	PaneCount   uint        `json:"pane_count"`
	AgentStatus AgentStatus `json:"agent_status"`
}

// WorkspaceInfo is one workspace.
type WorkspaceInfo struct {
	WorkspaceID string            `json:"workspace_id"`
	Number      uint              `json:"number"`
	Label       string            `json:"label"`
	Focused     bool              `json:"focused"`
	PaneCount   uint              `json:"pane_count"`
	TabCount    uint              `json:"tab_count"`
	ActiveTabID string            `json:"active_tab_id"`
	AgentStatus AgentStatus       `json:"agent_status"`
	Tokens      map[string]string `json:"tokens,omitempty"`
}

// SessionSnapshot is session.snapshot's topology and agents. Of the
// layouts, nhm reads each tab's zoom and focused pane.
type SessionSnapshot struct {
	Protocol           int             `json:"protocol"`
	Version            string          `json:"version"`
	Workspaces         []WorkspaceInfo `json:"workspaces"`
	Tabs               []TabInfo       `json:"tabs"`
	Panes              []PaneInfo      `json:"panes"`
	Agents             []AgentInfo     `json:"agents"`
	Layouts            []TabLayout     `json:"layouts"`
	FocusedWorkspaceID string          `json:"focused_workspace_id,omitempty"`
	FocusedTabID       string          `json:"focused_tab_id,omitempty"`
	FocusedPaneID      string          `json:"focused_pane_id,omitempty"`
}

// TabLayout is the part of a tab's layout (PaneLayoutSnapshot) that nhm
// reads: whether the tab is zoomed, its focused pane, and where each of
// its panes is.
type TabLayout struct {
	WorkspaceID   string       `json:"workspace_id"`
	TabID         string       `json:"tab_id"`
	Zoomed        bool         `json:"zoomed"`
	FocusedPaneID string       `json:"focused_pane_id"`
	Panes         []LayoutPane `json:"panes"`
}

// Pong is ping's answer.
type Pong struct {
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
}

// Created is the answer of workspace.create and tab.create. Workspace is
// nil for tab.create.
type Created struct {
	Workspace *WorkspaceInfo `json:"workspace,omitempty"`
	Tab       TabInfo        `json:"tab"`
	RootPane  PaneInfo       `json:"root_pane"`
}

// AgentStarted is agent.start's answer. On the socket, agent.start
// returns at once with LaunchPending set; wait for the agent (agent.wait)
// before you prompt or rename it.
type AgentStarted struct {
	Agent AgentInfo `json:"agent"`
	Argv  []string  `json:"argv"`
}

// PaneRead is the text of a pane, from pane.read or agent.read.
type PaneRead struct {
	PaneID      string     `json:"pane_id"`
	TabID       string     `json:"tab_id"`
	WorkspaceID string     `json:"workspace_id"`
	Source      ReadSource `json:"source"`
	Format      ReadFormat `json:"format"`
	Revision    uint64     `json:"revision"`
	Text        string     `json:"text"`
	Truncated   bool       `json:"truncated"`
}

// Process is one foreground process of a pane.
type Process struct {
	PID     uint32   `json:"pid"`
	Name    string   `json:"name"`
	Argv0   string   `json:"argv0,omitempty"`
	Argv    []string `json:"argv,omitempty"`
	Cmdline string   `json:"cmdline,omitempty"`
	Cwd     string   `json:"cwd,omitempty"`
}

// ProcessInfo is pane.process_info's answer. It has no start times; read
// them from /proc.
type ProcessInfo struct {
	PaneID                   string    `json:"pane_id"`
	ShellPID                 *int      `json:"shell_pid,omitempty"`
	ForegroundProcessGroupID *int      `json:"foreground_process_group_id,omitempty"`
	ForegroundProcesses      []Process `json:"foreground_processes,omitempty"`
	TTY                      string    `json:"tty,omitempty"`
}

// ZoomResult is pane.zoom's answer, with the workspace and tab of its
// layout.
type ZoomResult struct {
	PaneID        string     `json:"pane_id"`
	Zoomed        bool       `json:"zoomed"`
	Changed       bool       `json:"changed"`
	ZoomChanged   bool       `json:"zoom_changed"`
	FocusChanged  bool       `json:"focus_changed"`
	FocusedPaneID string     `json:"focused_pane_id"`
	Layout        ZoomLayout `json:"layout"`
	// Reason says why nothing changed: ZoomSinglePane, already_zoomed or
	// already_unzoomed; "" when the zoom changed.
	Reason string `json:"reason,omitempty"`
}

// ZoomSinglePane is pane.zoom's reason when the pane is alone in its tab:
// herdr zooms nothing.
const ZoomSinglePane = "single_pane"

// ZoomLayout is the part of pane.zoom's layout that nhm reads: the
// zoomed pane's workspace and tab, and whether the tab is zoomed.
type ZoomLayout struct {
	WorkspaceID string `json:"workspace_id"`
	TabID       string `json:"tab_id"`
	Zoomed      bool   `json:"zoomed"`
}

// PluginInfo is one installed plugin, as plugin.list gives it: the fields
// nhm reads.
type PluginInfo struct {
	PluginID        string       `json:"plugin_id"`
	Name            string       `json:"name"`
	Version         string       `json:"version"`
	Enabled         bool         `json:"enabled"`
	MinHerdrVersion string       `json:"min_herdr_version"`
	PluginRoot      string       `json:"plugin_root"`
	Panes           []PluginSpec `json:"panes"`
}

// PluginSpec is one pane a plugin's manifest declares (herdr's
// PluginManifestPane).
type PluginSpec struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Placement string   `json:"placement"`
	Command   []string `json:"command"`
}

// PaneMoveResult is pane.move's answer, without the layouts: the pane
// where it is now (a move to another workspace gives it a new id), and
// where it was.
type PaneMoveResult struct {
	Changed             bool     `json:"changed"`
	Pane                PaneInfo `json:"pane"`
	PreviousPaneID      string   `json:"previous_pane_id"`
	PreviousTabID       string   `json:"previous_tab_id"`
	PreviousWorkspaceID string   `json:"previous_workspace_id"`
	CreatedTab          *TabInfo `json:"created_tab"`
	// herdr closes the tab, and the workspace, that the move left empty.
	ClosedTabID       string `json:"closed_tab_id"`
	ClosedWorkspaceID string `json:"closed_workspace_id"`
}

// PaneSwapResult is pane.swap's answer: whether herdr swapped the two
// panes, why not when it did not ("cross_tab", "same_pane", "not_found"),
// and the layout of the source pane's tab after it.
type PaneSwapResult struct {
	Changed      bool       `json:"changed"`
	Reason       string     `json:"reason"`
	SourcePaneID string     `json:"source_pane_id"`
	TargetPaneID string     `json:"target_pane_id"`
	Layout       PaneLayout `json:"layout"`
}

// OutputMatched is pane.wait_for_output's answer.
type OutputMatched struct {
	PaneID      string   `json:"pane_id"`
	Revision    uint64   `json:"revision"`
	MatchedLine string   `json:"matched_line,omitempty"`
	Read        PaneRead `json:"read"`
}

// InvocationContext is a plugin invocation's context: what a plugin
// command finds in HERDR_PLUGIN_CONTEXT_JSON, and what nhm passes to
// plugin.action.invoke. For an event hook, FocusedPaneID is the event's
// pane, not the focused one.
type InvocationContext struct {
	WorkspaceID       string      `json:"workspace_id,omitempty"`
	WorkspaceLabel    string      `json:"workspace_label,omitempty"`
	WorkspaceCwd      string      `json:"workspace_cwd,omitempty"`
	TabID             string      `json:"tab_id,omitempty"`
	TabLabel          string      `json:"tab_label,omitempty"`
	FocusedPaneID     string      `json:"focused_pane_id,omitempty"`
	FocusedPaneCwd    string      `json:"focused_pane_cwd,omitempty"`
	FocusedPaneAgent  string      `json:"focused_pane_agent,omitempty"`
	FocusedPaneStatus AgentStatus `json:"focused_pane_status,omitempty"`
	InvocationSource  string      `json:"invocation_source,omitempty"`
	CorrelationID     string      `json:"correlation_id,omitempty"`
	SelectedText      string      `json:"selected_text,omitempty"`
	ClickedURL        string      `json:"clicked_url,omitempty"`
	LinkHandlerID     string      `json:"link_handler_id,omitempty"`
}

// PluginAction is one action of a linked plugin.
type PluginAction struct {
	PluginID    string   `json:"plugin_id"`
	ActionID    string   `json:"action_id"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Command     []string `json:"command"`
	Contexts    []string `json:"contexts,omitempty"`
}

// PluginCommandLog is herdr's record of one plugin command. herdr runs at
// most 32 plugin commands at once and refuses the rest: Error then says
// so and the command did not run.
type PluginCommandLog struct {
	LogID          string   `json:"log_id"`
	PluginID       string   `json:"plugin_id"`
	ActionID       string   `json:"action_id,omitempty"`
	Event          string   `json:"event,omitempty"`
	Command        []string `json:"command"`
	Status         string   `json:"status"`
	Error          string   `json:"error,omitempty"`
	ExitCode       *int     `json:"exit_code,omitempty"`
	StartedUnixMS  int64    `json:"started_unix_ms"`
	FinishedUnixMS *int64   `json:"finished_unix_ms,omitempty"`
	Stdout         string   `json:"stdout,omitempty"`
	Stderr         string   `json:"stderr,omitempty"`
}

// PluginActionInvoked is plugin.action.invoke's answer.
type PluginActionInvoked struct {
	Action  PluginAction      `json:"action"`
	Context InvocationContext `json:"context"`
	Log     PluginCommandLog  `json:"log"`
}

// PluginPane is a plugin pane that plugin.pane.open opened.
type PluginPane struct {
	PluginID   string   `json:"plugin_id"`
	Entrypoint string   `json:"entrypoint"`
	Pane       PaneInfo `json:"pane"`
}
