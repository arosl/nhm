package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core"
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
		if took := time.Since(start); code != 0 || took > budget {
			t.Fatalf("%s under a writer: exit %d after %s (%s)", c.verb, code, took, errb.String())
		}
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
	if took := time.Since(start); code != 0 || took > budget || !strings.Contains(errb.String(), "stdin") {
		t.Fatalf("exit %d after %s: %q", code, took, errb.String())
	}
}

func TestEventIDDependsOnThePayload(t *testing.T) {
	a, b := eventID([]byte(`{"a":1}`)), eventID([]byte(`{"a":2}`))
	if a == b || a != eventID([]byte(`{"a":1}`)) {
		t.Fatalf("ids %s %s", a, b)
	}
}
