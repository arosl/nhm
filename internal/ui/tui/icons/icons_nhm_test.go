package icons

import "testing"

// ntm's NTM_ICONS and NTM_USE_ICONS have no effect: nhm reads NHM_ICONS
// and NHM_USE_ICONS (nhm-2p6.28.3; the operator, 2026-10-24: never ntm's
// names).
func TestNtmIconNamesHaveNoEffect(t *testing.T) {
	t.Setenv("NHM_ICONS", "")
	t.Setenv("NHM_USE_ICONS", "")
	t.Setenv("NERD_FONTS", "")
	t.Setenv("NTM_ICONS", "nerd")
	t.Setenv("NTM_USE_ICONS", "1")
	if got := Detect(); got.Session != ASCII.Session {
		t.Errorf("NTM_ICONS=nerd, NTM_USE_ICONS=1: session %q, want ASCII's %q", got.Session, ASCII.Session)
	}
}
