package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBindVerb(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles", "herdr.toml")
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte("# mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A chain: config.toml -> mid.toml -> dotfiles/herdr.toml.
	mid := filepath.Join(dir, "mid.toml")
	if err := os.Symlink(filepath.Join("dotfiles", "herdr.toml"), mid); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.toml")
	if err := os.Symlink("mid.toml", link); err != nil {
		t.Fatal(err)
	}

	code, out, _ := runVerb(runBind, "--config", link, "--dry-run")
	if code != exitOK || !strings.Contains(out, `command = "nhm.board"`) {
		t.Fatalf("dry run = %d %q", code, out)
	}
	if data, _ := os.ReadFile(real); string(data) != "# mine\n" {
		t.Fatalf("--dry-run wrote %q", data)
	}

	if code, _, errOut := runVerb(runBind, "--config", link, "--no-reload"); code != exitOK {
		t.Fatalf("bind = %d %s", code, errOut)
	}
	for _, l := range []string{link, mid} {
		if info, err := os.Lstat(l); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("bind replaced the link %s", l)
		}
	}
	info, _ := os.Stat(real)
	data, _ := os.ReadFile(real)
	if info.Mode().Perm() != 0o600 || !strings.HasPrefix(string(data), "# mine\n") ||
		!strings.Contains(string(data), `key = "prefix+m"`) {
		t.Fatalf("bound file (mode %v) = %q", info.Mode().Perm(), data)
	}
	if _, _, errOut := runVerb(runBind, "--config", link, "--no-reload"); !strings.Contains(errOut, "already as it should be") {
		t.Errorf("second bind: %s", errOut)
	}

	if code, _, errOut := runVerb(runBind, "--config", link, "--unbind", "--no-reload"); code != exitOK {
		t.Fatalf("unbind = %d %s", code, errOut)
	}
	if data, _ := os.ReadFile(real); string(data) != "# mine\n" {
		t.Errorf("unbind left %q", data)
	}

	taken := filepath.Join(dir, "taken.toml")
	if err := os.WriteFile(taken, []byte("[[keys.command]]\nkey = \"prefix+d\"\ntype = \"shell\"\ncommand = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runVerb(runBind, "--config", taken, "--no-reload"); code != exitRefused {
		t.Errorf("a taken key: code %d, want %d", code, exitRefused)
	}

	code, out, _ = runVerb(runBind, "--print-client")
	if code != exitOK || !strings.Contains(out, "# >>> nhm >>>") {
		t.Errorf("--print-client = %d %q", code, out)
	}
	if code, _, _ := runVerb(runBind, "--print-client", "--unbind"); code != exitRefused {
		t.Errorf("--print-client --unbind: code %d", code)
	}
}
