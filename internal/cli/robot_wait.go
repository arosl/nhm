package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/robot"
)

// robotWaitConditions maps ntm's pane conditions that herdr's states can
// answer to nhm's (nhm-hir). ntm's complete is idle plus 5 s without
// output, a guard against a screen read in the middle of a turn. herdr's
// idle or done comes from the agent's own hooks at the end of its turn,
// so complete is idle.
var robotWaitConditions = map[string]wait.Until{
	robot.WaitConditionIdle:       wait.UntilWaiting,
	robot.WaitConditionComplete:   wait.UntilWaiting,
	robot.WaitConditionGenerating: wait.UntilGenerating,
	robot.WaitConditionHealthy:    wait.UntilUnblocked,
}

// ntmOnlyWaitConditions are ntm's other conditions: they need ntm's screen
// classifier, its attention feed or its productivity evidence.
var ntmOnlyWaitConditions = []string{"stalled", "rate_limited", "rate_limit_lifted", "agent_ready",
	"converged", "attention", "action_required", "mail_pending", "mail_ack_required", "context_hot",
	"reservation_conflict", "file_conflict", "session_changed", "pane_changed"}

// runRobotWait answers ntm's --robot-wait=S with ntm's WaitResponse
// (nhm-hir): ru's `--robot-wait=S --condition=idle --wait-timeout=D
// --exit-on-error --transition`, and ntm's other spellings of those
// flags. The exit codes are ntm's: 0 the condition holds, 1 it does not or
// an error, 2 a condition nhm does not answer.
func runRobotWait(args []string, stdout io.Writer) int {
	now := time.Now()
	fs := flag.NewFlagSet("nhm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	session := fs.String("robot-wait", "", "")
	format := fs.String("robot-format", "auto", "")
	var condition, timeoutS, pollS string
	fs.StringVar(&condition, "condition", robot.WaitConditionIdle, "")
	fs.StringVar(&condition, "wait-until", robot.WaitConditionIdle, "")
	fs.StringVar(&timeoutS, "timeout", "5m", "")
	fs.StringVar(&timeoutS, "wait-timeout", "5m", "")
	fs.StringVar(&pollS, "poll", "2s", "")
	fs.StringVar(&pollS, "wait-poll", "2s", "")
	var panes, types listFlag
	fs.Var(&panes, "panes", "")
	fs.Var(&panes, "wait-panes", "")
	fs.Var(&types, "type", "")
	fs.Var(&types, "wait-type", "")
	var anyFlag, exitOnError, transition bool
	fs.BoolVar(&anyFlag, "any", false, "")
	fs.BoolVar(&anyFlag, "wait-any", false, "")
	fs.BoolVar(&exitOnError, "exit-on-error", false, "")
	fs.BoolVar(&exitOnError, "wait-exit-on-error", false, "")
	fs.BoolVar(&transition, "transition", false, "")
	fs.BoolVar(&transition, "wait-transition", false, "")
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
		return robotFail(stdout, robot.ErrCodeInvalidFlag, "--robot-wait requires a session", "use --robot-wait=SESSION", *format, robotError, now)
	}
	until, ok := robotWaitConditions[condition]
	if !ok {
		if slices.Contains(ntmOnlyWaitConditions, condition) || strings.Contains(condition, ",") {
			return robotFail(stdout, robot.ErrCodeNotImplemented, fmt.Sprintf("condition '%s' is not in this nhm", condition),
				"nhm answers idle, generating and healthy, one at a time, from herdr's agent states", *format, robotUnavailable, now)
		}
		return robotFail(stdout, robot.ErrCodeInvalidFlag, fmt.Sprintf("invalid condition '%s'", condition),
			"Valid conditions: idle, generating, healthy", *format, robotError, now)
	}
	timeout, err := time.ParseDuration(timeoutS)
	if err != nil || timeout <= 0 {
		return robotFail(stdout, robot.ErrCodeInvalidFlag, fmt.Sprintf("invalid timeout %q", timeoutS), "Use a Go duration such as 5m or 30s", *format, robotError, now)
	}
	poll, err := time.ParseDuration(pollS)
	if err != nil || poll <= 0 {
		return robotFail(stdout, robot.ErrCodeInvalidFlag, fmt.Sprintf("invalid poll interval %q", pollS), "Use a Go duration such as 1s or 500ms", *format, robotError, now)
	}

	ie, err := openInstance(*socket)
	if err != nil {
		return robotFail(stdout, robot.ErrCodeDependencyMissing, err.Error(), "run inside herdr or give --socket", *format, robotUnavailable, now)
	}
	defer ie.db.Close()
	ctx := context.Background()
	ws, err := sessionWorkspace(ctx, ie.herdr, *session)
	if err != nil {
		return robotFail(stdout, robot.ErrCodeSessionNotFound, err.Error(), "list sessions: nhm --robot-status", *format, robotError, now)
	}
	store := bindings.NewStore(ie.db)
	list, err := wait.Select(ctx, store, ws, "", wait.Selector{Kinds: robotKinds(types)})
	if err == nil {
		list, err = robotWaitPanes(list, panes)
	}
	if err != nil {
		code := robot.ErrCodeInternalError
		if errors.Is(err, wait.ErrNoTargets) {
			code = robot.ErrCodePaneNotFound
		}
		return robotFail(stdout, code, err.Error(), "Check --panes and --type; nhm --robot-activity=SESSION lists the panes", *format, robotError, now)
	}
	targets := make([]wait.Target, len(list))
	for i, b := range list {
		targets[i] = wait.Target{Binding: b}
	}
	anyN := 0
	if anyFlag {
		anyN = 1
	}
	wenv := wait.Env{Herdr: ie.herdr, Bindings: store, Watch: wait.StreamWatch(ie.herdr),
		Inode: func() uint64 { return wait.SocketInode(ie.herdr.SocketPath) }}
	start := time.Now()
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	results := wait.All(wctx, wenv, targets, wait.Request{Until: until, Transition: transition, FailFast: exitOnError, Poll: poll}, anyN)
	if !exitOnError && !wait.Summarize(results, anyN).Met {
		// As ntm without --exit-on-error: an agent that cannot meet the
		// condition holds the wait to its deadline.
		<-wctx.Done()
	}
	resp := robotWaitResponse(*session, condition, list, results, anyN, exitOnError, timeout, start, *format, time.Now())
	if !resp.Success {
		resp.Meta = &robot.ResponseMeta{ExitCode: robotError}
		return robotWrite(stdout, resp, robotError)
	}
	return robotWrite(stdout, resp, robotOK)
}

// robotWaitPanes keeps the bindings that --panes names, by slot or by
// herdr pane id. A selector that names no agent is an error, as in ntm.
func robotWaitPanes(list []bindings.Binding, panes []string) ([]bindings.Binding, error) {
	if len(panes) == 0 {
		return list, nil
	}
	var out []bindings.Binding
	for _, p := range panes {
		i := slices.IndexFunc(list, func(b bindings.Binding) bool { return b.Slot == p || b.PaneID == p })
		if i < 0 {
			return nil, fmt.Errorf("%w: pane %s", wait.ErrNoTargets, p)
		}
		if !slices.ContainsFunc(out, func(b bindings.Binding) bool { return b.ID == list[i].ID }) {
			out = append(out, list[i])
		}
	}
	return out, nil
}

// robotWaitResponse projects the results onto ntm's WaitResponse. A met
// wait lists the agents that met it. With exitOnError, a target that
// cannot meet it any more is AGENT_ERROR, as ntm's error state is;
// otherwise the wait is TIMEOUT, with the pending panes. The detail of a
// target is in "_nhm".
func robotWaitResponse(session, condition string, list []bindings.Binding, results []wait.Result, anyN int, exitOnError bool,
	timeout time.Duration, start time.Time, format string, now time.Time) robot.WaitResponse {
	resp := robot.WaitResponse{RobotResponse: robot.NewResponse(true, format, now), Session: session, Condition: condition,
		WaitedSeconds: now.Sub(start).Seconds(), Agents: []robot.WaitAgentInfo{}, NHM: map[string]any{"targets": results}}
	info := func(i int) robot.WaitAgentInfo {
		r := results[i]
		state, _, _ := snapshot.Activity(r.State)
		return robot.WaitAgentInfo{Pane: robotPaneKey(list[i]), State: state, AgentType: snapshot.NTMType(list[i].Kind)}
	}
	if wait.Summarize(results, anyN).Met {
		for i, r := range results {
			if r.Outcome.Met() {
				a := info(i)
				a.MetAt = start.Add(time.Duration(r.WaitedMS) * time.Millisecond).UTC().Format(time.RFC3339)
				resp.Agents = append(resp.Agents, a)
			}
		}
		return resp
	}
	for i, r := range results {
		if exitOnError && r.Outcome.Lost() {
			resp.Success, resp.ErrorCode, resp.Hint = false, robot.ErrCodeAgentError, "Check agent output with --robot-tail"
			resp.Error = fmt.Sprintf("agent error detected in pane '%s'", robotPaneKey(list[i]))
			resp.Agents = []robot.WaitAgentInfo{info(i)}
			return resp
		}
	}
	resp.Success, resp.ErrorCode = false, robot.ErrCodeTimeout
	resp.Error, resp.Hint = fmt.Sprintf("timeout after %v", timeout), "Try increasing --timeout or check agent status with --robot-activity"
	for i, r := range results {
		if !r.Outcome.Met() {
			resp.AgentsPending = append(resp.AgentsPending, robotPaneKey(list[i]))
		}
	}
	return resp
}

// robotPaneKey names a pane as nhm's other robot outputs do: its slot, or
// its herdr pane id without one.
func robotPaneKey(b bindings.Binding) string {
	if b.Slot != "" {
		return b.Slot
	}
	return b.PaneID
}
