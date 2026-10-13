package cli

import (
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/ui"
)

const bindUsage = `Usage: nhm bind [--unbind | --force | --show | --print-client] [--dry-run] [--no-reload] [--config PATH] [--socket PATH]

Write nhm's keys into the herdr server's config.toml, between
"# >>> nhm >>>" and "# <<< nhm <<<", each a plugin_action key. [keys] in
nhm's config (~/.config/nhm/config.toml) sets them:

  [keys]
  palette = "prefix+a"   # the defaults
  board   = "prefix+d"
  menu    = "prefix+m"

An empty value gives that surface no key (the menu still opens it).
Change a key and run nhm bind again to move it. ACFS's old
"# >>> acfs palette >>>" block goes. Then herdr reloads its config
(--no-reload skips that).

It refuses, and changes nothing:
- unless nhm owns the herdr server (--socket, else the calling pane's):
  while ACFS's Bash owns it, nhm's palette cannot send, and ACFS's
  palette keeps prefix+a until the takeover;
- when the rest of the config already uses one of the keys (any quote
  style, arrays too), or [keys] gives one key to two surfaces;
- when the result would not parse, or herdr's config check reports an
  issue the old file did not have.
A symlinked config is written through: the link stays.

--unbind removes nhm's keys at any time and records an opt-out in their
place. While the opt-out is there, nhm bind (as ACFS's install and acfs
update run it) changes nothing and exits 5; ACFS's "unbound by acfs
palette unbind" block counts as an opt-out too. --force binds anyway and
removes the opt-out. --show prints the state and the keys, and changes
nothing. --print-client prints the block for the herdr client's own
config: a remote client (herdr --remote, on a Mac) uses its own keys
unless it attaches with --remote-keybindings server.
`

// exitOptedOut is nhm bind's exit code when herdr's config records an
// opt-out (nhm bind --unbind, acfs palette unbind): nothing changed, and
// the caller binds no other palette in its place.
const exitOptedOut = 5

func runBind(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bind", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, bindUsage) }
	unbind := fs.Bool("unbind", false, "remove nhm's keys and record an opt-out")
	force := fs.Bool("force", false, "bind even after an opt-out, and remove it")
	show := fs.Bool("show", false, "print the state and the keys, change nothing")
	printClient := fs.Bool("print-client", false, "print the block for the client's config")
	dryRun := fs.Bool("dry-run", false, "print the new config, write nothing")
	noReload := fs.Bool("no-reload", false, "do not ask herdr to reload its config")
	config := fs.String("config", "", "herdr's config.toml (default: $HERDR_CONFIG_PATH, $XDG_CONFIG_HOME/herdr/config.toml)")
	socket := fs.String("socket", "", "the herdr server: nhm must own it, its config is written and it reloads (default HERDR_SOCKET_PATH)")
	pos, err := parseInterleaved(fs, args)
	modes := 0
	for _, on := range []bool{*unbind, *force, *show, *printClient} {
		if on {
			modes++
		}
	}
	if err != nil || len(pos) > 0 || modes > 1 {
		fmt.Fprint(stderr, bindUsage)
		return exitRefused
	}
	// --unbind needs no keys, so a broken [keys] never blocks it.
	var keys []ui.Key
	var keysFrom string
	if !*unbind {
		if keys, keysFrom, err = bindKeys(); err != nil {
			fmt.Fprintf(stderr, "nhm bind: %v\n", err)
			return exitRefused
		}
	}
	if *printClient {
		fmt.Fprint(stdout, ui.ClientNote+"\n"+ui.Block(keys))
		return exitOK
	}
	// One herdr server for the owner check, the config and the reload.
	server := *socket
	if server == "" {
		server = os.Getenv("HERDR_SOCKET_PATH")
	}
	path := *config
	if path == "" {
		if path, err = serverConfig(server, *socket != ""); err != nil {
			fmt.Fprintf(stderr, "nhm bind: %v\n", err)
			return exitRefused
		}
	}
	// A dotfiles link (or a chain of them, even one whose target does not
	// exist yet) is written through: its target changes and the link
	// stays.
	if path, err = linkTarget(path); err != nil {
		fmt.Fprintf(stderr, "nhm bind: %v\n", err)
		return exitError
	}
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "nhm bind: %v\n", err)
		return exitError
	}
	// The state of the bytes just read, not of a second read.
	seen := ""
	if err == nil {
		seen = stateOf(path, old)
	}
	if *show {
		return showBind(stdout, stderr, path, string(old), keys, keysFrom)
	}
	var next string
	if *unbind {
		next, err = ui.OptOut(string(old))
	} else {
		next, err = ui.BindKeys(string(old), keys, *force)
	}
	if errors.Is(err, ui.ErrOptedOut) {
		// Before the owner check: the user's own choice is the answer.
		fmt.Fprintf(stderr, "nhm bind: herdr's config opts out of nhm's keys (%s, in %s), so nhm bind changes nothing; nhm bind --force binds them\n",
			ui.OptOutBy(string(old)), path)
		return exitOptedOut
	}
	if err != nil {
		fmt.Fprintf(stderr, "nhm bind: %v\n", err)
		return exitRefused
	}
	if !*unbind {
		// While ACFS's Bash owns the instance, nhm's palette cannot send
		// (nhm send refuses), and binding would also drop ACFS's own
		// palette block: ACFS's palette stays on prefix+a until the
		// takeover. --unbind only removes nhm's keys, so it may run any time.
		if code := requireNhmOwner(server, stderr); code != exitOK {
			return code
		}
	}
	if *dryRun {
		fmt.Fprint(stdout, next)
		return exitOK
	}
	if next == string(old) {
		fmt.Fprintf(stderr, "nhm bind: %s is already as it should be\n", path)
		return exitOK
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintf(stderr, "nhm bind: %v\n", err)
		return exitError
	}
	// herdr checks the result first: an issue the old file did not have
	// (a key that conflicts with another binding, a parse error that would
	// make herdr ignore the whole file) leaves the file as it was.
	if added, err := newConfigIssues(old, []byte(next), mode); err != nil {
		fmt.Fprintf(stderr, "nhm bind: %v; %s is unchanged\n", err, path)
		return exitError
	} else if len(added) > 0 {
		fmt.Fprintf(stderr, "nhm bind: herdr would not accept the new config, so %s is unchanged: %s\n", path, strings.Join(added, "; "))
		return exitRefused
	}
	// The user's editor, or another nhm bind, may have written the file
	// while herdr checked it: never overwrite that.
	if now := fileState(path); now != seen {
		fmt.Fprintf(stderr, "nhm bind: %s changed during bind; run again\n", path)
		return exitRefused
	}
	if err := store.WriteFileAtomic(path, []byte(next), mode); err != nil {
		fmt.Fprintf(stderr, "nhm bind: %v\n", err)
		return exitError
	}
	verb := "wrote nhm's keys (" + ui.Describe(keys) + ") to"
	if *unbind {
		verb = "removed nhm's keys and recorded the opt-out (nhm bind --force binds them again) in"
	}
	fmt.Fprintf(stderr, "nhm bind: %s %s\n", verb, path)
	if !*noReload {
		reloadHerdr(server, stderr)
	}
	fmt.Fprintln(stderr, "nhm bind: a herdr client on another machine uses its own keys: see nhm bind --print-client")
	return exitOK
}

// bindKeys are nhm's keys from [keys] of nhm's config, and where they come
// from: "[keys] in PATH", or the defaults when there is no config
// directory.
func bindKeys() ([]ui.Key, string, error) {
	path, err := kinds.ConfigPath()
	if err != nil {
		return slices.Clone(ui.Keys), "the defaults: no config directory", nil
	}
	keys, err := ui.LoadKeys(path)
	return keys, "[keys] in " + path, err
}

// showBind prints what herdr's config at path holds of nhm's keys, and
// the keys nhm bind would write. It changes nothing.
func showBind(stdout, stderr io.Writer, path, config string, keys []ui.Key, keysFrom string) int {
	rest, err := ui.Unbind(config)
	if err != nil {
		fmt.Fprintf(stderr, "nhm bind: %v\n", err)
		return exitRefused
	}
	state := "not bound; run nhm bind"
	if by := ui.OptOutBy(config); by != "" {
		state = "opted out (" + by + "); nhm bind --force binds them"
	} else if next, err := ui.BindKeys(config, keys, false); err == nil && next == config {
		state = "bound"
	} else if rest != config {
		state = "bound, but not as nhm bind writes them now; run nhm bind"
	}
	fmt.Fprintf(stdout, "herdr config: %s\nnhm's keys: %s\nkeys (%s): %s\n", path, state, keysFrom, ui.Describe(keys))
	return exitOK
}

// requireNhmOwner refuses unless nhm owns the herdr server bind is run
// for (--socket, else the calling pane's HERDR_SOCKET_PATH).
func requireNhmOwner(socket string, stderr io.Writer) int {
	if socket == "" {
		socket = os.Getenv("HERDR_SOCKET_PATH")
	}
	if socket == "" {
		fmt.Fprintln(stderr, "nhm bind: no herdr server: run it in a herdr pane, or give --socket")
		return exitRefused
	}
	root, err := store.StateRoot()
	var inst store.Instance
	if err == nil {
		inst, err = store.Lookup(root, socket)
	}
	if errors.Is(err, store.ErrNotEnrolled) {
		fmt.Fprintln(stderr, "nhm bind: this herdr server is not enrolled (nhm admin instance enroll), so nhm does not own it")
		return exitRefused
	}
	if err != nil {
		fmt.Fprintf(stderr, "nhm bind: %v\n", err)
		return exitError
	}
	o, err := core.ReadOwner(inst.Dir)
	if err != nil {
		fmt.Fprintf(stderr, "nhm bind: %v\n", err)
		return exitError
	}
	if o.Owner != core.OwnerNhm {
		fmt.Fprintf(stderr, "nhm bind: ACFS's Bash owns this herdr server (generation %d), so nhm's palette cannot send "+
			"and ACFS's palette keeps prefix+a. Bind after the takeover (nhm admin takeover).\n", o.Generation)
		return exitRefused
	}
	return exitOK
}

// serverConfig is the config.toml of the herdr server at socket:
// $HERDR_CONFIG_PATH when socket is the calling pane's server (no
// --socket, or --socket naming $HERDR_SOCKET_PATH), since that variable
// belongs to the pane's server; else the herdr directory of the socket in
// one of herdr's layouts (<dir>/herdr/herdr.sock or
// <dir>/herdr/sessions/<name>/herdr.sock); else the default. A --socket
// whose config cannot be told is refused: nhm would check one server and
// write another's keys.
func serverConfig(socket string, explicit bool) (string, error) {
	if p := os.Getenv("HERDR_CONFIG_PATH"); p != "" && (socket == "" || socket == os.Getenv("HERDR_SOCKET_PATH")) {
		return p, nil
	}
	if socket != "" {
		if dir, ok := herdrDir(socket); ok {
			return filepath.Join(dir, "config.toml"), nil
		}
		if explicit {
			return "", fmt.Errorf("cannot tell which config.toml the herdr server at %s reads; give --config", socket)
		}
	}
	return ui.ConfigPath(os.Getenv)
}

// herdrDir is the herdr directory of a socket in herdr's own layouts:
// <dir>/herdr/herdr.sock, or <dir>/herdr/sessions/<name>/herdr.sock. Any
// other path, such as a project directory that happens to be named herdr,
// is not one.
func herdrDir(socket string) (string, bool) {
	if filepath.Base(socket) != "herdr.sock" {
		return "", false
	}
	dir := filepath.Dir(socket)
	if filepath.Base(dir) == "herdr" {
		return dir, true
	}
	if sessions := filepath.Dir(dir); filepath.Base(sessions) == "sessions" && filepath.Base(filepath.Dir(sessions)) == "herdr" {
		return filepath.Dir(sessions), true
	}
	return "", false
}

// fileState is what a file looks like now: its size, modification time
// and content hash ("" when it does not exist).
func fileState(path string) string {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		return "unreadable"
	}
	return stateOf(path, data)
}

// stateOf is fileState for bytes already read from path: the file's size
// and modification time with the hash of those bytes, so a change between
// the read and this stat shows as a different state ("" when the file
// is gone).
func stateOf(path string, data []byte) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d %d %x", info.Size(), info.ModTime().UnixNano(), sha256.Sum256(data))
}

// linkTarget is the file path names after its symlinks, as the kernel
// opens it. When the last link dangles, the links are followed by hand,
// each relative one from the real directory of the link that holds it.
// A path that is not a link is its own target.
func linkTarget(path string) (string, error) {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real, nil
	}
	for range 40 {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			// The end of the chain, a file that does not exist yet; its
			// directory may be a link too.
			if dir, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
				return filepath.Join(dir, filepath.Base(path)), nil
			}
			return path, nil
		}
		dest, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(dest) {
			base := filepath.Dir(path)
			if real, err := filepath.EvalSymlinks(base); err == nil {
				base = real
			}
			dest = filepath.Join(base, dest)
		}
		path = dest
	}
	return "", fmt.Errorf("%s: too many levels of symlinks", path)
}

func herdrBin() string {
	if bin := os.Getenv("HERDR_BIN_PATH"); bin != "" {
		return bin
	}
	return "herdr"
}

// configIssues is what `herdr config check` reports for a config file,
// one issue per line; none when herdr finds none.
func configIssues(path string) ([]string, error) {
	cmd := exec.Command(herdrBin(), "config", "check")
	cmd.Env = append(os.Environ(), "HERDR_CONFIG_PATH="+path)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		return nil, fmt.Errorf("herdr config check: %w", err)
	}
	var issues []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" && line != "config: ok" && line != "config: issues found" {
			issues = append(issues, line)
		}
	}
	if err != nil && len(issues) == 0 {
		return nil, fmt.Errorf("herdr config check: %w", err)
	}
	return issues, nil
}

// newConfigIssues writes next to a private temporary file and returns
// the issues herdr reports for it that it does not report for old.
func newConfigIssues(old, next []byte, mode os.FileMode) ([]string, error) {
	dir, err := os.MkdirTemp("", "nhm-bind-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	// check returns the issues for content, with the checked file's path
	// replaced, so the same issue compares equal for old and next.
	check := func(name string, content []byte) ([]string, error) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, content, mode); err != nil {
			return nil, err
		}
		issues, err := configIssues(p)
		for i := range issues {
			issues[i] = strings.ReplaceAll(issues[i], p, "<config>")
		}
		return issues, err
	}
	before := map[string]bool{}
	if len(old) > 0 {
		issues, err := check("old.toml", old)
		if err != nil {
			return nil, err
		}
		for _, i := range issues {
			before[i] = true
		}
	}
	after, err := check("new.toml", next)
	if err != nil {
		return nil, err
	}
	var added []string
	for _, i := range after {
		if !before[i] {
			added = append(added, i)
		}
	}
	return added, nil
}

// reloadHerdr asks the herdr server at socket (else the default one) to
// reload its config; a failure is only a note.
func reloadHerdr(socket string, stderr io.Writer) {
	cmd := exec.Command(herdrBin(), "server", "reload-config")
	if socket != "" {
		cmd.Env = append(os.Environ(), "HERDR_SOCKET_PATH="+socket)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(stderr, "nhm bind: herdr did not reload its config (%v); it reads it at its next start: %s\n", err, out)
	}
}
