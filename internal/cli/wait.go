package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/core/wake"
	"github.com/arosl/nhm/internal/herdr"
)

// exitAmbiguous is the native exit code for an ambiguous completion or a
// possibly submitted prompt (plan-v8 2.12).
const exitAmbiguous = 4

// defaultWaitTimeout is ntm's default wait.
const defaultWaitTimeout = 5 * time.Minute

// listFlag is a repeatable flag; a value may also hold a comma-separated
// list.
type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }

func (l *listFlag) Set(v string) error {
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			*l = append(*l, s)
		}
	}
	return nil
}

// selectorFlags are the selector flags of wait and interrupt.
type selectorFlags struct {
	names, slots, kinds listFlag
}

func addSelectorFlags(fs *flag.FlagSet) *selectorFlags {
	f := &selectorFlags{}
	fs.Var(&f.names, "name", "an Agent Mail or herdr name (repeatable)")
	fs.Var(&f.slots, "pane", "ntm's pane number N.M (repeatable)")
	fs.Var(&f.kinds, "type", "an agent kind: claude, codex, agy, pi, omp (repeatable)")
	return f
}

func (f *selectorFlags) selector() wait.Selector {
	return wait.Selector{Names: f.names, Slots: f.slots, Kinds: f.kinds}
}

// sessionWorkspace resolves the session argument (a workspace id or
// label) or, without one, the calling pane's workspace. A label that
// names two workspaces selects none.
func sessionWorkspace(ctx context.Context, h *herdr.Client, session string) (string, error) {
	if session == "" {
		session = os.Getenv("HERDR_WORKSPACE_ID")
		if session == "" {
			return "", errors.New("name the session (a workspace) or run inside herdr")
		}
	}
	snap, err := h.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	var byLabel []string
	for _, w := range snap.Workspaces {
		if w.WorkspaceID == session {
			return w.WorkspaceID, nil
		}
		if w.Label == session {
			byLabel = append(byLabel, w.WorkspaceID)
		}
	}
	switch len(byLabel) {
	case 0:
		return "", fmt.Errorf("session %q: no such workspace", session)
	case 1:
		return byLabel[0], nil
	}
	return "", fmt.Errorf("session %q: the label names %d workspaces (%s); give the workspace id", session, len(byLabel), strings.Join(byLabel, ", "))
}

const waitUsage = `Usage: nhm wait [session] [--name N]… [--pane N.M]… [--type K]…
                [--until idle|done|complete|generating|healthy] [--after-op OP]
                [--timeout D] [--poll D] [--any [--count N]]
                [--project DIR] [--socket PATH] [--json]

Wait until the selected agents reach a state. The session is a workspace
id or label; without it, the calling pane's workspace. Without a
selector, every bound agent of the session. With --after-op, the targets
are the operation's, and only a state reached after the operation's
baseline counts.

--until idle is ntm's idle, an agent at its prompt: herdr shows it idle,
or done when nobody has viewed the pane since its turn ended. --until done
is herdr's done only.

--until complete waits for a completion after the baseline by the same
binding generation. It is "complete" only when nhm also saw the turn
start; otherwise it is "ambiguous". A blocked agent is never complete.

--after-op takes a send's operation, or the operation of a nudge that
herdr took (nhm wake --json gives it as operation_id). A nudge does not
wait for its turn to start, so that turn is "complete" only when this
wait sees it start.

Results per agent: idle, done, generating, healthy, complete, ambiguous,
timeout, replaced (a new occupant), continuity_lost (herdr restarted or
the agent left), not_submitted, pending (--any had enough).

Exit codes: 0 the condition holds; 1 it does not (timeout, replaced,
continuity_lost) or an error; 2 refused (bad arguments, no agent);
4 ambiguous.
`

// WaitOutput is the result of nhm wait --json.
type WaitOutput struct {
	Session  string        `json:"session,omitempty"`
	Until    wait.Until    `json:"until"`
	AfterOp  string        `json:"after_op,omitempty"`
	Any      int           `json:"any,omitempty"`
	Met      bool          `json:"met"`
	Ambig    bool          `json:"ambiguous"`
	WaitedMS int64         `json:"waited_ms"`
	Targets  []wait.Result `json:"targets"`
}

func runWait(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, waitUsage) }
	sel := addSelectorFlags(fs)
	until := fs.String("until", string(wait.UntilIdle), "idle, done, complete, generating or healthy")
	afterOp := fs.String("after-op", "", "count only states after this operation's baseline")
	timeout := fs.Duration("timeout", defaultWaitTimeout, "the longest wait")
	poll := fs.Duration("poll", wait.DefaultPoll, "the longest sleep between two reads")
	anyFlag := fs.Bool("any", false, "wait for any agent, not all")
	count := fs.Int("count", 1, "with --any, the number of agents")
	project := fs.String("project", "", "only this project's agents")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	u, uerr := wait.ParseUntil(*until)
	if len(pos) > 1 || uerr != nil || *timeout <= 0 || *poll <= 0 || *count < 1 {
		if uerr != nil {
			fmt.Fprintf(stderr, "nhm wait: %v\n", uerr)
		}
		fmt.Fprint(stderr, waitUsage)
		return exitRefused
	}
	session := ""
	if len(pos) == 1 {
		session = pos[0]
	}
	anyN := 0
	if *anyFlag {
		anyN = *count
	}
	env := envelope{Command: "wait"}
	ctx := context.Background()
	ie, err := openInstance(*socket)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	store := bindings.NewStore(ie.db)
	projectKey := ""
	if *project != "" {
		s, err := projectScope(ctx, ie.inst.ID, *project)
		if err != nil {
			return output(stdout, stderr, *asJSON, env, err, exitRefused, nil)
		}
		projectKey, env.Scope = s.MailProjectKey, s
	}
	var targets []wait.Target
	if *afterOp != "" {
		env.OperationID = *afterOp
		targets, err = opTargets(ctx, ie, store, *afterOp, projectKey, sel.selector())
	} else {
		var ws string
		if ws, err = sessionWorkspace(ctx, ie.herdr, session); err == nil {
			session = ws
			var list []bindings.Binding
			if list, err = wait.Select(ctx, store, ws, projectKey, sel.selector()); err == nil {
				for _, b := range list {
					targets = append(targets, wait.Target{Binding: b})
				}
			}
		}
	}
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitRefused, nil)
	}

	wenv := wait.Env{
		Herdr:    ie.herdr,
		Bindings: store,
		Watch:    wait.StreamWatch(ie.herdr),
		Inode:    func() uint64 { return wait.SocketInode(ie.herdr.SocketPath) },
	}
	start := time.Now()
	wctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	results := wait.All(wctx, wenv, targets, wait.Request{Until: u, AfterOp: *afterOp != "", Poll: *poll}, anyN)
	sum := wait.Summarize(results, anyN)
	out := WaitOutput{Session: session, Until: u, AfterOp: *afterOp, Any: anyN, Met: sum.Met, Ambig: sum.Ambiguous,
		WaitedMS: time.Since(start).Milliseconds(), Targets: results}
	env.Result = out
	code := exitError
	switch {
	case sum.Met:
		code = exitOK
	case sum.Ambiguous:
		code = exitAmbiguous
	}
	return output(stdout, stderr, *asJSON, env, nil, code, func(w io.Writer) { printWait(w, out) })
}

// opTargets reads an operation's receipts as wait targets, filtered by
// the selector.
func opTargets(ctx context.Context, ie *instanceEnv, store *bindings.Store, opID, projectKey string, sel wait.Selector) ([]wait.Target, error) {
	receipts, err := wait.Receipts(ctx, ie.db.SQL(), opID)
	if err != nil {
		return nil, err
	}
	op, err := ie.db.Operation(ctx, opID)
	if err != nil {
		return nil, err
	}
	// A nudge types its line without a wait for the turn's start
	// (nhm-qaa): herdr took it, but nobody saw the turn begin.
	nudge := op.Verb == wake.Verb
	var list []bindings.Binding
	byID := map[string]wait.Receipt{}
	for _, r := range receipts {
		if r.BindingID == "" {
			continue
		}
		b, err := store.Get(ctx, r.BindingID)
		if err != nil {
			return nil, err
		}
		if projectKey != "" && b.MailProjectKey != projectKey {
			continue
		}
		list = append(list, b)
		byID[b.ID] = r
	}
	list = wait.Filter(list, sel)
	if len(list) == 0 {
		return nil, fmt.Errorf("%w in operation %s", wait.ErrNoTargets, opID)
	}
	wait.SortBySlot(list)
	targets := make([]wait.Target, len(list))
	for i, b := range list {
		r := byID[b.ID]
		targets[i] = wait.Target{Binding: b, Baseline: r.Baseline, Submission: r.Status, StartUnseen: nudge}
	}
	return targets, nil
}

func printWait(w io.Writer, out WaitOutput) {
	for _, r := range out.Targets {
		line := fmt.Sprintf("%s (%s): %s", r.MailName, dash(r.Slot), r.Outcome)
		if r.Correlation != "" {
			line += " (" + r.Correlation + ")"
		}
		if r.Reason != "" {
			line += ": " + r.Reason
		}
		fmt.Fprintln(w, line)
	}
	verdict := "not met"
	switch {
	case out.Met:
		verdict = "met"
	case out.Ambig:
		verdict = "ambiguous"
	}
	fmt.Fprintf(w, "%s: %s after %s\n", out.Until, verdict, (time.Duration(out.WaitedMS) * time.Millisecond).Round(time.Millisecond))
}
