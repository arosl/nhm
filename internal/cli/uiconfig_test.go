package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// [ui] = "go" (aro-acf's config) still loads and does nothing; "fzf"
// says plainly that the fzf surfaces are gone (nhm-qbi.12).
func TestCheckUIConfig(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	path := filepath.Join(cfg, "nhm", "config.toml")
	if err := checkUIConfig(); err != nil {
		t.Fatalf("no config = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, good := range []string{"[reap]\nkeep = [\"IcyKnoll\"]\n", "[ui]\nmenu = \"go\"\npalette = \"go\"\nboard = \"go\"\n"} {
		write(good)
		if err := checkUIConfig(); err != nil {
			t.Errorf("%q = %v", good, err)
		}
	}
	write("[ui]\nmenu = \"go\"\nboard = \"fzf\"\n")
	if err := checkUIConfig(); err == nil || !strings.Contains(err.Error(), `ui.board is "fzf", but nhm no longer has the fzf`) {
		t.Errorf("fzf = %v", err)
	}
	// A typo is an error.
	for _, bad := range []string{"[ui]\nmenu = \"Go\"\n", "[ui]\nmneu = \"go\"\n", "[ui]\nmenu = true\n", "[ui]\nspawn = \"go\"\n", "[ui\n"} {
		write(bad)
		if err := checkUIConfig(); err == nil {
			t.Errorf("%q loaded", bad)
		}
	}
}
