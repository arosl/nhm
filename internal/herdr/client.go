// Package herdr is nhm's typed client for herdr's socket API, protocol 22
// (herdr 0.9.3). It covers the methods nhm uses, and no others.
//
// The wire: a request is one line of JSON, {"id","method","params"}, on a
// new connection to the Unix socket; herdr answers with one line,
// {"id","result"} or {"id","error":{"code","message"}}, and serves one
// request per connection. events.subscribe keeps its connection open and
// streams events (see Stream).
//
// Unknown variants stay explicit. A result type that nhm does not expect
// for a method is an UnexpectedResultError. An agent state outside the
// schema's five keeps its value (AgentStatus.Valid). An event kind nhm
// does not decode stays an Event with its raw data. An error code nhm
// does not name reaches the caller in Error.Code.
//
// methodResults lists each method with the result types it may return;
// schema_check_test.go checks it, and every request and result type,
// against the pinned schema in package schema.
package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"sync/atomic"
	"time"
)

// Protocol is the herdr protocol this client speaks.
const Protocol = 22

// DefaultTimeout bounds a call whose context has no deadline, except the
// calls that wait inside herdr (see budget).
const DefaultTimeout = 30 * time.Second

// waitMargin is added to a server-side timeout_ms, so that herdr's own
// timeout error arrives before the client gives up.
const waitMargin = 5 * time.Second

// methodResults maps each method nhm uses to the result types it may
// return. Observed against herdr 0.9.3 (ACFS bead acfs-9hkh.9).
var methodResults = map[string][]string{
	"ping":                      {"pong"},
	"session.snapshot":          {"session_snapshot"},
	"workspace.create":          {"workspace_created"},
	"tab.create":                {"tab_created"},
	"tab.get":                   {"tab_info"},
	"tab.rename":                {"tab_info"},
	"agent.list":                {"agent_list"},
	"agent.get":                 {"agent_info"},
	"agent.read":                {"pane_read"},
	"agent.send_keys":           {"ok"},
	"agent.rename":              {"agent_info"},
	"agent.start":               {"agent_started"},
	"agent.prompt":              {"agent_prompted"},
	"agent.wait":                {"agent_info"},
	"pane.split":                {"pane_info"},
	"pane.zoom":                 {"pane_zoom"},
	"pane.process_info":         {"pane_process_info"},
	"pane.rename":               {"pane_info"},
	"pane.read":                 {"pane_read"},
	"pane.report_agent_session": {"ok"},
	"pane.report_metadata":      {"ok"},
	"pane.close":                {"ok"},
	"workspace.close":           {"ok"},
	// Observed in the sandbox on herdr 0.9.3 (nhm-p1o, nhm move; nhm-qbi.11,
	// nhm doctor --herdr-fit).
	"pane.move":      {"pane_move"},
	"popup.close":    {"ok"},
	"pane.send_keys": {"ok"},
	"plugin.list":    {"plugin_list"},
	// nhm-qbi.5: the board's tab brought in front of an attached client.
	"tab.focus": {"tab_info"},
	// nhm-4c4: spawn splits the biggest pane of a project's tab.
	"pane.layout": {"pane_layout"},
	// From the pinned schema's response results, not yet observed
	// (acfs-9hkh.70; the rehearsal, acfs-9hkh.31, runs it against herdr).
	"workspace.focus":      {"workspace_info", "ok"},
	"events.subscribe":     {"subscription_started"},
	"events.wait":          {"wait_matched"},
	"pane.wait_for_output": {"output_matched"},
	"plugin.action.invoke": {"plugin_action_invoked"},
	// A popup or overlay answers "ok"; a split, tab or zoomed pane
	// answers with the pane.
	"plugin.pane.open":  {"ok", "plugin_pane_opened"},
	"plugin.pane.focus": {"plugin_pane_focused", "ok"},
	"plugin.pane.close": {"plugin_pane_closed", "ok"},
}

// Client calls one herdr server. The zero value is not usable; use New
// or FromEnv. A Client is safe for concurrent use.
type Client struct {
	// SocketPath is the server's API socket.
	SocketPath string
	// Timeout replaces DefaultTimeout when it is not zero.
	Timeout time.Duration

	nextID atomic.Uint64
}

// New returns a client for the socket at path.
func New(path string) *Client { return &Client{SocketPath: path} }

// FromEnv returns a client for HERDR_SOCKET_PATH, which herdr sets in
// every pane and plugin command.
func FromEnv() (*Client, error) {
	path := os.Getenv("HERDR_SOCKET_PATH")
	if path == "" {
		return nil, errors.New("herdr: HERDR_SOCKET_PATH is not set")
	}
	return New(path), nil
}

type request struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}

type envelope struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
}

// budget is how long a call may take when its context has no deadline:
// noBudget means no client limit (a wait without timeout_ms waits as long
// as herdr does), zero means the client's default.
type budget time.Duration

const noBudget budget = -1

// waitBudget is the budget of a call that waits inside herdr for
// timeoutMS milliseconds; nil means herdr waits without limit.
func waitBudget(timeoutMS *int64) budget {
	if timeoutMS == nil {
		return noBudget
	}
	return budget(time.Duration(*timeoutMS)*time.Millisecond + waitMargin)
}

// call sends one request and decodes its result into out (when out is not
// nil). It returns *Error for an error reply, *UnexpectedResultError for a
// result type outside methodResults, *TransportError for a failure on the
// socket, and the context's error when ctx ends first.
func (c *Client) call(ctx context.Context, method string, params, out any, b budget) error {
	conn, err := c.send(ctx, method, params, b)
	if err != nil {
		return err
	}
	defer conn.close()
	_, err = conn.reply(out)
	return err
}

// conn is one request's connection.
type conn struct {
	nc     net.Conn
	r      *bufio.Reader
	ctx    context.Context
	method string
	id     string
	stop   func() bool
}

func (cn *conn) close() {
	cn.stop()
	_ = cn.nc.Close()
}

// send dials, sets the deadline and writes the request.
func (c *Client) send(ctx context.Context, method string, params any, b budget) (*conn, error) {
	if _, ok := methodResults[method]; !ok {
		// A programming error: every method nhm calls is listed.
		panic("herdr: method not in methodResults: " + method)
	}
	if params == nil {
		params = struct{}{}
	}
	id := fmt.Sprintf("nhm-%d", c.nextID.Add(1))
	line, err := json.Marshal(request{ID: id, Method: method, Params: params})
	if err != nil {
		return nil, fmt.Errorf("herdr %s: encode params: %w", method, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var d net.Dialer
	nc, err := d.DialContext(ctx, "unix", c.SocketPath)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &TransportError{Method: method, Err: err}
	}
	cn := &conn{nc: nc, r: bufio.NewReader(nc), ctx: ctx, method: method, id: id}
	// Cancelling ctx closes the connection, which ends a blocked read.
	cn.stop = context.AfterFunc(ctx, func() { _ = nc.Close() })
	if deadline, ok := c.deadline(ctx, b); ok {
		_ = nc.SetDeadline(deadline)
	}
	if _, err := nc.Write(append(line, '\n')); err != nil {
		cn.close()
		return nil, cn.fail(err)
	}
	return cn, nil
}

func (c *Client) deadline(ctx context.Context, b budget) (time.Time, bool) {
	if d, ok := ctx.Deadline(); ok {
		return d, true
	}
	switch {
	case b == noBudget:
		return time.Time{}, false
	case b > 0:
		return time.Now().Add(time.Duration(b)), true
	case c.Timeout > 0:
		return time.Now().Add(c.Timeout), true
	default:
		return time.Now().Add(DefaultTimeout), true
	}
}

// fail turns a socket error into the context's error when ctx ended, and
// a *TransportError otherwise.
func (cn *conn) fail(err error) error {
	if cn.ctx.Err() != nil {
		return cn.ctx.Err()
	}
	return &TransportError{Method: cn.method, Err: err}
}

// reply reads the reply line, checks it, and decodes the result into out.
// It returns the result type. Subscribe reads the stream's first line
// with it.
func (cn *conn) reply(out any) (string, error) {
	line, err := cn.r.ReadBytes('\n')
	// A last line without its newline is still a reply.
	if err != nil && !(errors.Is(err, io.EOF) && len(line) > 0) {
		return "", cn.fail(err)
	}
	var env envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return "", &TransportError{Method: cn.method, Err: fmt.Errorf("decode reply: %w", err)}
	}
	if env.Error != nil {
		env.Error.Method = cn.method
		return "", env.Error
	}
	if env.ID != cn.id {
		return "", &TransportError{Method: cn.method, Err: fmt.Errorf("reply id %q, want %q", env.ID, cn.id)}
	}
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(env.Result, &head); err != nil {
		return "", &TransportError{Method: cn.method, Err: fmt.Errorf("decode result: %w", err)}
	}
	if !slices.Contains(methodResults[cn.method], head.Type) {
		return "", &UnexpectedResultError{Method: cn.method, Type: head.Type, Raw: env.Result}
	}
	if out != nil {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return "", &TransportError{Method: cn.method, Err: fmt.Errorf("decode %s: %w", head.Type, err)}
		}
	}
	return head.Type, nil
}
