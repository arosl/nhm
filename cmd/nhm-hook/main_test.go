package main

import (
	"bytes"
	"strings"
	"testing"
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

func TestEventIDDependsOnThePayload(t *testing.T) {
	a, b := eventID([]byte(`{"a":1}`)), eventID([]byte(`{"a":2}`))
	if a == b || a != eventID([]byte(`{"a":1}`)) {
		t.Fatalf("ids %s %s", a, b)
	}
}
