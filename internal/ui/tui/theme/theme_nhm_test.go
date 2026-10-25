package theme

import (
	"os"
	"testing"
)

// ntm's NTM_* names have no effect: nhm reads its own NHM_* names
// (nhm-2p6.28.3; the operator, 2026-10-24: never ntm's names).
func TestNtmEnvironmentNamesHaveNoEffect(t *testing.T) {
	for _, key := range []string{"NHM_NO_COLOR", "NHM_THEME", "NHM_BACKGROUND", "NHM_BG", "TERMINAL_BACKGROUND"} {
		t.Setenv(key, "")
	}
	// NO_COLOR counts when set at all, empty too; t.Setenv restores it.
	t.Setenv("NO_COLOR", "")
	if err := os.Unsetenv("NO_COLOR"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_NO_COLOR", "1")
	t.Setenv("NTM_THEME", "nord")
	t.Setenv("NTM_BACKGROUND", "light")
	t.Setenv("NTM_BG", "light")
	SetConfigured("")
	if NoColorEnabled() {
		t.Error("NTM_NO_COLOR=1 disabled colours")
	}
	if Current() == Nord {
		t.Error("NTM_THEME=nord chose the theme")
	}
	if _, ok := explicitDarkBackground(); ok {
		t.Error("NTM_BACKGROUND or NTM_BG set the background")
	}
	t.Setenv("NHM_THEME", "nord")
	t.Setenv("NHM_BG", "light")
	if Current() != Nord {
		t.Error("NHM_THEME=nord did not choose the theme")
	}
	if dark, ok := explicitDarkBackground(); !ok || dark {
		t.Errorf("NHM_BG=light: dark %v, set %v", dark, ok)
	}
}
