package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/ui"
)

// bindConfig is a herdr config file and nhm's ownership, for nhm bind.
func bindConfig(t *testing.T) string {
	t.Helper()
	fakeHerdr(t)
	ownedInstance(t)
	return filepath.Join(t.TempDir(), "config.toml")
}

func boundKeys(t *testing.T, cfg string) string {
	t.Helper()
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, k := range ui.BlockKeys(string(data)) {
		out = append(out, k.Key+"="+k.Action)
	}
	return strings.Join(out, " ")
}

// ntm's --key K binds K to the palette as well as nhm's keys, with ntm's
// lines; a later nhm bind keeps it; the same key twice is there once.
func TestBindKeyFlag(t *testing.T) {
	cfg := bindConfig(t)
	code, out, errOut := runVerb(runBind, "--config", cfg, "--no-reload", "--key", "F5")
	want := "✓ Bound F5 in herdr's config " + cfg + "\n\n  Press F5 in herdr to open the command palette.\n"
	if code != exitOK || out != want {
		t.Fatalf("bind --key F5 = %d %q %q", code, out, errOut)
	}
	if got := boundKeys(t, cfg); got != "prefix+a=nhm.palette prefix+d=nhm.board prefix+m=nhm.menu F5=nhm.palette" {
		t.Fatalf("keys = %s", got)
	}
	data, _ := os.ReadFile(cfg)
	if !strings.Contains(string(data), "# F5: the command palette (nhm bind --key)\n") {
		t.Fatalf("no tagged comment:\n%s", data)
	}
	// A bare nhm bind (as acfs update runs it) keeps the key.
	if code, _, errOut := runVerb(runBind, "--config", cfg, "--no-reload"); code != exitOK || !strings.Contains(errOut, "already as it should be") {
		t.Fatalf("bare bind = %d %q", code, errOut)
	}
	if code, out, _ := runVerb(runBind, "--config", cfg, "--no-reload", "-k", "F7"); code != exitOK || !strings.HasPrefix(out, "✓ Bound F7") {
		t.Fatalf("bind -k F7 = %d %q", code, out)
	}
	if code, out, _ := runVerb(runBind, "--config", cfg, "--no-reload", "--key", "F5"); code != exitOK || !strings.HasPrefix(out, "✓ Bound F5") {
		t.Fatalf("bind --key F5 again = %d %q", code, out)
	}
	if got := boundKeys(t, cfg); got != "prefix+a=nhm.palette prefix+d=nhm.board prefix+m=nhm.menu F7=nhm.palette F5=nhm.palette" {
		t.Fatalf("keys = %s", got)
	}
}

// --overlay binds the board to F12, or to --key; a palette --key takes
// the key from the board.
func TestBindOverlay(t *testing.T) {
	cfg := bindConfig(t)
	code, out, _ := runVerb(runBind, "--config", cfg, "--no-reload", "--overlay")
	if code != exitOK || out != "✓ Bound F12 in herdr's config "+cfg+"\n\n  Press F12 in herdr to open the agent board.\n" {
		t.Fatalf("--overlay = %d %q", code, out)
	}
	if code, _, _ := runVerb(runBind, "--config", cfg, "--no-reload", "--overlay", "-k", "F11"); code != exitOK {
		t.Fatalf("--overlay -k F11 = %d", code)
	}
	if code, _, _ := runVerb(runBind, "--config", cfg, "--no-reload", "--key", "F12"); code != exitOK {
		t.Fatalf("--key F12 = %d", code)
	}
	if got := boundKeys(t, cfg); got != "prefix+a=nhm.palette prefix+d=nhm.board prefix+m=nhm.menu F11=nhm.board F12=nhm.palette" {
		t.Fatalf("keys = %s", got)
	}
}

// ntm's key check: letters, digits, - and ^; and --key does not go with
// --print-client.
func TestBindRefusesBadKey(t *testing.T) {
	cfg := bindConfig(t)
	for _, k := range []string{"F5;rm -rf /", "prefix+x", ""} {
		code, _, errOut := runVerb(runBind, "--config", cfg, "--no-reload", "--key", k)
		if code != exitRefused || !strings.Contains(errOut, "invalid key format: \""+k+"\" (allowed: a-z, 0-9, -, ^)") {
			t.Errorf("--key %q = %d %q", k, code, errOut)
		}
	}
	if code, _, errOut := runVerb(runBind, "--key", "F5", "--print-client"); code != exitRefused || !strings.Contains(errOut, "Usage: nhm bind") {
		t.Fatalf("--key --print-client = %d %q", code, errOut)
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Fatalf("a refused bind wrote the config: %v", err)
	}
}

// --show --key says whether the key is bound; --unbind --key removes that
// key alone, records no opt-out, and refuses a key of [keys].
func TestBindShowAndUnbindKey(t *testing.T) {
	cfg := bindConfig(t)
	nhmKeys(t, "palette = \"F4\"\n")
	if code, _, errOut := runVerb(runBind, "--config", cfg, "--no-reload", "--key", "F5"); code != exitOK {
		t.Fatalf("bind --key F5 = %d %q", code, errOut)
	}
	code, out, _ := runVerb(runBind, "--config", cfg, "--show", "--key", "F5")
	if code != exitOK || out != "✓ Found binding:\n  key = \"F5\", command = \"nhm.palette\" (the command palette (nhm bind --key))\n" {
		t.Fatalf("--show --key F5 = %d %q", code, out)
	}
	if code, out, _ := runVerb(runBind, "--config", cfg, "--show", "-k", "F9"); code != exitOK ||
		out != "→ No F9 binding found in herdr's config\n\n  Run nhm bind --key F9 to set it up.\n" {
		t.Fatalf("--show -k F9 = %d %q", code, out)
	}
	if code, out, _ := runVerb(runBind, "--config", cfg, "--show"); code != exitOK || !strings.Contains(out, "nhm's keys: bound\n") ||
		!strings.Contains(out, "F5 palette") {
		t.Fatalf("--show = %d %q", code, out)
	}
	if code, out, _ := runVerb(runBind, "--config", cfg, "--no-reload", "--unbind", "--key", "F5"); code != exitOK ||
		out != "✓ Removed F5 binding from "+cfg+"\n" {
		t.Fatalf("--unbind --key F5 = %d %q", code, out)
	}
	data, _ := os.ReadFile(cfg)
	if got := boundKeys(t, cfg); got != "F4=nhm.palette prefix+d=nhm.board prefix+m=nhm.menu" || ui.OptOutBy(string(data)) != "" {
		t.Fatalf("after --unbind --key: %s, opt-out %q\n%s", got, ui.OptOutBy(string(data)), data)
	}
	if code, out, _ := runVerb(runBind, "--config", cfg, "--no-reload", "--unbind", "--key", "F5"); code != exitOK ||
		out != "→ No F5 binding found in herdr's config\n" {
		t.Fatalf("--unbind --key F5 again = %d %q", code, out)
	}
	if code, _, errOut := runVerb(runBind, "--config", cfg, "--no-reload", "--unbind", "--key", "F4"); code != exitRefused ||
		!strings.Contains(errOut, "F4 is the key of the command palette in [keys] of nhm's config") {
		t.Fatalf("--unbind --key F4 = %d %q", code, errOut)
	}
}
