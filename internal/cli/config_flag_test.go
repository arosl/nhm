package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/integrations/kinds"
)

// kindConfig writes an nhm config whose [kinds.*] table adds kind.
func kindConfig(t *testing.T, kind string) string {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfg, []byte("[kinds."+kind+"]\nntm_type = \""+kind+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// --config names a config whose kinds spawn takes as flags, before or
// after the verb; a "--config" that is another flag's value stays that
// value. NHM_CONFIG and the config's kinds end with the call.
func TestConfigFlagAddsItsKindFlags(t *testing.T) {
	t.Setenv("NHM_CONFIG", "")
	cfg := kindConfig(t, "zzcfg")
	t.Chdir(t.TempDir()) // no project: a dry run stops there
	for _, c := range []struct {
		args    []string
		unknown bool
	}{
		{[]string{"spawn", "s", "--zzcfg=1", "--dry-run"}, true},
		{[]string{"--config", cfg, "spawn", "s", "--zzcfg=1", "--dry-run"}, false},
		{[]string{"spawn", "s", "--zzcfg", "1", "--config=" + cfg, "--dry-run"}, false},
		{[]string{"spawn", "s", "--prompt", "--config", cfg, "--zzcfg=1", "--dry-run"}, true},
	} {
		_, _, errOut := run(c.args...)
		if strings.Contains(errOut, "unknown flag: --zzcfg") != c.unknown {
			t.Errorf("%v: stderr %q", c.args, errOut)
		}
		if v := os.Getenv("NHM_CONFIG"); v != "" {
			t.Errorf("%v: NHM_CONFIG outlived the run: %q", c.args, v)
		}
		if slices.Contains(kinds.Names(), "zzcfg") {
			t.Errorf("%v: the config's kinds outlived the run", c.args)
		}
	}
}
