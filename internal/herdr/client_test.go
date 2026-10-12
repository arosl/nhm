package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

// The fixtures follow what herdr 0.9.3 answered on a scratch server (ACFS
// bead acfs-9hkh.9), with neutral values.

func agentFixture(status string, extra map[string]any) map[string]any {
	a := map[string]any{
		"pane_id": "w1:p1", "tab_id": "w1:t1", "workspace_id": "w1", "terminal_id": "term_1",
		"name": "icyknoll", "agent": "claude", "agent_status": status, "focused": false,
		"revision": 1, "state_change_seq": 3, "cwd": "/work",
	}
	for k, v := range extra {
		a[k] = v
	}
	return a
}

func paneFixture(id string) map[string]any {
	return map[string]any{
		"pane_id": id, "tab_id": "w1:t1", "workspace_id": "w1", "terminal_id": "term_2",
		"agent_status": "unknown", "focused": false, "revision": 0, "cwd": "/work",
	}
}

func readFixture() map[string]any {
	return map[string]any{
		"pane_id": "w1:p1", "tab_id": "w1:t1", "workspace_id": "w1", "source": "recent",
		"format": "text", "revision": 4, "text": "Done.\n", "truncated": false,
	}
}

func newStub(t *testing.T) (*herdrtest.Server, *Client) {
	t.Helper()
	s := herdrtest.New(t)
	return s, New(s.SocketPath)
}

// answer registers a handler that records the params and returns result.
func answer(s *herdrtest.Server, method string, result map[string]any) *json.RawMessage {
	var got json.RawMessage
	s.Handle(method, func(r herdrtest.Request) (any, *herdrtest.Error) {
		got = r.Params
		return result, nil
	})
	return &got
}

func assertJSON(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("params %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want %s: %v", want, err)
	}
	gs, _ := json.Marshal(g)
	ws, _ := json.Marshal(w)
	if string(gs) != string(ws) {
		t.Fatalf("params = %s, want %s", gs, ws)
	}
}

func i64(v int64) *int64 { return &v }

func TestAgentStartIsLaunchPending(t *testing.T) {
	s, c := newStub(t)
	params := answer(s, "agent.start", map[string]any{
		"type": "agent_started", "argv": []string{"claude"},
		"agent": agentFixture("unknown", map[string]any{"launch_pending": true, "state_change_seq": 0}),
	})
	r, err := c.AgentStart(context.Background(), AgentStartParams{Name: "icyknoll", Kind: "claude", PaneID: "w1:p1", TimeoutMS: i64(20000)})
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, *params, `{"name":"icyknoll","kind":"claude","pane_id":"w1:p1","timeout_ms":20000}`)
	if r.Agent.LaunchPending == nil || !*r.Agent.LaunchPending || r.Agent.InteractiveReady != nil {
		t.Fatalf("launch flags = %v %v", r.Agent.LaunchPending, r.Agent.InteractiveReady)
	}
	if r.Agent.CompletionSeq != nil || r.Agent.StateChangeSeq == nil || *r.Agent.StateChangeSeq != 0 {
		t.Fatalf("seqs = %v %v", r.Agent.CompletionSeq, r.Agent.StateChangeSeq)
	}
	if len(r.Argv) != 1 || r.Argv[0] != "claude" {
		t.Fatalf("argv = %v", r.Argv)
	}
}

func TestAgentGetKeepsUnknownStatus(t *testing.T) {
	s, c := newStub(t)
	answer(s, "agent.get", map[string]any{"type": "agent_info", "agent": agentFixture("thinking", map[string]any{"completion_seq": nil})})
	a, err := c.AgentGet(context.Background(), "icyknoll")
	if err != nil {
		t.Fatal(err)
	}
	if a.AgentStatus != "thinking" || a.AgentStatus.Valid() {
		t.Fatalf("status = %q valid %v", a.AgentStatus, a.AgentStatus.Valid())
	}
	if a.CompletionSeq != nil {
		t.Fatalf("null completion_seq decoded as %v", *a.CompletionSeq)
	}
}

func TestPromptWaitsAtomically(t *testing.T) {
	s, c := newStub(t)
	params := answer(s, "agent.prompt", map[string]any{"type": "agent_prompted",
		"agent": agentFixture("done", map[string]any{"completion_seq": 5, "state_change_seq": 5})})
	a, err := c.AgentPrompt(context.Background(), AgentPromptParams{Target: "icyknoll", Text: "hello",
		Wait: &PromptWait{Until: []AgentStatus{StatusIdle, StatusDone}, TimeoutMS: i64(20000)}})
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, *params, `{"target":"icyknoll","text":"hello","wait":{"until":["idle","done"],"timeout_ms":20000}}`)
	if a.AgentStatus != StatusDone || a.CompletionSeq == nil || *a.CompletionSeq != 5 {
		t.Fatalf("agent = %+v", a)
	}
}

func TestRenameWithEmptyNameClears(t *testing.T) {
	s, c := newStub(t)
	agentParams := answer(s, "agent.rename", map[string]any{"type": "agent_info", "agent": agentFixture("idle", nil)})
	paneParams := answer(s, "pane.rename", map[string]any{"type": "pane_info", "pane": paneFixture("w1:p2")})
	if _, err := c.AgentRename(context.Background(), "icyknoll", ""); err != nil {
		t.Fatal(err)
	}
	assertJSON(t, *agentParams, `{"target":"icyknoll","name":null}`)
	if _, err := c.PaneRename(context.Background(), "w1:p2", "side"); err != nil {
		t.Fatal(err)
	}
	assertJSON(t, *paneParams, `{"pane_id":"w1:p2","label":"side"}`)
}

func TestPluginPaneOpen(t *testing.T) {
	s, c := newStub(t)
	params := answer(s, "plugin.pane.open", map[string]any{"type": "ok"})
	p, err := c.PluginPaneOpen(context.Background(), PluginPaneOpenParams{PluginID: "nhm", Entrypoint: "palette",
		Placement: "popup", Width: "80%", Height: 30, Env: map[string]string{"NHM_CONTEXT": "{}"}})
	if err != nil || p != nil {
		t.Fatalf("popup: pane %v, err %v", p, err)
	}
	assertJSON(t, *params, `{"plugin_id":"nhm","entrypoint":"palette","placement":"popup","width":"80%","height":30,"env":{"NHM_CONTEXT":"{}"}}`)
	answer(s, "plugin.pane.open", map[string]any{"type": "plugin_pane_opened",
		"plugin_pane": map[string]any{"plugin_id": "nhm", "entrypoint": "board", "pane": paneFixture("w1:p9")}})
	p, err = c.PluginPaneOpen(context.Background(), PluginPaneOpenParams{PluginID: "nhm", Entrypoint: "board", Placement: "tab"})
	if err != nil || p == nil || p.Pane.PaneID != "w1:p9" {
		t.Fatalf("tab: pane %+v, err %v", p, err)
	}
}

func TestEventsWaitDecodesTheEvent(t *testing.T) {
	s, c := newStub(t)
	params := answer(s, "events.wait", map[string]any{"type": "wait_matched", "event": map[string]any{
		"event": "pane_agent_status_changed",
		"data":  map[string]any{"type": "pane_agent_status_changed", "pane_id": "w1:p1", "workspace_id": "w1", "agent_status": "working", "agent": "claude"},
	}})
	ev, err := c.EventsWait(context.Background(), EventMatch{Event: EventPaneAgentStatusChanged, PaneID: "w1:p1", AgentStatus: StatusWorking}, i64(10000))
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, *params, `{"match_event":{"event":"pane_agent_status_changed","pane_id":"w1:p1","agent_status":"working"},"timeout_ms":10000}`)
	d, err := ev.AgentStatusChanged()
	if err != nil || d.AgentStatus != StatusWorking || d.PaneID != "w1:p1" {
		t.Fatalf("event = %+v, %v", d, err)
	}
}

func TestPluginActionInvokePassesContext(t *testing.T) {
	s, c := newStub(t)
	params := answer(s, "plugin.action.invoke", map[string]any{"type": "plugin_action_invoked",
		"action":  map[string]any{"plugin_id": "nhm", "action_id": "palette", "title": "Palette", "command": []string{"nhm", "ui", "palette"}},
		"context": map[string]any{"workspace_id": "w1", "focused_pane_id": "w1:p1", "invocation_source": "nhm"},
		"log":     map[string]any{"log_id": "plugin-log-1", "plugin_id": "nhm", "command": []string{"nhm", "ui", "palette"}, "status": "running", "started_unix_ms": 1},
	})
	r, err := c.PluginActionInvoke(context.Background(), "nhm", "palette", &InvocationContext{WorkspaceID: "w1", FocusedPaneID: "w1:p1", InvocationSource: "nhm"})
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, *params, `{"plugin_id":"nhm","action_id":"palette","context":{"workspace_id":"w1","focused_pane_id":"w1:p1","invocation_source":"nhm"}}`)
	if r.Log.Status != "running" || r.Context.FocusedPaneID != "w1:p1" || r.Log.FinishedUnixMS != nil {
		t.Fatalf("invoked = %+v", r)
	}
}

// Every method once, with the result type herdr gave: the stub checks
// each request against the schema's required parameters.
func TestEveryMethodRoundTrips(t *testing.T) {
	s, c := newStub(t)
	results := map[string]map[string]any{
		"ping":             {"type": "pong", "version": "0.9.3", "protocol": 22},
		"session.snapshot": {"type": "session_snapshot", "snapshot": map[string]any{"protocol": 22, "version": "0.9.3", "workspaces": []any{}, "tabs": []any{}, "panes": []any{paneFixture("w1:p1")}, "agents": []any{agentFixture("idle", nil)}, "layouts": []any{}}},
		"workspace.create": {"type": "workspace_created", "workspace": map[string]any{"workspace_id": "w2", "number": 2, "label": "x", "focused": true, "pane_count": 1, "tab_count": 1, "active_tab_id": "w2:t1", "agent_status": "unknown"}, "tab": map[string]any{"tab_id": "w2:t1", "workspace_id": "w2", "number": 1, "label": "1", "focused": true, "pane_count": 1, "agent_status": "unknown"}, "root_pane": paneFixture("w2:p1")},
		"tab.create":       {"type": "tab_created", "tab": map[string]any{"tab_id": "w1:t2", "workspace_id": "w1", "number": 2, "label": "two", "focused": false, "pane_count": 1, "agent_status": "unknown"}, "root_pane": paneFixture("w1:p2")},
		"tab.rename":       {"type": "tab_info", "tab": map[string]any{"tab_id": "w1:t2", "workspace_id": "w1", "number": 2, "label": "renamed", "focused": false, "pane_count": 1, "agent_status": "unknown"}},
		"agent.list":       {"type": "agent_list", "agents": []any{agentFixture("idle", nil)}},
		"agent.read":       {"type": "pane_read", "read": readFixture()},
		"agent.send_keys":  {"type": "ok"},
		"agent.wait":       {"type": "agent_info", "agent": agentFixture("idle", nil)},
		"pane.split":       {"type": "pane_info", "pane": paneFixture("w1:p3")},
		"pane.zoom":        {"type": "pane_zoom", "zoom": map[string]any{"pane_id": "w1:p1", "zoomed": true, "changed": true, "zoom_changed": true, "focus_changed": false, "focused_pane_id": "w1:p1", "layout": map[string]any{}}},
		"pane.process_info": {"type": "pane_process_info", "process_info": map[string]any{"pane_id": "w1:p1", "shell_pid": 100, "foreground_process_group_id": 101,
			"foreground_processes": []any{map[string]any{"pid": 101, "name": "claude", "argv": []string{"claude"}, "cmdline": "claude", "cwd": "/work"}}}},
		"pane.read":                 {"type": "pane_read", "read": readFixture()},
		"pane.report_agent_session": {"type": "ok"},
		"pane.report_metadata":      {"type": "ok"},
		"pane.close":                {"type": "ok"},
		"popup.close":               {"type": "ok"},
		"pane.wait_for_output":      {"type": "output_matched", "pane_id": "w1:p1", "revision": 4, "matched_line": "Done.", "read": readFixture()},
	}
	for m, r := range results {
		answer(s, m, r)
	}
	ctx := context.Background()
	calls := map[string]func() error{
		"ping": func() error {
			p, err := c.Ping(ctx)
			if err == nil && p.Protocol != Protocol {
				return errors.New("wrong protocol")
			}
			return err
		},
		"session.snapshot": func() error {
			sn, err := c.Snapshot(ctx)
			if err == nil && (len(sn.Panes) != 1 || len(sn.Agents) != 1) {
				return errors.New("snapshot lost its panes or agents")
			}
			return err
		},
		"workspace.create": func() error {
			r, err := c.WorkspaceCreate(ctx, WorkspaceCreateParams{Cwd: "/work", Label: "x"})
			if err == nil && (r.Workspace == nil || r.RootPane.PaneID != "w2:p1") {
				return errors.New("workspace not decoded")
			}
			return err
		},
		"tab.create": func() error {
			r, err := c.TabCreate(ctx, TabCreateParams{WorkspaceID: "w1", Label: "two"})
			if err == nil && (r.Workspace != nil || r.Tab.TabID != "w1:t2") {
				return errors.New("tab not decoded")
			}
			return err
		},
		"tab.rename": func() error { _, err := c.TabRename(ctx, "w1:t2", "renamed"); return err },
		"agent.list": func() error { _, err := c.AgentList(ctx); return err },
		"agent.read": func() error {
			r, err := c.AgentRead(ctx, AgentReadParams{Target: "icyknoll", Source: ReadRecent})
			if err == nil && r.Text != "Done.\n" {
				return errors.New("read text lost")
			}
			return err
		},
		"agent.send_keys": func() error { return c.AgentSendKeys(ctx, "icyknoll", "Escape") },
		"agent.wait": func() error {
			_, err := c.AgentWait(ctx, AgentWaitParams{Target: "icyknoll", Until: []AgentStatus{StatusIdle}})
			return err
		},
		"pane.split": func() error {
			_, err := c.PaneSplit(ctx, PaneSplitParams{Direction: "right", TargetPaneID: "w1:p1"})
			return err
		},
		"pane.zoom": func() error {
			z, err := c.PaneZoom(ctx, "w1:p1", "on")
			if err == nil && !z.Zoomed {
				return errors.New("zoom not decoded")
			}
			return err
		},
		"pane.process_info": func() error {
			p, err := c.PaneProcessInfo(ctx, "w1:p1")
			if err == nil && (p.ShellPID == nil || *p.ShellPID != 100 || len(p.ForegroundProcesses) != 1) {
				return errors.New("process info not decoded")
			}
			return err
		},
		"pane.read": func() error {
			_, err := c.PaneRead(ctx, PaneReadParams{PaneID: "w1:p1", Source: ReadVisible})
			return err
		},
		"pane.report_agent_session": func() error {
			return c.PaneReportAgentSession(ctx, ReportAgentSessionParams{PaneID: "w1:p1", Source: "nhm:codex", Agent: "codex",
				AgentSessionID: "thread-1", ResumeArgv: []string{"codex", "resume", "thread-1"}})
		},
		"pane.report_metadata": func() error {
			return c.PaneReportMetadata(ctx, ReportMetadataParams{PaneID: "w1:p1", Source: "nhm", Title: "IcyKnoll claude"})
		},
		"pane.close":  func() error { return c.PaneClose(ctx, "w1:p3") },
		"popup.close": func() error { return c.PopupClose(ctx) },
		"pane.wait_for_output": func() error {
			r, err := c.PaneWaitForOutput(ctx, WaitForOutputParams{PaneID: "w1:p1", Source: ReadRecent, Match: OutputMatch{Type: "substring", Value: "Done."}})
			if err == nil && r.MatchedLine != "Done." {
				return errors.New("match not decoded")
			}
			return err
		},
	}
	for m, call := range calls {
		if err := call(); err != nil {
			t.Errorf("%s: %v", m, err)
		}
	}
	for m := range results {
		if _, ok := calls[m]; !ok {
			t.Errorf("%s has a fixture and no call", m)
		}
	}
}

func TestHerdrErrorsKeepTheirCode(t *testing.T) {
	s, c := newStub(t)
	s.Handle("agent.get", func(r herdrtest.Request) (any, *herdrtest.Error) {
		return nil, &herdrtest.Error{Code: "agent_not_found", Message: "agent target nosuch not found"}
	})
	s.Handle("agent.rename", func(r herdrtest.Request) (any, *herdrtest.Error) {
		return nil, &herdrtest.Error{Code: "a_code_from_the_future", Message: "new"}
	})
	_, err := c.AgentGet(context.Background(), "nosuch")
	var herr *Error
	if !errors.As(err, &herr) || herr.Code != CodeAgentNotFound || herr.Method != "agent.get" {
		t.Fatalf("err = %#v", err)
	}
	if !strings.Contains(err.Error(), "agent.get: agent_not_found") {
		t.Fatalf("message = %q", err)
	}
	_, err = c.AgentRename(context.Background(), "x", "y")
	if ErrorCode(err) != "a_code_from_the_future" {
		t.Fatalf("unknown code lost: %v", err)
	}
	if ErrorCode(errors.New("plain")) != "" {
		t.Fatal("ErrorCode of a plain error is not empty")
	}
}

func TestUnexpectedResultTypeIsExplicit(t *testing.T) {
	s, c := newStub(t)
	answer(s, "agent.get", map[string]any{"type": "ok"})
	_, err := c.AgentGet(context.Background(), "icyknoll")
	var u *UnexpectedResultError
	if !errors.As(err, &u) || u.Method != "agent.get" || u.Type != "ok" || !strings.Contains(string(u.Raw), `"ok"`) {
		t.Fatalf("err = %#v", err)
	}
}

func TestNoServerIsATransportError(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "absent.sock"))
	_, err := c.Ping(context.Background())
	var te *TransportError
	if !errors.As(err, &te) || te.Method != "ping" {
		t.Fatalf("err = %#v", err)
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "")
	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv accepted an empty HERDR_SOCKET_PATH")
	}
	t.Setenv("HERDR_SOCKET_PATH", "/run/herdr.sock")
	c, err := FromEnv()
	if err != nil || c.SocketPath != "/run/herdr.sock" {
		t.Fatalf("FromEnv = %v, %v", c, err)
	}
}

// blockingStub answers method only when the test ends.
func blockingStub(t *testing.T, method string) *Client {
	t.Helper()
	s, c := newStub(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	s.Handle(method, func(r herdrtest.Request) (any, *herdrtest.Error) {
		<-release
		return nil, &herdrtest.Error{Code: "timeout", Message: "released"}
	})
	return c
}

func TestCancelEndsAWait(t *testing.T) {
	c := blockingStub(t, "agent.wait")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	_, err := c.AgentWait(ctx, AgentWaitParams{Target: "icyknoll", Until: []AgentStatus{StatusIdle}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("cancel took %v", time.Since(start))
	}
}

func TestClientTimeoutBoundsAPlainCall(t *testing.T) {
	c := blockingStub(t, "agent.get")
	c.Timeout = 50 * time.Millisecond
	_, err := c.AgentGet(context.Background(), "icyknoll")
	var te *TransportError
	if !errors.As(err, &te) || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("err = %#v", err)
	}
}

func TestWaitBudget(t *testing.T) {
	if waitBudget(nil) != noBudget {
		t.Fatal("a wait without timeout_ms must have no client limit")
	}
	if got := time.Duration(waitBudget(i64(1500))); got != 1500*time.Millisecond+waitMargin {
		t.Fatalf("budget = %v", got)
	}
	c := New("unused")
	if _, ok := c.deadline(context.Background(), noBudget); ok {
		t.Fatal("noBudget set a deadline")
	}
	d, ok := c.deadline(context.Background(), 0)
	if !ok || time.Until(d) > DefaultTimeout || time.Until(d) < DefaultTimeout-time.Second {
		t.Fatalf("default deadline = %v", time.Until(d))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if d, ok := c.deadline(ctx, noBudget); !ok || time.Until(d) > time.Second {
		t.Fatal("the context's deadline must win")
	}
}

// rawServer serves one connection with fixed lines, after it reads the
// request. It returns the socket path.
func rawServer(t *testing.T, lines ...string) string {
	t.Helper()
	// Under /tmp, not TMPDIR: a long TMPDIR passes the socket path limit.
	dir, err := os.MkdirTemp("/tmp", "herdrraw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { _ = ln.Close(); <-done })
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := bufio.NewReader(conn).ReadBytes('\n'); err != nil {
			return
		}
		for _, l := range lines {
			if _, err := conn.Write([]byte(l + "\n")); err != nil {
				return
			}
		}
	}()
	return path
}

func TestReplyForAnotherRequestIsRefused(t *testing.T) {
	c := New(rawServer(t, `{"id":"someone-else","result":{"type":"pong","version":"0.9.3","protocol":22}}`))
	_, err := c.Ping(context.Background())
	var te *TransportError
	if !errors.As(err, &te) || !strings.Contains(err.Error(), "reply id") {
		t.Fatalf("err = %#v", err)
	}
}

func TestClosedWithoutReplyIsATransportError(t *testing.T) {
	c := New(rawServer(t))
	_, err := c.Ping(context.Background())
	var te *TransportError
	if !errors.As(err, &te) {
		t.Fatalf("err = %#v", err)
	}
}

func TestUnlistedMethodPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("no panic for a method outside methodResults")
		}
	}()
	_ = New("unused").call(context.Background(), "server.stop", nil, nil, 0)
}
