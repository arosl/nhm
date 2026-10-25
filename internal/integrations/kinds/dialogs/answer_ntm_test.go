// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/dialogs_test.go and internal/robot/robot_test.go
// (TestOmpRobotStateDialogsAndContext, its key part) at commit 9d5ed977;
// see LICENSE in this directory and PROVENANCE.md. Local changes: the
// package and names; the tests of the answer only, as tables without
// subtests (the classifier's are in dialogs_ntm_test.go); TestHerdrKey is
// nhm's.

package dialogs

import (
	"reflect"
	"strings"
	"testing"
)

func TestResolveAnswerDestructivePolicy(t *testing.T) {
	state := Classify(destructiveConfirmFixture, "claude")
	if state.Class != DestructiveConfirm {
		t.Fatalf("fixture must classify destructive, got %s", state.Class)
	}
	// Accept-side answers refused, whether direct or by option number.
	for _, choice := range []string{"option-1", "option-2"} {
		if _, err := ResolveAnswer(state, choice); err == nil || !strings.Contains(err.Error(), "POLICY_REFUSED") {
			t.Errorf("choice %s must be POLICY_REFUSED, got err=%v", choice, err)
		}
	}
	// Decline-side allowed both ways.
	keys, err := ResolveAnswer(state, "option-3")
	if err != nil || len(keys) != 2 || keys[0] != "3" {
		t.Errorf("option-3 should type 3+Enter, got %v err=%v", keys, err)
	}
	keys, err = ResolveAnswer(state, "decline")
	if err != nil || keys[0] != "3" {
		t.Errorf("decline should pick option 3, got %v err=%v", keys, err)
	}
}

func TestResolveAnswerExtraUsage(t *testing.T) {
	keys, err := ResolveAnswer(Classify(rateLimitFixture, "claude"), "extra-usage")
	if err != nil || keys[0] != "2" {
		t.Errorf("extra-usage should pick option 2, got %v err=%v", keys, err)
	}
	if _, err := ResolveAnswer(Classify(trustPromptFixture, "claude"), "extra-usage"); err == nil {
		t.Error("extra-usage on a trust prompt must error")
	}
}

func TestResolveAnswerDismissAndNone(t *testing.T) {
	keys, err := ResolveAnswer(Classify(usageOverlayFixture, "claude"), "dismiss")
	if err != nil || len(keys) != 1 || keys[0] != "Escape" {
		t.Errorf("dismiss = %v err=%v", keys, err)
	}
	if _, err := ResolveAnswer(State{Class: None}, "dismiss"); err == nil || err.Error() != "no dialog detected on the pane" {
		t.Errorf("answering a pane with no dialog must error, got %v", err)
	}
	// A panel with no options declines with Escape; a paste limbo too.
	for _, s := range []State{Classify(usageOverlayFixture, "claude"), Classify(codexPasteLimboFixture, "codex")} {
		if keys, err := ResolveAnswer(s, " DECLINE "); err != nil || !reflect.DeepEqual(keys, []string{"Escape"}) {
			t.Errorf("decline on %s = %v, %v", s.Class, keys, err)
		}
	}
}

func TestResolveAnswerUnnumberedProviderProtocols(t *testing.T) {
	for _, tc := range []struct {
		name, capture, agent, choice string
		keys                         []string
	}{
		{"claude accept is second", claudeCursorTrustFixture, "claude", "option-2", []string{"Down", "Enter"}},
		{"claude decline is selected", claudeCursorTrustFixture, "claude", "decline", []string{"Enter"}},
		{"antigravity decline is second", antigravityCursorTrustFixture, "antigravity", "decline", []string{"Down", "Enter"}},
		{"antigravity accept is selected", antigravityCursorTrustFixture, "antigravity", "option-1", []string{"Enter"}},
		{"grok accept is immediate", grokHotkeyTrustFixture, "grok", "option-1", []string{"y"}},
		{"grok decline is immediate", grokHotkeyTrustFixture, "grok", "decline", []string{"n"}},
		{"numbered still works", trustPromptFixture, "claude", "decline", []string{"2", "Enter"}},
	} {
		keys, err := ResolveAnswer(Classify(tc.capture, tc.agent), tc.choice)
		if err != nil || !reflect.DeepEqual(keys, tc.keys) {
			t.Errorf("%s: answer = %v, %v; want %v", tc.name, keys, err, tc.keys)
		}
	}
}

func TestResolveAnswerCursorUsesActualSelection(t *testing.T) {
	capture := "Usage limit reached\n  Wait for the limit to reset\n  Use extra usage\n❯ Upgrade my plan\nEnter to confirm"
	state := Classify(capture, "claude")
	keys, err := ResolveAnswer(state, "extra-usage")
	if state.Class != RateLimitOptions || err != nil || !reflect.DeepEqual(keys, []string{"Up", "Enter"}) {
		t.Fatalf("extra-usage = %v, %v, state=%+v", keys, err, state)
	}
	keys, err = ResolveAnswer(state, "option-1")
	if err != nil || !reflect.DeepEqual(keys, []string{"Up", "Up", "Enter"}) {
		t.Fatalf("option-1 = %v, %v", keys, err)
	}
}

func TestResolveAnswerUnnumberedKeepsDestructivePolicy(t *testing.T) {
	for _, capture := range []string{
		"Run git push --force origin main?\n❯ Yes, run it\n  No, cancel\nEnter to confirm",
		"Run git reset --hard?\nYes, proceed  y\nNo, quit  n",
	} {
		state := Classify(capture, "claude")
		if keys, err := ResolveAnswer(state, "option-1"); err == nil || !strings.Contains(err.Error(), "POLICY_REFUSED") || len(keys) != 0 {
			t.Errorf("destructive acceptance not refused: keys=%v err=%v", keys, err)
		}
		if _, err := ResolveAnswer(state, "decline"); err != nil {
			t.Errorf("decline should remain possible: %v", err)
		}
	}
}

func TestResolveAnswerCursorFailsClosedWithoutUniqueSelection(t *testing.T) {
	for _, capture := range []string{
		strings.Replace(claudeCursorTrustFixture, "❯ No", "  No", 1),
		strings.Replace(claudeCursorTrustFixture, "   Yes", " ❯ Yes", 1),
	} {
		if keys, err := ResolveAnswer(Classify(capture, "claude"), "option-2"); err == nil || len(keys) != 0 {
			t.Errorf("ambiguous cursor allowed actuation: keys=%v err=%v", keys, err)
		}
	}
}

func TestResolveAnswerRefusesBadChoices(t *testing.T) {
	state := Classify(trustPromptFixture+"\nOld dialog answered\n"+claudeCursorTrustFixture, "claude")
	for _, choice := range []string{"option-0", "option-3", "option-9999999999999999999999999", "approve"} {
		if keys, err := ResolveAnswer(state, choice); err == nil || len(keys) != 0 {
			t.Errorf("invalid choice %q actuated: keys=%v err=%v", choice, keys, err)
		}
	}
	if _, err := ResolveAnswer(state, "approve"); err == nil || err.Error() != `unknown choice "approve"; use decline, extra-usage, dismiss, or option-K` {
		t.Errorf("approve = %v", err)
	}
}

func TestVerifyCursorSelectionBeforeEnter(t *testing.T) {
	before := Classify(claudeCursorTrustFixture, "claude")
	selected := strings.Replace(claudeCursorTrustFixture, "❯ No, exit\n   Yes, I trust this folder", "  No, exit\n ❯ Yes, I trust this folder", 1)
	if err := VerifyCursorSelection(before, Classify(selected, "claude"), "option-2"); err != nil {
		t.Fatalf("correctly selected option refused: %v", err)
	}
	for _, capture := range []string{
		claudeCursorTrustFixture,      // dropped Down: decline remains selected
		idleClaudeFixture,             // original dialog vanished
		antigravityCursorTrustFixture, // option order changed
		strings.Replace(selected, "trust this folder", "trust a different folder", 1),
		"git reset --hard\n" + selected, // destructive follow-up
	} {
		if err := VerifyCursorSelection(before, Classify(capture, "claude"), "option-2"); err == nil {
			t.Errorf("unverified Enter was permitted: %q", capture)
		}
	}
	if err := VerifyCursorSelection(before, before, "decline"); err != nil {
		t.Fatalf("already-selected decline refused: %v", err)
	}
}

func TestAdaptKeys(t *testing.T) {
	limbo := State{Class: PasteLimbo}
	if got := AdaptKeys([]string{"Escape"}, limbo, "omp"); len(got) != 1 || got[0] != "C-c" {
		t.Errorf("omp paste-limbo dismiss keys = %v, want [C-c]", got)
	}
	if got := AdaptKeys([]string{"Escape"}, limbo, "codex"); len(got) != 1 || got[0] != "Escape" {
		t.Errorf("codex paste-limbo dismiss keys = %v, want [Escape]", got)
	}
	if got := AdaptKeys([]string{"2", "Enter"}, State{Class: TrustPrompt}, "omp"); !reflect.DeepEqual(got, []string{"2", "Enter"}) {
		t.Errorf("omp trust keys = %v", got)
	}
}

func TestHerdrKey(t *testing.T) {
	for ntm, herdr := range map[string]string{"Escape": "esc", "Enter": "enter", "Up": "up", "Down": "down", "C-c": "ctrl+c", "3": "3", "y": "y"} {
		if got := HerdrKey(ntm); got != herdr {
			t.Errorf("HerdrKey(%q) = %q, want %q", ntm, got, herdr)
		}
	}
}
