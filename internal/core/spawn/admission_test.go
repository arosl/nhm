package spawn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The package's tests never read the caller's own Claude settings.
func init() { ClaudeSettingsPath = func() string { return "" } }

func withClaudeSettings(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.json")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := ClaudeSettingsPath
	ClaudeSettingsPath = func() string { return path }
	t.Cleanup(func() { ClaudeSettingsPath = old })
}

func guardCommands(t *testing.T, settings string) []string {
	t.Helper()
	if settings == "" {
		return nil
	}
	var s struct {
		Hooks struct {
			PreToolUse []hookEntry `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(settings), &s); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range s.Hooks.PreToolUse {
		for _, h := range e.Hooks {
			out = append(out, h.Command)
		}
	}
	return out
}

func lookAll(bin string) (string, error) { return "/usr/local/bin/" + bin, nil }

// A guard the user's own settings already run is not added again: Claude
// merges both, so dcg would run twice on every Bash call (the review of
// b0902bb).
func TestHookSettingsSkipsAGuardTheUserRuns(t *testing.T) {
	withClaudeSettings(t, "")
	if got := guardCommands(t, HookSettings(lookAll)); len(got) != 2 {
		t.Fatalf("no user settings: %v", got)
	}
	withClaudeSettings(t, `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"'/home/u/.local/bin/dcg'"}]}]}}`)
	got := guardCommands(t, HookSettings(lookAll))
	if len(got) != 1 || !strings.Contains(got[0], "rch") {
		t.Fatalf("the user runs dcg: %v", got)
	}
	withClaudeSettings(t, `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"dcg"},{"type":"command","command":"rch --hook"}]}]}}`)
	if s := HookSettings(lookAll); s != "" {
		t.Fatalf("the user runs both: %q", s)
	}
	// A hook that only mentions dcg is not dcg.
	withClaudeSettings(t, `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"echo dcg"}]}]}}`)
	if got := guardCommands(t, HookSettings(lookAll)); len(got) != 2 {
		t.Fatalf("echo dcg: %v", got)
	}
	// Only a hook that runs on Bash calls counts, and only while hooks are
	// on (the review of 3643fe5).
	for _, settings := range []string{
		`{"hooks":{"PreToolUse":[{"matcher":"Write","hooks":[{"type":"command","command":"dcg"}]}]}}`,
		`{"hooks":{"PreToolUse":[{"matcher":"Edit|Write","hooks":[{"type":"command","command":"dcg"}]}]}}`,
		`{"hooks":{"PreToolUse":[{"matcher":"Bash2","hooks":[{"type":"command","command":"dcg"}]}]}}`,
		`{"disableAllHooks":true,"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"dcg"}]}]}}`,
	} {
		withClaudeSettings(t, settings)
		if got := guardCommands(t, HookSettings(lookAll)); len(got) != 2 {
			t.Fatalf("%s: %v", settings, got)
		}
	}
	// A matcher whose meaning to Claude's JavaScript RegExp is not sure
	// counts as not covering: nhm keeps its guard (the review of ce21b11).
	for _, matcher := range []string{`(?i)bash`, `(?i:Bash)`, `[B]ash`, `\\w+`, `Bash{1}`, `\\ABash\\z`, `[[:alpha:]]+`} {
		withClaudeSettings(t, `{"hooks":{"PreToolUse":[{"matcher":"`+matcher+`","hooks":[{"type":"command","command":"dcg"}]}]}}`)
		if got := guardCommands(t, HookSettings(lookAll)); len(got) != 2 {
			t.Fatalf("matcher %q: %v", matcher, got)
		}
	}
	for _, matcher := range []string{"", "*", "Bash", "Bash|Edit", "Ba.*", "(Bash|Edit)"} {
		withClaudeSettings(t, `{"hooks":{"PreToolUse":[{"matcher":"`+matcher+`","hooks":[{"type":"command","command":"dcg"}]}]}}`)
		if got := guardCommands(t, HookSettings(lookAll)); len(got) != 1 {
			t.Fatalf("matcher %q: %v", matcher, got)
		}
	}
	// Unreadable settings add both guards.
	withClaudeSettings(t, `{not json`)
	if got := guardCommands(t, HookSettings(lookAll)); len(got) != 2 {
		t.Fatalf("bad settings: %v", got)
	}
}
