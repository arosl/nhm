package herdr

import "context"

// The result wrappers: the top level of each result type that nhm reads.
// schema_check_test.go checks their keys against the schema.

type snapshotResult struct {
	Snapshot SessionSnapshot `json:"snapshot"`
}

type agentResult struct {
	Agent AgentInfo `json:"agent"`
}

type agentListResult struct {
	Agents []AgentInfo `json:"agents"`
}

type tabResult struct {
	Tab TabInfo `json:"tab"`
}

type paneResult struct {
	Pane PaneInfo `json:"pane"`
}

type readResult struct {
	Read PaneRead `json:"read"`
}

type zoomResult struct {
	Zoom ZoomResult `json:"zoom"`
}

type processInfoResult struct {
	ProcessInfo ProcessInfo `json:"process_info"`
}

type waitMatchedResult struct {
	Event Event `json:"event"`
}

type pluginPaneResult struct {
	PluginPane *PluginPane `json:"plugin_pane"`
}

// nilIfEmpty sends JSON null for "", which clears a name or label.
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Ping checks the server and returns its version and protocol.
func (c *Client) Ping(ctx context.Context) (Pong, error) {
	var r Pong
	err := c.call(ctx, "ping", nil, &r, 0)
	return r, err
}

// Snapshot reads the whole session: workspaces, tabs, panes and agents.
func (c *Client) Snapshot(ctx context.Context) (SessionSnapshot, error) {
	var r snapshotResult
	err := c.call(ctx, "session.snapshot", nil, &r, 0)
	return r.Snapshot, err
}

// WorkspaceCreate creates a workspace with one tab and one pane.
func (c *Client) WorkspaceCreate(ctx context.Context, p WorkspaceCreateParams) (Created, error) {
	var r Created
	err := c.call(ctx, "workspace.create", p, &r, 0)
	return r, err
}

// TabCreate creates a tab with one pane. Created.Workspace is nil.
func (c *Client) TabCreate(ctx context.Context, p TabCreateParams) (Created, error) {
	var r Created
	err := c.call(ctx, "tab.create", p, &r, 0)
	return r, err
}

// TabRename sets a tab's label.
func (c *Client) TabRename(ctx context.Context, tabID, label string) (TabInfo, error) {
	var r tabResult
	err := c.call(ctx, "tab.rename", tabRenameParams{TabID: tabID, Label: label}, &r, 0)
	return r.Tab, err
}

// AgentList lists the live agents.
func (c *Client) AgentList(ctx context.Context) ([]AgentInfo, error) {
	var r agentListResult
	err := c.call(ctx, "agent.list", nil, &r, 0)
	return r.Agents, err
}

// AgentGet reads one agent by name, pane id or terminal id.
func (c *Client) AgentGet(ctx context.Context, target string) (AgentInfo, error) {
	var r agentResult
	err := c.call(ctx, "agent.get", targetParams{Target: target}, &r, 0)
	return r.Agent, err
}

// AgentRead reads an agent's pane.
func (c *Client) AgentRead(ctx context.Context, p AgentReadParams) (PaneRead, error) {
	var r readResult
	err := c.call(ctx, "agent.read", p, &r, 0)
	return r.Read, err
}

// AgentSendKeys sends key names ("Escape", "C-c", "Enter") to an agent.
func (c *Client) AgentSendKeys(ctx context.Context, target string, keys ...string) error {
	return c.call(ctx, "agent.send_keys", sendKeysParams{Target: target, Keys: keys}, nil, 0)
}

// AgentRename sets an agent's alias; "" clears it. Names match
// [a-z][a-z0-9_-]{0,31} and are unique among live agents. herdr refuses
// a rename while the launch is pending (CodeAgentLaunchPending).
func (c *Client) AgentRename(ctx context.Context, target, name string) (AgentInfo, error) {
	var r agentResult
	err := c.call(ctx, "agent.rename", agentRenameParams{Target: target, Name: nilIfEmpty(name)}, &r, 0)
	return r.Agent, err
}

// AgentStart starts an agent in a pane that is at its shell prompt. On
// the socket it returns before the launch finishes, with LaunchPending
// set: wait for the agent (AgentWait) before you prompt or rename it.
func (c *Client) AgentStart(ctx context.Context, p AgentStartParams) (AgentStarted, error) {
	var r AgentStarted
	timeout := p.TimeoutMS
	if timeout == nil {
		ms := agentStartDefaultMS
		timeout = &ms
	}
	err := c.call(ctx, "agent.start", p, &r, waitBudget(timeout))
	return r, err
}

// agentStartDefaultMS is herdr's own readiness timeout for agent.start
// without timeout_ms.
const agentStartDefaultMS int64 = 30000

// AgentPrompt submits text to an agent. With p.Wait it also waits. A
// timeout or CodeAgentPromptStalled does not prove that nothing was
// submitted. herdr refuses a blocked agent and one whose launch is
// pending (CodeAgentNotReady).
func (c *Client) AgentPrompt(ctx context.Context, p AgentPromptParams) (AgentInfo, error) {
	var r agentResult
	b := budget(0)
	if p.Wait != nil {
		b = waitBudget(p.Wait.TimeoutMS)
	}
	err := c.call(ctx, "agent.prompt", p, &r, b)
	return r.Agent, err
}

// AgentWait waits until the agent is in one of p.Until.
func (c *Client) AgentWait(ctx context.Context, p AgentWaitParams) (AgentInfo, error) {
	var r agentResult
	err := c.call(ctx, "agent.wait", p, &r, waitBudget(p.TimeoutMS))
	return r.Agent, err
}

// PaneSplit splits a pane and returns the new one.
func (c *Client) PaneSplit(ctx context.Context, p PaneSplitParams) (PaneInfo, error) {
	var r paneResult
	err := c.call(ctx, "pane.split", p, &r, 0)
	return r.Pane, err
}

// PaneZoom zooms a pane; mode is "toggle", "on" or "off".
func (c *Client) PaneZoom(ctx context.Context, paneID, mode string) (ZoomResult, error) {
	var r zoomResult
	err := c.call(ctx, "pane.zoom", paneZoomParams{PaneID: paneID, Mode: mode}, &r, 0)
	return r.Zoom, err
}

// PaneProcessInfo reads a pane's shell and foreground processes.
func (c *Client) PaneProcessInfo(ctx context.Context, paneID string) (ProcessInfo, error) {
	var r processInfoResult
	err := c.call(ctx, "pane.process_info", paneParams{PaneID: paneID}, &r, 0)
	return r.ProcessInfo, err
}

// PaneRename sets a pane's label; "" clears it.
func (c *Client) PaneRename(ctx context.Context, paneID, label string) (PaneInfo, error) {
	var r paneResult
	err := c.call(ctx, "pane.rename", paneRenameParams{PaneID: paneID, Label: nilIfEmpty(label)}, &r, 0)
	return r.Pane, err
}

// PaneRead reads a pane.
func (c *Client) PaneRead(ctx context.Context, p PaneReadParams) (PaneRead, error) {
	var r readResult
	err := c.call(ctx, "pane.read", p, &r, 0)
	return r.Read, err
}

// PaneReportAgentSession reports the native session of a pane's agent.
func (c *Client) PaneReportAgentSession(ctx context.Context, p ReportAgentSessionParams) error {
	return c.call(ctx, "pane.report_agent_session", p, nil, 0)
}

// PaneReportMetadata sets display metadata on a pane.
func (c *Client) PaneReportMetadata(ctx context.Context, p ReportMetadataParams) error {
	return c.call(ctx, "pane.report_metadata", p, nil, 0)
}

// PaneClose closes a pane and ends its processes.
func (c *Client) PaneClose(ctx context.Context, paneID string) error {
	return c.call(ctx, "pane.close", paneParams{PaneID: paneID}, nil, 0)
}

// PopupClose closes the open popup; CodePopupNotOpen when there is none.
func (c *Client) PopupClose(ctx context.Context) error {
	return c.call(ctx, "popup.close", nil, nil, 0)
}

// EventsWait waits for one event that matches m (see EventMatch for the
// matches herdr supports). timeoutMS nil waits without limit.
func (c *Client) EventsWait(ctx context.Context, m EventMatch, timeoutMS *int64) (Event, error) {
	var r waitMatchedResult
	err := c.call(ctx, "events.wait", eventsWaitParams{MatchEvent: m, TimeoutMS: timeoutMS}, &r, waitBudget(timeoutMS))
	return r.Event, err
}

// PaneWaitForOutput waits until a pane's text matches p.Match.
func (c *Client) PaneWaitForOutput(ctx context.Context, p WaitForOutputParams) (OutputMatched, error) {
	var r OutputMatched
	err := c.call(ctx, "pane.wait_for_output", p, &r, waitBudget(p.TimeoutMS))
	return r, err
}

// PluginActionInvoke runs a plugin action with an explicit context, which
// the CLI cannot pass. ictx nil lets herdr use the focused pane.
func (c *Client) PluginActionInvoke(ctx context.Context, pluginID, actionID string, ictx *InvocationContext) (PluginActionInvoked, error) {
	var r PluginActionInvoked
	err := c.call(ctx, "plugin.action.invoke", actionInvokeParams{PluginID: pluginID, ActionID: actionID, Context: ictx}, &r, 0)
	return r, err
}

// PluginPaneOpen opens a plugin pane. It returns nil for a popup or an
// overlay, which herdr answers with "ok", and the pane otherwise.
func (c *Client) PluginPaneOpen(ctx context.Context, p PluginPaneOpenParams) (*PluginPane, error) {
	var r pluginPaneResult
	if err := c.call(ctx, "plugin.pane.open", p, &r, 0); err != nil {
		return nil, err
	}
	return r.PluginPane, nil
}
