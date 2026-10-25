// Taken from ntm (github.com/Dicklesworthstone/ntm) internal/tui/styles/styles_theme_test.go
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: this header; the import path; nhm's NHM_* environment
// names in place of ntm's NTM_* (nhm-2p6.28.3).

package styles

import (
	"os"
	"reflect"
	"testing"

	"github.com/arosl/nhm/internal/ui/tui/theme"
)

func TestDefaultGradientUsesCurrentThemeColors(t *testing.T) {
	t.Setenv("NHM_THEME", "latte")
	t.Setenv("NHM_NO_COLOR", "0")

	got := defaultGradient()
	want := []string{
		string(theme.CatppuccinLatte.Blue),
		string(theme.CatppuccinLatte.Mauve),
		string(theme.CatppuccinLatte.Pink),
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("defaultGradient() = %v, want %v", got, want)
	}
}

func TestDefaultSurface1UsesCurrentThemeColor(t *testing.T) {
	t.Setenv("NHM_THEME", "latte")
	t.Setenv("NHM_NO_COLOR", "0")

	got := string(defaultSurface1())
	want := string(theme.CatppuccinLatte.Surface1)

	if got != want {
		t.Fatalf("defaultSurface1() = %s, want %s", got, want)
	}
}

func TestDefaultGradientFollowsThemeChange(t *testing.T) {
	t.Setenv("NHM_THEME", "mocha")
	t.Setenv("NHM_NO_COLOR", "0")

	got := defaultGradient()
	want := []string{
		string(theme.CatppuccinMocha.Blue),
		string(theme.CatppuccinMocha.Mauve),
		string(theme.CatppuccinMocha.Pink),
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after theme change, defaultGradient() = %v, want %v", got, want)
	}
}

// Ensure we don’t leak env between tests when running without -count=1.
func TestMain(m *testing.M) {
	code := m.Run()
	_ = os.Unsetenv("NHM_THEME")
	_ = os.Unsetenv("NHM_NO_COLOR")
	os.Exit(code)
}
