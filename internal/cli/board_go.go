package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/core/uilaunch"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/integrations/guards"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/store/history"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/board"
	"github.com/arosl/nhm/internal/ui/dashboard"
	"github.com/arosl/nhm/internal/ui/palette"
	"github.com/arosl/nhm/internal/ui/tui/theme"
)

// The Go board (plan-v4 section 7). prefix+d runs openGoBoard: it
// focuses the workspace's live board tab, or opens one, by its launch
// record (uilaunch), never by label. herdr runs the board pane's command
// in the new tab, nhm ui board-popup, which sees the launch token and
// runs runGoBoard.

// boardOpenWait bounds prefix+d: the workspace lock, the check of the
// old board and the open.
const boardOpenWait = 15 * time.Second

// openGoBoard is prefix+d with the Go board.
func openGoBoard(stderr io.Writer) int {
	c, err := ui.Capture(os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "nhm ui board: %v\n", err)
		return exitError
	}
	if c.WorkspaceID == "" {
		fmt.Fprintln(stderr, "nhm ui board: herdr named no workspace; the board opens in the workspace it is asked from")
		return exitRefused
	}
	root, err := store.StateRoot()
	var inst store.Instance
	if err == nil {
		inst, err = store.Lookup(root, c.SocketPath)
	}
	if errors.Is(err, store.ErrNotEnrolled) {
		fmt.Fprintln(stderr, "nhm ui board: this herdr server is not enrolled; run nhm admin instance enroll")
		return exitError
	}
	if err != nil {
		fmt.Fprintf(stderr, "nhm ui board: %v\n", err)
		return exitError
	}
	c.InstanceID = inst.ID
	data, err := json.Marshal(c)
	if err != nil {
		fmt.Fprintf(stderr, "nhm ui board: %v\n", err)
		return exitError
	}
	ctx, cancel := context.WithTimeout(context.Background(), boardOpenWait)
	defer cancel()
	res, err := uilaunch.Open(ctx, uilaunch.Env{InstanceDir: inst.Dir, InstanceID: inst.ID}, herdr.New(c.SocketPath),
		uilaunch.OpenRequest{WorkspaceID: c.WorkspaceID, Env: map[string]string{ui.ContextEnv: string(data)}, Cwd: c.Cwd})
	if err != nil {
		fmt.Fprintf(stderr, "nhm ui board: %v\n", err)
		return exitError
	}
	// herdr's plugin log keeps these lines.
	if res.StalePane != "" {
		// The old pane stays for the user to close.
		fmt.Fprintf(stderr, "nhm ui board: the old board in pane %s is gone (%s); a new board is open\n", res.StalePane, res.Dropped)
	}
	if res.Action == uilaunch.Awaiting && res.OpenedAt != nil {
		fmt.Fprintf(stderr, "nhm ui board: a board is starting in this workspace (opened %s ago); if it has not started after %s, prefix+d opens a new one\n",
			time.Since(*res.OpenedAt).Round(time.Second), uilaunch.StartGrace)
	}
	return exitOK
}

// runGoBoard is the board's program in its tab, or `nhm board --go` in a
// pane. It acts on the workspace it was opened for and never falls back
// to the focused one.
func runGoBoard(stderr io.Writer) int {
	// The path the board was started by, before anything changes the
	// working directory: an upgrade switches the links it resolves through.
	launch, launchErr := board.LaunchPath()
	c, err := popupContext()
	if err != nil {
		fmt.Fprintf(stderr, "nhm board: %v\n", err)
		return exitError
	}
	if c.WorkspaceID == "" {
		fmt.Fprintln(stderr, "nhm board: no workspace: open the board from herdr (prefix+d), or run it in a herdr pane")
		return exitRefused
	}
	ie, err := openInstance(c.SocketPath)
	if err != nil {
		fmt.Fprintf(stderr, "nhm board: %v\n", err)
		return exitError
	}
	defer func() { _ = ie.db.Close() }()
	// A restored or handed-on context must be this server's.
	if err := c.CheckInstance(ie.inst.ID); err != nil {
		fmt.Fprintf(stderr, "nhm board: %v\n", err)
		return exitRefused
	}
	// herdr closing the board's tab hangs the pane up: the board then ends
	// as it does on q, and unregisters.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	lenv := uilaunch.Env{InstanceDir: ie.inst.Dir, InstanceID: ie.inst.ID}
	if token := os.Getenv(uilaunch.TokenEnv); token != "" {
		rctx, cancel := context.WithTimeout(ctx, boardOpenWait)
		_, err := uilaunch.Register(rctx, lenv, ie.herdr, c.WorkspaceID, token,
			uilaunch.Self{PID: os.Getpid(), PaneID: os.Getenv("HERDR_PANE_ID")})
		cancel()
		if err != nil {
			fmt.Fprintf(stderr, "nhm board: %v\n", err)
			return exitRefused
		}
		defer func() {
			uctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = uilaunch.Unregister(uctx, lenv, c.WorkspaceID, token)
		}()
	}

	deps := snapshot.Deps{Herdr: ie.herdr, DB: ie.db, Beads: beadsInProgress,
		Guards: func(kind, projectRoot string) []guards.Result {
			return guards.Check(kind, guards.Env{ProjectRoot: projectRoot})
		}}
	if path, err := limitsSeenPath(&ie.inst); err == nil {
		deps.LimitsFile = path
	}
	var inbox func(context.Context, string, string) ([]agentmail.Message, error)
	if mail, err := agentmail.NewClient(""); err == nil {
		deps.Mail = mail
		inbox = func(ctx context.Context, project, name string) ([]agentmail.Message, error) {
			return mail.FetchInbox(ctx, project, name, 50)
		}
	}
	b, err := board.New(board.Config{Deps: deps, WorkspaceID: c.WorkspaceID, Reader: ie.herdr})
	if err != nil {
		fmt.Fprintf(stderr, "nhm board: %v\n", err)
		return exitRefused
	}
	defer b.Close()
	// A board that cannot see an upgrade takes no actions (it fails closed).
	var bin *board.Binary
	if launchErr == nil {
		bin, launchErr = board.WatchBinary(launch)
	}
	if launchErr != nil {
		fmt.Fprintf(stderr, "nhm board: %v; the board takes no actions\n", launchErr)
		bin = board.Unwatched(launchErr)
	}
	h := ie.herdr
	// The action menu runs the table's actions on the core: a mutation is
	// a runner job under the guard's operation id, so it outlives the board.
	jobs := uiJobs(ie, c)
	core := uiCore(ie, c, jobs)
	// The composer's memory preview is the palette's (nhm memory's
	// retrieval, the send's modes and budgets); the send itself goes
	// through the table. No project for the workspace: no preview.
	var memory *palette.Submitter
	if base, err := uiSendBase(ctx, ie, c); err == nil {
		memory = palette.NewSubmitter(core.Send, paletteReader(ie.db, jobs), paletteMemory(base.ProjectRoot),
			func() (string, error) { return store.NewOperationID(time.Now()) })
	}
	m, err := dashboard.New(dashboard.Config{Board: b, Context: c, Binary: bin, Inbox: inbox, Memory: memory,
		Core:           core,
		NewOperationID: func() (string, error) { return store.NewOperationID(time.Now()) },
		History:        func() ([]history.HistoryEntry, error) { return history.ReadRecent(200) },
		// space a: the palette popup on the board's frozen selection; the
		// palette sends to exactly those targets (nhm-qbi.6, 0531e21).
		OpenPalette: func(ctx context.Context, pc ui.Context) error { return ui.Open(ctx, h, "palette", pc) },
		// ctrl+w: ntm's spawn wizard (the menu's), for nhm add here.
		Kinds: kinds.All(),
		// The read view reads as nhm read --source recent does.
		Read: func(ctx context.Context, pane string, lines int) (string, error) {
			strip := true
			out, err := h.PaneRead(ctx, herdr.PaneReadParams{PaneID: pane, Source: herdr.ReadRecent, Format: herdr.FormatText,
				Lines: &lines, StripANSI: &strip})
			return out.Text, err
		},
		Zoom: func(ctx context.Context, ws, pane string) error {
			if err := h.WorkspaceFocus(ctx, ws); err != nil {
				return err
			}
			_, err := h.PaneZoom(ctx, pane, "on")
			return err
		}})
	if err != nil {
		fmt.Fprintf(stderr, "nhm board: %v\n", err)
		return exitError
	}
	// herdr's events only mark the agent state for a refresh.
	events := make(chan struct{}, 1)
	go watchEvents(ctx, h, events)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-events:
				b.Invalidate()
			}
		}
	}()
	theme.ApplyLipGlossDefaults(theme.Current())
	if _, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx)).Run(); err != nil &&
		!errors.Is(err, tea.ErrProgramKilled) {
		fmt.Fprintf(stderr, "nhm board: %v\n", err)
		return exitError
	}
	return exitOK
}
