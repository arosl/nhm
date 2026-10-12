package codex

import "testing"

// The cases are ACFS exp.3's observed hooks (codex-cli 0.162.1,
// --no-daemon).
func TestClassify(t *testing.T) {
	const bound = "thread-a"
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
		{"first turn of the spawned agent", Event{HookEvent: "SessionStart", Source: "startup", SessionID: "thread-b"}, "", true, Continue, false, false, true},
		{"first turn without the launch id", Event{HookEvent: "SessionStart", Source: "startup", SessionID: "thread-b"}, "", false, Displace, false, false, false},
		{"a new thread", Event{HookEvent: "SessionStart", Source: "startup", SessionID: "thread-b"}, bound, true, Displace, false, false, false},
		{"resume of the bound thread", Event{HookEvent: "SessionStart", Source: "resume", SessionID: bound}, bound, false, Continue, false, true, false},
		{"resume of another thread", Event{HookEvent: "SessionStart", Source: "resume", SessionID: "thread-b"}, bound, false, Displace, false, false, false},
		{"PostCompact is the compaction cue", Event{HookEvent: "PostCompact", SessionID: bound}, bound, false, Continue, true, false, false},
		{"PostCompact of another thread", Event{HookEvent: "PostCompact", SessionID: "thread-b"}, bound, false, Displace, false, false, false},
		{"PostCompact nhm never saw", Event{HookEvent: "PostCompact", SessionID: "thread-b"}, "", false, Ignore, false, false, false},
		{"PreCompact adds nothing", Event{HookEvent: "PreCompact", SessionID: bound}, bound, false, Ignore, false, false, false},
		{"Stop is no cue", Event{HookEvent: "Stop", SessionID: bound}, bound, false, Ignore, false, false, false},
		{"an unknown source", Event{HookEvent: "SessionStart", Source: "clear", SessionID: "thread-b"}, bound, false, Displace, false, false, false},
	}
	for _, c := range cases {
		d := Classify(c.e, c.bound, c.launch)
		if d.Verdict != c.verdict || d.Compacted != c.comp || d.Resumed != c.res || d.TakeSession != c.take || d.Reason == "" {
			t.Errorf("%s: %+v", c.name, d)
		}
	}
}
