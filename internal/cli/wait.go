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

// selectorFlags are nhm's selector flags of send.
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

const waitUsage = `Usage: nhm wait [session] [--until idle|complete|generating|healthy[,…]]
                [--timeout D] [--poll D] [--pane N] [--type T] [--any [--count N]]
                [--exit-on-error] [--name N]… [--project DIR] [--socket PATH]
       nhm wait [session] --after-op OP [--until idle|done|complete|generating|healthy]
                [--name N]… [--pane N] [--type T] [--any [--count N]] [--json]

ntm's wait: wait until the session's agents reach a state. Without a
session: the calling pane's workspace, the cwd's, the only one, or a
picker. The agents are the session's bound agents but the calling pane
(nhm's user pane), unless --name names it.

--until (default idle) takes one condition or a comma list that must all
hold: idle and complete, an agent at its prompt (herdr's idle or done);
generating, an agent at work; healthy, an agent not blocked at a dialog;
and nhm's done, herdr's done alone. Every agent must meet it in the same
poll, or --count (1) of them with --any. Each poll takes the agents
again: one that is gone from its pane, or whose pane holds another kind,
drops out. --pane is ntm's pane index (-1 for all), or the slot N.M;
--type takes ntm's names (cc, cod, agy …). --exit-on-error ends the wait
at the first agent that drops out. --timeout 5m, --poll 2s.

Prints ntm's lines, with its colours, under --json too: "Waiting for …",
then "Condition '…' met after …" and a line per agent that met it (its
herdr pane and ntm's state), "Timeout after …", or "No matching panes
found" when no agent is left.

--after-op, nhm's own, waits for the turn of a send, or of a nudge that
herdr took (nhm wake --json gives its operation_id): only a state after
the operation's baseline counts. --until complete is a completion by the
same binding generation, "complete" when nhm saw the turn start, else
"ambiguous". It prints a line per agent (idle, done, generating, healthy,
complete, ambiguous, timeout, replaced, continuity_lost, not_submitted,
pending), or nhm's envelope with --json.

Exit codes: 0 the condition holds; 1 it does not, or an error. With
--after-op: 2 a refusal (a bad value, an unknown operation); 4 an
ambiguous completion.
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

// runWaitAfterOp is nhm's own wait for an operation's turn (--after-op),
// which ntm has no form of: nhm's result, its envelope under --json, and
// its exits (0 met, 1 not, 4 ambiguous). Its errors are ntm's, returned
// for the root to write; exit 1.
func runWaitAfterOp(name string, f waitFlags, asJSON bool, stdout, stderr io.Writer) error {
	// nhm's own form keeps nhm's refusals: a bad value is exit 2, with the
	// usage on stderr.
	u, err := wait.ParseUntil(f.until)
	var kindsOf []string
	if err == nil && f.agentType != "" {
		kindsOf, err = ntmTypeKinds(f.agentType)
	}
	if err != nil || f.timeout <= 0 || f.poll <= 0 || f.count < 1 {
		if err != nil {
			fmt.Fprintf(stderr, "nhm wait: %v\n", err)
		}
		fmt.Fprint(stderr, waitUsage)
		return verbExit{code: exitRefused}
	}
	anyN := 0
	if f.any {
		anyN = f.count
	}
	done := func(code int) error {
		if code != exitOK {
			return verbExit{code: code}
		}
		return nil
	}
	env := envelope{Command: "wait", OperationID: f.afterOp}
	ctx := context.Background()
	ie, err := openInstance(f.socket)
	if err != nil {
		return done(output(stdout, stderr, asJSON, env, err, exitError, nil))
	}
	defer ie.db.Close()
	store := bindings.NewStore(ie.db)
	projectKey := ""
	if f.project != "" {
		s, err := projectScope(ctx, ie.inst.ID, f.project)
		if err != nil {
			return done(output(stdout, stderr, asJSON, env, err, exitRefused, nil))
		}
		projectKey, env.Scope = s.MailProjectKey, s
	}
	targets, err := opTargets(ctx, ie, store, f.afterOp, projectKey, wait.Selector{Names: f.names, Kinds: kindsOf}, f.pane)
	if err != nil {
		return done(output(stdout, stderr, asJSON, env, err, exitRefused, nil))
	}
	wenv := wait.Env{
		Herdr:    ie.herdr,
		Bindings: store,
		Watch:    wait.StreamWatch(ie.herdr),
		Inode:    func() uint64 { return wait.SocketInode(ie.herdr.SocketPath) },
	}
	start := time.Now()
	wctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	results := wait.All(wctx, wenv, targets, wait.Request{Until: u, AfterOp: true, Poll: f.poll}, anyN)
	sum := wait.Summarize(results, anyN)
	out := WaitOutput{Session: name, Until: u, AfterOp: f.afterOp, Any: anyN, Met: sum.Met, Ambig: sum.Ambiguous,
		WaitedMS: time.Since(start).Milliseconds(), Targets: results}
	env.Result = out
	code := exitError
	switch {
	case sum.Met:
		code = exitOK
	case sum.Ambiguous:
		code = exitAmbiguous
	}
	return done(output(stdout, stderr, asJSON, env, nil, code, func(w io.Writer) { printWait(w, out) }))
}

// opTargets reads an operation's receipts as wait targets, filtered by
// the selector and --pane: ntm's index counts in the agents' one tab.
func opTargets(ctx context.Context, ie *instanceEnv, store *bindings.Store, opID, projectKey string, sel wait.Selector, pane ntmPaneIndex) ([]wait.Target, error) {
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
	if len(list) > 0 {
		slot, err := waitPaneSlot(ctx, ie, list[0].WorkspaceID, pane, list)
		if err != nil {
			return nil, err
		}
		if slot != "" {
			sel.Slots = []string{slot}
		}
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
