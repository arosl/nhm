package cli

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/robot"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/store/history"
)

// interruptPoll is ntm's poll for the agents' return to their prompt.
const interruptPoll = 300 * time.Millisecond

// runRobotInterrupt answers ntm's --robot-interrupt=S with ntm's
// InterruptOutput (nhm-hir; ru calls the bare form). Each selected agent
// that is not at its prompt gets its kind's interrupt key, every agent
// with --force; then nhm waits up to --timeout (10 s) for each to be at
// its prompt again, unless --no-wait. With --msg, the agents at their
// prompt then get the message through the send core. Without --panes
// the calling pane is left out, as nhm interrupt does.
func runRobotInterrupt(args []string, stdout io.Writer) int {
	now := time.Now()
	fs := flag.NewFlagSet("nhm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	session := fs.String("robot-interrupt", "", "")
	format := fs.String("robot-format", "auto", "")
	var msg, timeoutS string
	fs.StringVar(&msg, "msg", "", "")
	fs.StringVar(&msg, "interrupt-msg", "", "")
	fs.StringVar(&timeoutS, "timeout", "10s", "")
	fs.StringVar(&timeoutS, "interrupt-timeout", "10s", "")
	var force bool
	fs.BoolVar(&force, "force", false, "")
	fs.BoolVar(&force, "interrupt-force", false, "")
	var noWait, all bool
	fs.BoolVar(&noWait, "no-wait", false, "")
	fs.BoolVar(&noWait, "interrupt-no-wait", false, "")
	var panes, types listFlag
	fs.Var(&panes, "panes", "")
	fs.Var(&types, "type", "")
	// ntm's --all adds the user pane; nhm keeps none.
	fs.BoolVar(&all, "all", false, "")
	fs.BoolVar(&all, "interrupt-all", false, "")
	dryRun := fs.Bool("dry-run", false, "")
	opID := fs.String("op-id", "", "")
	socket := fs.String("socket", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		msg := "unexpected arguments"
		if err != nil {
			msg = err.Error()
		}
		return robotFail(stdout, robot.ErrCodeInvalidFlag, msg, "nhm --robot-capabilities lists the robot flags", "json", robotError, now)
	}
	switch *format {
	case "auto", "json":
		*format = "json"
	default:
		return robotFail(stdout, robot.ErrCodeInvalidFlag, fmt.Sprintf("--robot-format=%s: nhm writes json only", *format),
			"use --robot-format=json", "json", robotError, now)
	}
	if *session == "" {
		return robotFail(stdout, robot.ErrCodeInvalidFlag, "--robot-interrupt requires a session", "use --robot-interrupt=SESSION", *format, robotError, now)
	}
	timeout, err := time.ParseDuration(timeoutS)
	if err != nil || timeout <= 0 {
		return robotFail(stdout, robot.ErrCodeInvalidFlag, fmt.Sprintf("invalid timeout %q", timeoutS), "Use a duration such as 30s or 500ms", *format, robotError, now)
	}

	ie, err := openInstance(*socket)
	if err != nil {
		return robotFail(stdout, robot.ErrCodeDependencyMissing, err.Error(), "run inside herdr or give --socket", *format, robotError, now)
	}
	defer ie.db.Close()
	ctx := context.Background()
	ws, err := sessionWorkspace(ctx, ie.herdr, *session)
	if err != nil {
		return robotFail(stdout, robot.ErrCodeSessionNotFound, err.Error(), "Use --robot-status to list available sessions", *format, robotError, now)
	}
	bs := bindings.NewStore(ie.db)
	sel := wait.Selector{Kinds: robotKinds(types)}
	if len(panes) == 0 {
		sel.Exclude = ie.callerPane(ctx)
	}
	list, err := wait.Select(ctx, bs, ws, "", sel)
	if err == nil {
		list, err = robotWaitPanes(list, panes)
	}
	if err != nil {
		code := robot.ErrCodeInternalError
		if errors.Is(err, wait.ErrNoTargets) {
			code = robot.ErrCodePaneNotFound
		}
		return robotFail(stdout, code, "no panes matched the interrupt request: "+err.Error(),
			"Drop --panes to target all agent panes; nhm --robot-activity=SESSION lists them", *format, robotError, now)
	}

	req := wait.InterruptRequest{OperationID: *opID, Scope: map[string]string{"instance_id": ie.inst.ID, "workspace_id": ws},
		Targets: list, SkipResting: !force, DryRun: *dryRun}
	var res wait.InterruptResult
	lctx, cancel := context.WithTimeout(ctx, lockWait)
	err = core.WithOwnerShared(lctx, ie.inst.Dir, func(o core.Owner) error {
		if err := core.RequireOwner(o, core.OwnerNhm); err != nil {
			return err
		}
		var ierr error
		res, ierr = wait.Interrupt(ctx, ie.db, ie.herdr, bs, req, time.Now)
		return ierr
	})
	cancel()
	if err != nil {
		code := robot.ErrCodeInternalError
		switch {
		case errors.Is(err, store.ErrOperationIDConflict):
			code = robot.ErrCodeIdempotencyConflict
		case errors.Is(err, core.ErrNotOwner):
			code = robot.ErrCodeNotOwner
		}
		return robotFail(stdout, code, err.Error(), "", *format, robotError, now)
	}

	out := robot.InterruptOutput{RobotResponse: robot.NewResponse(true, *format, now), Session: *session, InterruptedAt: now.UTC(),
		Interrupted: []string{}, PreviousStates: map[string]robot.PaneState{}, Method: robot.InterruptMethodCtrlC,
		ReadyForInput: []string{}, Failed: []robot.InterruptError{}, TimeoutMs: int(timeout.Milliseconds()), DryRun: *dryRun,
		NHM: map[string]any{"operation_id": res.OperationID, "targets": res.Targets}}
	if msg != "" {
		out.Method, out.Message = robot.InterruptMethodCtrlCThenSend, msg
	}
	sendOp := ""
	if *opID != "" {
		sendOp = *opID + ".msg"
	}
	if res.Existing {
		// An identical retry sends nothing: it reports the operation and
		// whether its message went out.
		out.Operation = &robot.OperationInfo{OperationID: res.OperationID, Kind: wait.VerbInterrupt, Status: res.Status, Replayed: true}
		stored, serr := send.Stored(ctx, ie.db, sendOp)
		if msg != "" && serr == nil {
			out.MessageSent = anySubmitted(stored)
		}
		switch {
		case res.Status != "done":
			out.Success, out.ErrorCode = false, robot.ErrCodeInternalError
			out.Error = fmt.Sprintf("operation %s was claimed before with status %s; nothing was sent again", res.OperationID, res.Status)
		case msg != "" && errors.Is(serr, sql.ErrNoRows):
			// The first run stopped before its message (no agent was
			// ready, or the run died): the replay does not guess.
			out.Success, out.ErrorCode = false, robot.ErrCodeInternalError
			out.Error = fmt.Sprintf("operation %s interrupted the agents but recorded no message; the message was not sent", res.OperationID)
			out.Hint = "Send it with --robot-send=SESSION --msg=..., or interrupt again with a new --op-id"
		case msg != "" && serr != nil:
			out.Success, out.ErrorCode = false, robot.ErrCodeInternalError
			out.Error = fmt.Sprintf("operation %s: reading its message's record: %v", res.OperationID, serr)
		}
		return robotInterruptWrite(stdout, out)
	}
	if *opID != "" && !*dryRun {
		out.Operation = &robot.OperationInfo{OperationID: res.OperationID, Kind: wait.VerbInterrupt, Status: res.Status}
	}

	var sent []bindings.Binding
	for i, t := range res.Targets {
		key := robotPaneKey(list[i])
		ps := robot.PaneState{State: snapshot.PaneState(t.StateBefore), AgentType: snapshot.NTMType(t.Kind),
			ObservationFreshness: snapshot.Fresh, ObservationConfidence: 1, ObservedAt: now.UTC().Format(time.RFC3339)}
		if t.StateBefore == "" {
			ps.ObservationConfidence, ps.ObservationError = 0, t.Reason
		}
		out.PreviousStates[key] = ps
		switch t.Status {
		case wait.InterruptSent:
			out.Interrupted = append(out.Interrupted, key)
			sent = append(sent, list[i])
		case wait.InterruptSkipped, wait.InterruptWouldSend:
			if *dryRun {
				// ntm's dry run names every target in order, the ones at
				// their prompt too.
				out.WouldAffect = append(out.WouldAffect, key)
			} else {
				out.ReadyForInput = append(out.ReadyForInput, key)
			}
		default:
			out.Failed = append(out.Failed, robot.InterruptError{Pane: key, Reason: t.Status + ": " + t.Reason})
		}
	}
	if *dryRun {
		out.CompletedAt = time.Now().UTC()
		return robotInterruptWrite(stdout, out)
	}

	switch {
	case noWait:
		out.ReadyForInput = append(out.ReadyForInput, out.Interrupted...)
	case len(sent) > 0:
		ready := robotInterruptWait(ctx, ie, bs, sent, timeout)
		for i, b := range sent {
			if ready[i] {
				out.ReadyForInput = append(out.ReadyForInput, robotPaneKey(b))
			}
		}
		if len(ready) != countTrue(ready) {
			out.TimedOut = true
			out.Success, out.ErrorCode = false, robot.ErrCodeTimeout
			out.Error, out.Hint = "interrupt timed out", "Increase --timeout or check agent health"
		}
	}

	if msg != "" && len(out.ReadyForInput) > 0 {
		var ids []string
		for _, b := range list {
			if slices.Contains(out.ReadyForInput, robotPaneKey(b)) {
				ids = append(ids, b.ID)
			}
		}
		// The follow-up goes as given, as ntm's: a zero Enrichment adds
		// no CM or CASS and runs no duplicate check.
		sreq := send.Request{Message: msg, OperationID: sendOp, Source: history.SourceRobot, Caller: ie.callerPane(ctx),
			Selector: send.Selector{Bindings: ids}}
		if err := ie.fillScope(ctx, &sreq, *session, ""); err != nil {
			out.Failed = append(out.Failed, robot.InterruptError{Pane: "dispatch", Reason: "failed to send message: " + err.Error()})
		} else if sres, _, err := runSendCore(ctx, ie, sreq); err != nil {
			out.Failed = append(out.Failed, robot.InterruptError{Pane: "dispatch", Reason: "failed to send message: " + err.Error()})
		} else {
			out.MessageSent = anySubmitted(sres)
			for _, rc := range sres.Targets {
				if rc.Status != send.StatusSubmitted {
					out.Failed = append(out.Failed, robot.InterruptError{Pane: rc.Key,
						Reason: fmt.Sprintf("failed to send message: %s %s %s", rc.Status, rc.ErrorCode, rc.Result.Reason)})
				}
			}
		}
	}
	if out.Success && len(out.Failed) > 0 {
		out.Success, out.ErrorCode = false, robot.ErrCodeInternalError
		out.Error = fmt.Sprintf("%d interrupt action(s) failed", len(out.Failed))
		out.Hint = "Inspect the 'failed' array for per-pane reasons"
	}
	out.CompletedAt = time.Now().UTC()
	return robotInterruptWrite(stdout, out)
}

// robotInterruptWait waits until each interrupted agent is at its prompt
// again, or the timeout ends; it reports which ones are.
func robotInterruptWait(ctx context.Context, ie *instanceEnv, bs *bindings.Store, sent []bindings.Binding, timeout time.Duration) []bool {
	targets := make([]wait.Target, len(sent))
	for i, b := range sent {
		targets[i] = wait.Target{Binding: b}
	}
	wenv := wait.Env{Herdr: ie.herdr, Bindings: bs, Watch: wait.StreamWatch(ie.herdr),
		Inode: func() uint64 { return wait.SocketInode(ie.herdr.SocketPath) }}
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	results := wait.All(wctx, wenv, targets, wait.Request{Until: wait.UntilIdle, Poll: interruptPoll}, 0)
	ready := make([]bool, len(results))
	for i, r := range results {
		ready[i] = r.Outcome.Met()
	}
	return ready
}

func robotInterruptWrite(stdout io.Writer, out robot.InterruptOutput) int {
	if !out.Success {
		out.Meta = &robot.ResponseMeta{ExitCode: robotError}
		return robotWrite(stdout, out, robotError)
	}
	return robotWrite(stdout, out, robotOK)
}

func anySubmitted(res send.Result) bool {
	for _, rc := range res.Targets {
		if rc.Status == send.StatusSubmitted {
			return true
		}
	}
	return false
}

func countTrue(bs []bool) int {
	n := 0
	for _, b := range bs {
		if b {
			n++
		}
	}
	return n
}
