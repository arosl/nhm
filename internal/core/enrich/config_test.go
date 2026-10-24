package enrich

import (
	"path/filepath"
	"testing"
)

// NHM_CONFIG, which the root's --config sets for a call, names nhm's
// config file; without it the file is under XDG_CONFIG_HOME.
func TestConfigPathHonoursNHMConfig(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("NHM_CONFIG", "")
	if p, err := ConfigPath(); err != nil || p != filepath.Join(xdg, "nhm", "config.toml") {
		t.Errorf("default: %q, %v", p, err)
	}
	t.Setenv("NHM_CONFIG", "/elsewhere/nhm.toml")
	if p, err := ConfigPath(); err != nil || p != "/elsewhere/nhm.toml" {
		t.Errorf("NHM_CONFIG: %q, %v", p, err)
	}
}
