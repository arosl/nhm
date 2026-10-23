package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/ui"
)

const uiUsage = `Usage: nhm ui palette|board|menu      (herdr's plugin actions run these)
       nhm ui palette-popup|board-popup|menu-popup   (the popups run these)
       nhm ui run JOB-FILE                    (a surface starts this for a mutation)

An action captures the workspace and pane it was invoked in and opens
the popup with them (the board: a tab of its own); the popup acts on
those ids only. A server that is not enrolled gets a note and no popup.
Typed by hand, use nhm palette, nhm board and nhm menu instead. nhm ui
run runs a job a surface wrote, once, in a session of its own, so the
job outlives the popup.
`

// runUI is `nhm ui`.
func runUI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 2 && args[0] == "run" {
		return runJob(args[1], stdout, stderr)
	}
	if len(args) != 1 {
		fmt.Fprint(stderr, uiUsage)
		return exitRefused
	}
	switch name := args[0]; name {
	case "board":
		// The board opens in a tab of its own.
		return openGoBoard(stderr)
	case "palette", "menu":
		return openPopup(name, stderr)
	case "palette-popup":
		return holdOnFailure(runPalette(nil, stdout, stderr), stderr)
	case "board-popup":
		// A board tab that openGoBoard opened, by its launch token
		// (uilaunch.TokenEnv), or the board in a popup.
		return holdOnFailure(runGoBoard("", stderr), stderr)
	case "menu-popup":
		return holdOnFailure(runGoMenu(stderr), stderr)
	case "fit-probe":
		// nhm doctor --herdr-fit's probe (fitprobe.go).
		return runFitProbe(stderr)
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
		var inst store.Instance
		inst, err = store.Lookup(root, c.SocketPath)
		c.InstanceID = inst.ID
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

// self is the running nhm binary, for the surfaces and jobs it starts.
func self() string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return "nhm"
}

// nhmEnv is the environment of a child nhm: the popup's, with the
// captured context and socket made explicit.
func nhmEnv(c ui.Context) []string {
	data, _ := json.Marshal(c)
	return append(os.Environ(), "HERDR_SOCKET_PATH="+c.SocketPath, ui.ContextEnv+"="+string(data), "NHM_BIN="+self())
}

// boardContextFor is c moved to session's workspace. The calling pane's
// tab, pane and agent are another workspace's, so they are left out.
func boardContextFor(ctx context.Context, h *herdr.Client, c ui.Context, session string) (ui.Context, error) {
	ws, err := sessionWorkspace(ctx, h, session)
	if err != nil {
		return c, err
	}
	if ws != c.WorkspaceID {
		c.TabID, c.PaneID, c.Agent = "", "", ""
	}
	c.WorkspaceID, c.WorkspaceLabel = ws, ""
	if session != ws {
		c.WorkspaceLabel = session
	}
	return c, nil
}

func runBoard(args []string, stdout, stderr io.Writer) int {
	args = withoutGo(args)
	if len(args) == 1 && !strings.HasPrefix(args[0], "-") {
		return runGoBoard(args[0], stderr)
	}
	if len(args) > 0 {
		fmt.Fprint(stderr, paletteUsage)
		return exitRefused
	}
	return runGoBoard("", stderr)
}

func runMenu(args []string, stdout, stderr io.Writer) int {
	if len(withoutGo(args)) > 0 {
		fmt.Fprint(stderr, paletteUsage)
		return exitRefused
	}
	return runGoMenu(stderr)
}

// withoutGo is args without --go, which is accepted for old callers and
// does nothing.
func withoutGo(args []string) []string {
	var out []string
	for _, a := range args {
		if a != "--go" {
			out = append(out, a)
		}
	}
	return out
}
