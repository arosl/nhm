package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/enrich"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/ui/palette"
	"github.com/arosl/nhm/internal/ui/runner"
	"github.com/arosl/nhm/internal/ui/tui/theme"
)

// The palette (nhm-qbi.6, plan-v4 section 5): ntm's palette on nhm, in
// this terminal. prefix+a runs it in the popup. Its sends are runner jobs
// through uiCore, as the board's and the menu's are, so a send outlives
// the popup. It took the place of the fzf palette, nhm-palette
// (nhm-qbi.12), and keeps its flags, its files and ACFS's hand-off.

const paletteUsage = `Usage: nhm palette [--workspace ID] [--file PATH]
       nhm palette --list [--json] [--file PATH]
       nhm palette --show KEY [--file PATH]
       nhm board [session]
       nhm menu

The command palette (ntm's), the agent board and the nhm menu, in this
terminal, for the calling pane's workspace; nhm board <session> shows
that session's workspace (an id or label). In herdr, prefix+a opens the
palette and prefix+m the menu in a popup, and prefix+d opens the board
in a tab of its own (nhm bind). nhm dashboard [session], or dash or d,
is nhm board, by ntm's names.

In the palette, pick a prompt, edit it, pick the agents by kind digit or
by hand, and read a receipt per agent.

  --workspace ID   the agents of this workspace (default: the context's;
                   else pick one)
  --file PATH      read prompts from this file only
  --list           print the prompts (key, label, category, file) and exit
  --json           with --list, as JSON
  --show KEY       print one prompt's text and exit
  --go             accepted by all three; it does nothing

Palette files, first key wins: --file (or NHM_PALETTE_FILE) alone when
given; otherwise ./command_palette.md in the pane's directory
(NHM_PALETTE_CWD when set) and at its project root,
~/.config/nhm/command_palette.md, then ACFS's
~/.acfs/onboard/docs/ntm/command_palette.md. Pins and favorites live
under ~/.local/state/nhm/palette/.

Keys: f1 in the list, ? elsewhere.
`

// paletteGoWait bounds the palette's reads before the program starts.
const paletteGoWait = 15 * time.Second

// runPalette is nhm palette, and prefix+a's popup body.
func runPalette(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("palette", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, paletteUsage) }
	// --go chose this palette while the fzf one was the default.
	fs.Bool("go", true, "accepted; it does nothing")
	workspace := fs.String("workspace", "", "the workspace whose agents get prompts")
	file := fs.String("file", "", "read prompts from this file only")
	list := fs.Bool("list", false, "print the prompts and exit")
	asJSON := fs.Bool("json", false, "with --list, as JSON")
	show := fs.String("show", "", "print one prompt's text and exit")
	if hasFlag(args, "h") || hasFlag(args, "help") {
		fmt.Fprint(stdout, paletteUsage)
		return exitOK
	}
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) > 0 || *asJSON && !*list || *list && *show != "" {
		fs.Usage()
		return exitRefused
	}
	fail := func(code int, err error) int {
		fmt.Fprintf(stderr, "nhm palette: %v\n", err)
		return code
	}
	if *file != "" {
		if *file, err = filepath.Abs(*file); err != nil {
			return fail(exitRefused, err)
		}
		if st, err := os.Stat(*file); err != nil || !st.Mode().IsRegular() {
			return fail(exitRefused, fmt.Errorf("--file %s: no such readable file", *file))
		}
	}
	if *list || *show != "" {
		return paletteCatalog(*file, *show, *asJSON, stdout, stderr)
	}
	if err := checkUIConfig(); err != nil {
		return fail(exitError, err)
	}
	c, err := popupContext()
	if err != nil {
		return fail(exitError, err)
	}
	if dir := os.Getenv("NHM_PALETTE_CWD"); dir != "" {
		// ACFS's acfs palette hands off with the directory it was opened
		// in (scripts/lib/palette.sh, palette_hand_to_nhm).
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			c.Cwd = dir
		}
	}
	// A selection another surface handed on (the board's space a) is the
	// palette's only target; an explicitly empty one is refused.
	picked, pick, err := c.Selection()
	if err != nil {
		return fail(exitRefused, err)
	}
	if pick {
		picked = nil
	}
	if *workspace != "" {
		c.WorkspaceID, c.WorkspaceLabel = *workspace, ""
	}
	if st, err := os.Stat(c.Cwd); c.Cwd != "" && err == nil && st.IsDir() {
		// The palette acts for the pane it was opened over: that pane's
		// project and catalogs, not the plugin's directory, where herdr
		// starts a popup.
		if err := os.Chdir(c.Cwd); err != nil {
			return fail(exitError, err)
		}
	}
	ie, err := openInstance(c.SocketPath)
	if err != nil {
		return fail(exitError, err)
	}
	defer func() { _ = ie.db.Close() }()
	// A restored or handed-on context must be this server's.
	if err := c.CheckInstance(ie.inst.ID); err != nil {
		return fail(exitRefused, err)
	}
	src, closeSrc, err := pickerSource(c.SocketPath)
	if err != nil {
		return fail(exitError, err)
	}
	defer closeSrc()

	theme.ApplyLipGlossDefaults(theme.Current())
	ctx, cancel := context.WithTimeout(context.Background(), paletteGoWait)
	m, err := src(ctx)
	cancel()
	if err != nil {
		return fail(exitError, err)
	}
	if c.WorkspaceID == "" {
		// Typed by hand outside a workspace: pick one, as ntm picks a session.
		if c.WorkspaceID, err = palette.RunSessionSelector(palette.SessionsFrom(m)); err != nil {
			return fail(exitError, err)
		}
		if c.WorkspaceID == "" {
			return exitOK
		}
	}
	label := c.WorkspaceLabel
	if w, err := m.FindWorkspace(c.WorkspaceID); err == nil && label == "" {
		label = w.Label
	}
	if label == "" {
		label = c.WorkspaceID
	}

	jobs := uiJobs(ie, c)
	core := uiCore(ie, c, jobs)
	ctx, cancel = context.WithTimeout(context.Background(), paletteGoWait)
	base, err := core.SendBase(ctx)
	cancel()
	if err != nil {
		return fail(exitError, err)
	}
	cmds, errs := palette.LoadCommands(palette.CatalogPaths(os.Getenv, c.Cwd, base.ProjectRoot, *file))
	for _, e := range errs {
		fmt.Fprintf(stderr, "nhm palette: %v\n", e)
	}
	if len(cmds) == 0 {
		return fail(exitRefused, errors.New("no palette prompts found (see nhm palette --help for the files it reads)"))
	}
	pstore := palette.Store{Dir: palette.StateDir(os.Getenv)}
	state, err := pstore.Load()
	if err != nil {
		fmt.Fprintf(stderr, "nhm palette: pins and favorites: %v\n", err)
	}
	bodies, err := palette.NewBodies(palette.StateDir(os.Getenv), time.Now())
	if err != nil {
		return fail(exitError, fmt.Errorf("the drafts' directory: %w", err))
	}

	ws := c.WorkspaceID
	// uiCore's send keeps the outcome honest: a lost answer names the
	// operation (uiLostError), so the palette shows "unknown"; a job that
	// did not start, or the core's refusal before its claim, names none.
	sub := palette.NewSubmitter(core.Send, paletteReader(ie.db, jobs), paletteMemory(base.ProjectRoot),
		func() (string, error) { return store.NewOperationID(time.Now()) })
	model := palette.NewWithOptions(label, cmds, palette.Options{
		PaletteState: state,
		Store:        pstore,
		Panes: func(ctx context.Context) ([]palette.Pane, int, error) {
			m, err := src(ctx)
			if err != nil {
				return nil, 0, err
			}
			panes, unbound := palette.PanesFrom(m, ws)
			return panes, unbound, nil
		},
		KindKeys:  palette.KindKeys(kinds.All()),
		Submitter: sub,
		Base:      base,
		Bodies:    bodies,
		Handed:    picked,
		HandedBy:  c.Origin,
	})
	// herdr closing the popup hangs the palette up: it then ends as on q,
	// and a send in flight is handed to its job before the process ends.
	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGHUP, syscall.SIGTERM)
	defer stop()
	p := tea.NewProgram(model, tea.WithAltScreen())
	go func() {
		<-sigCtx.Done()
		p.Quit()
	}()
	final, err := p.Run()
	pending := ""
	if fm, ok := final.(palette.Model); ok {
		pending = fm.Pending()
	}
	if pending != "" {
		// The digit started the send in this process: it must reach its
		// job, which then runs on its own, before this process ends.
		if paletteHandOff(sub, jobs, pending, paletteHandOffWait) {
			fmt.Fprintf(stderr, "nhm palette: the send %s runs on; recent operations shows its result\n", pending)
		} else {
			fmt.Fprintf(stderr, "nhm palette: the send %s had not started after %s; recent operations shows whether it ran\n",
				pending, paletteHandOffWait)
		}
	}
	if err != nil {
		return fail(exitError, err)
	}
	if fm, ok := final.(palette.Model); ok {
		if _, perr := fm.Result(); perr != nil {
			return fail(exitError, perr)
		}
	}
	return exitOK
}

// paletteHandOffWait bounds how long the palette waits at its exit for a
// send in flight to reach its job.
const paletteHandOffWait = 10 * time.Second

// paletteHandOff waits until the send opID no longer needs this process:
// nhm ui run has claimed its job (the job runs in a session of its own
// and outlives the palette), or the submission has ended. It is false
// when neither happened within wait.
func paletteHandOff(sub *palette.Submitter, jobs runner.Client, opID string, wait time.Duration) bool {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	t := time.NewTicker(runner.WaitPoll / 4)
	defer t.Stop()
	for {
		// A job with a result has its claim too: one that did not start
		// is claimed and finished at once.
		if !sub.InFlight() || runner.Claimed(jobs.JobDir, opID) {
			return true
		}
		select {
		case <-deadline.C:
			return false
		case <-t.C:
		}
	}
}

// paletteCatalog is nhm palette --list and --show: the prompts of the
// palette's files, read in the palette's directory (NHM_PALETTE_CWD,
// else the context's, else this one), with no herdr call. --list prints
// key, label, category and file, tab-separated; --show prints one
// prompt's text.
func paletteCatalog(file, key string, asJSON bool, stdout, stderr io.Writer) int {
	dir := os.Getenv("NHM_PALETTE_CWD")
	if dir == "" {
		if c, err := popupContext(); err == nil {
			dir = c.Cwd
		}
	}
	if st, err := os.Stat(dir); dir != "" && err == nil && st.IsDir() {
		if err := os.Chdir(dir); err != nil {
			fmt.Fprintf(stderr, "nhm palette: %v\n", err)
			return exitError
		}
	}
	dir, _ = os.Getwd()
	// The project root as a send finds it: the git work tree that holds
	// dir, else none.
	root := ""
	if s, err := core.ResolveScope(context.Background(), core.ScopeRequest{Cwd: dir}, nil); err == nil {
		root = s.ProjectRoot
	}
	cmds, errs := palette.LoadCommands(palette.CatalogPaths(os.Getenv, dir, root, file))
	for _, e := range errs {
		fmt.Fprintf(stderr, "nhm palette: %v\n", e)
	}
	if key != "" {
		for _, c := range cmds {
			if c.Key == key {
				fmt.Fprintln(stdout, c.Prompt)
				return exitOK
			}
		}
		fmt.Fprintf(stderr, "nhm palette: no palette prompt has the key %s\n", key)
		return exitError
	}
	type entry struct {
		Key      string `json:"key"`
		Label    string `json:"label"`
		Category string `json:"category"`
		File     string `json:"file"`
	}
	rows := make([]entry, 0, len(cmds))
	for _, c := range cmds {
		e := entry{Key: c.Key, Label: c.Label, Category: c.Category, File: c.Source}
		if e.Category == "" {
			e.Category = "Other"
		}
		rows = append(rows, e)
	}
	if asJSON {
		data, err := json.Marshal(rows)
		if err != nil {
			fmt.Fprintf(stderr, "nhm palette: %v\n", err)
			return exitError
		}
		fmt.Fprintln(stdout, string(data))
		return exitOK
	}
	for _, e := range rows {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", e.Key, tabless(e.Label), tabless(e.Category), e.File)
	}
	return exitOK
}

// tabless is s with its tabs as spaces, for one field of a tab-separated
// line.
func tabless(s string) string { return strings.ReplaceAll(s, "\t", " ") }

// paletteReader reads what the core recorded for a send. A record still
// open whose job has ended is not in progress: the core no longer runs
// it, and its receipts say what reached each target. With no record
// yet, the job decides: one that still runs is in progress; one that
// ended with an error refused before the claim, so nothing was sent; so
// did one whose process ended with no result, because the core records a
// send before its first prompt.
func paletteReader(db *store.DB, jobs runner.Client) palette.Reader {
	return func(ctx context.Context, id string) (send.Result, error) {
		for again := true; ; again = false {
			res, err := send.Stored(ctx, db, id)
			if err == nil && res.InProgress && jobs.Ended(id) {
				if again {
					// It ended between the reads: the record may have
					// been completed just before.
					continue
				}
				res.InProgress = false
				res.Warnings = append(res.Warnings, "its job ended during the send: each target's receipt is as the core recorded it")
				return res, nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return res, err
			}
			r, done, rerr := jobs.Result(id)
			ended := rerr == nil && !done && jobs.Ended(id)
			switch {
			case rerr != nil:
				return send.Result{}, rerr
			case done && r.Error != "":
				return send.Result{}, fmt.Errorf("%s: %w", r.Error, err)
			case done:
				return res, err
			case ended && again:
				// It ended between the reads: its record or result may
				// be newer than they are.
				continue
			case ended:
				return send.Result{}, fmt.Errorf("its job ended before the core recorded the send: %w", err)
			}
			if _, serr := os.Stat(runner.JobPath(jobs.JobDir, id)); serr == nil {
				return send.Result{OperationID: id, InProgress: true}, nil
			}
			return res, err
		}
	}
}

// paletteMemory is the memory preview's retrieval: nhm memory's, with the
// modes a send of the palette uses (the palette's choice, else nhm's
// config) and the config's budgets.
func paletteMemory(projectRoot string) palette.MemoryFunc {
	return func(ctx context.Context, text string, modes palette.MemoryModes) (palette.Memory, error) {
		cfg, err := loadEnrichConfig()
		if err != nil {
			return palette.Memory{}, err
		}
		cmM, cassM := modes.CM, modes.CASS
		if cmM == "" {
			if cmM, err = enrich.ParseMode(string(cfg.Memory.CM)); err != nil {
				return palette.Memory{}, fmt.Errorf("[memory] cm: %w", err)
			}
		}
		if cassM == "" {
			if cassM, err = enrich.ParseMode(string(cfg.CASS.Context)); err != nil {
				return palette.Memory{}, fmt.Errorf("[cass] context: %w", err)
			}
		}
		cmc, cs, budgets := enrichClients(cfg)
		q := enrich.QueryOf(text)
		retr := enrich.NewRetriever(cmc, cs, budgets, time.Now()).Retrieve(ctx,
			enrich.Request{Query: q, Workspace: projectRoot, CM: cmM, CASS: cassM})
		plan := enrich.PlanFor(retr, nil, budgets.BlockBytes)
		return palette.Memory{Query: q, Block: plan.Block, Items: append(append([]enrich.Item{}, plan.CM...), plan.CASS...),
			CM: retr.CM, CASS: retr.CASS}, retr.Err()
	}
}
