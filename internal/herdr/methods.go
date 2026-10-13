package herdr

// Request parameter types. A parameter that the schema requires has no
// omitempty; an optional one is left out when it has its zero value, so
// herdr applies its own default. schema_check_test.go checks both rules.

// WorkspaceCreateParams are workspace.create's parameters.
type WorkspaceCreateParams struct {
	Cwd   string            `json:"cwd,omitempty"`
	Label string            `json:"label,omitempty"`
	Env   map[string]string `json:"env,omitempty"`
	Focus *bool             `json:"focus,omitempty"`
}

// TabCreateParams are tab.create's parameters.
type TabCreateParams struct {
	WorkspaceID string            `json:"workspace_id,omitempty"`
	Cwd         string            `json:"cwd,omitempty"`
	Label       string            `json:"label,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Focus       *bool             `json:"focus,omitempty"`
}

// AgentReadParams are agent.read's parameters.
type AgentReadParams struct {
	Target    string     `json:"target"`
	Source    ReadSource `json:"source"`
	Format    ReadFormat `json:"format,omitempty"`
	Lines     *int       `json:"lines,omitempty"`
	StripANSI *bool      `json:"strip_ansi,omitempty"`
}

// AgentStartParams are agent.start's parameters. agent.start takes no
// environment: set it on the pane (tab.create, pane.split).
type AgentStartParams struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	PaneID    string   `json:"pane_id"`
	Args      []string `json:"args,omitempty"`
	TimeoutMS *int64   `json:"timeout_ms,omitempty"`
}

// PromptWait makes agent.prompt wait, atomically with the submission.
type PromptWait struct {
	Until     []AgentStatus `json:"until,omitempty"`
	TimeoutMS *int64        `json:"timeout_ms,omitempty"`
}

// AgentPromptParams are agent.prompt's parameters.
type AgentPromptParams struct {
	Target string      `json:"target"`
	Text   string      `json:"text"`
	Wait   *PromptWait `json:"wait,omitempty"`
}

// AgentWaitParams are agent.wait's parameters. agent.wait has no
// baseline: it returns as soon as the agent is in one of Until.
type AgentWaitParams struct {
	Target    string        `json:"target"`
	Until     []AgentStatus `json:"until,omitempty"`
	TimeoutMS *int64        `json:"timeout_ms,omitempty"`
}

// PaneSplitParams are pane.split's parameters. Direction is "right" or
// "down".
type PaneSplitParams struct {
	Direction    string            `json:"direction"`
	TargetPaneID string            `json:"target_pane_id,omitempty"`
	WorkspaceID  string            `json:"workspace_id,omitempty"`
	Cwd          string            `json:"cwd,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	Focus        *bool             `json:"focus,omitempty"`
	Ratio        *float64          `json:"ratio,omitempty"`
}

// PaneMoveParams are pane.move's parameters. herdr does not move the
// focus with the pane unless asked.
type PaneMoveParams struct {
	PaneID      string              `json:"pane_id"`
	Destination PaneMoveDestination `json:"destination"`
}

// PaneMoveDestination is herdr's PaneMoveDestination. nhm sends only the
// new_tab variant (NewTabIn).
type PaneMoveDestination struct {
	Type        string `json:"type"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Label       string `json:"label,omitempty"`
}

// paneSendKeysParams are pane.send_keys's parameters: keys in herdr's
// names ("esc", "ctrl+c", "a").
type paneSendKeysParams struct {
	PaneID string   `json:"pane_id"`
	Keys   []string `json:"keys"`
}

// pluginListParams are plugin.list's parameters; "" lists every plugin.
type pluginListParams struct {
	PluginID string `json:"plugin_id,omitempty"`
}

// NewTabIn is the destination "a new tab of workspace W", with herdr's
// default label.
func NewTabIn(workspaceID string) PaneMoveDestination {
	return PaneMoveDestination{Type: "new_tab", WorkspaceID: workspaceID}
}

// PaneReadParams are pane.read's parameters.
type PaneReadParams struct {
	PaneID    string     `json:"pane_id"`
	Source    ReadSource `json:"source"`
	Format    ReadFormat `json:"format,omitempty"`
	Lines     *int       `json:"lines,omitempty"`
	StripANSI *bool      `json:"strip_ansi,omitempty"`
}

// ReportAgentSessionParams are pane.report_agent_session's parameters:
// how a wrapper tells herdr the native session of the agent in a pane.
type ReportAgentSessionParams struct {
	PaneID             string   `json:"pane_id"`
	Source             string   `json:"source"`
	Agent              string   `json:"agent"`
	AgentSessionID     string   `json:"agent_session_id,omitempty"`
	AgentSessionPath   string   `json:"agent_session_path,omitempty"`
	ResumeArgv         []string `json:"resume_argv,omitempty"`
	SessionStartSource string   `json:"session_start_source,omitempty"`
	Seq                *int64   `json:"seq,omitempty"`
}

// ReportMetadataParams are pane.report_metadata's parameters. The tokens
// are for display only; herdr does not restore them.
type ReportMetadataParams struct {
	PaneID            string            `json:"pane_id"`
	Source            string            `json:"source"`
	Agent             string            `json:"agent,omitempty"`
	DisplayAgent      string            `json:"display_agent,omitempty"`
	Title             string            `json:"title,omitempty"`
	AppliesToSource   string            `json:"applies_to_source,omitempty"`
	StateLabels       map[string]string `json:"state_labels,omitempty"`
	Tokens            map[string]string `json:"tokens,omitempty"`
	ClearDisplayAgent bool              `json:"clear_display_agent,omitempty"`
	ClearStateLabels  bool              `json:"clear_state_labels,omitempty"`
	ClearTitle        bool              `json:"clear_title,omitempty"`
	Seq               *int64            `json:"seq,omitempty"`
	TTLMS             *int64            `json:"ttl_ms,omitempty"`
}

// OutputMatch is a pattern for pane.wait_for_output and the
// pane.output_matched subscription. Type is "substring" or "regex".
type OutputMatch struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// WaitForOutputParams are pane.wait_for_output's parameters.
type WaitForOutputParams struct {
	PaneID    string      `json:"pane_id"`
	Source    ReadSource  `json:"source"`
	Match     OutputMatch `json:"match"`
	Lines     *int        `json:"lines,omitempty"`
	StripANSI *bool       `json:"strip_ansi,omitempty"`
	TimeoutMS *int64      `json:"timeout_ms,omitempty"`
}

// EventMatch is events.wait's match. herdr 0.9.3 supports only the agent
// matches: Event "pane_agent_status_changed" with PaneID and AgentStatus,
// or "pane_agent_detected" with PaneID and an optional Agent. herdr
// refuses any other match with CodeUnsupportedEventWaitMatch.
type EventMatch struct {
	Event       EventKind   `json:"event"`
	PaneID      string      `json:"pane_id"`
	AgentStatus AgentStatus `json:"agent_status,omitempty"`
	Agent       string      `json:"agent,omitempty"`
}

// PluginPaneOpenParams are plugin.pane.open's parameters. A popup or
// overlay opens on the active pane: herdr refuses TargetPaneID for them
// (CodeInvalidParams). Width and Height are a number of cells or a string
// such as "80%".
type PluginPaneOpenParams struct {
	PluginID     string            `json:"plugin_id"`
	Entrypoint   string            `json:"entrypoint"`
	Placement    string            `json:"placement,omitempty"`
	Width        any               `json:"width,omitempty"`
	Height       any               `json:"height,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	Cwd          string            `json:"cwd,omitempty"`
	Direction    string            `json:"direction,omitempty"`
	Focus        *bool             `json:"focus,omitempty"`
	TargetPaneID string            `json:"target_pane_id,omitempty"`
	WorkspaceID  string            `json:"workspace_id,omitempty"`
}

type targetParams struct {
	Target string `json:"target"`
}

type paneParams struct {
	PaneID string `json:"pane_id"`
}

type workspaceCloseParams struct {
	WorkspaceID string `json:"workspace_id"`
}

// workspaceParams is herdr's WorkspaceTarget.
type workspaceParams struct {
	WorkspaceID string `json:"workspace_id"`
}

type sendKeysParams struct {
	Target string   `json:"target"`
	Keys   []string `json:"keys"`
}

// agentRenameParams sends Name as JSON null to clear the alias.
type agentRenameParams struct {
	Target string  `json:"target"`
	Name   *string `json:"name"`
}

// paneRenameParams sends Label as JSON null to clear the label.
type paneRenameParams struct {
	PaneID string  `json:"pane_id"`
	Label  *string `json:"label"`
}

// tabParams is herdr's TabTarget.
type tabParams struct {
	TabID string `json:"tab_id"`
}

type tabRenameParams struct {
	TabID string `json:"tab_id"`
	Label string `json:"label"`
}

type paneZoomParams struct {
	PaneID string `json:"pane_id,omitempty"`
	Mode   string `json:"mode,omitempty"`
}

type eventsWaitParams struct {
	MatchEvent EventMatch `json:"match_event"`
	TimeoutMS  *int64     `json:"timeout_ms,omitempty"`
}

type actionInvokeParams struct {
	PluginID string             `json:"plugin_id,omitempty"`
	ActionID string             `json:"action_id"`
	Context  *InvocationContext `json:"context,omitempty"`
}

type subscribeParams struct {
	Subscriptions []Subscription `json:"subscriptions"`
}
