package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/integrations/guards"
	"github.com/arosl/nhm/internal/store"
)

// reader is what the read verbs need: herdr, and the enrolled instance's
// database when there is one. A server that is not enrolled still shows
// herdr's view, with the local sources marked unavailable.
type reader struct {
	socket   string
	herdr    *herdr.Client
	inst     *store.Instance
	db       *store.DB
	mail     snapshot.Mail
	warnings []string
}

func openReader(socket string, withMail bool) (*reader, error) {
	if socket == "" {
		socket = os.Getenv("HERDR_SOCKET_PATH")
	}
	if socket == "" {
		return nil, errors.New("no herdr socket: give --socket or run inside herdr (HERDR_SOCKET_PATH)")
	}
	r := &reader{socket: socket, herdr: herdr.New(socket)}
	root, err := store.StateRoot()
	if err != nil {
		return nil, err
	}
	inst, err := store.Lookup(root, socket)
	switch {
	case errors.Is(err, store.ErrNotEnrolled):
		r.warnings = append(r.warnings, "this herdr server is not enrolled; nhm shows herdr's view only")
	case err != nil:
		return nil, err
	default:
		r.inst = &inst
		if r.db, err = store.Open(inst.Dir); err != nil {
			return nil, err
		}
	}
	if withMail {
		c, err := agentmail.NewClient("")
		if err != nil {
			r.warnings = append(r.warnings, "Agent Mail: "+err.Error())
		} else {
			r.mail = c
		}
	}
	return r, nil
}

func (r *reader) close() {
	if r.db != nil {
		_ = r.db.Close()
	}
}

// beadsInProgress lists a project's beads in progress by assignee, in the
// bead databases retire searches (the project's .beads and nhm's
// configured ones), for each agent's bead (METHOD-MATRIX E14,
// acfs-9hkh.82). A project with no bead database has none. Tests replace
// it.
var beadsInProgress = func(ctx context.Context, root string) (map[string][]string, error) {
	got, err := lifecycle.CLI{LoadBeadDBs: beadDBs}.InProgress(ctx, root)
	if errors.Is(err, lifecycle.ErrNoBeadDB) {
		return nil, nil
	}
	return got, err
}

func (r *reader) model(ctx context.Context, projectKey string) *snapshot.Model {
	return r.build(ctx, projectKey, false)
}

// build is model with each agent's bead read too (withBeads): only nhm
// status shows beads, so the other readers never run br, and a host
// without it does not degrade their health (acfs-9hkh.82).
func (r *reader) build(ctx context.Context, projectKey string, withBeads bool) *snapshot.Model {
	deps := snapshot.Deps{Herdr: r.herdr, DB: r.db, Mail: r.mail,
		Guards: func(kind, projectRoot string) []guards.Result {
			return guards.Check(kind, guards.Env{ProjectRoot: projectRoot})
		}}
	if withBeads {
		deps.Beads = beadsInProgress
	}
	if path, err := limitsSeenPath(r.inst); err == nil {
		deps.LimitsFile = path
	}
	return snapshot.Build(ctx, deps, snapshot.Options{ProjectKey: projectKey})
}

// selectWorkspace narrows the model to one session (a workspace label or
// id); "" keeps every workspace.
func selectWorkspace(m *snapshot.Model, session string) (*snapshot.Model, *snapshot.Workspace, error) {
	if session == "" {
		return m, nil, nil
	}
	if !m.HerdrOK() {
		return nil, nil, fmt.Errorf("herdr did not answer: %s", m.Sources[snapshot.SourceHerdr].Error)
	}
	w, err := m.FindWorkspace(session)
	if err != nil {
		return nil, nil, err
	}
	return m.Only(w.ID), &w, nil
}

// readFlags are the flags every read verb has.
type readFlags struct {
	socket  *string
	project *string
	asJSON  *bool
	noMail  *bool
}

func addReadFlags(fs *flag.FlagSet) readFlags {
	return readFlags{
		socket:  fs.String("socket", "", "herdr API socket (default HERDR_SOCKET_PATH)"),
		project: fs.String("project", "", "keep the agents bound in this project"),
		asJSON:  fs.Bool("json", false, "nhm's JSON envelope"),
		noMail:  fs.Bool("no-mail", false, "do not ask Agent Mail"),
	}
}

func (f readFlags) projectKey(ctx context.Context) (string, error) {
	if *f.project == "" {
		return "", nil
	}
	s, err := projectScope(ctx, "", *f.project)
	if err != nil {
		return "", err
	}
	return s.MailProjectKey, nil
}

// degradedWarnings turns the degraded sources into warnings.
func degradedWarnings(m *snapshot.Model) []string {
	var out []string
	for _, name := range m.Degraded() {
		h := m.Sources[name]
		out = append(out, fmt.Sprintf("%s is %s: %s", name, h.Status, h.Error))
	}
	return out
}

// readVerb runs the common frame of a read verb: flags, reader, model,
// session, and output.
type readVerb struct {
	name     string
	usage    string
	session  bool // takes an optional session argument
	needSess bool // the session is required
	withMail bool
	withBead bool // read each agent's bead (nhm status only)
	extra    func(fs *flag.FlagSet)
	run      func(ctx context.Context, r *reader, m *snapshot.Model, ws *snapshot.Workspace, f readFlags) (any, func(io.Writer), error)
}

func (v readVerb) main(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(v.name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, v.usage) }
	f := addReadFlags(fs)
	if v.extra != nil {
		v.extra(fs)
	}
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	maxPos := 0
	if v.session {
		maxPos = 1
	}
	if len(pos) > maxPos || (v.needSess && len(pos) == 0) {
		fmt.Fprint(stderr, v.usage)
		return exitRefused
	}
	session := ""
	if len(pos) == 1 {
		session = pos[0]
	}
	env := envelope{Command: v.name}
	ctx := context.Background()
	project, err := f.projectKey(ctx)
	if err != nil {
		return output(stdout, stderr, *f.asJSON, env, err, exitRefused, nil)
	}
	r, err := openReader(*f.socket, v.withMail && !*f.noMail)
	if err != nil {
		return output(stdout, stderr, *f.asJSON, env, err, exitError, nil)
	}
	defer r.close()
	m := r.build(ctx, project, v.withBead)
	m, ws, err := selectWorkspace(m, session)
	env.Warnings = append(env.Warnings, r.warnings...)
	if err != nil {
		return output(stdout, stderr, *f.asJSON, env, err, exitError, nil)
	}
	env.Warnings = append(env.Warnings, degradedWarnings(m)...)
	result, text, err := v.run(ctx, r, m, ws, f)
	env.Result = result
	code := exitOK
	if err != nil {
		code = exitError
		// A read verb that fails a check (health) still shows what it read.
		if !*f.asJSON && text != nil {
			text(stdout)
		}
	}
	return output(stdout, stderr, *f.asJSON, env, err, code, text)
}

const statusUsage = `Usage: nhm status [session] [--watch] [--interval D] [--project DIR] [--no-mail] [--socket PATH] [--json]

One row per agent: slot, Agent Mail name, kind, model, herdr state,
unread mail and pending acks, pane. Bindings whose pane holds no agent and
displaced bindings follow. A source that did not answer shows "?" and a
warning, never zeros. --watch redraws on herdr's events and every 30 s.
`

func runStatus(args []string, stdout, stderr io.Writer) int {
	var interval *time.Duration
	var count *int
	v := readVerb{name: "status", usage: statusUsage, session: true, withMail: true, withBead: true,
		extra: func(fs *flag.FlagSet) {
			fs.Bool("watch", false, "redraw on changes")
			interval = fs.Duration("interval", 30*time.Second, "full refresh interval with --watch")
			count = fs.Int("count", 0, "with --watch, stop after N renders (0: until interrupted)")
		},
		run: func(ctx context.Context, r *reader, m *snapshot.Model, ws *snapshot.Workspace, f readFlags) (any, func(io.Writer), error) {
			return statusResult(m), func(w io.Writer) { printStatus(w, m) }, nil
		}}
	if !hasFlag(args, "watch") {
		return v.main(args, stdout, stderr)
	}
	return watchVerb(v, args, stdout, stderr, func() (*time.Duration, *int) { return interval, count },
		func(w io.Writer, m *snapshot.Model) { printStatus(w, m) }, func(m *snapshot.Model) any { return statusResult(m) })
}

// hasFlag reports whether args hold --name or -name, with or without a
// value.
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		for _, p := range []string{"--" + name, "-" + name} {
			if a == p || strings.HasPrefix(a, p+"=") {
				return true
			}
		}
	}
	return false
}

// statusResult is status's JSON result.
func statusResult(m *snapshot.Model) map[string]any {
	return map[string]any{"generated_at": m.GeneratedAt, "workspaces": m.Workspaces, "agents": m.Agents,
		"absent_bindings": m.Absent, "displaced_bindings": m.Displaced, "sources": m.Sources}
}

func wsLabel(m *snapshot.Model, id string) string {
	for _, w := range m.Workspaces {
		if w.ID == id {
			if w.Label != "" {
				return w.Label
			}
			return w.ID
		}
	}
	return id
}

// beadCell is an agent's bead in progress, with "+N" when it has more.
func beadCell(beads []string) string {
	switch len(beads) {
	case 0:
		return "-"
	case 1:
		return beads[0]
	}
	return fmt.Sprintf("%s +%d", beads[0], len(beads)-1)
}

func printStatus(w io.Writer, m *snapshot.Model) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION\tSLOT\tNAME\tKIND\tMODEL\tSTATE\tBEAD\tMAIL\tGUARDS\tPANE")
	for _, a := range m.Agents {
		name, model := dash(a.HerdrName), "-"
		if a.Binding != nil {
			name, model = a.Binding.MailName, dash(a.Binding.Model)
			if !a.Binding.NameMatches {
				name += " (herdr: " + dash(a.HerdrName) + ")"
			}
		}
		state := string(a.State)
		if a.Limit != "" {
			state += " (at limit)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", wsLabel(m, a.WorkspaceID), dash(a.Slot), name, a.Kind, model,
			state, beadCell(a.Beads), mailCell(a.Mail, a.Binding != nil), guardsCell(a.Guards), a.PaneID)
	}
	for _, b := range m.Absent {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", wsLabel(m, b.WorkspaceID), dash(b.Slot), b.MailName, b.Kind,
			dash(b.Model), "absent", "-", "-", "-", dash(b.PaneID))
	}
	for _, b := range m.Displaced {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", wsLabel(m, b.WorkspaceID), dash(b.Slot), b.MailName, b.Kind,
			dash(b.Model), "displaced", "-", "-", "-", dash(b.PaneID))
	}
	_ = tw.Flush()
	if len(m.Agents)+len(m.Absent)+len(m.Displaced) == 0 {
		fmt.Fprintln(w, "no agents")
	}
}

// guardsCell shows "ok" when every guard the kind supports runs, else the
// ones that do not, with their state; "-" when the guards were not checked.
func guardsCell(states map[string]guards.State) string {
	if states == nil {
		return "-"
	}
	var bad []string
	for _, g := range guards.Guards {
		switch st := states[g]; st {
		case guards.Configured, guards.VerifiedForLaunch, guards.Unsupported:
		default:
			bad = append(bad, g+":"+string(st))
		}
	}
	if len(bad) == 0 {
		return "ok"
	}
	return strings.Join(bad, ",")
}

// mailCell shows unread/pending acks, "?" when Agent Mail did not answer,
// and "-" for an unbound agent or when mail was not asked.
func mailCell(s *snapshot.MailState, bound bool) string {
	switch {
	case !bound || s == nil:
		return "-"
	case s.Status != snapshot.Fresh:
		return "?"
	case s.PendingAcks > 0:
		return fmt.Sprintf("%d/%d ack", s.Unread, s.PendingAcks)
	default:
		return fmt.Sprintf("%d", s.Unread)
	}
}

const listUsage = `Usage: nhm list [--project DIR] [--socket PATH] [--json]

Every bound agent across workspaces, with the native session herdr reports
for it (that is how a "fresh" reviewer is proved), and the bindings whose
pane holds no agent.
`

func runList(args []string, stdout, stderr io.Writer) int {
	return readVerb{name: "list", usage: listUsage,
		run: func(ctx context.Context, r *reader, m *snapshot.Model, ws *snapshot.Workspace, f readFlags) (any, func(io.Writer), error) {
			var bound []snapshot.Agent
			for _, a := range m.Agents {
				if a.Binding != nil {
					bound = append(bound, a)
				}
			}
			if bound == nil {
				bound = []snapshot.Agent{}
			}
			res := map[string]any{"agents": bound, "absent_bindings": m.Absent, "displaced_bindings": m.Displaced}
			return res, func(w io.Writer) {
				tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "NAME\tKIND\tSTATE\tSESSION\tSLOT\tPANE\tBINDING\tNATIVE SESSION")
				for _, a := range bound {
					session := "-"
					if a.Session != nil {
						session = a.Session.Value
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s/%d\t%s\n", a.Binding.MailName, a.Kind, a.State,
						wsLabel(m, a.WorkspaceID), dash(a.Slot), a.PaneID, a.Binding.ID, a.Binding.Generation, session)
				}
				for _, b := range m.Absent {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s/%d\t-\n", b.MailName, b.Kind, "absent",
						wsLabel(m, b.WorkspaceID), dash(b.Slot), dash(b.PaneID), b.ID, b.Generation)
				}
				_ = tw.Flush()
			}, nil
		}}.main(args, stdout, stderr)
}

const snapshotUsage = `Usage: nhm snapshot [session] [--project DIR] [--no-mail] [--socket PATH]

The one machine read model, as JSON: workspaces and agents, binding ids
and generations, states, mail, open operations, uncertain sends, quota,
pending wakes, and each source's health. A robot bootstraps with one call.
`

func runSnapshot(args []string, stdout, stderr io.Writer) int {
	return readVerb{name: "snapshot", usage: snapshotUsage, session: true, withMail: true,
		run: func(ctx context.Context, r *reader, m *snapshot.Model, ws *snapshot.Workspace, f readFlags) (any, func(io.Writer), error) {
			*f.asJSON = true
			return snapshotResult(m), nil, nil
		}}.main(args, stdout, stderr)
}

func snapshotResult(m *snapshot.Model) map[string]any {
	return map[string]any{"model": m, "attention": snapshot.Attention(m)}
}

const attentionUsage = `Usage: nhm attention [session] [--watch] [--json|--jsonl] [--project DIR] [--no-mail] [--socket PATH]

What needs a decision, most severe first: blocked agents, finished turns
nobody looked at, unread or overdue mail, sends that may have arrived,
unresolved operations, bindings without an agent or displaced, names that
do not match, unbound agents, quota at a limit or stale, pending wakes,
and sources that did not answer.
`

func runAttention(args []string, stdout, stderr io.Writer) int {
	var jsonl *bool
	var interval *time.Duration
	var count *int
	v := readVerb{name: "attention", usage: attentionUsage, session: true, withMail: true,
		extra: func(fs *flag.FlagSet) {
			jsonl = fs.Bool("jsonl", false, "one JSON object per item")
			fs.Bool("watch", false, "print again on changes")
			interval = fs.Duration("interval", 30*time.Second, "full refresh interval with --watch")
			count = fs.Int("count", 0, "with --watch, stop after N renders (0: until interrupted)")
		},
		run: func(ctx context.Context, r *reader, m *snapshot.Model, ws *snapshot.Workspace, f readFlags) (any, func(io.Writer), error) {
			items := snapshot.Attention(m)
			return items, func(w io.Writer) { printAttention(w, items, *jsonl) }, nil
		}}
	if !hasFlag(args, "watch") {
		return v.main(args, stdout, stderr)
	}
	return watchVerb(v, args, stdout, stderr, func() (*time.Duration, *int) { return interval, count },
		func(w io.Writer, m *snapshot.Model) { printAttention(w, snapshot.Attention(m), *jsonl) },
		func(m *snapshot.Model) any { return snapshot.Attention(m) })
}

func printAttention(w io.Writer, items []snapshot.Item, jsonl bool) {
	if jsonl {
		enc := json.NewEncoder(w)
		for _, it := range items {
			_ = enc.Encode(it)
		}
		return
	}
	if len(items) == 0 {
		fmt.Fprintln(w, "nothing needs a decision")
		return
	}
	for _, it := range items {
		where := strings.TrimSpace(strings.Join([]string{it.MailName, it.Slot, it.PaneID, it.OperationID}, " "))
		if where != "" {
			where = " [" + where + "]"
		}
		fmt.Fprintf(w, "%-8s %-24s%s %s\n", it.Severity, it.Kind, where, it.Detail)
	}
}

const activityUsage = `Usage: nhm activity <session> [--project DIR] [--socket PATH] [--json]

Each agent's activity in ntm's terms: GENERATING (herdr: working),
WAITING (idle or done), STALLED (blocked: it waits for a person), UNKNOWN.
`

func runActivity(args []string, stdout, stderr io.Writer) int {
	return readVerb{name: "activity", usage: activityUsage, session: true, needSess: true,
		run: func(ctx context.Context, r *reader, m *snapshot.Model, ws *snapshot.Workspace, f readFlags) (any, func(io.Writer), error) {
			out := snapshot.ActivityFor(m, *ws, "json", time.Now())
			return out, func(w io.Writer) {
				tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "SLOT\tTYPE\tSTATE\tDISPATCH")
				for _, a := range out.Agents {
					d := "no"
					if a.SafeToDispatch {
						d = "yes"
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", dash(a.Pane), a.AgentType, a.State, d)
				}
				_ = tw.Flush()
			}, nil
		}}.main(args, stdout, stderr)
}

const healthUsage = `Usage: nhm health [session] [--project DIR] [--no-mail] [--socket PATH] [--json]

Runtime health: each source, each session, and the attention counts.
Exits 1 when the overall status is critical.
`

func runHealth(args []string, stdout, stderr io.Writer) int {
	return readVerb{name: "health", usage: healthUsage, session: true, withMail: true,
		run: func(ctx context.Context, r *reader, m *snapshot.Model, ws *snapshot.Workspace, f readFlags) (any, func(io.Writer), error) {
			items := snapshot.Attention(m)
			st := snapshot.Status(m, items, buildInfo(), "json", time.Now())
			res := map[string]any{"overall_status": st.OverallStatus, "health_score": st.Summary.HealthScore,
				"sessions": st.Sessions, "sources": m.Sources, "attention": snapshot.CountBySeverity(items)}
			var err error
			if st.OverallStatus == "critical" {
				err = fmt.Errorf("overall status is critical: %d items need a decision now", snapshot.CountBySeverity(items)[snapshot.SevCritical])
			}
			return res, func(w io.Writer) {
				fmt.Fprintf(w, "overall: %s (score %.2f)\n", st.OverallStatus, st.Summary.HealthScore)
				for _, name := range []string{snapshot.SourceHerdr, snapshot.SourceBindings, snapshot.SourceQuota, snapshot.SourceMail} {
					if h, ok := m.Sources[name]; ok {
						fmt.Fprintf(w, "  source %-10s %-11s %s\n", name, h.Status, h.Error)
					}
				}
				for _, s := range st.Sessions {
					fmt.Fprintf(w, "  session %s: %s %s (%d agents)\n", s.Name, s.Health.Status, s.Health.Reason, s.AgentCount)
				}
			}, err
		}}.main(args, stdout, stderr)
}

// watchVerb runs a read verb with --watch: it subscribes first, then
// renders; an event invalidates the view and a fresh build replaces it;
// refreshes coalesce; a full refresh runs every interval and right after
// a reconnect, because events can be lost.
func watchVerb(v readVerb, args []string, stdout, stderr io.Writer, knobs func() (*time.Duration, *int),
	render func(io.Writer, *snapshot.Model), result func(*snapshot.Model) any) int {
	fs := flag.NewFlagSet(v.name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, v.usage) }
	f := addReadFlags(fs)
	if v.extra != nil {
		v.extra(fs)
	}
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) > 1 {
		return exitRefused
	}
	session := ""
	if len(pos) == 1 {
		session = pos[0]
	}
	interval, count := knobs()
	if *interval <= 0 {
		fmt.Fprint(stderr, v.usage)
		return exitRefused
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	project, err := f.projectKey(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "nhm %s: %v\n", v.name, err)
		return exitRefused
	}
	r, err := openReader(*f.socket, v.withMail && !*f.noMail)
	if err != nil {
		fmt.Fprintf(stderr, "nhm %s: %v\n", v.name, err)
		return exitError
	}
	defer r.close()
	for _, w := range r.warnings {
		fmt.Fprintf(stderr, "nhm %s: warning: %s\n", v.name, w)
	}
	renders := 0
	draw := func() bool {
		m, _, err := selectWorkspace(r.model(ctx, project), session)
		if err != nil {
			fmt.Fprintf(stderr, "nhm %s: %v\n", v.name, err)
			return true
		}
		if *f.asJSON {
			_ = json.NewEncoder(stdout).Encode(envelope{Schema: "nhm.v1", Command: v.name, Result: result(m),
				Warnings: append([]string{}, degradedWarnings(m)...)})
		} else {
			fmt.Fprintf(stdout, "--- %s\n", time.Now().UTC().Format(time.RFC3339))
			render(stdout, m)
		}
		renders++
		return *count > 0 && renders >= *count
	}
	events := make(chan struct{}, 1)
	go watchEvents(ctx, r.herdr, events)
	if draw() {
		return exitOK
	}
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return exitOK
		case <-ticker.C:
		case <-events:
			// Coalesce a burst of events into one refresh.
			time.Sleep(250 * time.Millisecond)
			select {
			case <-events:
			default:
			}
		}
		if draw() {
			return exitOK
		}
	}
}

// watchEvents subscribes to agent changes and signals on each event. It
// subscribes again after a loss (and signals then too: the loss may hide
// a change), at once after a stream that delivered events (and was not
// ended at once), else after a growing wait (watchWait). It subscribes to every live agent pane, since
// herdr's status subscription is per pane.
func watchEvents(ctx context.Context, h *herdr.Client, events chan<- struct{}) {
	signalOnce := func() {
		select {
		case events <- struct{}{}:
		default:
		}
	}
	var wait time.Duration
	for ctx.Err() == nil {
		if wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
		subs := []herdr.Subscription{herdr.SubscribeAgentDetected(), herdr.SubscribePaneClosed(), herdr.SubscribePaneExited()}
		if snap, err := h.Snapshot(ctx); err == nil {
			for _, a := range snap.Agents {
				subs = append(subs, herdr.SubscribeAgentStatus(a.PaneID))
			}
		}
		st, err := h.Subscribe(ctx, subs...)
		if err != nil {
			wait = watchWait(wait, false, 0)
			continue
		}
		start, delivered := time.Now(), false
		for {
			ev, err := st.Next()
			if err != nil {
				break
			}
			delivered = true
			signalOnce()
			if ev.Kind == herdr.EventPaneAgentDetected {
				// A new pane needs its own status subscription.
				break
			}
		}
		st.Close()
		signalOnce()
		wait = watchWait(wait, delivered, time.Since(start))
	}
}

// The wait before watchEvents subscribes again, after a failed subscribe
// or a stream that ended with no event: it doubles from watchBackoffMin
// up to watchBackoffMax, so a herdr that ends each stream at once gets no
// tight snapshot and subscribe loop (nor the board continuous refreshes).
var (
	watchBackoffMin = 250 * time.Millisecond
	watchBackoffMax = 10 * time.Second
)

// watchWait is the wait before the next subscription: none after a
// stream that lived watchBackoffMax, or that delivered an event and lived
// watchBackoffMin; else the last wait doubled, within watchBackoffMin and
// watchBackoffMax. A herdr that sends one event on each new stream and
// ends it at once gets the backoff too.
func watchWait(last time.Duration, delivered bool, lived time.Duration) time.Duration {
	if lived >= watchBackoffMax || delivered && lived >= watchBackoffMin {
		return 0
	}
	return min(max(2*last, watchBackoffMin), watchBackoffMax)
}

func buildInfo() snapshot.BuildInfo {
	return snapshot.BuildInfo{Version: Version, Commit: Commit}
}
