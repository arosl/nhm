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

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/herdr"
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

const sendUsage = `Usage: nhm send <session> [prompt] [--all | --cc | --cod | --agy | --pi | --omp | --type K | --pane N.M | --panes N.M,…]
                [--name N]… [--state idle|done|working|blocked]… [--limit N] [--include-self]
                [--msg M | --msg-file F|-] [-t KEY | --template KEY] [--bead ID] [--var k=v]… [--file F|-]
                [--when ready|any] [--force] [--wait [--until done|idle|complete] [--timeout D]]
                [--with-memory=false] [--no-cass] [--no-cass-check]
                [--op-id ID] [--dry-run] [--project DIR] [--socket PATH] [--json]

Send one prompt to the selected agents of a session (a workspace id or
label). Without a selector, every bound agent of the session except the
caller. Names and panes are a union; kinds, states and the session are
an intersection. --state done leaves Codex out unless it is named.

The prompt is the text after the session, --msg, --msg-file (- is
stdin), or --file without a template. -t takes a template key (the
project's .nhm/templates, ~/.config/nhm/templates, the palette catalogs,
ntm's builtins) or a .md path; --file fills {{file}}, --bead the bead_*
placeholders, --var any placeholder. A placeholder that nothing fills
refuses the send before anything goes out.

The delivery gate refuses a blocked agent and an unsafe pane always, and
a busy or unknown one unless --when any or --force. The operation is
claimed before the first prompt: the same --op-id never sends twice and
returns the stored receipt. nhm never resends on its own.

Per target: submitted, possibly submitted (herdr did not confirm; never
retried), refused or failed. --wait then waits for each target (nhm
wait --after-op). --dry-run prints the targets and the prompt and writes
nothing. Memory and CASS injection, --thread and --group come with later
releases and are refused until then.

Exit codes: 0 every target submitted; 1 an error or a partial result;
2 refused before any dispatch; 3 the gate refused every target;
4 possibly submitted, or an ambiguous completion.
`

// sendFlags are the native send flags.
type sendFlags struct {
	sel                          selectorFlags
	states                       listFlag
	vars                         listFlag
	panes                        listFlag
	all, cc, cod, agy, pi, omp   bool
	limit                        int
	includeSelf                  bool
	msg, msgFile, tmpl, bead     string
	file, when, thread, group    string
	force, dryRun, wait, asJSON  bool
	until                        string
	timeout, poll                time.Duration
	withMemory, withCASS, noCASS bool
	cassCheck, noCassCheck       bool
	opID, project, socket        string
}

func runSend(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, sendUsage) }
	f := sendFlags{}
	fs.Var(&f.sel.names, "name", "an Agent Mail or herdr name (repeatable)")
	fs.Var(&f.sel.slots, "pane", "ntm's pane number N.M, or a pane id (repeatable)")
	fs.Var(&f.panes, "panes", "pane numbers, comma-separated")
	fs.Var(&f.sel.kinds, "type", "an agent kind (repeatable)")
	fs.Var(&f.states, "state", "idle, done, working or blocked (repeatable)")
	fs.Var(&f.vars, "var", "a template variable k=v (repeatable)")
	fs.BoolVar(&f.all, "all", false, "every agent of the session except the caller")
	fs.BoolVar(&f.cc, "cc", false, "Claude agents")
	fs.BoolVar(&f.cod, "cod", false, "Codex agents")
	fs.BoolVar(&f.agy, "agy", false, "agy agents")
	fs.BoolVar(&f.pi, "pi", false, "pi agents")
	fs.BoolVar(&f.omp, "omp", false, "omp agents")
	fs.IntVar(&f.limit, "limit", 0, "the first N targets in slot order")
	fs.BoolVar(&f.includeSelf, "include-self", false, "also the calling pane")
	fs.StringVar(&f.msg, "msg", "", "the prompt")
	fs.StringVar(&f.msgFile, "msg-file", "", "read the prompt from a file (- is stdin)")
	fs.StringVar(&f.tmpl, "template", "", "a template key or .md file")
	fs.StringVar(&f.tmpl, "t", "", "a template key or .md file")
	fs.StringVar(&f.bead, "bead", "", "fill bead_* placeholders from this bead")
	fs.StringVar(&f.file, "file", "", "{{file}} with a template, the prompt without (- is stdin)")
	fs.StringVar(&f.when, "when", "ready", "ready or any")
	fs.BoolVar(&f.force, "force", false, "send to busy and unknown agents too")
	fs.BoolVar(&f.wait, "wait", false, "wait for each target after the send")
	fs.StringVar(&f.until, "until", string(wait.UntilComplete), "with --wait: done, idle or complete")
	fs.DurationVar(&f.timeout, "timeout", defaultWaitTimeout, "with --wait: the longest wait")
	fs.DurationVar(&f.poll, "poll", wait.DefaultPoll, "with --wait: the longest sleep between reads")
	fs.BoolVar(&f.withMemory, "with-memory", false, "inject CM memory (comes with nhm.memory)")
	fs.BoolVar(&f.withCASS, "with-cass", false, "inject CASS context (comes with nhm.memory)")
	fs.BoolVar(&f.noCASS, "no-cass", false, "no CASS context")
	fs.BoolVar(&f.cassCheck, "cass-check", false, "the CASS duplicate check (comes with nhm.memory)")
	fs.BoolVar(&f.noCassCheck, "no-cass-check", false, "no CASS duplicate check")
	fs.StringVar(&f.thread, "thread", "", "an Agent Mail thread (comes with nhm.wake)")
	fs.StringVar(&f.group, "group", "", "a spawn group (comes with nhm.spawn)")
	fs.StringVar(&f.opID, "op-id", "", "operation id, for an idempotent retry")
	fs.BoolVar(&f.dryRun, "dry-run", false, "print the targets and the prompt; send nothing")
	fs.StringVar(&f.project, "project", "", "the project (templates, --bead, a name's project)")
	fs.StringVar(&f.socket, "socket", "", "herdr API socket")
	fs.BoolVar(&f.asJSON, "json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	env := envelope{Command: "send"}
	refuse := func(err error) int { return output(stdout, stderr, f.asJSON, env, err, exitRefused, nil) }
	if err := laterFlags(fs, f); err != nil {
		return refuse(err)
	}
	session, prompt := "", ""
	if len(pos) > 0 {
		session, prompt = pos[0], strings.Join(pos[1:], " ")
	}
	req, err := f.request(prompt)
	if err != nil {
		return refuse(err)
	}
	until, err := wait.ParseUntil(f.until)
	if err != nil || until == wait.UntilGenerating || until == wait.UntilHealthy || f.timeout <= 0 || f.poll <= 0 {
		return refuse(fmt.Errorf("--until is done, idle or complete; --timeout and --poll are positive"))
	}

	ctx := context.Background()
	ie, err := openInstance(f.socket)
	if err != nil {
		return output(stdout, stderr, f.asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	if err := ie.fillScope(ctx, &req, session, f.project); err != nil {
		return refuse(err)
	}
	env.Scope = req.Scope
	res, code, err := runSendCore(ctx, ie, req)
	env.OperationID, env.Result = res.OperationID, res
	if err != nil {
		return output(stdout, stderr, f.asJSON, env, err, code, nil)
	}
	env.Warnings = res.Warnings
	var waited []wait.Result
	if f.wait && !res.DryRun && res.OperationID != "" {
		waited, res, err = observe(ctx, ie, res.OperationID, until, f.timeout, f.poll)
		env.Result = sendWithWait{Result: res, Wait: waited}
		if err != nil {
			return output(stdout, stderr, f.asJSON, env, err, exitError, nil)
		}
	}
	return output(stdout, stderr, f.asJSON, env, nil, sendExit(res, waited), func(w io.Writer) { printSend(w, res, waited) })
}

// sendWithWait is send --wait's JSON result.
type sendWithWait struct {
	send.Result
	Wait []wait.Result `json:"wait"`
}

// laterFlags refuses the flags whose behaviour a later release brings:
// accepting a flag and ignoring it is not compatibility.
func laterFlags(fs *flag.FlagSet, f sendFlags) error {
	var later []string
	fs.Visit(func(fl *flag.Flag) {
		switch {
		case fl.Name == "with-memory" && f.withMemory, fl.Name == "with-cass" && f.withCASS,
			fl.Name == "cass-check" && f.cassCheck:
			later = append(later, "--"+fl.Name+" (nhm.memory, acfs-9hkh.21)")
		case fl.Name == "thread":
			later = append(later, "--thread (nhm.wake, acfs-9hkh.15)")
		case fl.Name == "group":
			later = append(later, "--group (nhm.spawn, acfs-9hkh.19)")
		}
	})
	if len(later) > 0 {
		return fmt.Errorf("not in this release: %s", strings.Join(later, ", "))
	}
	return nil
}

// request builds the core request from the flags, without the scope.
func (f sendFlags) request(prompt string) (send.Request, error) {
	req := send.Request{Template: f.tmpl, BeadID: f.bead, Force: f.force, OperationID: f.opID, DryRun: f.dryRun,
		Source: history.SourceCLI, Caller: os.Getenv("HERDR_PANE_ID")}
	switch f.when {
	case "ready":
	case "any":
		req.WhenAny = true
	default:
		return req, fmt.Errorf("--when is ready or any, not %q", f.when)
	}
	inputs := 0
	for _, s := range []string{prompt, f.msg, f.msgFile} {
		if s != "" {
			inputs++
		}
	}
	if inputs > 1 {
		return req, errors.New("give the prompt once: as an argument, --msg or --msg-file")
	}
	req.Message = prompt + f.msg
	if f.msgFile != "" {
		text, err := readInput(f.msgFile)
		if err != nil {
			return req, fmt.Errorf("--msg-file: %w", err)
		}
		req.Message = text
	}
	if f.file != "" {
		if f.file == "-" && f.msgFile == "-" {
			return req, errors.New("--file - and --msg-file - both read stdin")
		}
		text, err := readInput(f.file)
		if err != nil {
			return req, fmt.Errorf("--file: %w", err)
		}
		req.FileContent = text
	}
	if len(f.vars) > 0 {
		req.Vars = map[string]string{}
		for _, v := range f.vars {
			k, val, ok := strings.Cut(v, "=")
			if !ok || k == "" {
				return req, fmt.Errorf("--var %q: use key=value", v)
			}
			req.Vars[k] = val
		}
	}
	sel := send.Selector{All: f.all, Names: f.sel.names, Panes: append(append([]string{}, f.sel.slots...), f.panes...),
		Kinds: f.sel.kinds, Limit: f.limit, IncludeSelf: f.includeSelf}
	for kind, on := range map[string]bool{"claude": f.cc, "codex": f.cod, "agy": f.agy, "pi": f.pi, "omp": f.omp} {
		if on {
			sel.Kinds = append(sel.Kinds, kind)
		}
	}
	for _, s := range f.states {
		st := herdr.AgentStatus(s)
		if st != herdr.StatusIdle && st != herdr.StatusDone && st != herdr.StatusWorking && st != herdr.StatusBlocked {
			return req, fmt.Errorf("--state is idle, done, working or blocked, not %q", s)
		}
		sel.States = append(sel.States, st)
	}
	if f.limit < 0 {
		return req, errors.New("--limit is positive")
	}
	req.Selector = sel
	return req, nil
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
	return send.Env{DB: ie.db, Bindings: bindings.NewStore(ie.db), Herdr: ie.herdr, InstanceDir: ie.inst.Dir,
		Inode: func() uint64 { return wait.SocketInode(ie.herdr.SocketPath) }, Bead: send.BrShow, History: history.Append}
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

// sendExit is the native exit code of a finished send.
func sendExit(res send.Result, waited []wait.Result) int {
	c := res.Counts()
	unknown := c[send.StatusPossiblySubmitted] + c[send.StatusAttemptStarted] + c[send.StatusPending]
	switch {
	case res.DryRun:
		return exitOK
	case res.InProgress || unknown > 0:
		return exitAmbiguous
	case c[send.StatusFailed] > 0:
		return exitError
	case c[send.StatusSubmitted] == 0 && c[send.StatusRefused] > 0:
		return exitAdmission
	case c[send.StatusSubmitted] != len(res.Targets):
		return exitError
	}
	if waited != nil {
		s := wait.Summarize(waited, 0)
		switch {
		case s.Ambiguous:
			return exitAmbiguous
		case !s.Met:
			return exitError
		}
	}
	return exitOK
}

func printSend(w io.Writer, res send.Result, waited []wait.Result) {
	if res.Replayed {
		fmt.Fprintf(w, "operation %s was claimed before; nothing sent. Its receipt:\n", res.OperationID)
	}
	for _, rc := range res.Targets {
		name := rc.MailName
		if name == "" {
			name = rc.Key
		}
		line := fmt.Sprintf("%s (%s): %s", name, rc.Key, strings.ReplaceAll(rc.Status, "_", " "))
		switch {
		case rc.Result.StateAfter != "":
			line += " (" + rc.Result.StateAfter + ")"
		case rc.Result.Gate != "" && rc.Status != send.StatusSubmitted:
			line += " (" + string(rc.Result.Gate) + ")"
		}
		if rc.Result.Reason != "" && rc.Status != send.StatusSubmitted && rc.Status != "would_send" {
			line += ": " + rc.Result.Reason
		} else if rc.ErrorCode != "" && rc.Status != send.StatusSubmitted {
			line += ": " + rc.ErrorCode
		}
		fmt.Fprintln(w, line)
	}
	for _, r := range waited {
		line := fmt.Sprintf("%s (%s): %s", r.MailName, dash(r.Slot), r.Outcome)
		if r.Correlation != "" {
			line += " (" + r.Correlation + ")"
		}
		fmt.Fprintln(w, line)
	}
	if res.DryRun {
		for i, p := range res.Rendered {
			fmt.Fprintf(w, "--- %s (%s) ---\n%s\n", res.Targets[i].MailName, res.Targets[i].Key, p)
		}
		fmt.Fprintln(w, "dry run: nothing sent, nothing written")
		return
	}
	if res.InProgress {
		fmt.Fprintf(w, "operation %s has no final outcome; never resend with this id\n", res.OperationID)
		return
	}
	fmt.Fprintf(w, "operation %s\n", res.OperationID)
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
	fs := flag.NewFlagSet("nhm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sendS := fs.String("robot-send", "", "")
	receiptOp := fs.String("robot-send-receipt", "", "")
	ackS := fs.String("robot-ack", "", "")
	format := fs.String("robot-format", "auto", "")
	var panes, types listFlag
	fs.Var(&panes, "panes", "")
	fs.Var(&panes, "pane", "")
	fs.Var(&types, "type", "")
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
		return robotFail(stdout, robot.ErrCodeInvalidFlag, msg, "nhm --robot-capabilities lists the robot flags", "json", robotError, now)
	}
	_ = noCASS
	switch *format {
	case "auto", "json":
		*format = "json"
	default:
		return robotFail(stdout, robot.ErrCodeInvalidFlag, fmt.Sprintf("--robot-format=%s: nhm writes json only", *format),
			"use --robot-format=json", "json", robotError, now)
	}
	if *withMemory {
		return robotFail(stdout, "NOT_IMPLEMENTED", "--with-memory comes with nhm.memory (acfs-9hkh.21)",
			"pass --with-memory=false", *format, robotUnavailable, now)
	}
	ie, err := openInstance(*socket)
	if err != nil {
		return robotFail(stdout, robot.ErrCodeDependencyMissing, err.Error(), "run inside herdr or give --socket", *format, robotUnavailable, now)
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
	req := send.Request{Message: text, OperationID: *opID, DryRun: *dryRun, Source: history.SourceRobot,
		Caller: os.Getenv("HERDR_PANE_ID"), Selector: send.Selector{All: *all, Panes: panes, Kinds: types}}
	if err := ie.fillScope(ctx, &req, *sendS, ""); err != nil {
		return robotFail(stdout, robot.ErrCodeSessionNotFound, err.Error(), "list sessions: nhm --robot-status", *format, robotError, now)
	}
	res, _, err := runSendCore(ctx, ie, req)
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
