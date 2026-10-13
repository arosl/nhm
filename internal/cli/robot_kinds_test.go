package cli

import (
	"slices"
	"testing"
)

// ntm's --type takes a kind's name, its short flag or ntm's type name;
// nhm's robot forms map each to nhm's kind, and leave another value as
// given, so it matches no agent (review of 9b97ea1).
func TestRobotKinds(t *testing.T) {
	got := robotKinds([]string{"cc", "CLAUDE", "cod", "antigravity", "agy", "bogus"})
	want := []string{"claude", "claude", "codex", "agy", "agy", "bogus"}
	if !slices.Equal(got, want) {
		t.Fatalf("robotKinds = %v, want %v", got, want)
	}
}
