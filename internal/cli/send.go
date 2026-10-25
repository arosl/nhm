package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/enrich"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/core/wake"
	"github.com/arosl/nhm/internal/robot"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/store/history"
)

// stdin is where --msg-file - and --file - read; tests replace it.
var stdin io.Reader = os.Stdin

// exitAdmission is the native exit code when the gate refused every
// target (plan-v8 2.12).
const exitAdmission = 3

// maxPayload bounds a prompt read from a file or stdin.
const maxPayload = 1 << 20

const sendUsage = `Usage: nhm send <session> [prompt...] [--all | --cc[=V] | --cod[=V] | --KIND[=V]]… [--include-user]
                [-p N | --panes N,…] [-s] [--tag T] [-f F] [--prefix T] [--suffix T]
                [-t KEY [--var k=v]… [--bead ID]] [--with-cass | --no-cass] [--no-cass-check]
                [--dry-run] [--json]
       nhm send --project P <prompt…> [selector] [--dry-run] [--json]
   nhm's flags: [--name N]… [--type K]… [--state idle|done|working|blocked|between]… [--limit N] [--include-self]
                [--msg M | --msg-file F|-] [--when any|ready] [--force]
                [--wait [--until done|idle|complete] [--timeout D] [--poll D]]
                [--with-memory[=false]] [--memory-query Q] [--thread ID]
                [--if-generation ID:G]… [--op-id ID] [--project-key DIR] [--socket PATH]

ntm's send: type one prompt into the session's agents. Without a
selector, every bound agent of the session but the calling pane (ntm's
user pane; --include-user or --include-self adds it). --cc, --cod and the
other kind flags (each kind of the registry and of nhm's config) take an
optional variant, the binding's model (--cc=opus). --tag reads the end of
the herdr pane label (Name[a,b]). -p takes ntm's pane index in the active
agent tab, or W.P; --panes a comma list; -s leaves out the first pane.
--project P sends to every session whose base name is P. nhm's --name
and -p are a union; kinds, states and the session an intersection.
--state between is idle or done: between turns. --state done (and
between) leaves Codex out unless it is named.

The prompt is the text after the session, -f F (- is stdin), a piped
stdin, --msg or --msg-file; --prefix and --suffix wrap the file or stdin
text. -t takes a template key (the project's .nhm/templates,
~/.config/nhm/templates, the palette catalogs, ntm's builtins) or a .md
path; --var fills a placeholder, --bead the bead_* ones, -f the {{file}}.

nhm's core send: the delivery gate refuses an agent at a dialog and an
unsafe pane; a busy agent gets the prompt, as ntm's (--when ready refuses
it, unless --force). A prompt that an agent would run as a shell command,
or a "!" left in its input box, is refused. A refused target does not
stop the others; the send then fails with "sending to pane P: why". The
operation is claimed before the first prompt: the same --op-id never
sends twice. --if-generation ID:G sends to binding ID only at generation
G. --wait waits for each target's turn; --thread wakes the targets for
the thread's mail. --with-memory puts CM's rules before the prompt,
--with-cass CASS's hits (--no-cass wins); the CASS duplicate check warns
(--no-cass-check turns it off).

Prints "Sent to N pane(s)", or "Sent to pane P" with -p; a dry run lists
the prompts it would send. --json prints ntm's SendResult on one line,
with nhm's operation and receipts under "_nhm".

Exit codes: 0 sent; 1 an error, a refused target or no matching pane.
--wait: 1 when a target did not complete, 4 when its completion is
ambiguous; 4 also for an --op-id whose operation has no final outcome.
`

// sendFlags are the enrichment flags of a send that the UI composes
// (uicore): its zero value is nhm's config defaults.
type sendFlags struct {
	withMemory, withCASS, noCASS bool
	cassCheck, noCassCheck       bool
	memoryQuery                  string
	// memorySet: --with-memory was given (true or false).
	memorySet bool
}

// wakeTargets runs a wake pass on a send's bound targets (send
// --thread): a target that is busy keeps its pending mail, and
// maintenance delivers it. It returns warnings; the send's outcome does
// not change.
func (ie *instanceEnv) wakeTargets(ctx context.Context, res send.Result) []string {
	w, err := ie.wakeEnv(true)
	if err != nil {
		return []string{"wake: " + err.Error()}
	}
	targets := []bindings.Binding{}
	for _, rc := range res.Targets {
		if rc.BindingID == "" {
			continue
		}
		if b, err := w.Bindings.Get(ctx, rc.BindingID); err == nil {
			targets = append(targets, b)
		}
	}
	wr, err := ie.wakePass(ctx, w, targets, wake.TriggerManual)
	if err != nil {
		return []string{"wake: " + err.Error()}
	}
	return wr.Warnings
}

// parsePins reads --if-generation's ID:G values into the send's Expect;
// nil when there are none.
func parsePins(values []string) (map[string]int64, error) {
	if len(values) == 0 {
		return nil, nil
	}
	expect := map[string]int64{}
	for _, v := range values {
		i := strings.LastIndex(v, ":")
		if i <= 0 {
			return nil, fmt.Errorf("--if-generation %q: use BINDING_ID:GENERATION", v)
		}
		id := v[:i]
		gen, err := strconv.ParseInt(v[i+1:], 10, 64)
		if err != nil || gen < 1 {
			return nil, fmt.Errorf("--if-generation %q: the generation is a whole number from 1", v)
		}
		if old, ok := expect[id]; ok && old != gen {
			return nil, fmt.Errorf("--if-generation pins %s at generations %d and %d", id, old, gen)
		}
		expect[id] = gen
	}
	return expect, nil
}

// pinSelection is the selector of a pinned send: without --all, names or
// panes, the pinned bindings themselves, so that an empty selector never
// means every agent; with them, the selector as given (Expect refuses
// the selected agents that are not pinned).
func pinSelection(sel send.Selector, expect map[string]int64) send.Selector {
	if len(expect) == 0 || sel.All || len(sel.Names) > 0 || len(sel.Panes) > 0 {
		return sel
	}
	for id := range expect {
		sel.Bindings = append(sel.Bindings, id)
	}
	slices.Sort(sel.Bindings)
	return sel
}

func readInput(path string) (string, error) {
	var r io.Reader
	if path == "-" {
		r = stdin
	} else {
		fh, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer fh.Close()
		r = fh
	}
	data, err := io.ReadAll(io.LimitReader(r, maxPayload+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxPayload {
		return "", fmt.Errorf("more than %d bytes", maxPayload)
	}
	return string(data), nil
}

// fillScope resolves the session to a workspace and the project. The
// project is --project, else the cwd's git work tree when there is one;
// only --project limits the targets to one project.
func (ie *instanceEnv) fillScope(ctx context.Context, req *send.Request, session, project string) error {
	ws, err := sessionWorkspace(ctx, ie.herdr, session)
	if err != nil {
		return err
	}
	req.Session, req.WorkspaceID = session, ws
	if req.Session == "" {
		req.Session = ws
	}
	scope := map[string]string{"instance_id": ie.inst.ID, "workspace_id": ws}
	if s, err := projectScope(ctx, ie.inst.ID, project); err == nil {
		req.ProjectRoot = s.ProjectRoot
		scope["project_root"] = s.ProjectRoot
		if project != "" {
			req.ProjectKey = s.MailProjectKey
			scope["mail_project_key"] = s.MailProjectKey
		}
	} else if project != "" {
		return err
	}
	req.Scope = scope
	return nil
}

func (ie *instanceEnv) sendEnv() send.Env {
	env := send.Env{DB: ie.db, Bindings: bindings.NewStore(ie.db), Herdr: ie.herdr, InstanceDir: ie.inst.Dir,
		Inode: func() uint64 { return wait.SocketInode(ie.herdr.SocketPath) }, Bead: send.BrShow, History: history.Append}
	// A config error refused the send before; here the defaults serve.
	cfg, _ := loadEnrichConfig()
	env.CM, env.CASS, env.Budgets = enrichClients(cfg)
	return env
}

// runSendCore runs a send under the owner lock; a dry run reads only and
// runs under any owner. It returns the exit code for an error.
func runSendCore(ctx context.Context, ie *instanceEnv, req send.Request) (send.Result, int, error) {
	if req.DryRun {
		res, err := send.Send(ctx, ie.sendEnv(), req)
		return res, exitRefused, err
	}
	lctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	var res send.Result
	code := exitRefused
	err := core.WithOwnerShared(lctx, ie.inst.Dir, func(o core.Owner) error {
		if err := core.RequireOwner(o, core.OwnerNhm); err != nil {
			return err
		}
		var serr error
		res, serr = send.Send(ctx, ie.sendEnv(), req)
		if serr != nil && res.OperationID != "" {
			// The claim exists: something after it failed.
			code = exitError
		}
		return serr
	})
	if errors.Is(err, store.ErrOperationIDConflict) {
		code = exitRefused
	}
	return res, code, err
}

// observe waits for the operation's targets and reads the receipts again.
func observe(ctx context.Context, ie *instanceEnv, opID string, until wait.Until, timeout, poll time.Duration) ([]wait.Result, send.Result, error) {
	wenv := wait.Env{Herdr: ie.herdr, Bindings: bindings.NewStore(ie.db), Watch: wait.StreamWatch(ie.herdr),
		Inode: func() uint64 { return wait.SocketInode(ie.herdr.SocketPath) }}
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	results, err := send.Observe(wctx, ie.sendEnv(), wenv, opID, until, poll)
	res, rerr := send.Stored(ctx, ie.db, opID)
	res.Replayed = false
	if err == nil {
		err = rerr
	}
	return results, res, err
}

// The robot dispatch flags (plan-v8 2.12): --robot-send=S,
// --robot-send-receipt=OP and --robot-ack=S, with ntm's types.

// robotDispatchFlag returns the robot dispatch flag in args, or "".
func robotDispatchFlag(args []string) string {
	for _, a := range args {
		name, _, _ := strings.Cut(a, "=")
		switch name {
		case "--robot-send", "--robot-send-receipt", "--robot-ack":
			return name
		}
	}
	return ""
}

func runRobotDispatch(args []string, stdout, stderr io.Writer) int {
	now := time.Now()
	fs, root := newRobotFlagSet(args, stdout)
	defer root.undo()
	sendS := fs.String("robot-send", "", "")
	receiptOp := fs.String("robot-send-receipt", "", "")
	ackS := fs.String("robot-ack", "", "")
	format := &root.format
	var panes, types listFlag
	fs.Var(&panes, "panes", "")
	fs.Var(&panes, "pane", "")
	fs.Var(&types, "type", "")
	var pins listFlag
	fs.Var(&pins, "if-generation", "")
	all := fs.Bool("all", false, "")
	msg := fs.String("msg", "", "")
	msgFile := fs.String("msg-file", "", "")
	opID := fs.String("op-id", "", "")
	dryRun := fs.Bool("dry-run", false, "")
	noCASS := fs.Bool("no-cass", false, "")
	withMemory := fs.Bool("with-memory", false, "")
	timeout := fs.Duration("timeout", 30*time.Second, "")
	socket := fs.String("socket", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		msg := "unexpected arguments"
		if err != nil {
			msg = err.Error()
		}
		return robotFail(stdout, robot.ErrCodeInvalidFlag, msg, "nhm --robot-capabilities lists the robot flags", "auto", robotError, now)
	}
	if code, done := applyRobotRoot(root, now); done {
		return code
	}
	cfg, err := loadEnrichConfig()
	if err != nil {
		return robotFail(stdout, robot.ErrCodeInvalidFlag, err.Error(), "fix ~/.config/nhm/config.toml", *format, robotError, now)
	}
	// Robot sends never run the duplicate check (plan-v8 2.13).
	enrichment := send.Enrichment{DefaultCM: cfg.Memory.CM, DefaultCASS: cfg.CASS.Context}
	if *withMemory {
		enrichment.CM = enrich.BestEffort
	}
	if *noCASS {
		enrichment.CASS = enrich.Off
	}
	ie, err := openInstance(*socket)
	if err != nil {
		return robotFail(stdout, robot.ErrCodeDependencyMissing, err.Error(), "run inside herdr or give --socket", *format, robotError, now)
	}
	defer ie.db.Close()
	ctx := context.Background()
	switch {
	case *receiptOp != "":
		res, err := send.Stored(ctx, ie.db, *receiptOp)
		if err != nil {
			return robotFail(stdout, robot.ErrCodeNotFound, fmt.Sprintf("operation '%s' not found", *receiptOp),
				"receipts exist for sends that went out; give the --op-id of one", *format, robotError, now)
		}
		return robotWrite(stdout, send.ReceiptOutput(res, *format, now), robotOK)
	case *ackS != "":
		return robotAck(ctx, stdout, ie, *ackS, *opID, *timeout, *format, now)
	}
	var text string
	switch {
	case *msg != "" && *msgFile != "":
		return robotFail(stdout, robot.ErrCodeInvalidFlag, "give --msg or --msg-file, not both", "", *format, robotError, now)
	case *msgFile != "":
		if text, err = readInput(*msgFile); err != nil {
			return robotFail(stdout, robot.ErrCodeInvalidFlag, "--msg-file: "+err.Error(), "", *format, robotError, now)
		}
	default:
		text = *msg
	}
	expect, err := parsePins(pins)
	if err != nil {
		return robotFail(stdout, robot.ErrCodeInvalidFlag, err.Error(), "", *format, robotError, now)
	}
	// WhenAny: ntm's robot send types into a busy pane (nhm-r0v); a
	// blocked agent and an unsafe pane are still refused.
	req := send.Request{Message: text, OperationID: *opID, DryRun: *dryRun, Source: history.SourceRobot, WhenAny: true,
		Caller: ie.callerPane(ctx), Selector: pinSelection(send.Selector{All: *all, Panes: panes, Kinds: robotKinds(types)}, expect),
		Expect: expect, Enrich: enrichment}
	if err := ie.fillScope(ctx, &req, *sendS, ""); err != nil {
		return robotFail(stdout, robot.ErrCodeSessionNotFound, err.Error(), "list sessions: nhm --robot-status", *format, robotError, now)
	}
	res, _, err := runSendCore(ctx, ie, req)
	if errors.Is(err, send.ErrShellEscape) {
		return robotWrite(stdout, send.GuardOutput(res, err, *dryRun, *format, now), robotError)
	}
	if err != nil {
		code := robot.ErrCodeInvalidFlag
		switch {
		case errors.Is(err, store.ErrOperationIDConflict):
			code = robot.ErrCodeIdempotencyConflict
		case errors.Is(err, core.ErrNotOwner):
			code = robot.ErrCodeNotOwner
		case errors.Is(err, send.ErrNoTargets):
			code = robot.ErrCodePaneNotFound
		case res.OperationID != "":
			code = robot.ErrCodeInternalError
		}
		return robotFail(stdout, code, err.Error(), "", *format, robotError, now)
	}
	out := send.SendOutput(res, *format, now)
	if !out.Success {
		return robotWrite(stdout, out, robotError)
	}
	return robotWrite(stdout, out, robotOK)
}

// robotAck waits for the targets of a send (the given operation, or the
// session's latest send) and reports ntm's ack type.
func robotAck(ctx context.Context, stdout io.Writer, ie *instanceEnv, session, opID string, timeout time.Duration, format string, now time.Time) int {
	ws, err := sessionWorkspace(ctx, ie.herdr, session)
	if err != nil {
		return robotFail(stdout, robot.ErrCodeSessionNotFound, err.Error(), "list sessions: nhm --robot-status", format, robotError, now)
	}
	if opID == "" {
		if opID, err = latestSend(ctx, ie.db, ws); err != nil {
			return robotFail(stdout, robot.ErrCodeNotFound, err.Error(), "give --op-id", format, robotError, now)
		}
	}
	res, err := send.Stored(ctx, ie.db, opID)
	if err != nil {
		return robotFail(stdout, robot.ErrCodeNotFound, fmt.Sprintf("operation '%s' not found", opID), "", format, robotError, now)
	}
	senv := ie.sendEnv()
	targets, index, err := send.WaitTargets(ctx, senv, res.Targets)
	if err != nil {
		return robotFail(stdout, robot.ErrCodeInternalError, err.Error(), "", format, robotError, now)
	}
	wenv := wait.Env{Herdr: ie.herdr, Bindings: senv.Bindings, Watch: wait.StreamWatch(ie.herdr), Inode: senv.Inode}
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	results := wait.All(wctx, wenv, targets, wait.Request{Until: wait.UntilComplete, AfterOp: true}, 0)
	out := send.AckOutput(res, results, index, timeout, format, started, time.Now())
	if !out.Success {
		return robotWrite(stdout, out, robotError)
	}
	return robotWrite(stdout, out, robotOK)
}

// latestSend is the newest send operation in a workspace.
func latestSend(ctx context.Context, db *store.DB, workspaceID string) (string, error) {
	var id string
	err := db.SQL().QueryRowContext(ctx, `SELECT operation_id FROM operations WHERE verb = ?
		AND json_extract(scope_json, '$.workspace_id') = ? ORDER BY created_at DESC, operation_id DESC LIMIT 1`,
		send.Verb, workspaceID).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("no send in workspace %s", workspaceID)
	}
	return id, nil
}
