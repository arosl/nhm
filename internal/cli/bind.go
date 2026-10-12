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
	"strings"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/ui"
)

const bindUsage = `Usage: nhm bind [--unbind] [--print-client] [--dry-run] [--no-reload] [--config PATH] [--socket PATH]

Write nhm's keys into the herdr server's config.toml, between
"# >>> nhm >>>" and "# <<< nhm <<<": prefix+a the palette, prefix+d the
agent board, prefix+m the nhm menu (each a plugin_action key). ACFS's old
"# >>> acfs palette >>>" block goes. Then herdr reloads its config
(--no-reload skips that).

It refuses, and changes nothing:
- unless nhm owns the herdr server (--socket, else the calling pane's):
  while ACFS's Bash owns it, nhm's palette cannot send, and ACFS's
  palette keeps prefix+a until the takeover;
- when the rest of the config already uses one of the keys (any quote
  style, arrays too);
- when the result would not parse, or herdr's config check reports an
  issue the old file did not have.
A symlinked config is written through: the link stays.

--unbind removes nhm's block only, at any time. --print-client prints the block for the
herdr client's own config: a remote client (herdr --remote, on a Mac)
uses its own keys unless it attaches with --remote-keybindings server.
`

func runBind(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bind", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, bindUsage) }
	unbind := fs.Bool("unbind", false, "remove nhm's keys")
	printClient := fs.Bool("print-client", false, "print the block for the client's config")
	dryRun := fs.Bool("dry-run", false, "print the new config, write nothing")
	noReload := fs.Bool("no-reload", false, "do not ask herdr to reload its config")
	config := fs.String("config", "", "herdr's config.toml (default: $HERDR_CONFIG_PATH, $XDG_CONFIG_HOME/herdr/config.toml)")
	socket := fs.String("socket", "", "the herdr server: nhm must own it, its config is written and it reloads (default HERDR_SOCKET_PATH)")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) > 0 || (*printClient && *unbind) {
		fmt.Fprint(stderr, bindUsage)
		return exitRefused
	}
	if *printClient {
		fmt.Fprint(stdout, ui.ClientNote+"\n"+ui.Block())
		return exitOK
	}
	// One herdr server for the owner check, the config and the reload.
	server := *socket
	if server == "" {
		server = os.Getenv("HERDR_SOCKET_PATH")
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
	seen := fileState(path)
	var next string
	if *unbind {
		next, err = ui.Unbind(string(old))
	} else {
		next, err = ui.Bind(string(old))
	}
	if err != nil {
		fmt.Fprintf(stderr, "nhm bind: %v\n", err)
		return exitRefused
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
	verb := "wrote nhm's keys (prefix+a palette, prefix+d board, prefix+m menu) to"
	if *unbind {
		verb = "removed nhm's keys from"
	}
	fmt.Fprintf(stderr, "nhm bind: %s %s\n", verb, path)
	if !*noReload {
		reloadHerdr(server, stderr)
	}
	fmt.Fprintln(stderr, "nhm bind: a herdr client on another machine uses its own keys: see nhm bind --print-client")
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
// $HERDR_CONFIG_PATH, else the herdr directory the socket lives in
// (herdr/herdr.sock or herdr/sessions/<name>/herdr.sock), else the
// default. A --socket whose config cannot be told is refused: nhm would
// check one server and write another's keys.
func serverConfig(socket string, explicit bool) (string, error) {
	if p := os.Getenv("HERDR_CONFIG_PATH"); p != "" {
		return p, nil
	}
	if socket != "" {
		for dir := filepath.Dir(socket); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
			if filepath.Base(dir) == "herdr" {
				return filepath.Join(dir, "config.toml"), nil
			}
		}
		if explicit {
			return "", fmt.Errorf("cannot tell which config.toml the herdr server at %s reads; give --config", socket)
		}
	}
	return ui.ConfigPath(os.Getenv)
}

// fileState is what a file looked like: its size, modification time and
// content hash ("" when it does not exist).
func fileState(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "unreadable"
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
