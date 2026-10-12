package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/ui"
)

const uiUsage = `Usage: nhm ui palette|board|menu|spawn      (herdr's plugin actions run these)
       nhm ui palette-popup|board-popup|menu-popup|spawn-popup   (the popups run these)

An action captures the workspace and pane it was invoked in and opens
the popup with them; the popup acts on those ids only. A server that is
not enrolled gets a note and no popup. Typed by hand, use nhm palette,
nhm board and nhm menu instead.
`

// runUI is `nhm ui`.
func runUI(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprint(stderr, uiUsage)
		return exitRefused
	}
	switch name := args[0]; name {
	case "palette", "board", "menu", "spawn":
		return openPopup(name, stderr)
	case "spawn-popup":
		return holdOnFailure(runSpawnPopup(stderr), stderr)
	case "palette-popup":
		return holdOnFailure(runPaletteBody(nil, stderr), stderr)
	case "board-popup":
		return holdOnFailure(runBoardBody(stderr), stderr)
	case "menu-popup":
		return holdOnFailure(runMenuBody(stderr), stderr)
	default:
		fmt.Fprint(stderr, uiUsage)
		return exitRefused
	}
}

// holdOnFailure keeps a failed popup open until a key, so its message is
// read: herdr closes a popup when its command exits.
func holdOnFailure(code int, stderr io.Writer) int {
	if code != exitOK {
		fmt.Fprint(stderr, "\npress enter to close ")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	}
	return code
}

// openPopup is a plugin action: capture the context, check the server is
// enrolled, and open the popup.
func openPopup(name string, stderr io.Writer) int {
	c, err := ui.Capture(os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "nhm ui %s: %v\n", name, err)
		return exitError
	}
	root, err := store.StateRoot()
	if err == nil {
		_, err = store.Lookup(root, c.SocketPath)
	}
	if errors.Is(err, store.ErrNotEnrolled) {
		// Plugins are user-global: a server nhm is not enrolled for gets a
		// note, not a popup (plan-v8 2.1).
		fmt.Fprintf(stderr, "nhm ui %s: this herdr server is not enrolled; run nhm admin instance enroll\n", name)
		return exitError
	}
	if err != nil {
		fmt.Fprintf(stderr, "nhm ui %s: %v\n", name, err)
		return exitError
	}
	if err := ui.Open(context.Background(), herdr.New(c.SocketPath), name, c); err != nil {
		fmt.Fprintf(stderr, "nhm ui %s: %v\n", name, err)
		return exitError
	}
	return exitOK
}

// popupContext is the context a popup body acts on: the one its action
// captured, else the calling pane's (a verb typed in a pane).
func popupContext() (ui.Context, error) {
	c, ok, err := ui.FromPopup(os.Getenv)
	if err != nil || ok {
		return c, err
	}
	return ui.Capture(os.Getenv)
}

// self is the running nhm binary, for the key actions and fzf callbacks.
func self() string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return "nhm"
}

// nhmEnv is the environment of a child nhm or nhm-palette: the popup's,
// with the captured context and socket made explicit.
func nhmEnv(c ui.Context) []string {
	ctx, _ := jsonString(c)
	return append(os.Environ(), "HERDR_SOCKET_PATH="+c.SocketPath, ui.ContextEnv+"="+ctx, "NHM_BIN="+self())
}

func jsonString(v any) (string, error) {
	data, err := json.Marshal(v)
	return string(data), err
}

// paletteScript is nhm-palette: beside the binary in a release, else on
// PATH.
func paletteScript() (string, error) {
	if p := ui.Beside("nhm-palette"); p != "" {
		return p, nil
	}
	return exec.LookPath("nhm-palette")
}

// runPaletteBody runs nhm-palette in this terminal for the context's
// workspace, and returns its exit code.
func runPaletteBody(extra []string, stderr io.Writer) int {
	c, err := popupContext()
	if err != nil {
		fmt.Fprintf(stderr, "nhm palette: %v\n", err)
		return exitError
	}
	return runPaletteIn(c, extra, stderr)
}

// runPaletteIn runs nhm-palette for context c.
func runPaletteIn(c ui.Context, extra []string, stderr io.Writer) int {
	script, err := paletteScript()
	if err != nil {
		fmt.Fprintf(stderr, "nhm palette: nhm-palette is not installed beside nhm: %v\n", err)
		return exitError
	}
	args := []string{script}
	if c.WorkspaceID != "" && !hasFlag(extra, "workspace") {
		args = append(args, "--workspace", c.WorkspaceID)
	}
	cmd := exec.Command("bash", append(args, extra...)...)
	cmd.Env = nhmEnv(c)
	if c.Cwd != "" {
		// The palette reads ./command_palette.md of the pane it was
		// opened over.
		cmd.Dir = c.Cwd
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, stderr
	err = cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil {
		fmt.Fprintf(stderr, "nhm palette: %v\n", err)
		return exitError
	}
	return exitOK
}

// fzfCommand is fzf, or $NHM_FZF (tests).
func fzfCommand() string {
	if f := os.Getenv("NHM_FZF"); f != "" {
		return f
	}
	return "fzf"
}

// runFzf feeds lines to fzf and returns what it printed. fzf exits 1 when
// nothing matched and 130 on esc; both give closed.
func runFzf(lines []string, args ...string) (out string, closed bool, err error) {
	cmd := exec.Command(fzfCommand(), args...)
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	cmd.Stderr = os.Stderr
	data, err := cmd.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) && (ee.ExitCode() == 1 || ee.ExitCode() == 130) {
		return "", true, nil
	}
	return string(data), false, err
}

// page shows text in $NHM_PAGER (default less -R), then returns.
func page(text string) {
	pager := os.Getenv("NHM_PAGER")
	if pager == "" {
		pager = "less -R"
	}
	fields := strings.Fields(pager)
	cmd := exec.Command(fields[0], fields[1:]...)
	cmd.Stdin = strings.NewReader(text)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	_ = cmd.Run()
}

// runChild runs nhm with args in the popup's context and returns its
// output, stdout and stderr together.
func runChild(c ui.Context, args ...string) string {
	cmd := exec.Command(self(), args...)
	cmd.Env = nhmEnv(c)
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return err.Error() + "\n"
	}
	return string(out)
}

// runBoardBody is the board: fzf over a fresh snapshot of the context's
// workspace; each key runs the ordinary verb on the selected agent and
// the board comes back from a new snapshot.
func runBoardBody(stderr io.Writer) int {
	c, err := popupContext()
	if err != nil {
		fmt.Fprintf(stderr, "nhm board: %v\n", err)
		return exitError
	}
	r, err := openReader(c.SocketPath, true)
	if err != nil {
		fmt.Fprintf(stderr, "nhm board: %v\n", err)
		return exitError
	}
	defer r.close()
	expect := make([]string, 0, len(ui.BoardKeys))
	for _, k := range ui.BoardKeys {
		if k.Key != "enter" {
			expect = append(expect, k.Key)
		}
	}
	preview := shellQuote(self()) + " read {1} --source recent --lines 80 --socket " + shellQuote(c.SocketPath)
	for {
		m := r.model(context.Background(), "")
		if c.WorkspaceID != "" {
			if one, _, err := selectWorkspace(m, c.WorkspaceID); err == nil {
				m = one
			}
		}
		rows := ui.BoardRows(m)
		lines := make([]string, 0, len(rows))
		for _, row := range rows {
			lines = append(lines, row.Line())
		}
		if len(lines) == 0 {
			lines = append(lines, "-\t-\t-\tno agents")
		}
		out, closed, err := runFzf(lines, "--ansi", "--delimiter=\t", "--with-nth=4", "--layout=reverse", "--border=rounded",
			"--no-sort", "--header="+ui.BoardHeader(c, m), "--expect="+strings.Join(expect, ","),
			"--preview="+preview, "--preview-window=right,55%,<110(down,45%)")
		if err != nil {
			fmt.Fprintf(stderr, "nhm board: %v\n", err)
			return exitError
		}
		if closed {
			return exitOK
		}
		key, line, _ := strings.Cut(strings.TrimRight(out, "\n"), "\n")
		row, ok := ui.ParseBoardLine(line)
		if key == "ctrl-r" || !ok {
			continue
		}
		boardAction(c, m, key, row)
	}
}

// shellQuote quotes s for fzf's --preview, which a shell runs.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// boardAction runs one key's verb on a row.
func boardAction(c ui.Context, m *snapshot.Model, key string, row ui.BoardRow) {
	ws := c.WorkspaceID
	switch key {
	case "":
		page(runChild(c, "read", row.PaneID, "--source", "recent", "--lines", "2000"))
	case "ctrl-p":
		holdOnFailure(runPaletteIn(c, nil, os.Stderr), os.Stderr)
	case "ctrl-x":
		if row.MailName == "" {
			page("This agent has no binding: nhm interrupts bound agents only (nhm adopt binds it).\n")
			return
		}
		args := []string{"interrupt"}
		if ws != "" {
			args = append(args, ws)
		}
		page(runChild(c, append(args, "--name", row.MailName)...))
	case "ctrl-t":
		args := []string{"attention"}
		if ws != "" {
			args = append(args, ws)
		}
		page(runChild(c, args...))
	}
}

// runMenuBody is the nhm menu: fzf over the verbs; a popup verb takes the
// terminal, any other shows its output, then the menu comes back.
func runMenuBody(stderr io.Writer) int {
	c, err := popupContext()
	if err != nil {
		fmt.Fprintf(stderr, "nhm menu: %v\n", err)
		return exitError
	}
	items := ui.MenuItems(c)
	for {
		lines := make([]string, len(items))
		for i, it := range items {
			lines[i] = fmt.Sprintf("%d\t%s", i, it.Label)
		}
		out, closed, err := runFzf(lines, "--delimiter=\t", "--with-nth=2", "--layout=reverse", "--border=rounded",
			"--no-sort", "--header="+c.Header()+"\nenter run · esc close")
		if err != nil {
			fmt.Fprintf(stderr, "nhm menu: %v\n", err)
			return exitError
		}
		if closed {
			return exitOK
		}
		idx, _, _ := strings.Cut(strings.TrimSpace(out), "\t")
		var it *ui.MenuItem
		for i := range items {
			if fmt.Sprint(i) == idx {
				it = &items[i]
			}
		}
		if it == nil {
			continue
		}
		if it.Popup {
			cmd := exec.Command(self(), it.Args...)
			cmd.Env = nhmEnv(c)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			_ = cmd.Run()
			continue
		}
		page(runChild(c, it.Args...))
	}
}

const paletteUsage = `Usage: nhm palette [--workspace ID] [nhm-palette's flags]
       nhm board
       nhm menu

The palette, the agent board and the nhm menu, in this terminal, for the
calling pane's workspace. In herdr, prefix+a, prefix+d and prefix+m open
them as popups (nhm bind).
`

func runPalette(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(stdout, paletteUsage)
		return exitOK
	}
	return runPaletteBody(args, stderr)
}

func runBoard(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprint(stderr, paletteUsage)
		return exitRefused
	}
	return runBoardBody(stderr)
}

func runMenu(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprint(stderr, paletteUsage)
		return exitRefused
	}
	return runMenuBody(stderr)
}
