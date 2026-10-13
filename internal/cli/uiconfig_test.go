package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSurfaceEngine(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	path := filepath.Join(cfg, "nhm", "config.toml")
	if e, err := surfaceEngine("menu"); err != nil || e != engineFzf {
		t.Fatalf("no config = %q %v", e, err)
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
	write("[reap]\nkeep = [\"IcyKnoll\"]\n\n[ui]\nmenu = \"go\"\nboard = \"fzf\"\n")
	for surface, want := range map[string]string{"menu": engineGo, "board": engineFzf, "palette": engineFzf} {
		if e, err := surfaceEngine(surface); err != nil || e != want {
			t.Errorf("%s = %q %v, want %q", surface, e, err, want)
		}
	}
	if _, err := surfaceEngine("spawn"); err == nil {
		t.Error("an unknown surface was read")
	}
	// A typo is an error, never the other engine.
	for _, bad := range []string{"[ui]\nmenu = \"Go\"\n", "[ui]\nmneu = \"go\"\n", "[ui]\nmenu = true\n", "[ui\n"} {
		write(bad)
		if e, err := surfaceEngine("menu"); err == nil {
			t.Errorf("%q gave %q", bad, e)
		}
	}
}
