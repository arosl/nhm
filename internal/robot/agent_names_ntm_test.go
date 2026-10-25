// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/agent_names_test.go at commit 9d5ed977; see LICENSE in
// this directory and PROVENANCE.md. Local changes: only the tests of the
// generator (TestNATOAlphabetLength, TestAssignNew, TestAllNames,
// TestParseCustomNames, TestAgentNameMapConcurrency); the session tests
// are nhm's, on herdr (internal/cli); plus nhm's test of the custom
// names and the user pane.

package robot

import (
	"strings"
	"testing"
)

func TestNATOAlphabetLength(t *testing.T) {
	if len(NATOAlphabet) != 26 {
		t.Errorf("NATOAlphabet should have 26 entries, got %d", len(NATOAlphabet))
	}
}

func TestAssignNew(t *testing.T) {
	m := NewAgentNameMap("test-session")
	for i, c := range []struct{ typ, pane, want string }{
		{"claude", "0.1", "claude-alpha"}, {"codex", "0.2", "codex-bravo"}, {"gemini", "0.3", "gemini-charlie"},
	} {
		if got := m.AssignNew(c.typ, c.pane); got != c.want {
			t.Errorf("AssignNew %d = %q, want %q", i, got, c.want)
		}
	}
	entries := m.AllNames()
	if len(entries) != 3 || entries[0].Name != "claude-alpha" || entries[0].Pane != "0.1" {
		t.Errorf("AllNames() = %+v", entries)
	}
}

func TestAllNames(t *testing.T) {
	m := NewAgentNameMap("test-session")
	m.AssignNew("claude", "0.1")
	m.AssignNew("codex", "0.2")
	m.AssignNew("user", "0.0")
	entries := m.AllNames()
	if len(entries) != 3 || entries[0].Pane != "0.0" || entries[1].Pane != "0.1" || entries[2].Pane != "0.2" {
		t.Fatalf("AllNames() = %+v, want sorted by pane", entries)
	}
}

func TestParseCustomNames(t *testing.T) {
	for _, c := range []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"alice,bob,charlie", []string{"alice", "bob", "charlie"}},
		{" alice , bob , charlie ", []string{"alice", "bob", "charlie"}},
		{"single", []string{"single"}},
		{"a,,b", []string{"a", "b"}}, // Empty parts are skipped
	} {
		got := ParseCustomNames(c.input)
		if (c.want == nil) != (got == nil) || strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("ParseCustomNames(%q) = %v, want %v", c.input, got, c.want)
		}
	}
}

func TestAgentNameMapConcurrency(t *testing.T) {
	m := NewAgentNameMap("test-session")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			m.AssignNew("claude", "0."+string(rune('0'+i%10)))
		}
	}()
	for i := 0; i < 100; i++ {
		m.AllNames()
	}
	<-done
}

// Custom names go to the agents only, in order; the user pane takes a NATO
// name, and the NATO count moves only for NATO names; past 26 names a
// suffix follows.
func TestCustomNamesSkipTheUserPane(t *testing.T) {
	m := NewAgentNameMapWithCustomNames("s", []string{"alice"})
	got := []string{m.AssignNew("user", "a"), m.AssignNew("claude", "b"), m.AssignNew("codex", "c")}
	if strings.Join(got, ",") != "user-alpha,alice,codex-bravo" {
		t.Errorf("names = %v", got)
	}
	m = NewAgentNameMap("s")
	var last string
	for i := 0; i < 28; i++ {
		last = m.AssignNew("pi", "p")
	}
	if last != "pi-bravo-2" {
		t.Errorf("28th name = %q, want pi-bravo-2", last)
	}
}
