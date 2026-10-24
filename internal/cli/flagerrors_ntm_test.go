// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/cli/root_flag_hints_test.go; see LICENSE in this directory and
// PROVENANCE.md. Local changes: none beyond this header and the comments.
package cli

import (
	"testing"

	"github.com/spf13/pflag"
)

// curatedFlagHint backs the robot INVALID_FLAG hints and the did-you-mean
// of flagError. Exact curated keys and their typos one edit away hit the
// curated hint (the edit-distance suggester picks red herrings for them:
// --sesion's nearest flag on send is --json); the rest falls through.
func TestCuratedFlagHint(t *testing.T) {
	tests := []struct {
		unknown string
		wantHit bool
	}{
		{"session", true},
		{"sesion", true},   // 1 edit from "session"
		{"sessions", true}, // 1 edit from "session"
		{"message", true},
		{"messge", true}, // 1 edit from "message"
		{"pane", true},
		{"timeout", true},
		{"project-dir", true},
		{"msg", true},
		{"pnae", false},          // 2 edits from "pane": the flag suggester's
		{"robot-state", false},   // a near-miss robot flag: the flag suggester's
		{"totally-novel", false}, //
		{"", false},
	}
	for _, tt := range tests {
		hint, ok := curatedFlagHint(tt.unknown)
		if ok != tt.wantHit {
			t.Errorf("curatedFlagHint(%q) hit = %v, want %v (hint %q)", tt.unknown, ok, tt.wantHit, hint)
		}
		if ok && hint == "" {
			t.Errorf("curatedFlagHint(%q) hit with empty hint", tt.unknown)
		}
	}
}

// flagErrorGuidance's order: the exact curated hint; then a real flag one
// edit away (a verb with --session answers --sesion with its own flag);
// then the fuzzy curated hint; then the general suggester.
func TestFlagErrorGuidancePrecedence(t *testing.T) {
	withSession := pflag.NewFlagSet("withSession", pflag.ContinueOnError)
	withSession.String("session", "", "")
	withSession.Bool("json", false, "")

	sendLike := pflag.NewFlagSet("sendLike", pflag.ContinueOnError)
	sendLike.Bool("json", false, "")
	sendLike.String("panes", "", "")

	// The exact curated key wins even when the verb has a near flag.
	if hint, sugg := flagErrorGuidance("session", sendLike); hint == "" || sugg != "" {
		t.Errorf("exact curated: got hint=%q sugg=%q", hint, sugg)
	}
	// A typo of a real flag of this verb: suggest the real flag.
	if hint, sugg := flagErrorGuidance("sesion", withSession); hint != "" || sugg != "session" {
		t.Errorf("real-flag typo: got hint=%q sugg=%q, want suggestion 'session'", hint, sugg)
	}
	// A typo of a curated key with no near real flag: the curated hint,
	// not --json.
	if hint, sugg := flagErrorGuidance("sesion", sendLike); hint == "" || sugg != "" {
		t.Errorf("fuzzy curated: got hint=%q sugg=%q, want curated hint", hint, sugg)
	}
	// A plain near-miss of a real flag: the general suggester.
	if hint, sugg := flagErrorGuidance("pane", sendLike); hint != flagMisGuessHints["pane"] || sugg != "" {
		t.Errorf("exact curated 'pane': got hint=%q sugg=%q", hint, sugg)
	}
	if hint, sugg := flagErrorGuidance("jsno", sendLike); hint != "" || sugg != "json" {
		t.Errorf("suggester: got hint=%q sugg=%q, want suggestion 'json'", hint, sugg)
	}
	// Nothing close: no guidance.
	if hint, sugg := flagErrorGuidance("zzzzzzzzzz", sendLike); hint != "" || sugg != "" {
		t.Errorf("no match: got hint=%q sugg=%q", hint, sugg)
	}
}

// The fuzzy match gives the same answer each time, whatever the map
// order.
func TestCuratedFlagHintDeterministic(t *testing.T) {
	first, ok := curatedFlagHint("sesion")
	if !ok {
		t.Fatal("expected curated hit for 'sesion'")
	}
	for i := 0; i < 20; i++ {
		if got, _ := curatedFlagHint("sesion"); got != first {
			t.Fatalf("curatedFlagHint non-deterministic: %q then %q", first, got)
		}
	}
}
