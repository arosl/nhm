// Package herdrtest is a stub herdr server for hermetic tests. It listens
// on a Unix socket and speaks herdr's socket protocol as herdr 0.9.3
// (protocol 22) does:
//
//   - a request is one line of JSON, {"id","method","params"}; the reply is
//     one line, {"id","result"} or {"id","error":{"code","message"}};
//   - the server answers the first request on a connection only;
//   - an unknown method or a missing required parameter is
//     error.code "invalid_request", checked against the pinned schema;
//   - events.subscribe keeps the connection open, and Emit writes events
//     to it as lines.
//
// Tests register a Handler per method. A method without a handler gets
// error.code "stub_unhandled", except ping, which answers pong. A handler
// result whose "type" is not a result type of the pinned schema fails the
// test, so the stub cannot drift from herdr.
//
// agent.start and agent.rename follow herdr's name rules before their
// handler runs: a name herdr refuses is "invalid_agent_name", and a name
// that another pane's agent holds in the test's agent.list is
// "agent_name_taken".
package herdrtest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/herdr/schema"
)

// Request is one request the stub received.
type Request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// Error is a herdr error body.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Handler answers one request with a result object or an error.
type Handler func(Request) (any, *Error)

// Server is a running stub.
type Server struct {
	// SocketPath is the Unix socket to set as HERDR_SOCKET_PATH.
	SocketPath string

	t      testing.TB
	schema *schema.Schema
	ln     net.Listener

	mu       sync.Mutex
	handlers map[string]Handler
	requests []Request
	subs     map[net.Conn]bool
	conns    map[net.Conn]bool // every open connection, for cleanup
	closed   bool
	wg       sync.WaitGroup
}

// agentName is herdr's rule for an agent name (valid_agent_name in
// src/app/agents.rs, herdr 0.9.3).
var agentName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// checkName applies herdr's name rules to agent.start and agent.rename,
// before their handler. A rename's target is a pane id or a name. A
// null or empty name clears, and no rule applies.
func (s *Server) checkName(req Request) *Error {
	if req.Method != "agent.start" && req.Method != "agent.rename" {
		return nil
	}
	var p struct {
		Name   *string `json:"name"`
		PaneID string  `json:"pane_id"`
		Target string  `json:"target"`
	}
	if json.Unmarshal(req.Params, &p) != nil || p.Name == nil || *p.Name == "" {
		return nil
	}
	if !agentName.MatchString(*p.Name) {
		return &Error{Code: "invalid_agent_name", Message: fmt.Sprintf("invalid agent name %q: use [a-z][a-z0-9_-]{0,31}", *p.Name)}
	}
	names := s.liveNames()
	pane := p.PaneID
	if req.Method == "agent.rename" {
		pane = p.Target
		if held, ok := names[p.Target]; ok {
			pane = held
		}
	}
	if held, ok := names[*p.Name]; ok && held != pane {
		return &Error{Code: "agent_name_taken", Message: fmt.Sprintf("agent name %q is already in use", *p.Name)}
	}
	return nil
}

// liveNames are the agent names the test's agent.list gives, name ->
// pane: herdr's list is its truth. Without an agent.list handler the stub
// knows no names, and no name is taken.
func (s *Server) liveNames() map[string]string {
	s.mu.Lock()
	list := s.handlers["agent.list"]
	s.mu.Unlock()
	names := map[string]string{}
	if list == nil {
		return names
	}
	result, herr := list(Request{Method: "agent.list", Params: json.RawMessage(`{}`)})
	if herr != nil {
		return names
	}
	data, err := json.Marshal(result)
	if err != nil {
		return names
	}
	var out struct {
		Agents []struct {
			Name   string `json:"name"`
			PaneID string `json:"pane_id"`
		} `json:"agents"`
	}
	if json.Unmarshal(data, &out) != nil {
		return names
	}
	for _, a := range out.Agents {
		if a.Name != "" {
			names[a.Name] = a.PaneID
		}
	}
	return names
}

// writeTimeout bounds a write to a client that does not read.
const writeTimeout = 2 * time.Second

// New starts a stub and stops it when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	// t.TempDir paths can pass the 108-byte limit of a socket path, and so
	// can any path under a long TMPDIR: the socket goes under /tmp.
	dir, err := os.MkdirTemp("/tmp", "herdrtest")
	if err != nil {
		t.Fatalf("herdrtest: %v", err)
	}
	s := &Server{
		SocketPath: filepath.Join(dir, "herdr.sock"),
		t:          t,
		schema:     schema.MustLoad(),
		handlers:   map[string]Handler{},
		subs:       map[net.Conn]bool{},
		conns:      map[net.Conn]bool{},
	}
	s.ln, err = net.Listen("unix", s.SocketPath)
	if err != nil {
		_ = os.RemoveAll(dir)
		t.Fatalf("herdrtest: listen: %v", err)
	}
	s.wg.Add(1)
	go s.serve()
	t.Cleanup(func() {
		_ = s.ln.Close()
		// Closing every connection also ends a handler that waits for a
		// request line that never comes.
		s.mu.Lock()
		s.closed = true
		for c := range s.conns {
			_ = c.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
		_ = os.RemoveAll(dir)
	})
	return s
}

// Handle sets the handler for a method. The method must exist in the
// pinned schema.
func (s *Server) Handle(method string, h Handler) {
	s.t.Helper()
	if _, ok := s.schema.Methods[method]; !ok {
		s.t.Fatalf("herdrtest: method %q is not in the pinned schema", method)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = h
}

// Requests returns the requests received so far, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Emit writes one event, {"event": kind, "data": data}, to every open
// subscription. The kind must be an event kind of the pinned schema.
// It returns the number of subscriptions written to.
func (s *Server) Emit(kind string, data any) int {
	s.t.Helper()
	if !s.schema.EventKinds[kind] {
		s.t.Fatalf("herdrtest: event kind %q is not in the pinned schema", kind)
	}
	line, err := json.Marshal(map[string]any{"event": kind, "data": data})
	if err != nil {
		s.t.Fatalf("herdrtest: marshal event: %v", err)
	}
	line = append(line, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for c := range s.subs {
		_ = c.SetWriteDeadline(time.Now().Add(writeTimeout))
		if _, err := c.Write(line); err != nil {
			// The client went away: drop the subscription, as herdr does.
			_ = c.Close()
			delete(s.subs, c)
			delete(s.conns, c)
			continue
		}
		n++
	}
	return n
}

// DropSubscriptions closes every open subscription, as a herdr restart
// or an overflow would.
func (s *Server) DropSubscriptions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.subs {
		_ = c.Close()
		delete(s.subs, c)
		delete(s.conns, c)
	}
}

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = c.Close()
			return
		}
		s.conns[c] = true
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(c)
		}()
	}
}

func (s *Server) handle(c net.Conn) {
	keep := false
	defer func() {
		if !keep {
			_ = c.Close()
			s.mu.Lock()
			delete(s.conns, c)
			s.mu.Unlock()
		}
	}()
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		s.reply(c, "", nil, &Error{Code: "invalid_request", Message: "invalid request: " + err.Error()})
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, req)
	h := s.handlers[req.Method]
	s.mu.Unlock()

	if e := s.validate(req); e != nil {
		s.reply(c, req.ID, nil, e)
		return
	}
	if e := s.checkName(req); e != nil {
		s.reply(c, req.ID, nil, e)
		return
	}
	var result any
	var herr *Error
	switch {
	case h != nil:
		result, herr = h(req)
	case req.Method == "ping":
		result = map[string]any{"type": "pong", "version": "0.9.3", "protocol": s.schema.Protocol}
	default:
		herr = &Error{Code: "stub_unhandled", Message: "herdrtest: no handler for " + req.Method}
	}
	if herr == nil {
		if e := s.checkResult(req.Method, result); e != nil {
			s.t.Errorf("%s", e.Message)
			herr = e
		}
	}
	if !s.reply(c, req.ID, result, herr) {
		return
	}
	if herr == nil && req.Method == "events.subscribe" {
		s.mu.Lock()
		s.subs[c] = true
		s.mu.Unlock()
		keep = true
	}
}

func (s *Server) validate(req Request) *Error {
	m, ok := s.schema.Methods[req.Method]
	if !ok {
		return &Error{Code: "invalid_request", Message: fmt.Sprintf("invalid request: unknown variant `%s`", req.Method)}
	}
	if len(req.Params) == 0 {
		return &Error{Code: "invalid_request", Message: "invalid request: missing field `params`"}
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &Error{Code: "invalid_request", Message: "invalid request: params: " + err.Error()}
	}
	for _, key := range m.Required {
		if _, ok := params[key]; !ok {
			return &Error{Code: "invalid_request", Message: fmt.Sprintf("invalid request: missing field `%s`", key)}
		}
	}
	return nil
}

func (s *Server) checkResult(method string, result any) *Error {
	data, err := json.Marshal(result)
	if err != nil {
		return &Error{Code: "stub_bad_result", Message: fmt.Sprintf("herdrtest: %s: marshal result: %v", method, err)}
	}
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &head); err != nil || !s.schema.ResultTypes[head.Type] {
		return &Error{Code: "stub_bad_result", Message: fmt.Sprintf("herdrtest: %s: result type %q is not in the pinned schema", method, head.Type)}
	}
	return nil
}

func (s *Server) reply(c net.Conn, id string, result any, e *Error) bool {
	msg := map[string]any{"id": id}
	if e != nil {
		msg["error"] = e
	} else {
		msg["result"] = result
	}
	line, err := json.Marshal(msg)
	if err != nil {
		return false
	}
	// Hold the lock so a reply and an Emit never interleave on one conn.
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = c.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err = c.Write(append(line, '\n'))
	return err == nil
}
