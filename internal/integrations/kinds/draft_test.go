package kinds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The screens below are the ends of the panes that herdr 0.9.5's
// pane.read showed in the sandbox (nhm-mowp: Claude Code 2.1.310, Codex
// 0.164.0, pi 0.8.0), cut to their last lines, with the rules shortened.

var rule = strings.Repeat("─", 60)

// claudeScreen is Claude's screen: the transcript, the prompt box and the
// footer below it.
func claudeScreen(transcript, box, footer string) string {
	return transcript + "\n" + rule + "\n" + box + "\n" + rule + "\n" + footer + "\n"
}

const claudeNormal = "  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents"

// piBar is pi's status bar: its icons are in Unicode's private use area.
var piBar = c(0xf06a9) + " Mock " + c(0xe0b0) + " " + c(0xf1719) + " off " + c(0xe0b0) + " ACT " + c(0xe0b0) + " " + c(0xf07c) + " ~/proj"

const codexShell = "                                                                 Shell mode"

// A "!" typed or pasted first puts Claude in bash mode, also while it
// works, after which herdr's paste of nhm's prompt ran as a shell command;
// a "!" after a space stayed in the prompt and ran nothing. An earlier
// shell command in the transcript, a reply that quotes the marks, or the
// "?" shortcuts panel (it names "! for shell mode") shows no draft.
func TestClaudeShellDraft(t *testing.T) {
	a := Lookup("claude")
	effort := "                                                    ◉ xhigh · /effort"
	working := "❯ slow: please wait\n●\n✽ Sketching… (3s · ↓ 1 tokens)"
	for name, screen := range map[string]string{
		"a typed or pasted !":       claudeScreen(effort, "!", "  ! for shell mode"),
		"!ls -la":                   claudeScreen(effort, "! ls -la", "  ! for shell mode"),
		"while it works":            claudeScreen(working, "!", "  ! for shell mode"),
		"the box only":              claudeScreen("", "! ls", "  ⏵⏵ bypass permissions on"),
		"the top rule not read":     "  ls -la\n  | head\n" + rule + "\n  ! for shell mode\n",
		"trailing blank lines kept": claudeScreen("", "!", "  ! for shell mode") + "\n\n\n",
	} {
		if got := a.ShellDraft(screen); got != `Claude Code's bash mode (a "!" draft)` {
			t.Errorf("%s: %q", name, got)
		}
	}
	ran := "! echo person-ran-this\n  ⎿  person-ran-this\n\n❯ mock-reply: !x Shell mode ! for shell mode\n\n● !x Shell mode ! for shell mode\n\n✻ Baked for 1s · done 11:30 PM\n"
	panel := "  ! for shell mode        double tap esc to clear input      ctrl + shift + _ to undo\n" +
		"  / for commands          shift + tab to auto-accept edits   ctrl + z to suspend\n" +
		"  @ for file paths        ctrl + o for verbose output        ctrl + v to paste images"
	for name, screen := range map[string]string{
		"empty":                    claudeScreen(effort, "❯", claudeNormal),
		"a space, then !":          claudeScreen("", "❯  !", "  ⏵⏵ bypass permissions on (shift+tab to cycle)"),
		"an ordinary draft":        claudeScreen("", "❯ hello there", claudeNormal),
		"a transcript":             claudeScreen(ran, "❯", claudeNormal),
		"the shortcuts panel":      claudeScreen(effort, "❯", panel),
		"the panel, one line each": claudeScreen(effort, "❯", "  ! for shell mode\n  / for commands\n  @ for file paths"),
		"the panel, one rule":      "❯\n" + rule + "\n" + panel + "\n",
		"no prompt box":            "! touch x\n  ⎿  (Bash completed with no output)\n",
		"a dialog in its place":    "▔▔▔▔▔▔▔▔\n   Background work is running\n   ❯ 1. Exit and stop tasks\n\n   Enter to confirm · Esc to cancel\n",
		"nothing":                  "",
	} {
		if got := a.ShellDraft(screen); got != "" {
			t.Errorf("%s: %q", name, got)
		}
	}
}

// Codex says "Shell mode" for a "!" first, typed or pasted, after a space
// too, and while it works: each ran nhm's prompt as a shell command. Its
// file popup shows above the composer. Its composer starts with "!", or
// with › and white space before the "!". A long paste that starts with
// "!" shows as a placeholder, and nhm's prompt after it ran nothing.
func TestCodexShellDraft(t *testing.T) {
	a := Lookup("codex")
	head := "     ~/proj\n  permissions: YOLO mode\n"
	popup := "  heads                      .git/logs/refs/                           Dir\n" +
		"  enter/tab insert · esc close · ↑/↓ select · ←/→ filter\n"
	for name, screen := range map[string]string{
		"a typed or pasted !": head + "!\n\n  mock-model xhigh · ~/proj\n" + codexShell + "\n",
		"!ls -la":             head + "! ls -la\n\n  mock-model xhigh · ~/proj\n" + codexShell + "\n",
		"a space, then !":     head + "›  !\n\n  mock-model xhigh · ~/proj\n" + codexShell + "\n",
		"while it works": "› slow: please wait\n• mock\n!\n  mock-model xhigh · ~/proj\n" +
			"  tab to queue message                                           Shell mode\n",
		"after the turn": "• mock reply 3\n  Worked for 8s • 00:04\n!\n  mock-model xhigh · ~/proj\n" +
			"                                      Shell mode    ⚠ 1 warning · f2 to view\n",
		"the file popup":      popup + "! cat @tra\n  mock-model xhigh · ~/proj\n" + codexShell + "\n",
		"the composer only":   head + "›  !ls\n\n  mock-model xhigh · ~/proj\n  ? for shortcuts\n",
		"! in its place only": head + "!ls\n\n  mock-model xhigh · ~/proj\n",
	} {
		if got := a.ShellDraft(screen); got != `Codex's shell mode (a "!" draft)` {
			t.Errorf("%s: %q", name, got)
		}
	}
	ran := "• You ran echo person-ran-this\n  └ person-ran-this\n\n› mock-reply: !x Shell mode ! for shell mode\n\n• !x Shell mode ! for shell mode\n\n  Worked for 1s • 23:31\n\n"
	for name, screen := range map[string]string{
		"empty":             "› Ask Codex to do anything\n\n  mock-model xhigh · ~/proj\n  ? for shortcuts\n",
		"an ordinary draft": "  Worked for <1s • 23:30\n\n› hello there\n\n  mock-model xhigh · ~/proj\n",
		"a transcript": ran + "› Ask Codex to do anything\n\n  mock-model xhigh · ~/proj\n" +
			"  ? for shortcuts                                   ⚠ 1 warning · f2 to view\n",
		"! on a later line": "› hello\n  !x\n\n  mock-model xhigh · ~/proj\n",
		"a long paste": "  Worked for <1s • 00:04\n› [Pasted Content 1545 chars]\n  mock-model xhigh · ~/proj\n" +
			"                                                    ⚠ 1 warning · f2 to view\n",
		"nothing": "",
	} {
		if got := a.ShellDraft(screen); got != "" {
			t.Errorf("%s: %q", name, got)
		}
	}
}

// pi shows no shell mode: its input box, between its status bar and its
// footer, started with "!" (after a space too, pasted, or while pi works,
// when the bar holds its spinner) each time nhm's prompt ran as a shell
// command.
func TestPiShellDraft(t *testing.T) {
	a := Lookup("pi")
	top := "pi · ready\n· pi interactive stack\n"
	busy := "pi · working\n· pi interactive stack\n› slow: please wait\nmock reply\n⠴ responding ...  " + c(0xf06a9) + " Mock " + c(0xe0b0) + " ACT"
	for name, screen := range map[string]string{
		"a typed !":           top + piBar + "\n!\npi\n",
		"while it works":      busy + "\n!\npi\n",
		"!ls -la":             top + piBar + "\n!ls -la\npi\n",
		"a space, then !":     top + piBar + "\n !\npi\n",
		"a pasted !":          "mock reply 6\n\n\n" + piBar + "\n!\ntokens 42↑ 8↓ · total 50\n",
		"a blank line first":  top + piBar + "\n\n!x\npi\n",
		"no bar: the line up": "mock reply 6\n!x\npi\n",
	} {
		if got := a.ShellDraft(screen); got != `a "!" draft (pi shows no shell mode)` {
			t.Errorf("%s: %q", name, got)
		}
	}
	ran := "› !echo person-ran-this\n✓ bash\n  Ran `echo person-ran-this`\n  ```\n  person-ran-this\n  ```\nmock reply 8\n" +
		"› mock-reply: !x Shell mode ! for\nshell mode\n!x Shell mode ! for shell mode\n\n\n\n\n\n"
	for name, screen := range map[string]string{
		"empty":             top + piBar + "\nType a message (Enter to send, Alt+En\npi\n",
		"an ordinary draft": "mock reply 7\n" + piBar + "\nhello there\ntokens 42↑ 8↓ · total 50\n",
		"a transcript":      ran + piBar + "\nType a message (Enter to send, Alt+En\ntokens 42↑ 8↓ · total 50\n",
		"! on a later line": top + piBar + "\nhello\n!x\npi\n",
		"the bar last":      "!x\n" + piBar + "\n",
		"nothing":           "",
	} {
		if got := a.ShellDraft(screen); got != "" {
			t.Errorf("%s: %q", name, got)
		}
	}
}

// Only the three kinds whose screens the sandbox read have a rule (the
// operator's ruling, 2026-10-24); the other kinds with an escape say in
// Cannot that a draft in their input box can make nhm's prompt a shell
// command.
func TestShellDraftRules(t *testing.T) {
	r := NewRegistry()
	for _, name := range r.Names() {
		a, _ := r.Get(name)
		switch name {
		case "claude", "codex", "pi":
			if a.ShellDraft == nil {
				t.Errorf("%s has no draft rule", name)
			}
		default:
			if a.ShellDraft != nil {
				t.Errorf("%s has a draft rule", name)
			}
			if a.Escape() != "" && !strings.Contains(strings.Join(a.Cannot, "; "), "that a person leaves in its input box") {
				t.Errorf("%s's Cannot names no draft: %q", name, a.Cannot)
			}
		}
	}
	if Lookup("unregistered").ShellDraft != nil {
		t.Error("an unregistered kind has a draft rule")
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[kinds.mycli]\nflags = [\"my\"]\ncannot = [\"its usage\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	if m, _ := r.Get("mycli"); m.ShellDraft != nil || len(m.Cannot) != 2 || m.Cannot[0] != "its usage" ||
		!strings.Contains(m.Cannot[1], "that a person leaves in its input box") {
		t.Errorf("a kind from config = %q", m.Cannot)
	}
}
