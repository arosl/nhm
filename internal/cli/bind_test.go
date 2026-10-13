package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/ui"
)

// fakeHerdr is a herdr whose `config check` reports "unknown config key
// legacy" for a file with a `legacy =` line (an issue the user already
// had), and, with FAKE_REJECT=1, "key conflict" for a file that binds
// nhm.board (an issue the new block would add). FAKE_TOUCH=FILE makes the
// check append to FILE (someone editing it during the bind);
// FAKE_RELOAD_LOG=FILE records the socket each reload-config went to.
func fakeHerdr(t *testing.T) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "herdr")
	script := `#!/bin/sh
if [ "$1 $2" = "server reload-config" ]; then
  [ -z "$FAKE_RELOAD_LOG" ] || echo "$HERDR_SOCKET_PATH" >> "$FAKE_RELOAD_LOG"
  exit 0
fi
[ "$1 $2" = "config check" ] || exit 2
[ -z "$FAKE_TOUCH" ] || echo "# edited" >> "$FAKE_TOUCH"
issues=""
grep -q '^legacy =' "$HERDR_CONFIG_PATH" 2>/dev/null && issues="unknown config key legacy in $HERDR_CONFIG_PATH"
if [ "$FAKE_REJECT" = 1 ] && grep -q 'nhm.board' "$HERDR_CONFIG_PATH"; then issues="$issues
key conflict: prefix+d"; fi
if [ -z "$issues" ]; then echo "config: ok"; exit 0; fi
echo "config: issues found"; printf '%s\n' "$issues" | sed '/^$/d'; exit 1
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_BIN_PATH", bin)
}

// ownedInstance is an enrolled instance that nhm owns, with its socket
// in HERDR_SOCKET_PATH; it returns the socket.
func ownedInstance(t *testing.T) string {
	t.Helper()
	s, inst := cliInstance(t)
	if _, err := core.SwitchOwner(context.Background(), inst.Dir, core.OwnerNhm, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_SOCKET_PATH", s.SocketPath)
	return s.SocketPath
}

func TestBindNeedsNhmAsOwner(t *testing.T) {
	fakeHerdr(t)
	s, inst := cliInstance(t)
	t.Setenv("HERDR_SOCKET_PATH", s.SocketPath)
	cfg := filepath.Join(t.TempDir(), "config.toml")
	acfs := "# >>> acfs palette >>>\n[[keys.command]]\nkey = \"prefix+a\"\ntype = \"popup\"\ncommand = \"acfs palette\"\n# <<< acfs palette <<<\n"
	if err := os.WriteFile(cfg, []byte(acfs), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runVerb(runBind, "--config", cfg, "--no-reload")
	if code != exitRefused || !strings.Contains(errOut, "ACFS's Bash owns") || !strings.Contains(errOut, "takeover") {
		t.Fatalf("bind under the Bash = %d %s", code, errOut)
	}
	if data, _ := os.ReadFile(cfg); string(data) != acfs {
		t.Fatalf("a refused bind changed the file: %q", data)
	}
	// --unbind removes only nhm's block, so it runs under the Bash too
	// and keeps ACFS's palette; it records the opt-out after it.
	if code, _, errOut := runVerb(runBind, "--config", cfg, "--unbind", "--no-reload"); code != exitOK {
		t.Fatalf("unbind under the Bash = %d %s", code, errOut)
	}
	optedOut, _ := os.ReadFile(cfg)
	if !strings.HasPrefix(string(optedOut), acfs) || ui.OptOutBy(string(optedOut)) != "nhm bind --unbind" {
		t.Fatalf("unbind touched ACFS's block or left no opt-out: %q", optedOut)
	}
	// The opt-out is the answer before the owner check.
	if code, _, errOut := runVerb(runBind, "--config", cfg, "--no-reload"); code != exitOptedOut || strings.Contains(errOut, "Bash") {
		t.Fatalf("bind under the Bash after the opt-out = %d %s", code, errOut)
	}
	t.Setenv("HERDR_SOCKET_PATH", "")
	if code, _, errOut := runVerb(runBind, "--config", cfg, "--force", "--no-reload"); code != exitRefused || !strings.Contains(errOut, "--socket") {
		t.Fatalf("bind without a server = %d %s", code, errOut)
	}
	if _, err := core.SwitchOwner(context.Background(), inst.Dir, core.OwnerNhm, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runVerb(runBind, "--config", cfg, "--socket", s.SocketPath, "--no-reload"); code != exitOptedOut {
		t.Fatalf("bind once nhm owns it, after the opt-out = %d %s", code, errOut)
	}
	if data, _ := os.ReadFile(cfg); string(data) != string(optedOut) {
		t.Fatalf("an opted-out bind changed the file: %q", data)
	}
	if code, _, errOut := runVerb(runBind, "--config", cfg, "--socket", s.SocketPath, "--force", "--no-reload"); code != exitOK {
		t.Fatalf("bind --force once nhm owns it = %d %s", code, errOut)
	}
	if data, _ := os.ReadFile(cfg); strings.Contains(string(data), "acfs palette") || !strings.Contains(string(data), "nhm.palette") || ui.OptOutBy(string(data)) != "" {
		t.Fatalf("bind --force did not replace ACFS's block and the opt-out: %q", data)
	}
}

func TestBindVerb(t *testing.T) {
	fakeHerdr(t)
	ownedInstance(t)
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
	if data, _ := os.ReadFile(real); !strings.HasPrefix(string(data), "# mine\n") || strings.Contains(string(data), "keys.command") ||
		ui.OptOutBy(string(data)) != "nhm bind --unbind" {
		t.Errorf("unbind left %q", data)
	}

	code, out, _ = runVerb(runBind, "--print-client")
	if code != exitOK || !strings.Contains(out, "# >>> nhm >>>") {
		t.Errorf("--print-client = %d %q", code, out)
	}
	if code, _, _ := runVerb(runBind, "--print-client", "--unbind"); code != exitRefused {
		t.Errorf("--print-client --unbind: code %d", code)
	}
}

func TestBindWritesThroughADanglingLink(t *testing.T) {
	fakeHerdr(t)
	ownedInstance(t)
	dir := t.TempDir()
	link := filepath.Join(dir, "config.toml")
	if err := os.Symlink(filepath.Join("dotfiles", "herdr.toml"), link); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runVerb(runBind, "--config", link, "--no-reload"); code != exitOK {
		t.Fatalf("bind = %d %s", code, errOut)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("bind replaced the dangling link with a file")
	}
	if data, err := os.ReadFile(filepath.Join(dir, "dotfiles", "herdr.toml")); err != nil || !strings.Contains(string(data), "nhm.menu") {
		t.Fatalf("the link's target = %q, %v", data, err)
	}
}

func TestBindRefusals(t *testing.T) {
	fakeHerdr(t)
	ownedInstance(t)
	dir := t.TempDir()
	for name, content := range map[string]string{
		"single quotes":      "[keys]\nnew_tab = 'prefix+a'\n",
		"an array":           "[keys]\nzoom = [\n  \"ctrl+z\",\n  \"PREFIX+D\",\n]\n",
		"an inline table":    "[keys]\ncommand = [{ key = \"prefix+x\", type = \"shell\", command = \"x\" }]\n",
		"a herdr issue":      "legacy = 1\n",
		"a taken key=string": "[[keys.command]]\nkey = \"prefix+m\"\ntype = \"shell\"\ncommand = \"x\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			if name == "a herdr issue" {
				t.Setenv("FAKE_REJECT", "1")
			}
			cfg := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".toml")
			if err := os.WriteFile(cfg, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			code, _, errOut := runVerb(runBind, "--config", cfg, "--no-reload")
			if code != exitRefused {
				t.Errorf("code %d, want %d: %s", code, exitRefused, errOut)
			}
			if name == "a herdr issue" && (!strings.Contains(errOut, "key conflict") || strings.Contains(errOut, "legacy")) {
				t.Errorf("only the new issue should be named: %s", errOut)
			}
			if data, _ := os.ReadFile(cfg); string(data) != content {
				t.Errorf("a refused bind changed the file: %q", data)
			}
		})
	}
	// An issue the file already had does not refuse.
	cfg := filepath.Join(dir, "old-issue.toml")
	if err := os.WriteFile(cfg, []byte("legacy = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runVerb(runBind, "--config", cfg, "--no-reload"); code != exitOK {
		t.Errorf("an old issue refused the bind: %d %s", code, errOut)
	}
}

// The review of f3ca4fb: ~/.config/herdr -> dotfiles/herdr, and
// config.toml there -> ../shared/config.toml. The kernel opens
// dotfiles/shared/config.toml; so must nhm bind, existing or not.
func TestBindFollowsALinkInsideALinkedDirectory(t *testing.T) {
	fakeHerdr(t)
	ownedInstance(t)
	for _, exists := range []bool{true, false} {
		root := t.TempDir()
		for _, d := range []string{"dotfiles/herdr", "dotfiles/shared", "home/.config"} {
			if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(filepath.Join(root, "dotfiles", "herdr"), filepath.Join(root, "home", ".config", "herdr")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..", "shared", "config.toml"), filepath.Join(root, "dotfiles", "herdr", "config.toml")); err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(root, "dotfiles", "shared", "config.toml")
		if exists {
			if err := os.WriteFile(want, []byte("# shared\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		cfg := filepath.Join(root, "home", ".config", "herdr", "config.toml")
		if code, _, errOut := runVerb(runBind, "--config", cfg, "--no-reload"); code != exitOK {
			t.Fatalf("exists=%v: bind = %d %s", exists, code, errOut)
		}
		if data, err := os.ReadFile(want); err != nil || !strings.Contains(string(data), "nhm.palette") {
			t.Errorf("exists=%v: the file the kernel opens = %q, %v", exists, data, err)
		}
		if _, err := os.Stat(filepath.Join(root, "home", ".config", "shared")); err == nil {
			t.Errorf("exists=%v: bind wrote beside the unresolved directory", exists)
		}
	}
}

func TestBindUsesOneServer(t *testing.T) {
	fakeHerdr(t)
	socket := ownedInstance(t)
	log := filepath.Join(t.TempDir(), "reloads")
	t.Setenv("FAKE_RELOAD_LOG", log)
	cfg := filepath.Join(t.TempDir(), "config.toml")
	if code, _, errOut := runVerb(runBind, "--config", cfg, "--socket", socket); code != exitOK {
		t.Fatalf("bind = %d %s", code, errOut)
	}
	if data, _ := os.ReadFile(log); strings.TrimSpace(string(data)) != socket {
		t.Errorf("reload went to %q, want %q", data, socket)
	}
	// Without --config, an explicit --socket whose config cannot be told
	// is refused, not written to another server's config.
	t.Setenv("HERDR_CONFIG_PATH", "")
	if code, _, errOut := runVerb(runBind, "--socket", socket, "--no-reload"); code != exitRefused || !strings.Contains(errOut, "give --config") {
		t.Errorf("bind --socket without a derivable config = %d %s", code, errOut)
	}
}

func TestServerConfig(t *testing.T) {
	t.Setenv("HERDR_CONFIG_PATH", "")
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "xdg"))
	for socket, want := range map[string]string{
		filepath.Join(base, "a", "herdr", "herdr.sock"):                     filepath.Join(base, "a", "herdr", "config.toml"),
		filepath.Join(base, "a", "herdr", "sessions", "plug", "herdr.sock"): filepath.Join(base, "a", "herdr", "config.toml"),
		"": filepath.Join(base, "xdg", "herdr", "config.toml"),
	} {
		if got, err := serverConfig(socket, socket != ""); err != nil || got != want {
			t.Errorf("serverConfig(%q) = %q, %v; want %q", socket, got, err, want)
		}
	}
	if _, err := serverConfig(filepath.Join(base, "elsewhere.sock"), true); err == nil {
		t.Error("an explicit socket outside a herdr dir gave a config")
	}
	if got, err := serverConfig(filepath.Join(base, "elsewhere.sock"), false); err != nil || got != filepath.Join(base, "xdg", "herdr", "config.toml") {
		t.Errorf("the pane's socket outside a herdr dir = %q, %v", got, err)
	}
	// Only herdr's own layouts count, not any directory named herdr
	// (acfs-9hkh.63).
	for _, socket := range []string{
		filepath.Join(base, "projects", "herdr", "x.sock"),
		filepath.Join(base, "herdr", "deep", "herdr.sock"),
		filepath.Join(base, "herdr", "sessions", "herdr.sock"),
	} {
		if got, err := serverConfig(socket, true); err == nil {
			t.Errorf("serverConfig(%q) = %q; want a refusal", socket, got)
		}
	}
	// HERDR_CONFIG_PATH belongs to the calling pane's server: it counts
	// for that server, not for a --socket that names another.
	pane := filepath.Join(base, "a", "herdr", "herdr.sock")
	other := filepath.Join(base, "b", "herdr", "herdr.sock")
	t.Setenv("HERDR_SOCKET_PATH", pane)
	t.Setenv("HERDR_CONFIG_PATH", filepath.Join(base, "mine.toml"))
	if got, _ := serverConfig(pane, true); got != filepath.Join(base, "mine.toml") {
		t.Errorf("HERDR_CONFIG_PATH did not count for the pane's own server: %q", got)
	}
	if got, _ := serverConfig("", false); got != filepath.Join(base, "mine.toml") {
		t.Errorf("HERDR_CONFIG_PATH did not count without a socket: %q", got)
	}
	if got, err := serverConfig(other, true); err != nil || got != filepath.Join(base, "b", "herdr", "config.toml") {
		t.Errorf("--socket for another server took the pane's HERDR_CONFIG_PATH: %q, %v", got, err)
	}
}

// The state bind compares against is that of the bytes it read: a file
// rewritten between the read and the stat does not match it (acfs-9hkh.63).
func TestStateOfHashesTheBytesRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("a = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if stateOf(path, []byte("a = 1\n")) != fileState(path) {
		t.Error("the state of the bytes on disk differs from fileState")
	}
	if stateOf(path, []byte("a = 2\n")) == fileState(path) {
		t.Error("the state of other bytes matched fileState")
	}
	if stateOf(filepath.Join(t.TempDir(), "gone"), nil) != "" || fileState(filepath.Join(t.TempDir(), "gone")) != "" {
		t.Error("a missing file has a state")
	}
}

func TestBindRefusesAFileChangedDuringTheBind(t *testing.T) {
	fakeHerdr(t)
	ownedInstance(t)
	cfg := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfg, []byte("# mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_TOUCH", cfg)
	code, _, errOut := runVerb(runBind, "--config", cfg, "--no-reload")
	if code != exitRefused || !strings.Contains(errOut, "changed during bind; run again") {
		t.Fatalf("bind = %d %s", code, errOut)
	}
	// The fake check edits the file each time it runs (old and new).
	if data, _ := os.ReadFile(cfg); !strings.HasPrefix(string(data), "# mine\n# edited\n") || strings.Contains(string(data), "nhm") {
		t.Errorf("the edit made during the bind was lost: %q", data)
	}
}

// nhmKeys writes [keys] into nhm's config (under XDG_CONFIG_HOME, which
// cliInstance makes temporary).
func nhmKeys(t *testing.T, table string) {
	t.Helper()
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "nhm", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[keys]\n"+table), 0o644); err != nil {
		t.Fatal(err)
	}
}

// [keys] in nhm's config sets the keys; an empty one binds nothing and
// leaves its default key to the rest of herdr's config; a changed key
// moves (plan-v4 section 2).
func TestBindKeysFromNhmConfig(t *testing.T) {
	fakeHerdr(t)
	ownedInstance(t)
	nhmKeys(t, "palette = \"ctrl+alt+p\"\nboard = \"\"\n")
	cfg := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfg, []byte("[keys]\nzoom = \"prefix+d\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runVerb(runBind, "--config", cfg, "--no-reload")
	if code != exitOK || !strings.Contains(errOut, "ctrl+alt+p palette, board without a key, prefix+m menu") {
		t.Fatalf("bind = %d %s", code, errOut)
	}
	data, _ := os.ReadFile(cfg)
	for _, want := range []string{`zoom = "prefix+d"`, `key = "ctrl+alt+p"`, `command = "nhm.palette"`, `key = "prefix+m"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the bound file lacks %s: %q", want, data)
		}
	}
	if strings.Contains(string(data), `command = "nhm.board"`) || strings.Contains(string(data), `key = "prefix+a"`) {
		t.Errorf("the bound file binds the board or the default palette key: %q", data)
	}
	if code, out, _ := runVerb(runBind, "--print-client"); code != exitOK || !strings.Contains(out, `key = "ctrl+alt+p"`) || strings.Contains(out, `command = "nhm.board"`) {
		t.Errorf("--print-client = %d %q", code, out)
	}

	// Change a key, bind again: it moves.
	nhmKeys(t, "palette = \"prefix+a\"\nboard = \"\"\n")
	if code, _, errOut := runVerb(runBind, "--config", cfg, "--no-reload"); code != exitOK {
		t.Fatalf("bind after the change = %d %s", code, errOut)
	}
	data, _ = os.ReadFile(cfg)
	if strings.Contains(string(data), "ctrl+alt+p") || !strings.Contains(string(data), `key = "prefix+a"`) || strings.Count(string(data), ui.BlockBegin) != 1 {
		t.Fatalf("the key did not move: %q", data)
	}

	// A [keys] that gives one key to two surfaces is refused; --unbind
	// needs no keys and still runs.
	nhmKeys(t, "board = \"prefix+m\"\n")
	if code, _, errOut := runVerb(runBind, "--config", cfg, "--no-reload"); code != exitRefused || !strings.Contains(errOut, "board and menu are both prefix+m") {
		t.Fatalf("bind with a clash in [keys] = %d %s", code, errOut)
	}
	if again, _ := os.ReadFile(cfg); string(again) != string(data) {
		t.Fatalf("a refused bind changed the file: %q", again)
	}
	if code, _, errOut := runVerb(runBind, "--print-client"); code != exitRefused {
		t.Errorf("--print-client with a clash in [keys] = %d %s", code, errOut)
	}
	if code, _, errOut := runVerb(runBind, "--config", cfg, "--unbind", "--no-reload"); code != exitOK {
		t.Errorf("unbind with a clash in [keys] = %d %s", code, errOut)
	}
}

// ACFS's install and acfs update run nhm bind each time: after nhm bind
// --unbind, or ACFS's acfs palette unbind, it binds nothing and exits
// exitOptedOut until --force (ACFS acfs-uetm, nhm-7kz).
func TestBindKeepsTheOptOut(t *testing.T) {
	fakeHerdr(t)
	ownedInstance(t)
	cfg := filepath.Join(t.TempDir(), "config.toml")
	show := func() string {
		t.Helper()
		code, out, errOut := runVerb(runBind, "--config", cfg, "--show")
		if code != exitOK {
			t.Fatalf("--show = %d %s", code, errOut)
		}
		return out
	}
	if out := show(); !strings.Contains(out, "nhm's keys: not bound") || !strings.Contains(out, "prefix+a palette, prefix+d board, prefix+m menu") {
		t.Errorf("--show before a bind: %q", out)
	}
	if _, err := os.Stat(cfg); err == nil {
		t.Fatal("--show wrote the config")
	}
	if code, _, errOut := runVerb(runBind, "--config", cfg, "--no-reload"); code != exitOK {
		t.Fatalf("bind = %d %s", code, errOut)
	}
	bound, _ := os.ReadFile(cfg)
	if out := show(); !strings.Contains(out, "nhm's keys: bound\n") {
		t.Errorf("--show after a bind: %q", out)
	}
	nhmKeys(t, "menu = \"\"\n")
	if out := show(); !strings.Contains(out, "bound, but not as nhm bind writes them now") || !strings.Contains(out, "menu without a key") {
		t.Errorf("--show after a change in [keys]: %q", out)
	}
	nhmKeys(t, "")

	if code, _, errOut := runVerb(runBind, "--config", cfg, "--unbind", "--no-reload"); code != exitOK || !strings.Contains(errOut, "opt-out") {
		t.Fatalf("unbind = %d %s", code, errOut)
	}
	optedOut, _ := os.ReadFile(cfg)
	if strings.Contains(string(optedOut), "keys.command") {
		t.Fatalf("unbind left keys: %q", optedOut)
	}
	for _, args := range [][]string{{"--no-reload"}, {"--dry-run"}} {
		code, out, errOut := runVerb(runBind, append([]string{"--config", cfg}, args...)...)
		if code != exitOptedOut || out != "" || !strings.Contains(errOut, "nhm bind --unbind") || !strings.Contains(errOut, "--force") {
			t.Errorf("bind %v after the opt-out = %d %q %s", args, code, out, errOut)
		}
		if data, _ := os.ReadFile(cfg); string(data) != string(optedOut) {
			t.Fatalf("bind %v changed an opted-out file: %q", args, data)
		}
	}
	if out := show(); !strings.Contains(out, "opted out (nhm bind --unbind)") {
		t.Errorf("--show after the opt-out: %q", out)
	}
	if code, _, errOut := runVerb(runBind, "--config", cfg, "--unbind", "--no-reload"); code != exitOK || !strings.Contains(errOut, "already as it should be") {
		t.Errorf("a second unbind = %d %s", code, errOut)
	}
	if code, _, errOut := runVerb(runBind, "--config", cfg, "--force", "--no-reload"); code != exitOK {
		t.Fatalf("bind --force = %d %s", code, errOut)
	}
	if data, _ := os.ReadFile(cfg); string(data) != string(bound) {
		t.Fatalf("bind --force = %q, want %q", data, bound)
	}
	if code, _, errOut := runVerb(runBind, "--config", cfg, "--no-reload"); code != exitOK || !strings.Contains(errOut, "already as it should be") {
		t.Errorf("bind after --force = %d %s", code, errOut)
	}

	// ACFS's unbound palette block is an opt-out too, and stays.
	acfs := "# >>> acfs palette >>> unbound by `acfs palette unbind`; `acfs palette bind` binds it again\n# <<< acfs palette <<<\n"
	if err := os.WriteFile(cfg, []byte(acfs), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runVerb(runBind, "--config", cfg, "--no-reload"); code != exitOptedOut || !strings.Contains(errOut, "acfs palette unbind") {
		t.Fatalf("bind over ACFS's unbound block = %d %s", code, errOut)
	}
	if data, _ := os.ReadFile(cfg); string(data) != acfs {
		t.Fatalf("bind changed ACFS's unbound block: %q", data)
	}
	if code, _, errOut := runVerb(runBind, "--config", cfg, "--force", "--no-reload"); code != exitOK {
		t.Fatalf("bind --force over ACFS's unbound block = %d %s", code, errOut)
	}
	if data, _ := os.ReadFile(cfg); strings.Contains(string(data), "acfs palette") || !strings.Contains(string(data), "nhm.menu") {
		t.Fatalf("bind --force kept ACFS's block: %q", data)
	}

	for _, args := range [][]string{{"--force", "--unbind"}, {"--show", "--unbind"}, {"--show", "--print-client"}, {"--force", "--show"}} {
		if code, _, _ := runVerb(runBind, args...); code != exitRefused {
			t.Errorf("%v: code %d, want %d", args, code, exitRefused)
		}
	}
}

// The swarm host after v0.1.6 (nhm-4mx): herdr's config holds ACFS's
// empty unbound block, which acfs palette unbind left when nhm took the
// keys, and nhm's bound block. That is no opt-out: --show says bound, and
// a [keys] change binds.
func TestBindBesideACFSsUnboundBlock(t *testing.T) {
	fakeHerdr(t)
	ownedInstance(t)
	acfs := "# >>> acfs palette >>> unbound by `acfs palette unbind`; `acfs palette bind` binds it again\n# <<< acfs palette <<<\n"
	cfg := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfg, []byte(acfs+"\n"+ui.Block(ui.Keys)), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := runVerb(runBind, "--config", cfg, "--show"); code != exitOK || strings.Contains(out, "opted out") ||
		!strings.Contains(out, "nhm's keys: bound") {
		t.Errorf("--show = %d %q", code, out)
	}
	nhmKeys(t, "palette = \"ctrl+alt+p\"\n")
	if code, _, errOut := runVerb(runBind, "--config", cfg, "--no-reload"); code != exitOK {
		t.Fatalf("bind after a [keys] change = %d %s", code, errOut)
	}
	if data, _ := os.ReadFile(cfg); !strings.Contains(string(data), `key = "ctrl+alt+p"`) || strings.Contains(string(data), "acfs palette") {
		t.Errorf("the bound file = %q", data)
	}
}
