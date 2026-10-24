package kinds

import (
	"os"
	"path/filepath"
	"slices"
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

// Default reads the file that NHM_CONFIG names, and reads again when it
// names another: the root's --config sets it after the registry may have
// been read.
func TestDefaultRereadsANewConfigPath(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.toml"), filepath.Join(dir, "b.toml")
	for path, kind := range map[string]string{a: "zza", b: "zzb"} {
		if err := os.WriteFile(path, []byte("[kinds."+kind+"]\nntm_type = \""+kind+"\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	has := func(name string) bool { return slices.Contains(Names(), name) }
	t.Setenv("NHM_CONFIG", a)
	if !has("zza") || has("zzb") {
		t.Fatalf("NHM_CONFIG a: %v", Names())
	}
	t.Setenv("NHM_CONFIG", b)
	if has("zza") || !has("zzb") {
		t.Fatalf("NHM_CONFIG b: %v", Names())
	}
	t.Setenv("NHM_CONFIG", filepath.Join(dir, "none.toml"))
	if has("zza") || has("zzb") || ConfigError() != nil {
		t.Fatalf("no file: %v, %v", Names(), ConfigError())
	}
}
