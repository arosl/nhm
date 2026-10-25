// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/dialogs_test.go and internal/robot/robot_test.go
// (TestOmpRobotStateDialogsAndContext, its dialog part) at commit
// 9d5ed977; see LICENSE in this directory and PROVENANCE.md. Local
// changes: the package and names; the tests of the classifier only, as
// tables without subtests (the answer's tests come with
// --robot-answer-dialog); the omp captures are ntm's fixtures, copied to
// testdata.

package dialogs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Live fixture captured 2026-08-04 from a real Claude Code first boot.
const trustPromptFixture = `
 Quick safety check: Is this a project you created or one you trust? (Like your
 own code, a well-known open source project, or work from your team). If not,
 take a moment to review what's in this folder first.

 Claude Code'll be able to read, edit, and execute files here.

 Security guide

 ❯ 1. Yes, I trust this folder
   2. No, exit

 Enter to confirm · Esc to cancel
`

const rateLimitFixture = `
 You've reached your usage limit for this session.

   1. Wait for the limit to reset
 ❯ 2. Use extra usage beyond the included amount
   3. Upgrade my plan
`

const destructiveConfirmFixture = `
 Claude wants to run the following command:

   git push --force origin main

 Do you want to proceed?

 ❯ 1. Yes, run it
   2. Yes, and don't ask again for git push
   3. No, and tell Claude what to do differently
`

const codexPasteLimboFixture = `
transcript line
› [Pasted text #1 +48 lines]
  send a message
`

const usageOverlayFixture = `
 Usage

 Current session: 42% used
 Weekly limit: 18% used

 esc to close
`

const idleClaudeFixture = `
 some transcript

 ❯ Try "refactor <filepath>"
`

// Live captures from GH#325, not inferred provider layouts. Claude's default
// selection is the DECLINE option and differs from Antigravity's default.
const claudeCursorTrustFixture = `
 Accessing workspace:
 /tmp/project
 Quick safety check: Is this a project you created or one you trust?
 Claude Code'll be able to read, edit, and execute files here.
 Security guide
 ❯ No, exit
   Yes, I trust this folder
 Enter to confirm · Esc to cancel
`

const antigravityCursorTrustFixture = `
Accessing workspace:
/tmp/project
Do you trust the contents of this project?
Antigravity CLI requires permission to read, edit, and execute files here.
> Yes, I trust this folder
  No, exit
  ↑/↓ Navigate · enter Confirm
`

const grokHotkeyTrustFixture = `
/tmp/project
Grok Build may run or modify contents in this directory,
posing security risks.
Yes, proceed                 y
No, quit                     n
`

func TestClassifyFixtures(t *testing.T) {
	for _, tc := range []struct {
		name, capture, agent, want string
	}{
		{"trust prompt", trustPromptFixture, "claude", TrustPrompt},
		{"rate limit options", rateLimitFixture, "claude", RateLimitOptions},
		{"destructive confirm", destructiveConfirmFixture, "claude", DestructiveConfirm},
		{"codex paste limbo", codexPasteLimboFixture, "codex", PasteLimbo},
		{"usage overlay", usageOverlayFixture, "claude", UsageOverlay},
		{"idle composer is none", idleClaudeFixture, "claude", None},
	} {
		if got := Classify(tc.capture, tc.agent); got.Class != tc.want {
			t.Errorf("Classify(%s) = %s, want %s (options: %s)", tc.name, got.Class, tc.want, DescribeOptions(got.Options))
		}
	}
}

func TestClassifyExtractsOptionsAndSelection(t *testing.T) {
	state := Classify(trustPromptFixture, "claude")
	if len(state.Options) != 2 {
		t.Fatalf("expected 2 options, got %s", DescribeOptions(state.Options))
	}
	if !state.Options[0].Selected || state.Options[1].Selected {
		t.Fatalf("expected option 1 selected, option 2 not: %+v", state.Options)
	}
	if !strings.Contains(state.Options[1].Label, "No, exit") {
		t.Fatalf("option 2 label = %q", state.Options[1].Label)
	}
	if state.InputMode != "numbered" || !strings.Contains(state.Evidence, "you trust?") {
		t.Fatalf("state = %+v", state)
	}
}

// Each provider's unnumbered menu keeps its input mode and its display
// order.
func TestClassifyUnnumberedProviderProtocols(t *testing.T) {
	for _, tc := range []struct {
		name, capture, agent, mode string
		selected                   int
	}{
		{"claude cursor", claudeCursorTrustFixture, "claude", "cursor", 1},
		{"antigravity cursor", antigravityCursorTrustFixture, "antigravity", "cursor", 1},
		{"grok hotkey", grokHotkeyTrustFixture, "grok", "hotkey", 0},
		{"numbered", trustPromptFixture, "claude", "numbered", 1},
	} {
		state := Classify(tc.capture, tc.agent)
		if state.Class != TrustPrompt || state.InputMode != tc.mode || len(state.Options) != 2 {
			t.Errorf("%s: unexpected classification: %+v", tc.name, state)
			continue
		}
		for i, option := range state.Options {
			if option.Number != i+1 {
				t.Errorf("%s: unstable display order: %+v", tc.name, state.Options)
			}
			if option.Selected != (i+1 == tc.selected) {
				t.Errorf("%s: option %d selected = %v", tc.name, i+1, option.Selected)
			}
		}
	}
	if keys := Classify(grokHotkeyTrustFixture, "grok").Options; keys[0].Key != "y" || keys[1].Key != "n" || keys[1].Label != "No, quit" {
		t.Errorf("grok hotkeys = %+v", keys)
	}
	// Grok's gate has no "trust": it is a trust prompt for grok only.
	if state := Classify(grokHotkeyTrustFixture, "claude"); state.Class == TrustPrompt {
		t.Errorf("grok's gate on claude = %+v", state)
	}
}

func TestClassifyRejectsNonLiveMenus(t *testing.T) {
	for _, capture := range []string{
		"We discussed trust.\nYes, that looks good\nNo, that is not necessary",
		"```text\n" + claudeCursorTrustFixture + "```",
		claudeCursorTrustFixture + "\n❯ Try refactoring a file",
		grokHotkeyTrustFixture + "\nThe workspace is ready.\n❯",
		trustPromptFixture + "\nThe job finished.\n❯",
		"trust\n1. Yes\ntext interrupting the menu\n2. No\nEnter to confirm",
		"trust\n1. Yes\n2. No\n3. Maybe\n4. A\n5. B\n6. C\n7. D\n8. E\n9. F\n10. G",
		"trust\nYes, proceed  y\nNo, quit  y",
	} {
		if state := Classify(capture, "claude"); state.Class != None {
			t.Errorf("non-live/invalid menu classified as %+v: %q", state, capture)
		}
	}
}

// An unnumbered menu with destructive evidence is a destructive confirm.
func TestClassifyUnnumberedDestructive(t *testing.T) {
	for _, capture := range []string{
		"Run git push --force origin main?\n❯ Yes, run it\n  No, cancel\nEnter to confirm",
		"Run git reset --hard?\nYes, proceed  y\nNo, quit  n",
	} {
		if state := Classify(capture, "claude"); state.Class != DestructiveConfirm {
			t.Errorf("not classified destructive: %+v", state)
		}
	}
}

// A cursor menu with no cursor, or with two, is still a dialog.
func TestClassifyCursorWithoutUniqueSelection(t *testing.T) {
	for _, capture := range []string{
		strings.Replace(claudeCursorTrustFixture, "❯ No", "  No", 1),
		strings.Replace(claudeCursorTrustFixture, "   Yes", " ❯ Yes", 1),
	} {
		if state := Classify(capture, "claude"); state.Class != TrustPrompt {
			t.Errorf("gate must remain visible even without usable cursor: %+v", state)
		}
	}
}

func TestClassifyNewestMenuWins(t *testing.T) {
	capture := trustPromptFixture + "\nOld dialog answered\n" + claudeCursorTrustFixture
	state := Classify(capture, "claude")
	if state.InputMode != "cursor" || len(state.Options) != 2 || state.Options[0].Label != "No, exit" {
		t.Fatalf("historical numbered menu displaced the live one: %+v", state)
	}
}

// omp's own states, on real omp v18.2.3 captures: a staged paste is paste
// limbo, the model selector an unknown dialog, a composer idle or at work
// none.
func TestClassifyOmp(t *testing.T) {
	read := func(name string) string {
		raw, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		return string(raw)
	}
	if got := Classify(read("omp_nerd_paste_token.txt"), "omp"); got.Class != PasteLimbo || !strings.Contains(got.Evidence, "#1") {
		t.Errorf("paste token dialog = %+v, want paste_limbo with the #1 token", got)
	}
	if got := Classify(read("omp_nerd_model_selector.txt"), "omp"); got.Class != Unknown || !strings.Contains(got.Evidence, "Esc close") {
		t.Errorf("selector overlay dialog = %+v, want unknown with the Esc close footer", got)
	}
	for _, name := range []string{"omp_nerd_idle_done.txt", "omp_nerd_working.txt", "omp_unicode_steering.txt"} {
		if got := Classify(read(name), "omp"); got.Class != None {
			t.Errorf("Classify(%s) = %+v, want none", name, got)
		}
	}
	// The same paste on another kind's screen is not omp's limbo.
	if got := Classify(read("omp_nerd_paste_token.txt"), "claude"); got.Class == PasteLimbo {
		t.Errorf("omp's paste on claude = %+v", got)
	}
}

func TestDescribeOptions(t *testing.T) {
	if got := DescribeOptions(nil); got != "none extracted" {
		t.Errorf("none = %q", got)
	}
	if got := DescribeOptions(Classify(trustPromptFixture, "claude").Options); got != `1="Yes, I trust this folder", 2="No, exit"` {
		t.Errorf("trust = %q", got)
	}
}
