package herdrtest

import (
	"bufio"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

type reply struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
}

func call(t *testing.T, s *Server, line string) (reply, *bufio.Reader, net.Conn) {
	t.Helper()
	c, err := net.Dial("unix", s.SocketPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := bufio.NewReader(c)
	data, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var rep reply
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	return rep, r, c
}

func TestPingAnswersPong(t *testing.T) {
	s := New(t)
	rep, _, _ := call(t, s, `{"id":"1","method":"ping","params":{}}`)
	if rep.ID != "1" || rep.Error != nil || !strings.Contains(string(rep.Result), `"protocol":22`) {
		t.Fatalf("ping reply = %+v %s", rep, rep.Result)
	}
	if got := s.Requests(); len(got) != 1 || got[0].Method != "ping" {
		t.Fatalf("requests = %+v", got)
	}
}

func TestSchemaErrorsMatchHerdr(t *testing.T) {
	s := New(t)
	for line, want := range map[string]string{
		`{"id":"a","method":"nope","params":{}}`:      "unknown variant `nope`",
		`{"id":"b","method":"agent.get","params":{}}`: "missing field `target`",
		`{"id":"c","method":"agent.get"}`:             "missing field `params`",
		`not json`:                                    "invalid request",
	} {
		rep, _, _ := call(t, s, line)
		if rep.Error == nil || rep.Error.Code != "invalid_request" || !strings.Contains(rep.Error.Message, want) {
			t.Errorf("%s: reply = %+v, want invalid_request with %q", line, rep.Error, want)
		}
	}
}

func TestHandlerAndUnhandled(t *testing.T) {
	s := New(t)
	s.Handle("agent.get", func(r Request) (any, *Error) {
		var p struct{ Target string }
		_ = json.Unmarshal(r.Params, &p)
		if p.Target != "icyknoll" {
			return nil, &Error{Code: "agent_not_found", Message: "no agent " + p.Target}
		}
		return map[string]any{"type": "agent_info", "agent": map[string]any{"name": p.Target}}, nil
	})
	rep, _, _ := call(t, s, `{"id":"1","method":"agent.get","params":{"target":"icyknoll"}}`)
	if rep.Error != nil || !strings.Contains(string(rep.Result), `"agent_info"`) {
		t.Fatalf("agent.get = %+v %s", rep.Error, rep.Result)
	}
	rep, _, _ = call(t, s, `{"id":"2","method":"agent.get","params":{"target":"ghost"}}`)
	if rep.Error == nil || rep.Error.Code != "agent_not_found" {
		t.Fatalf("agent.get ghost = %+v", rep.Error)
	}
	rep, _, _ = call(t, s, `{"id":"3","method":"agent.list","params":{}}`)
	if rep.Error == nil || rep.Error.Code != "stub_unhandled" {
		t.Fatalf("agent.list without handler = %+v", rep.Error)
	}
}

// fakeT records Errorf and Fatalf instead of failing the real test.
type fakeT struct {
	testing.TB
	errors []string
}

func (f *fakeT) Helper()                           {}
func (f *fakeT) Errorf(format string, args ...any) { f.errors = append(f.errors, format) }
func (f *fakeT) Fatalf(format string, args ...any) {
	f.errors = append(f.errors, format)
	panic("fatal")
}

func TestBadResultTypeFailsTheTest(t *testing.T) {
	s := New(t)
	ft := &fakeT{TB: t}
	s.t = ft
	s.Handle("agent.list", func(Request) (any, *Error) {
		return map[string]any{"type": "agent_lst"}, nil
	})
	rep, _, _ := call(t, s, `{"id":"1","method":"agent.list","params":{}}`)
	if rep.Error == nil || rep.Error.Code != "stub_bad_result" {
		t.Fatalf("reply = %+v", rep.Error)
	}
	if len(ft.errors) != 1 {
		t.Fatalf("errors recorded = %v", ft.errors)
	}
	func() {
		defer func() { _ = recover() }()
		s.Handle("agent.nope", nil)
	}()
	if len(ft.errors) != 2 {
		t.Fatalf("Handle of an unknown method was accepted: %v", ft.errors)
	}
}

// A client that connects and never sends a line must not hang cleanup.
// The subtest's cleanup runs before t.Run returns; a hang fails the test
// binary by its timeout.
func TestSilentClientDoesNotBlockCleanup(t *testing.T) {
	t.Run("stub", func(t *testing.T) {
		s := New(t)
		c, err := net.Dial("unix", s.SocketPath)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		// Make sure the stub accepted the connection before cleanup runs.
		rep, _, _ := call(t, s, `{"id":"1","method":"ping","params":{}}`)
		if rep.Error != nil {
			t.Fatalf("ping = %+v", rep.Error)
		}
	})
}

func TestSubscriptionReceivesEvents(t *testing.T) {
	s := New(t)
	s.Handle("events.subscribe", func(Request) (any, *Error) {
		return map[string]any{"type": "subscription_started"}, nil
	})
	rep, r, _ := call(t, s, `{"id":"1","method":"events.subscribe","params":{"subscriptions":[]}}`)
	if rep.Error != nil {
		t.Fatalf("subscribe = %+v", rep.Error)
	}
	if n := s.Emit("pane_exited", map[string]any{"pane_id": "w1:p1"}); n != 1 {
		t.Fatalf("Emit wrote to %d subscriptions, want 1", n)
	}
	line, err := r.ReadBytes('\n')
	if err != nil || !strings.Contains(string(line), `"event":"pane_exited"`) {
		t.Fatalf("event line = %q, %v", line, err)
	}
	s.DropSubscriptions()
	if _, err := r.ReadBytes('\n'); err == nil {
		t.Fatal("connection still open after DropSubscriptions")
	}
	if n := s.Emit("pane_exited", map[string]any{}); n != 0 {
		t.Fatalf("Emit after drop wrote to %d", n)
	}
}

// agent.start and agent.rename follow herdr's name rules before their
// handlers: a name outside [a-z][a-z0-9_-]{0,31} is invalid_agent_name,
// and a name that another pane's agent holds in the test's agent.list is
// agent_name_taken; its own name, a clear and a free suffix pass
// (nhm-4xt).
func TestAgentNameRules(t *testing.T) {
	s := New(t)
	handled := 0
	s.Handle("agent.start", func(Request) (any, *Error) {
		handled++
		return map[string]any{"type": "agent_started"}, nil
	})
	s.Handle("agent.rename", func(Request) (any, *Error) {
		handled++
		return map[string]any{"type": "agent_info", "agent": map[string]any{}}, nil
	})
	start := func(name, pane string) string {
		return `{"id":"s","method":"agent.start","params":{"name":` + name + `,"kind":"claude","pane_id":"` + pane + `"}}`
	}
	rename := func(target, name string) string {
		return `{"id":"r","method":"agent.rename","params":{"target":"` + target + `","name":` + name + `}}`
	}
	expect := func(line, code string) {
		t.Helper()
		before := handled
		rep, _, _ := call(t, s, line)
		switch {
		case code == "" && (rep.Error != nil || handled != before+1):
			t.Errorf("%s = %+v, handled %d", line, rep.Error, handled-before)
		case code != "" && (rep.Error == nil || rep.Error.Code != code || handled != before):
			t.Errorf("%s = %+v, handled %d; want %s before the handler", line, rep.Error, handled-before, code)
		}
	}
	expect(start(`"IcyKnoll"`, "w1:p1"), "invalid_agent_name")
	expect(start(`"2icy"`, "w1:p1"), "invalid_agent_name")
	expect(rename("w1:p1", `"icy knoll"`), "invalid_agent_name")
	expect(start(`"`+strings.Repeat("a", 33)+`"`, "w1:p1"), "invalid_agent_name")
	// Without an agent.list, the stub knows no live names.
	expect(start(`"icyknoll"`, "w1:p2"), "")
	expect(rename("w1:p1", `null`), "")

	s.Handle("agent.list", func(Request) (any, *Error) {
		return map[string]any{"type": "agent_list", "agents": []any{map[string]any{"name": "icyknoll", "pane_id": "w1:p1"}}}, nil
	})
	expect(start(`"icyknoll"`, "w1:p2"), "agent_name_taken")
	expect(rename("w1:p2", `"icyknoll"`), "agent_name_taken")
	expect(rename("w1:p1", `"icyknoll"`), "")
	expect(rename("icyknoll", `"icyknoll"`), "")
	expect(start(`"icyknoll-2"`, "w1:p2"), "")
}
