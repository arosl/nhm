package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/store"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// Every hook exits 0: a broken hook must never block the agent.
func TestHooksFailOpen(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cases := []struct {
		name    string
		args    []string
		stdin   string
		vars    map[string]string
		wantErr string
	}{
		{"no arguments", nil, "", nil, "Usage"},
		{"unknown hook", []string{"mail-recv"}, "", nil, "unknown hook"},
		{"outside herdr", []string{"sessionstart"}, `{"hook_event_name":"SessionStart"}`, map[string]string{}, ""},
		{"not enrolled", []string{"sessionstart"}, `{"hook_event_name":"SessionStart"}`,
			map[string]string{"HERDR_SOCKET_PATH": "/nonexistent/herdr.sock", "HERDR_PANE_ID": "w1:p1"}, ""},
		{"plugin event outside herdr", []string{"reconcile-pane"}, "", map[string]string{}, ""},
	}
	for _, c := range cases {
		var out, errb bytes.Buffer
		code := run(c.args, strings.NewReader(c.stdin), &out, &errb, env(c.vars))
		if code != 0 {
			t.Errorf("%s: exit %d", c.name, code)
		}
		if out.Len() != 0 {
			t.Errorf("%s: stdout %q", c.name, out.String())
		}
		if c.wantErr == "" && errb.Len() != 0 || c.wantErr != "" && !strings.Contains(errb.String(), c.wantErr) {
			t.Errorf("%s: stderr %q", c.name, errb.String())
		}
	}
}

func TestDebugSaysWhyAHookSkipped(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var out, errb bytes.Buffer
	run([]string{"agent-status"}, strings.NewReader(""), &out, &errb, env(map[string]string{"NHM_HOOK_DEBUG": "1"}))
	if !strings.Contains(errb.String(), "no HERDR_SOCKET_PATH") {
		t.Fatalf("stderr = %q", errb.String())
	}
}

// A hook stays inside its budget while another writer holds the
// database: a burst of hooks or a long nhm transaction must not hold the
// agent's turn.
func TestHookExitsWithinBudgetUnderAWriter(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s := herdrtest.New(t)
	s.Handle("agent.get", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "agent_info", "agent": map[string]any{"pane_id": "w1:p1", "tab_id": "w1:t1",
			"workspace_id": "w1", "terminal_id": "t1", "agent": "claude", "agent_status": "idle", "focused": false,
			"revision": 1, "agent_session": map[string]any{"source": "herdr:claude", "agent": "claude", "kind": "id", "value": "sess-a"}}}, nil
	})
	root, err := store.StateRoot()
	if err != nil {
		t.Fatal(err)
	}
	inst, _, err := store.Enroll(root, s.SocketPath, "default", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := core.SwitchOwner(ctx, inst.Dir, core.OwnerNhm, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.SQL().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.ExecContext(ctx, "ROLLBACK") }()
	vars := map[string]string{"HERDR_SOCKET_PATH": s.SocketPath, "HERDR_PANE_ID": "w1:p1"}
	for _, c := range []struct {
		verb, stdin string
	}{
		{"sessionstart", `{"hook_event_name":"SessionStart","session_id":"sess-a","source":"compact"}`},
		{"reconcile-pane", ""},
	} {
		var out, errb bytes.Buffer
		start := time.Now()
		code := run([]string{c.verb}, strings.NewReader(c.stdin), &out, &errb, env(vars))
		// The clean stop must come before the hard stop.
		if took := time.Since(start); code != 0 || took >= hardStop {
			t.Fatalf("%s under a writer: exit %d after %s (%s)", c.verb, code, took, errb.String())
		}
	}
}

// The mail-sent hook nudges an idle recipient once, without a wait, and
// does nothing while the Bash owns the instance.
func TestMailSentNudgesTheRecipient(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s := herdrtest.New(t)
	agent := map[string]any{"pane_id": "w1:p2", "tab_id": "w1:t2", "workspace_id": "w1", "terminal_id": "t2",
		"agent": "claude", "agent_status": "idle", "focused": false, "revision": 1}
	s.Handle("agent.get", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "agent_info", "agent": agent}, nil
	})
	s.Handle("agent.prompt", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "agent_prompted", "agent": agent}, nil
	})
	root, err := store.StateRoot()
	if err != nil {
		t.Fatal(err)
	}
	inst, _, err := store.Enroll(root, s.SocketPath, "default", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := bindings.NewStore(db).Create(ctx, bindings.New{MailName: "IcyKnoll", MailProjectKey: "/p", ProjectID: "proj",
		Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t2", PaneID: "w1:p2",
			HerdrAlias: "icyknoll", Kind: "claude"}}); err != nil {
		t.Fatal(err)
	}
	payload := `{"hook_event_name":"PostToolUse","session_id":"s1","tool_name":"mcp__mcp-agent-mail__send_message",
		"tool_input":{"project_key":"/p","sender_name":"SageOak","to":["IcyKnoll"]},
		"tool_response":[{"type":"text","text":"{\"deliveries\":[{\"project\":\"/p\",\"payload\":{\"id\":42,\"thread_id\":\"t1\",\"from\":\"SageOak\",\"to\":[\"IcyKnoll\"]}}],\"count\":1}"}]}`
	vars := map[string]string{"HERDR_SOCKET_PATH": s.SocketPath, "HERDR_PANE_ID": "w1:p1", "NHM_HOOK_DEBUG": "1"}
	prompts := func() []herdrtest.Request {
		var out []herdrtest.Request
		for _, r := range s.Requests() {
			if r.Method == "agent.prompt" {
				out = append(out, r)
			}
		}
		return out
	}
	var out, errb bytes.Buffer
	// The Bash owns a new instance: its own wake runs, nhm's does not.
	if code := run([]string{"mail-sent"}, strings.NewReader(payload), &out, &errb, env(vars)); code != 0 ||
		len(prompts()) != 0 || !strings.Contains(errb.String(), "owned by acfs-bash") {
		t.Fatalf("under the Bash: exit %d, prompts %d, stderr %q", code, len(prompts()), errb.String())
	}
	if _, err := core.SwitchOwner(ctx, inst.Dir, core.OwnerNhm, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		errb.Reset()
		if code := run([]string{"mail-sent"}, strings.NewReader(payload), &out, &errb, env(vars)); code != 0 || out.Len() != 0 {
			t.Fatalf("exit %d, stdout %q", code, out.String())
		}
	}
	// Twice the same message: one nudge.
	got := prompts()
	if len(got) != 1 || !strings.Contains(string(got[0].Params), `"text":"Check your Agent Mail inbox."`) ||
		strings.Contains(string(got[0].Params), `"wait"`) {
		t.Fatalf("prompts = %+v; stderr %q", got, errb.String())
	}
	// Another tool's PostToolUse is not a send.
	errb.Reset()
	other := strings.Replace(payload, "send_message", "fetch_inbox", 1)
	if run([]string{"mail-sent"}, strings.NewReader(other), &out, &errb, env(vars)); !strings.Contains(errb.String(), "not an Agent Mail send") {
		t.Fatalf("stderr = %q", errb.String())
	}
}

// A stdin that never ends does not hold the hook.
func TestStdinHasADeadline(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	r, w := io.Pipe()
	defer w.Close()
	var out, errb bytes.Buffer
	start := time.Now()
	code := run([]string{"sessionstart"}, r, &out, &errb, env(map[string]string{}))
	if took := time.Since(start); code != 0 || took >= hardStop || !strings.Contains(errb.String(), "stdin") {
		t.Fatalf("exit %d after %s: %q", code, took, errb.String())
	}
}

func TestAPanicExitsZero(t *testing.T) {
	var errb bytes.Buffer
	boom := func([]string, io.Reader, io.Writer, io.Writer, func(string) string) int { panic("boom") }
	if code := safeRun(boom, nil, strings.NewReader(""), io.Discard, &errb, env(nil)); code != 0 || !strings.Contains(errb.String(), "panic: boom") {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
}

func TestTheCleanStopComesFirst(t *testing.T) {
	if budget >= hardStop || hardStop >= 2*time.Second {
		t.Fatalf("budget %s, hard stop %s: the work must end before the backstop, and both inside 2 s", budget, hardStop)
	}
}

func TestEventIDDependsOnThePayload(t *testing.T) {
	a, b := eventID([]byte(`{"a":1}`)), eventID([]byte(`{"a":2}`))
	if a == b || a != eventID([]byte(`{"a":1}`)) {
		t.Fatalf("ids %s %s", a, b)
	}
}
