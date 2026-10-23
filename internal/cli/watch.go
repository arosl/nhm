// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/cli/watch.go (newWatchCmd's flags, runWatch, watchLoop,
// runBeadWatch, runFileWatch, filterPanes, watchJSONFrame,
// emitWatchJSONFrame, emitWatchPaneOutput, printPaneOutput,
// printBeadMention, printBeadStatus, watchEventMatchesPattern,
// parseWatchInterval, beadMentionRegexp, extractBeadMentions); see LICENSE
// in this directory and PROVENANCE.md. Local changes: herdr's agents in
// place of tmux panes, each named after its agent (mail name, herdr name
// or pane id) in place of ntm's pane title; output to a writer as ntm's
// --no-color text (nhm prints no colours), with times in UTC; an agent's
// new lines by logs -f's rule (the live region and freshLines) in place of
// computeDiff, and its first read prints its last --tail lines; --pane
// takes a name, herdr name, slot or pane id and refuses a tmux index;
// without a session, the calling pane's workspace; the project is the one
// the session's bound agents share; ntm's fsnotify watcher (nhm's
// internal/core/watcher), whose debounce only a matching change holds
// back, and the bursts that come during a send go as one; when fsnotify
// cannot start or inotify has no watch left, a paced polling scan of the
// project, whose matching change waits for a scan without one, said once
// on stderr (ntm polls on its watcher's ticker only when fsnotify cannot
// start, and leaves the folders past the limit unwatched); --command goes
// through core send to the bound agents, the calling pane left out;
// extractBeadMentions takes lines; nhm's extras: agents named after the
// session, and --count.

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/core/watcher"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store/history"
)

const watchUsage = `Usage: nhm watch [session] [name|slot ...] [--cc] [--cod] [--gmi] [--agy] [--pane P]
                 [--activity] [--tail N] [--interval D] [--no-color] [--no-timestamps]
                 [--bead ID | --pattern GLOB --command TEXT] [--count N] [--socket PATH] [--json]
       (also nhm w)

ntm's watch on herdr. A session is a workspace id or label; without one,
the calling pane's workspace. Every --interval (250ms: a duration, or
whole milliseconds), watch reads each agent's pane and prints its new
lines as [Name HH:MM:SS] line (times in UTC). An agent's first read
prints its last --tail lines (20). Agents that start or stop join or
leave the watch.

A pane's last 10 lines are its live region: the prompt box, status line
and spinner that a working agent redraws in place. watch prints a line
only once it is above them, so an idle agent's last few lines wait for
its next output. A later read prints a line when its text is not in the
last read, so a line that the agent redraws prints once, with its new
text, and a line whose exact text is already there does not print again.
Blank lines are left out.

--cc, --cod, --gmi and --agy keep those kinds; --pane keeps one agent (a
name, herdr name, slot N.M or pane id) and wins over the kinds. With
--activity, a read that fails is skipped; without it, the watch ends.
--no-timestamps leaves the times out; nhm prints no colours.

--bead ID prints the new lines that name the bead, and the bead's status
(br show, in the session's project) when it changes: every 30s, or every
--interval when you give one.

--pattern GLOB --command TEXT watches the session's project instead,
with fsnotify, as ntm does. When a file that matches GLOB changes, watch
waits until such files have had no change for 250ms, then sends TEXT to
the agents once, as nhm send does. A GLOB with a slash matches the path
from the project's root; one without matches the file's name. When
fsnotify cannot start or inotify has no watch left
(fs.inotify.max_user_watches: one watch per folder), watch says so once
on stderr and polls the project: it waits until a scan sees no change to
a matching file, and a scan waits --interval, or five times the last
scan's time when that is longer, so a big tree is scanned less often.

--json prints one JSON object per event: watch_started, pane_output,
bead_mention, bead_status, file_change, dispatch and dispatch_error.
nhm's extras: agents named after the session, and --count N, which
stops after N rounds (with --pattern: N bursts of changes, or N scans
when watch polls).
`

// watchStreamLines and watchBeadLines are the lines a read takes after
// an agent's first, above its live region: ntm's 100 for the stream and
// 200 for a bead.
const (
	watchStreamLines = 100
	watchBeadLines   = 200
)

// watchIgnored are the file and folder names that the file watch skips,
// as ntm's.
var watchIgnored = []string{".git", "node_modules", "dist", "build", "vendor", "coverage", "__pycache__", ".venv", ".idea", ".vscode"}

// watchOptions are watch's flags.
type watchOptions struct {
	filter       watchFilter
	activity     bool
	tail         int
	interval     time.Duration
	intervalSet  bool
	noColor      bool
	noTimestamps bool
	bead         string
	pattern      string
	command      string
	count        int
}

// done reports whether --count rounds are done (0: never).
func (o watchOptions) done(round int) bool { return o.count > 0 && round >= o.count }

// watchFilter is the part of the flags that filterPanes reads.
type watchFilter struct {
	cc, cod, gmi, agy bool
	pane              string
}

func runWatch(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, watchUsage) }
	var o watchOptions
	fs.BoolVar(&o.filter.cc, "cc", false, "only Claude agents")
	fs.BoolVar(&o.filter.cod, "cod", false, "only Codex agents")
	fs.BoolVar(&o.filter.gmi, "gmi", false, "only Gemini agents")
	fs.BoolVar(&o.filter.agy, "agy", false, "only Antigravity agents")
	fs.StringVar(&o.filter.pane, "pane", "", "one agent: a name, herdr name, slot N.M or pane id")
	fs.BoolVar(&o.activity, "activity", false, "skip a read that fails")
	fs.IntVar(&o.tail, "tail", 20, "the lines of an agent's first read")
	interval := fs.String("interval", "250ms", "time between reads: a duration, or whole milliseconds")
	fs.BoolVar(&o.noColor, "no-color", false, "no colours (nhm prints none)")
	fs.BoolVar(&o.noTimestamps, "no-timestamps", false, "leave the times out")
	fs.StringVar(&o.bead, "bead", "", "watch one bead's mentions and status")
	fs.StringVar(&o.pattern, "pattern", "", "watch the files that match this glob")
	fs.StringVar(&o.command, "command", "", "send this to the agents when a file changes")
	fs.IntVar(&o.count, "count", 0, "stop after N rounds (0: until interrupted)")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "one JSON object per event")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	if o.tail < 0 || o.count < 0 {
		fmt.Fprint(stderr, watchUsage)
		return exitRefused
	}
	env := envelope{Command: "watch"}
	refuse := func(err error) int { return output(stdout, stderr, *asJSON, env, err, exitRefused, nil) }
	if o.interval, err = parseWatchInterval(*interval); err != nil {
		return refuse(err)
	}
	fs.Visit(func(f *flag.Flag) { o.intervalSet = o.intervalSet || f.Name == "interval" })
	if indexPattern.MatchString(o.filter.pane) {
		return refuse(fmt.Errorf("--pane %s: herdr has no tmux pane indexes; give a name, the slot N.M (nhm status shows it) or a pane id", o.filter.pane))
	}
	if o.pattern != "" && o.bead != "" {
		return refuse(errors.New("--pattern and --bead cannot be used together"))
	}
	if o.pattern != "" && o.command == "" {
		return refuse(errors.New("--command is required with --pattern"))
	}
	var mentionRE *regexp.Regexp
	if o.bead != "" {
		if mentionRE, err = beadMentionRegexp(o.bead); err != nil {
			return refuse(err)
		}
	}
	session, selectors := "", []string(nil)
	if len(pos) > 0 {
		session, selectors = pos[0], pos[1:]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r, err := openReader(*socket, false)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer r.close()
	ws, err := sessionWorkspace(ctx, r.herdr, session)
	if err != nil {
		return refuse(fmt.Errorf("%w (nhm list shows the sessions)", err))
	}
	t := &watchAgents{r: r, ws: ws, sel: selectors, filter: o.filter}
	if err := t.refresh(ctx); err != nil {
		return output(stdout, stderr, *asJSON, env, fmt.Errorf("failed to get panes: %w", err), exitError, nil)
	}
	out := watchOut{w: stdout, json: *asJSON, noTimestamps: o.noTimestamps, session: t.name, now: time.Now}
	var runErr error
	switch {
	case o.pattern != "" || o.bead != "":
		root, perr := t.project(session == "")
		if perr != nil {
			return output(stdout, stderr, *asJSON, env, perr, exitError, nil)
		}
		if o.bead != "" {
			runErr = watchBead(ctx, t, o, out, mentionRE, root)
			break
		}
		ie, ierr := openInstance(*socket)
		if ierr != nil {
			return output(stdout, stderr, *asJSON, env, ierr, exitError, nil)
		}
		defer ie.db.Close()
		runErr = watchFiles(ctx, t, o, out, stderr, root, func(ctx context.Context, targets []snapshot.Agent) (send.Result, error) {
			req := watchRequest(ie, t, root, targets, o.command)
			if len(req.Selector.Bindings) == 0 {
				// No bound target: an empty selector would select every
				// bound agent of the session.
				return send.Result{}, nil
			}
			return watchSend(ctx, ie, req)
		})
	default:
		runErr = watchStream(ctx, t, o, out)
	}
	if runErr != nil {
		return output(stdout, stderr, *asJSON, env, runErr, exitError, nil)
	}
	if ctx.Err() != nil && !*asJSON {
		fmt.Fprint(stdout, "\n\nWatch mode stopped.\n")
	}
	return exitOK
}

// watchAgents are the agents a watch reads: the session's agents, with
// nhm's selectors and ntm's filters. ntm lists the panes again on every
// poll; watch asks herdr for its agents each round and builds the model
// again when the session's agents change, so an agent that starts or
// stops joins or leaves the watch.
type watchAgents struct {
	r      *reader
	ws     string
	sel    []string
	filter watchFilter
	// name is the session's name for the output: its label, or its id.
	name string
	// key is the session's agents at the last model: pane ids and names.
	key   string
	built bool
	// all are the session's agents; agents are the ones the watch reads.
	all, agents []snapshot.Agent
}

// refresh reads herdr's agents and, when the session's agents changed,
// the model.
func (t *watchAgents) refresh(ctx context.Context) error {
	list, err := t.r.herdr.AgentList(ctx)
	if err != nil {
		return err
	}
	var ids []string
	for _, a := range list {
		if a.WorkspaceID == t.ws {
			ids = append(ids, a.PaneID+"="+a.Name)
		}
	}
	slices.Sort(ids)
	key := strings.Join(ids, ",")
	if t.built && key == t.key {
		return nil
	}
	m, w, err := selectWorkspace(t.r.model(ctx, ""), t.ws)
	if err != nil {
		return err
	}
	t.name, t.key, t.built = firstOf(w.Label, w.ID), key, true
	t.all = m.Agents
	t.agents = filterWatchAgents(selectAgents(m.Agents, t.sel), t.filter)
	return nil
}

// project is the session's project, for --bead and --pattern: the one
// its bound agents share, as ntm reads the session's project folder.
// Without one, an inferred session falls back to the working folder, as
// ntm's does; a named one fails.
func (t *watchAgents) project(inferred bool) (string, error) {
	var roots []string
	for _, a := range t.all {
		if a.Binding != nil && a.Binding.ProjectKey != "" && !slices.Contains(roots, a.Binding.ProjectKey) {
			roots = append(roots, a.Binding.ProjectKey)
		}
	}
	if len(roots) == 1 {
		return roots[0], nil
	}
	if inferred {
		if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
			return cwd, nil
		}
	}
	return "", fmt.Errorf("getting project root failed: the bound agents of session %s are in %d projects, not one", t.name, len(roots))
}

// filterWatchAgents is ntm's filterPanes: without a filter, every agent;
// with --pane, the agent it names; else the agents of the kinds asked.
func filterWatchAgents(agents []snapshot.Agent, f watchFilter) []snapshot.Agent {
	if !f.cc && !f.cod && !f.gmi && !f.agy && f.pane == "" {
		return agents
	}
	var out []snapshot.Agent
	for _, a := range agents {
		if f.pane != "" {
			if watchPaneMatches(a, f.pane) {
				out = append(out, a)
			}
			continue
		}
		switch watchAgentType(a.Kind) {
		case "cc":
			if f.cc {
				out = append(out, a)
			}
		case "cod":
			if f.cod {
				out = append(out, a)
			}
		case "gmi":
			if f.gmi {
				out = append(out, a)
			}
		case "agy":
			if f.agy {
				out = append(out, a)
			}
		}
	}
	return out
}

// watchPaneMatches reports whether sel names the agent: its mail name or
// herdr name (ntm's pane title), its slot or its pane id.
func watchPaneMatches(a snapshot.Agent, sel string) bool {
	return a.Slot == sel || a.PaneID == sel || strings.EqualFold(a.HerdrName, sel) ||
		(a.Binding != nil && strings.EqualFold(a.Binding.MailName, sel))
}

// selectAgents keeps the agents named by mail name, herdr name, slot or
// pane id; no selector keeps all.
func selectAgents(agents []snapshot.Agent, sel []string) []snapshot.Agent {
	if len(sel) == 0 {
		return agents
	}
	var out []snapshot.Agent
	for _, a := range agents {
		if slices.ContainsFunc(sel, func(s string) bool { return watchPaneMatches(a, s) }) {
			out = append(out, a)
		}
	}
	return out
}

// watchAgentType is ntm's canonical agent type of a kind, as its watch
// frames carry it: cc, cod, gmi, agy, and ntm's name for the others.
func watchAgentType(kind string) string {
	switch t := snapshot.NTMType(kind); t {
	case "claude":
		return "cc"
	case "codex":
		return "cod"
	case "gemini":
		return "gmi"
	case "antigravity":
		return "agy"
	default:
		return t
	}
}

// watchRead reads an agent's last n lines above its live region.
func watchRead(ctx context.Context, h *herdr.Client, pane string, n int) ([]string, error) {
	n += logsLiveLines
	r, err := h.PaneRead(ctx, herdr.PaneReadParams{PaneID: pane, Source: herdr.ReadRecent, Lines: &n})
	if err != nil {
		return nil, err
	}
	return settledLines(r.Text), nil
}

// watchNext waits for the next round: false when the watch is stopped.
// Tests replace it.
var watchNext = func(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// watchStream is ntm's watchLoop: each round, the agents' new lines; an
// agent's first read prints its last --tail lines.
func watchStream(ctx context.Context, t *watchAgents, o watchOptions, out watchOut) error {
	if out.json {
		out.frame(watchJSONFrame{Event: "watch_started"})
	} else {
		fmt.Fprintf(out.w, "\nWatching session: %s\nPress Ctrl+C to stop\n", out.session)
	}
	last := map[string][]string{}
	for round := 1; ; round++ {
		if round > 1 {
			if !watchNext(ctx, o.interval) {
				return nil
			}
			if err := t.refresh(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("failed to get panes: %w", err)
			}
		}
		for _, a := range t.agents {
			cur, err := watchRead(ctx, t.r.herdr, a.PaneID, max(o.tail, watchStreamLines))
			if err != nil {
				if o.activity || ctx.Err() != nil {
					continue
				}
				return fmt.Errorf("failed to capture pane output: %w", err)
			}
			prev, seen := last[a.PaneID]
			last[a.PaneID] = cur
			if !seen {
				out.paneOutput(a, cur[max(0, len(cur)-o.tail):])
				continue
			}
			out.paneOutput(a, freshLines(prev, cur))
		}
		if o.done(round) {
			return nil
		}
	}
}

// watchBeadStatus reads a bead's status with br show in the project, as
// ntm's bv.GetBeadStatus. Tests replace it.
var watchBeadStatus = func(ctx context.Context, root, id string) (string, error) {
	b, err := send.BrShow(ctx, root, id)
	if err == nil && b.Status == "" {
		err = errors.New("status field not found in bead response")
	}
	return b.Status, err
}

// watchBead is ntm's runBeadWatch: each round, the agents' new lines that
// name the bead (all of an agent's first read), and the bead's status
// when it changes, read every 30s or every --interval given.
func watchBead(ctx context.Context, t *watchAgents, o watchOptions, out watchOut, mentionRE *regexp.Regexp, root string) error {
	statusPoll := 30 * time.Second
	if o.intervalSet {
		statusPoll = o.interval
	}
	if out.json {
		out.frame(watchJSONFrame{Event: "watch_started", Bead: o.bead})
	} else {
		fmt.Fprintf(out.w, "\nWatching bead %s in session: %s\nPress Ctrl+C to stop\n", o.bead, out.session)
	}
	last := map[string][]string{}
	lastStatus := ""
	var lastCheck time.Time
	for round := 1; ; round++ {
		if round > 1 {
			if !watchNext(ctx, o.interval) {
				return nil
			}
			if err := t.refresh(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("failed to get panes: %w", err)
			}
		}
		for _, a := range t.agents {
			cur, err := watchRead(ctx, t.r.herdr, a.PaneID, watchBeadLines)
			if err != nil {
				continue
			}
			lines := freshLines(last[a.PaneID], cur)
			last[a.PaneID] = cur
			for _, m := range extractBeadMentions(lines, mentionRE) {
				out.beadMention(a, o.bead, m)
			}
		}
		if lastCheck.IsZero() || out.now().Sub(lastCheck) >= statusPoll {
			status, err := watchBeadStatus(ctx, root, o.bead)
			if err != nil {
				status = "unknown"
			}
			if status != lastStatus {
				out.beadStatus(o.bead, status, err)
				lastStatus = status
			}
			lastCheck = out.now()
		}
		if o.done(round) {
			return nil
		}
	}
}

// fileMeta is what a scan keeps of a path, as ntm's polling watcher.
type fileMeta struct {
	modTime time.Time
	size    int64
	mode    fs.FileMode
}

func (m fileMeta) same(o fileMeta) bool {
	return m.modTime.Equal(o.modTime) && m.size == o.size && m.mode == o.mode
}

// scanTree lists the paths under root and their metadata, without the
// ignored names; a path that cannot be read is left out. It is nil when
// root itself cannot be read, so that a missing root does not read as
// every file removed.
func scanTree(root string) map[string]fileMeta {
	out := map[string]fileMeta{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				out = nil
				return filepath.SkipAll
			}
			return nil
		}
		if path != root && slices.Contains(watchIgnored, d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info, err := d.Info(); err == nil {
			out[path] = fileMeta{modTime: info.ModTime(), size: info.Size(), mode: info.Mode()}
		}
		return nil
	})
	return out
}

// changedPaths are the paths created, written, changed in mode or
// removed between two scans, sorted.
func changedPaths(prev, cur map[string]fileMeta) []string {
	var out []string
	for p, m := range cur {
		if old, ok := prev[p]; !ok || !old.same(m) {
			out = append(out, p)
		}
	}
	for p := range prev {
		if _, ok := cur[p]; !ok {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

// watchSend runs a file watch's send as nhm send does: core send under
// the owner lock. Tests replace it.
var watchSend = func(ctx context.Context, ie *instanceEnv, req send.Request) (send.Result, error) {
	res, _, err := runSendCore(ctx, ie, req)
	return res, err
}

// watchRequest is the send of --command to the bound targets, in any
// state, as ntm's dispatch delivers to every target.
func watchRequest(ie *instanceEnv, t *watchAgents, root string, targets []snapshot.Agent, text string) send.Request {
	var ids []string
	for _, a := range targets {
		if a.Binding != nil {
			ids = append(ids, a.Binding.ID)
		}
	}
	return send.Request{Session: t.name, WorkspaceID: t.ws, ProjectRoot: root, Message: text, WhenAny: true,
		Source: history.SourceCLI, Selector: send.Selector{Bindings: ids},
		Scope: map[string]string{"instance_id": ie.inst.ID, "workspace_id": t.ws, "project_root": root}}
}

// watchScanFactor paces the file watch: a scan waits at least this many
// times the last scan's duration, so that a big tree does not keep a
// core busy (ACFS's 43,000 paths take about 1s a scan; ScarletOrchid's
// review, nhm-zac).
const watchScanFactor = 5

// scanPause is the wait before the next scan: --interval, or five times
// the last scan's duration when that is longer.
func scanPause(interval, scan time.Duration) time.Duration {
	return max(interval, watchScanFactor*scan)
}

// timedScan is scanTree and how long it took.
func timedScan(root string) (map[string]fileMeta, time.Duration) {
	start := time.Now()
	tree := scanTree(root)
	return tree, time.Since(start)
}

// watchNotifier is what the file watch uses of ntm's watcher.
type watchNotifier interface {
	Add(path string) error
	Close() error
}

// watchNotify starts ntm's fsnotify watcher for the file watch:
// recursive, without ntm's ignored names, and with a debounce that only
// the paths that match hold back. Tests replace it.
var watchNotify = func(handler watcher.Handler, onErr watcher.ErrorHandler, match func(string) bool) (watchNotifier, error) {
	w, err := watcher.New(handler, watcher.WithRecursive(true), watcher.WithIgnorePaths(watchIgnored),
		watcher.WithMatch(match), watcher.WithErrorHandler(onErr))
	if err != nil {
		return nil, err
	}
	return w, nil
}

// watchNoWatches is the note of a file watch that polls because inotify
// has no watch left: it takes one per folder.
const watchNoWatches = "nhm watch: inotify has no watch left (fs.inotify.max_user_watches); polling the project instead"

// watchFiles is ntm's runFileWatch: ntm's fsnotify watcher on the
// project. When paths that match --pattern have had no change for the
// debounce's 250ms, the first of them goes to watchDispatch, and
// --command to the agents once; the bursts that come during a send go as
// one, after it. When fsnotify cannot start, or inotify has no watch
// left at the start or for a new folder, the watcher's watches are
// released and the project is polled instead, said once on stderr.
func watchFiles(ctx context.Context, t *watchAgents, o watchOptions, out watchOut, stderr io.Writer, root string,
	dispatch func(context.Context, []snapshot.Agent) (send.Result, error)) error {
	if out.json {
		out.frame(watchJSONFrame{Event: "watch_started", Path: root, Text: o.pattern})
	} else {
		fmt.Fprintf(out.w, "\nWatching files matching '%s' in %s...\nWill run command: %s\nPress Ctrl+C to stop\n", o.pattern, root, o.command)
	}
	changed := make(chan string, 1)
	full := make(chan struct{}, 1)
	n, err := watchNotify(func(events []watcher.Event) {
		select {
		case changed <- events[0].Path:
		default:
			// A burst waits already; this one goes with it.
		}
	}, func(err error) {
		if errors.Is(err, watcher.ErrNoWatches) {
			select {
			case full <- struct{}{}:
			default:
			}
		}
	}, func(path string) bool { return watchEventMatchesPattern(o.pattern, root, path) })
	if err != nil {
		fmt.Fprintf(stderr, "nhm watch: fsnotify cannot start (%v); polling the project instead\n", err)
		return pollFiles(ctx, t, o, out, root, 1, "", dispatch)
	}
	defer n.Close()
	if err := n.Add(root); err != nil {
		if !errors.Is(err, watcher.ErrNoWatches) {
			return fmt.Errorf("failed to watch directory: %w", err)
		}
		_ = n.Close()
		fmt.Fprintln(stderr, watchNoWatches)
		return pollFiles(ctx, t, o, out, root, 1, "", dispatch)
	}
	for round := 1; ; round++ {
		select {
		case <-ctx.Done():
			return nil
		case path := <-changed:
			watchDispatch(ctx, t, o, out, path, dispatch)
		case <-full:
			// Close waits for a burst that is being handed on.
			_ = n.Close()
			fmt.Fprintln(stderr, watchNoWatches)
			pending := ""
			select {
			case pending = <-changed:
			default:
			}
			return pollFiles(ctx, t, o, out, root, round, pending, dispatch)
		}
		if o.done(round) {
			return nil
		}
	}
}

// pollFiles is the file watch's fallback: a paced scan of the project.
// The first path that matches --pattern waits until a scan sees no
// change to a matching path, then --command goes to the agents once.
// Other paths do not hold it back: with scans seconds apart on a big
// tree, unrelated writes (a log, a database) would hold it back for good.
// The rounds go on from first, and pending is a matching path that
// fsnotify handed on and that is not sent yet.
func pollFiles(ctx context.Context, t *watchAgents, o watchOptions, out watchOut, root string, first int, pending string,
	dispatch func(context.Context, []snapshot.Agent) (send.Result, error)) error {
	seen, took := timedScan(root)
	for round := first; ; round++ {
		if !watchNext(ctx, scanPause(o.interval, took)) {
			return nil
		}
		var cur map[string]fileMeta
		cur, took = timedScan(root)
		if cur == nil || seen == nil {
			// The root could not be read, now or at the last scan: no
			// change is known.
			seen = cur
			if o.done(round) {
				return nil
			}
			continue
		}
		hit := ""
		for _, p := range changedPaths(seen, cur) {
			if watchEventMatchesPattern(o.pattern, root, p) {
				hit = p
				break
			}
		}
		seen = cur
		switch {
		case hit != "":
			if pending == "" {
				pending = hit
			}
		case pending != "":
			watchDispatch(ctx, t, o, out, pending, dispatch)
			pending = ""
		}
		if o.done(round) {
			return nil
		}
	}
}

// watchDispatch is the handler of ntm's runFileWatch for a changed path
// that matches: the agents again, then the send, and what failed. The
// calling pane, which runs the watch, is left out, as nhm send leaves it
// out.
func watchDispatch(ctx context.Context, t *watchAgents, o watchOptions, out watchOut, path string,
	dispatch func(context.Context, []snapshot.Agent) (send.Result, error)) {
	if out.json {
		out.frame(watchJSONFrame{Event: "file_change", Path: path})
	} else if !o.noColor {
		fmt.Fprintf(out.w, "File changed: %s\n", path)
	}
	if err := t.refresh(ctx); err != nil {
		if ctx.Err() != nil {
			return
		}
		if out.json {
			out.frame(watchJSONFrame{Event: "dispatch_error", Error: fmt.Sprintf("getting panes: %v", err)})
		} else {
			fmt.Fprintf(out.w, "Error getting panes: %v\n", err)
		}
		return
	}
	caller := os.Getenv("HERDR_PANE_ID")
	var targets []snapshot.Agent
	for _, a := range t.agents {
		if a.PaneID != caller {
			targets = append(targets, a)
		}
	}
	if len(targets) == 0 {
		if out.json {
			out.frame(watchJSONFrame{Event: "dispatch_error", Error: "no target agents found"})
		} else {
			fmt.Fprintln(out.w, "No target agents found")
		}
		return
	}
	if !out.json {
		fmt.Fprintf(out.w, "Triggering command on %d pane(s)...\n", len(targets))
	}
	res, err := dispatch(ctx, targets)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		if out.json {
			out.frame(watchJSONFrame{Event: "dispatch_error", Error: err.Error()})
		} else {
			fmt.Fprintf(out.w, "File-watch dispatch completed with failures: %v\n", err)
		}
	}
	delivered, failed := 0, 0
	fail := func(pane, reason string) {
		failed++
		if out.json {
			out.frame(watchJSONFrame{Event: "dispatch_error", PaneID: pane, Error: reason})
		} else {
			fmt.Fprintf(out.w, "Failed to send to pane %s: %s\n", pane, reason)
		}
	}
	for _, a := range targets {
		if a.Binding == nil {
			fail(a.PaneID, "no binding: nhm send reaches bound agents only")
		}
	}
	for _, r := range res.Targets {
		switch {
		case r.Status == send.StatusSubmitted:
			delivered++
		case !send.Unknown(r.Status):
			fail(r.PaneID, firstOf(r.Result.Reason, r.ErrorCode, r.Status))
		}
	}
	if out.json {
		out.frame(watchJSONFrame{Event: "dispatch", Text: o.command, Delivered: delivered, Failed: failed})
	}
}

// watchJSONFrame is one NDJSON event of watch --json.
type watchJSONFrame struct {
	Event     string   `json:"event"` // watch_started | pane_output | bead_mention | bead_status | file_change | dispatch | dispatch_error
	Timestamp string   `json:"timestamp"`
	Session   string   `json:"session"`
	Pane      string   `json:"pane,omitempty"`
	PaneID    string   `json:"pane_id,omitempty"`
	AgentType string   `json:"agent_type,omitempty"`
	Lines     []string `json:"lines,omitempty"`
	Bead      string   `json:"bead,omitempty"`
	Text      string   `json:"text,omitempty"`
	Status    string   `json:"status,omitempty"`
	Path      string   `json:"path,omitempty"`
	Delivered int      `json:"delivered,omitempty"`
	Failed    int      `json:"failed,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// watchOut prints a watch's events: ntm's --no-color text, or one JSON
// frame per event.
type watchOut struct {
	w            io.Writer
	json         bool
	noTimestamps bool
	session      string
	now          func() time.Time
}

// frame is emitWatchJSONFrame: the frame with its time and the session,
// on one line.
func (o watchOut) frame(f watchJSONFrame) {
	f.Timestamp = o.now().UTC().Format(time.RFC3339Nano)
	f.Session = o.session
	_ = json.NewEncoder(o.w).Encode(f)
}

// clock is the time of a printed line, or "" with --no-timestamps.
func (o watchOut) clock() string {
	if o.noTimestamps {
		return ""
	}
	return o.now().UTC().Format("15:04:05")
}

// paneOutput is emitWatchPaneOutput: an agent's lines that are not
// blank, as one pane_output frame or one line each.
func (o watchOut) paneOutput(a snapshot.Agent, lines []string) {
	var kept []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			kept = append(kept, l)
		}
	}
	if len(kept) == 0 {
		return
	}
	if o.json {
		o.frame(watchJSONFrame{Event: "pane_output", Pane: label(a), PaneID: a.PaneID, AgentType: watchAgentType(a.Kind), Lines: kept})
		return
	}
	ts := o.clock()
	for _, l := range kept {
		if ts != "" {
			fmt.Fprintf(o.w, "[%s %s] %s\n", label(a), ts, l)
		} else {
			fmt.Fprintf(o.w, "[%s] %s\n", label(a), l)
		}
	}
}

// beadMention is printBeadMention, or a bead_mention frame.
func (o watchOut) beadMention(a snapshot.Agent, bead, line string) {
	if o.json {
		o.frame(watchJSONFrame{Event: "bead_mention", Pane: label(a), PaneID: a.PaneID, AgentType: watchAgentType(a.Kind), Bead: bead, Text: line})
		return
	}
	if ts := o.clock(); ts != "" {
		fmt.Fprintf(o.w, "[%s] [%s] mention: %s\n", ts, label(a), line)
	} else {
		fmt.Fprintf(o.w, "[%s] mention: %s\n", label(a), line)
	}
}

// beadStatus is printBeadStatus, or a bead_status frame.
func (o watchOut) beadStatus(bead, status string, err error) {
	if o.json {
		f := watchJSONFrame{Event: "bead_status", Bead: bead, Status: status}
		if err != nil {
			f.Error = err.Error()
		}
		o.frame(f)
		return
	}
	message := fmt.Sprintf("bead status: %s", status)
	if err != nil {
		message = fmt.Sprintf("%s (lookup error: %v)", message, err)
	}
	if ts := o.clock(); ts != "" {
		fmt.Fprintf(o.w, "[%s] %s\n", ts, message)
	} else {
		fmt.Fprintln(o.w, message)
	}
}

// watchEventMatchesPattern reports whether a changed path matches the
// pattern: with a slash, the path from the watch root; without, the
// file's name.
func watchEventMatchesPattern(pattern, watchRoot, eventPath string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	if strings.Contains(pattern, string(os.PathSeparator)) || strings.Contains(pattern, "/") {
		rootAbs, err := filepath.Abs(watchRoot)
		if err != nil {
			return false
		}
		eventAbs, err := filepath.Abs(eventPath)
		if err != nil {
			return false
		}
		rel, err := filepath.Rel(rootAbs, eventAbs)
		if err != nil {
			return false
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return false
		}
		matched, _ := filepath.Match(filepath.ToSlash(pattern), filepath.ToSlash(rel))
		return matched
	}
	matched, _ := filepath.Match(pattern, filepath.Base(eventPath))
	return matched
}

// parseWatchInterval reads --interval: a duration or whole milliseconds,
// 250ms when empty.
func parseWatchInterval(raw string) (time.Duration, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 250 * time.Millisecond, nil
	}
	if millis, err := strconv.Atoi(value); err == nil {
		if millis <= 0 {
			return 0, fmt.Errorf("--interval must be positive")
		}
		return time.Duration(millis) * time.Millisecond, nil
	}
	interval, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid --interval %q: use duration (e.g. 250ms, 2s) or integer milliseconds", raw)
	}
	if interval <= 0 {
		return 0, fmt.Errorf("--interval must be positive")
	}
	return interval, nil
}

// beadMentionRegexp matches the bead id as a whole word, in any case.
func beadMentionRegexp(beadID string) (*regexp.Regexp, error) {
	id := strings.TrimSpace(beadID)
	if id == "" {
		return nil, fmt.Errorf("--bead is required")
	}
	pattern := fmt.Sprintf(`(?i)\b%s\b`, regexp.QuoteMeta(id))
	return regexp.Compile(pattern)
}

// extractBeadMentions keeps the lines that name the bead, trimmed.
func extractBeadMentions(lines []string, mentionRE *regexp.Regexp) []string {
	mentions := make([]string, 0)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if mentionRE.MatchString(trimmed) {
			mentions = append(mentions, trimmed)
		}
	}
	return mentions
}
