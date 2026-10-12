package claude

import "testing"

// The cases are ACFS exp.4's observed payloads (Claude Code 2.1.296).
func TestClassify(t *testing.T) {
	const bound = "sess-a"
	cases := []struct {
		name    string
		e       Event
		bound   string
		launch  bool
		verdict string
		comp    bool
		res     bool
		take    bool
	}{
		{"compact keeps the session", Event{HookEvent: "SessionStart", Source: "compact", SessionID: bound, Entrypoint: "cli"}, bound, false, Continue, true, false, false},
		{"compact of another session", Event{HookEvent: "SessionStart", Source: "compact", SessionID: "sess-b"}, bound, false, Displace, false, false, false},
		{"compact before nhm saw a session", Event{HookEvent: "SessionStart", Source: "compact", SessionID: "sess-b"}, "", false, Continue, true, false, true},
		{"resume of the bound session", Event{HookEvent: "SessionStart", Source: "resume", SessionID: bound}, bound, false, Continue, false, true, false},
		{"resume of another session", Event{HookEvent: "SessionStart", Source: "resume", SessionID: "sess-b"}, bound, false, Displace, false, false, false},
		{"clear", Event{HookEvent: "SessionStart", Source: "clear", SessionID: "sess-b"}, bound, false, Displace, false, false, false},
		{"fork", Event{HookEvent: "SessionStart", Source: "fork", SessionID: "sess-b"}, bound, true, Displace, false, false, false},
		{"startup of the spawned agent", Event{HookEvent: "SessionStart", Source: "startup", SessionID: "sess-b"}, "", true, Continue, false, false, true},
		{"startup without the launch id", Event{HookEvent: "SessionStart", Source: "startup", SessionID: "sess-b"}, "", false, Displace, false, false, false},
		{"startup of a new session", Event{HookEvent: "SessionStart", Source: "startup", SessionID: "sess-b"}, bound, true, Displace, false, false, false},
		{"startup again of the bound session", Event{HookEvent: "SessionStart", Source: "startup", SessionID: bound}, bound, false, Continue, false, false, false},
		{"an unknown source", Event{HookEvent: "SessionStart", Source: "teleport", SessionID: bound}, bound, false, Displace, false, false, false},
		{"nested claude -p", Event{HookEvent: "SessionStart", Source: "startup", SessionID: "sess-n", Entrypoint: "sdk-cli"}, bound, false, Ignore, false, false, false},
		{"PreCompact adds nothing", Event{HookEvent: "PreCompact", SessionID: bound}, bound, false, Ignore, false, false, false},
	}
	for _, c := range cases {
		d := Classify(c.e, c.bound, c.launch)
		if d.Verdict != c.verdict || d.Compacted != c.comp || d.Resumed != c.res || d.TakeSession != c.take || d.Reason == "" {
			t.Errorf("%s: %+v", c.name, d)
		}
	}
}

func TestNested(t *testing.T) {
	for ep, want := range map[string]bool{"": false, "cli": false, "sdk-cli": true, "sdk-ts": true} {
		if got := Nested(Event{Entrypoint: ep}); got != want {
			t.Errorf("Nested(%q) = %v", ep, got)
		}
	}
}
