// Taken from ntm (github.com/Dicklesworthstone/ntm) internal/tui/theme/configured_test.go
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: this header; nhm's NHM_* environment names in place of
// ntm's NTM_* (nhm-2p6.28.3).

package theme

import "testing"

// The configured theme is the fallback for every Current() caller when
// NHM_THEME is unset; an explicit NHM_THEME still wins.
func TestCurrentFallsBackToConfiguredTheme(t *testing.T) {
	t.Setenv("NHM_THEME", "")
	t.Setenv("NHM_NO_COLOR", "0")
	withDetector(t, func() bool { return true })
	t.Cleanup(func() { SetConfigured("") })

	SetConfigured("latte")
	if got := Current(); got.Base != CatppuccinLatte.Base {
		t.Fatalf("Current() with configured latte and no NHM_THEME = base %s, want Latte", got.Base)
	}

	t.Setenv("NHM_THEME", "nord")
	if got := Current(); got.Base != Nord.Base {
		t.Fatalf("Current() must prefer NHM_THEME over the configured theme, got base %s", got.Base)
	}

	t.Setenv("NHM_THEME", "   ")
	SetConfigured("  latte ")
	if got := Current(); got.Base != CatppuccinLatte.Base {
		t.Fatalf("blank NHM_THEME must fall back to the trimmed configured name, got base %s", got.Base)
	}

	SetConfigured("")
	if got := Current(); got.Base != CatppuccinMocha.Base {
		t.Fatalf("clearing the configured theme must restore auto-detection (dark), got base %s", got.Base)
	}
}
