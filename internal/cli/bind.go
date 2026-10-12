package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/ui"
)

const bindUsage = `Usage: nhm bind [--unbind] [--print-client] [--dry-run] [--no-reload] [--config PATH]

Write nhm's keys into the herdr server's config.toml, between
"# >>> nhm >>>" and "# <<< nhm <<<": prefix+a the palette, prefix+d the
agent board, prefix+m the nhm menu (each a plugin_action key). ACFS's old
"# >>> acfs palette >>>" block goes. A key the rest of the config already
uses is refused. Then herdr reloads its config (--no-reload skips that).

--unbind removes the block. --print-client prints the block for the
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
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) > 0 || (*printClient && *unbind) {
		fmt.Fprint(stderr, bindUsage)
		return exitRefused
	}
	if *printClient {
		fmt.Fprint(stdout, ui.ClientNote+"\n"+ui.Block())
		return exitOK
	}
	path := *config
	if path == "" {
		if path, err = ui.ConfigPath(os.Getenv); err != nil {
			fmt.Fprintf(stderr, "nhm bind: %v\n", err)
			return exitError
		}
	}
	// A dotfiles link (or a chain of them) is edited where it points, so
	// the link stays a link.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "nhm bind: %v\n", err)
		return exitError
	}
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
		reloadHerdr(stderr)
	}
	fmt.Fprintln(stderr, "nhm bind: a herdr client on another machine uses its own keys: see nhm bind --print-client")
	return exitOK
}

// reloadHerdr asks the herdr server (the calling pane's, or the default
// one) to reload its config; a failure is only a note.
func reloadHerdr(stderr io.Writer) {
	bin := os.Getenv("HERDR_BIN_PATH")
	if bin == "" {
		bin = "herdr"
	}
	out, err := exec.Command(bin, "server", "reload-config").CombinedOutput()
	if err != nil {
		fmt.Fprintf(stderr, "nhm bind: herdr did not reload its config (%v); it reads it at its next start: %s\n", err, out)
	}
}
