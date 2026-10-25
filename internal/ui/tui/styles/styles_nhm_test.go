package styles

import "testing"

// ntm's NTM_ANIMATIONS has no effect: nhm reads NHM_ANIMATIONS
// (nhm-2p6.28.3; the operator, 2026-10-24: never ntm's names). CI turns
// animations off, and only nhm's name turns them back on.
func TestNtmAnimationNamesHaveNoEffect(t *testing.T) {
	t.Setenv("NHM_ANIMATIONS", "")
	t.Setenv("NHM_REDUCE_MOTION", "")
	t.Setenv("NHM_NO_COLOR", "")
	t.Setenv("CI", "1")
	t.Setenv("NTM_ANIMATIONS", "1")
	if AnimationsEnabled() {
		t.Fatal("NTM_ANIMATIONS=1 turned animations on")
	}
	t.Setenv("NHM_ANIMATIONS", "1")
	if !AnimationsEnabled() {
		t.Fatal("NHM_ANIMATIONS=1 did not turn animations on")
	}
}
