package wait

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

// Until is what a wait waits for.
type Until string

const (
	UntilIdle       Until = "idle"
	UntilDone       Until = "done"
	UntilComplete   Until = "complete"
	UntilGenerating Until = "generating"
	UntilHealthy    Until = "healthy"
)

// ParseUntil checks an --until value.
func ParseUntil(s string) (Until, error) {
	switch u := Until(s); u {
	case UntilIdle, UntilDone, UntilComplete, UntilGenerating, UntilHealthy:
		return u, nil
	}
	return "", fmt.Errorf("invalid --until %q: use idle, done, complete, generating or healthy", s)
}

// Outcome is the result of a wait for one target (plan-v8 2.6).
type Outcome string

const (
	// The state conditions: herdr's state, or the condition, was reached.
	Idle       Outcome = "idle"
	Done       Outcome = "done"
	Generating Outcome = "generating"
	Healthy    Outcome = "healthy"
	// Complete: a completion after the baseline, by the same generation,
	// and nhm saw the turn start. Correlation strong.
	Complete Outcome = "complete"
	// Ambiguous: a completion after the baseline, but the start was not
	// seen or the agent was already working. Correlation weak.
	Ambiguous Outcome = "ambiguous"
	Timeout   Outcome = "timeout"
	// Replaced: a new occupant holds the pane or the identity.
	Replaced Outcome = "replaced"
	// ContinuityLost: herdr restarted, its counters went back, or the
	// agent left the pane. The old wait is invalid.
	ContinuityLost Outcome = "continuity_lost"
	// NotSubmitted: the operation never submitted to this target.
	NotSubmitted Outcome = "not_submitted"
	// Pending: the wait stopped early, because enough other targets met
	// the condition (--any) or the caller cancelled it.
	Pending Outcome = "pending"
	// Failed: nhm could not read the binding or herdr's answer.
	Failed Outcome = "error"
)

// Met reports whether o satisfies the wait's condition.
func (o Outcome) Met() bool {
	switch o {
	case Idle, Done, Generating, Healthy, Complete:
		return true
	}
	return false
}

// Correlation values of a completion.
const (
	Strong = "strong"
	Weak   = "weak"
)

// Herdr is what wait needs from herdr; *herdr.Client has it.
type Herdr interface {
	AgentGet(ctx context.Context, target string) (herdr.AgentInfo, error)
	AgentWait(ctx context.Context, p herdr.AgentWaitParams) (herdr.AgentInfo, error)
	AgentSendKeys(ctx context.Context, target string, keys ...string) error
}

// Bindings reads a binding again at each poll.
type Bindings interface {
	Get(ctx context.Context, id string) (bindings.Binding, error)
}

// Env is what a wait runs against.
type Env struct {
	Herdr    Herdr
	Bindings Bindings
	// Watch returns a channel that receives at each agent state change
	// of the pane and is closed when the stream ends. nil, or a nil
	// channel, means that nhm only polls.
	Watch func(ctx context.Context, paneID string) <-chan struct{}
	// Inode returns herdr's socket inode (0 when unknown). nil: not
	// checked.
	Inode func() uint64
	Now   func() time.Time
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e Env) inode() uint64 {
	if e.Inode != nil {
		return e.Inode()
	}
	return 0
}

// Target is one agent to wait for.
type Target struct {
	Binding bindings.Binding
	// Baseline is the receipt's pre-submission baseline; nil reads one at
	// the start of the wait.
	Baseline *Baseline
	// Submission is the receipt's status; empty without --after-op.
	Submission string
}

// Request is one wait.
type Request struct {
	Until Until
	// AfterOp: only a state reached after the baseline counts.
	AfterOp bool
	// Poll is the longest sleep between two reads of the agent.
	Poll time.Duration
}

// DefaultPoll is ntm's poll interval.
const DefaultPoll = 2 * time.Second

// Result is the wait's result for one target.
type Result struct {
	BindingID    string            `json:"binding_id"`
	MailName     string            `json:"mail_name"`
	PaneID       string            `json:"pane_id"`
	Slot         string            `json:"slot,omitempty"`
	Generation   int64             `json:"generation"`
	Outcome      Outcome           `json:"outcome"`
	State        herdr.AgentStatus `json:"state,omitempty"`
	Correlation  string            `json:"correlation,omitempty"`
	BaselineSeq  *uint64           `json:"baseline_seq,omitempty"`
	StartedSeq   *uint64           `json:"started_seq,omitempty"`
	CompletedSeq *uint64           `json:"completed_seq,omitempty"`
	Reason       string            `json:"reason,omitempty"`
	WaitedMS     int64             `json:"waited_ms"`
}

// errEnough cancels the other waits once --any has its count.
var errEnough = errors.New("wait: enough targets met the condition")

// One waits for one target until the condition holds, the target is
// replaced or loses continuity, or ctx ends. ctx's deadline is the
// timeout.
func One(ctx context.Context, env Env, t Target, req Request) Result {
	w := waiter{env: env, req: req, start: env.now(), r: Result{
		BindingID: t.Binding.ID, MailName: t.Binding.MailName, PaneID: t.Binding.PaneID,
		Slot: t.Binding.Slot, Generation: t.Binding.Generation,
	}}
	if w.req.Poll <= 0 {
		w.req.Poll = DefaultPoll
	}
	return w.run(ctx, t)
}

type waiter struct {
	env     Env
	req     Request
	start   time.Time
	r       Result
	base    Baseline
	started bool
}

func (w *waiter) finish(o Outcome, reason string) Result {
	w.r.Outcome, w.r.Reason = o, reason
	w.r.WaitedMS = w.env.now().Sub(w.start).Milliseconds()
	return w.r
}

// ended turns the end of ctx into an outcome.
func (w *waiter) ended(ctx context.Context) Result {
	cause := context.Cause(ctx)
	switch {
	case errors.Is(cause, errEnough):
		return w.finish(Pending, "enough other targets met the condition")
	case errors.Is(cause, context.DeadlineExceeded):
		reason := "the deadline passed"
		if w.req.Until == UntilComplete && w.started {
			reason += "; the turn started but did not complete"
		}
		return w.finish(Timeout, reason)
	default:
		return w.finish(Pending, cause.Error())
	}
}

func (w *waiter) run(ctx context.Context, t Target) Result {
	if w.req.AfterOp && t.Baseline == nil {
		return w.finish(NotSubmitted, "the operation recorded no baseline for this target (receipt "+t.Submission+")")
	}
	// A state wait without --after-op sleeps in herdr's agent.wait. The
	// other waits sleep on the event stream: watch first, so a change
	// between the first read and the subscription still wakes the loop.
	inHerdr := w.req.Until != UntilComplete && !w.req.AfterOp
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var watch <-chan struct{}
	if !inHerdr {
		watch = w.subscribe(wctx, t.Binding.PaneID)
	}

	if t.Baseline != nil {
		w.base = *t.Baseline
	} else {
		// agentError compares the binding with this until the read
		// gives the full baseline.
		w.base = Baseline{BindingID: t.Binding.ID, Generation: t.Binding.Generation, PaneID: t.Binding.PaneID}
		a, err := w.env.Herdr.AgentGet(ctx, t.Binding.PaneID)
		if err != nil {
			if ctx.Err() != nil {
				return w.ended(ctx)
			}
			return w.agentError(ctx, err)
		}
		if w.base, err = Capture(t.Binding, a, w.env.inode(), w.env.now()); err != nil {
			return w.finish(Failed, err.Error())
		}
	}
	seq := w.base.StateChangeSeq
	w.r.BaselineSeq = &seq
	w.r.Generation = w.base.Generation
	w.started = t.Submission == Submitted

	for {
		if r, done := w.check(ctx); done {
			return r
		}
		if ctx.Err() != nil {
			return w.ended(ctx)
		}
		if inHerdr {
			w.sleepInHerdr(ctx)
			continue
		}
		if watch == nil {
			watch = w.subscribe(wctx, w.base.PaneID)
		}
		timer := time.NewTimer(w.req.Poll)
		select {
		case <-ctx.Done():
		case _, ok := <-watch:
			if !ok {
				// The stream ended; events may be lost. Poll, and
				// subscribe again on the next round.
				watch = nil
			}
		case <-timer.C:
		}
		timer.Stop()
	}
}

func (w *waiter) subscribe(ctx context.Context, paneID string) <-chan struct{} {
	if w.env.Watch == nil {
		return nil
	}
	return w.env.Watch(ctx, paneID)
}

// sleepInHerdr waits in herdr's agent.wait for at most one poll interval.
// The loop checks the binding and the counters after it returns.
func (w *waiter) sleepInHerdr(ctx context.Context) {
	ms := w.req.Poll.Milliseconds()
	if ms < 1 {
		ms = 1
	}
	_, err := w.env.Herdr.AgentWait(ctx, herdr.AgentWaitParams{
		Target: w.base.PaneID, Until: statesFor(w.req.Until), TimeoutMS: &ms,
	})
	if err != nil && ctx.Err() == nil && herdr.ErrorCode(err) != herdr.CodeTimeout {
		// A refused or failed wait: the next check reads the cause.
		// Sleep, so that a herdr that refuses at once is not hammered.
		timer := time.NewTimer(w.req.Poll)
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
		timer.Stop()
	}
}

// statesFor maps a state condition to herdr's states.
func statesFor(u Until) []herdr.AgentStatus {
	switch u {
	case UntilIdle:
		return []herdr.AgentStatus{herdr.StatusIdle}
	case UntilDone:
		return []herdr.AgentStatus{herdr.StatusDone}
	case UntilGenerating:
		return []herdr.AgentStatus{herdr.StatusWorking}
	case UntilHealthy:
		return []herdr.AgentStatus{herdr.StatusIdle, herdr.StatusDone, herdr.StatusWorking}
	}
	return nil
}

func stateMet(u Until, s herdr.AgentStatus) bool {
	for _, st := range statesFor(u) {
		if s == st {
			return true
		}
	}
	return false
}

// check reads the binding and the agent once. It returns done when the
// wait has its result.
func (w *waiter) check(ctx context.Context) (Result, bool) {
	cur, err := w.env.Bindings.Get(ctx, w.base.BindingID)
	if err != nil {
		if ctx.Err() != nil {
			return w.ended(ctx), true
		}
		return w.finish(Failed, err.Error()), true
	}
	if r, done := w.replaced(cur); done {
		return r, true
	}
	if cur.PaneID != w.base.PaneID {
		return w.finish(ContinuityLost, fmt.Sprintf("the agent moved from pane %s to %s", w.base.PaneID, cur.PaneID)), true
	}
	if w.base.SocketInode != 0 {
		if ino := w.env.inode(); ino != 0 && ino != w.base.SocketInode {
			return w.finish(ContinuityLost, "herdr restarted: its socket is new"), true
		}
	}
	a, err := w.env.Herdr.AgentGet(ctx, w.base.PaneID)
	if err != nil {
		if ctx.Err() != nil {
			return w.ended(ctx), true
		}
		if transient(err) {
			// herdr may be restarting; the socket check or a later
			// read decides.
			return Result{}, false
		}
		return w.agentError(ctx, err), true
	}
	if cur.Kind != "" && a.Agent != "" && a.Agent != cur.Kind {
		return w.finish(Replaced, fmt.Sprintf("the pane holds a %s agent; the binding is %s", a.Agent, cur.Kind)), true
	}
	if w.base.NativeSession != "" && a.AgentSession != nil && a.AgentSession.Value != "" && a.AgentSession.Value != w.base.NativeSession {
		return w.finish(Replaced, "the pane's native session changed"), true
	}
	if a.StateChangeSeq == nil {
		return w.finish(Failed, fmt.Sprintf("%v: %s", ErrNoSequence, a.PaneID)), true
	}
	seq := *a.StateChangeSeq
	if seq < w.base.StateChangeSeq {
		return w.finish(ContinuityLost, fmt.Sprintf("herdr's state_change_seq went back from %d to %d", w.base.StateChangeSeq, seq)), true
	}
	w.r.State = a.AgentStatus
	after := seq > w.base.StateChangeSeq
	if after && (a.AgentStatus == herdr.StatusWorking || a.AgentStatus == herdr.StatusBlocked) {
		w.started = true
		if w.r.StartedSeq == nil {
			w.r.StartedSeq = &seq
		}
	}
	if w.req.Until == UntilComplete {
		return w.completion(a)
	}
	if stateMet(w.req.Until, a.AgentStatus) && (!w.req.AfterOp || after) {
		o := Outcome(w.req.Until)
		if w.req.Until == UntilIdle || w.req.Until == UntilDone {
			o = Outcome(a.AgentStatus)
		}
		return w.finish(o, ""), true
	}
	return Result{}, false
}

// completion applies step 3 and 4 of the completion loop.
func (w *waiter) completion(a herdr.AgentInfo) (Result, bool) {
	if a.AgentStatus != herdr.StatusIdle && a.AgentStatus != herdr.StatusDone {
		// A blocked state alone is never a completion.
		return Result{}, false
	}
	if a.CompletionSeq == nil || *a.CompletionSeq <= w.base.StateChangeSeq {
		return Result{}, false
	}
	c := *a.CompletionSeq
	w.r.CompletedSeq = &c
	switch {
	case w.base.Working:
		w.r.Correlation = Weak
		return w.finish(Ambiguous, "the agent was already working at the baseline; the completion may end the earlier turn"), true
	case !w.started:
		w.r.Correlation = Weak
		return w.finish(Ambiguous, "a completion came after the baseline, but nhm did not see the turn start"), true
	}
	w.r.Correlation = Strong
	return w.finish(Complete, ""), true
}

// replaced checks the binding against the baseline's generation.
func (w *waiter) replaced(cur bindings.Binding) (Result, bool) {
	switch {
	case cur.Generation != w.base.Generation:
		w.r.Generation = cur.Generation
		return w.finish(Replaced, fmt.Sprintf("the binding's generation changed from %d to %d", w.base.Generation, cur.Generation)), true
	case cur.Status != bindings.Active:
		reason := "the binding is " + string(cur.Status)
		if cur.StatusReason != "" {
			reason += ": " + cur.StatusReason
		}
		return w.finish(Replaced, reason), true
	}
	return Result{}, false
}

// agentError turns herdr's refusal to read the agent into an outcome. A
// gone agent is a replacement when the binding moved on, and a loss of
// continuity otherwise.
func (w *waiter) agentError(ctx context.Context, err error) Result {
	switch herdr.ErrorCode(err) {
	case herdr.CodeAgentNotRunning, herdr.CodeAgentNotFound, herdr.CodePaneNotFound:
		if w.base.BindingID != "" {
			if cur, gerr := w.env.Bindings.Get(ctx, w.base.BindingID); gerr == nil {
				if r, done := w.replaced(cur); done {
					return r
				}
			}
		}
		return w.finish(ContinuityLost, "the agent is gone from the pane: "+herdr.ErrorCode(err))
	}
	return w.finish(Failed, err.Error())
}

// transient reports a failure to reach herdr, which a restart causes.
func transient(err error) bool {
	var te *herdr.TransportError
	return errors.As(err, &te)
}

// All waits for every target at once. With anyN > 0 it stops when anyN
// targets met the condition; the rest are Pending. Results keep the
// targets' order.
func All(ctx context.Context, env Env, targets []Target, req Request, anyN int) []Result {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	out := make([]Result, len(targets))
	var mu sync.Mutex
	met := 0
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := One(ctx, env, t, req)
			mu.Lock()
			defer mu.Unlock()
			out[i] = r
			if r.Outcome.Met() {
				met++
				if anyN > 0 && met >= anyN {
					cancel(errEnough)
				}
			}
		}()
	}
	wg.Wait()
	return out
}

// Summary is the overall result of a wait.
type Summary struct {
	// Met: the condition holds (for every target, or for anyN of them).
	Met bool `json:"met"`
	// Ambiguous: it would hold if the ambiguous completions counted.
	Ambiguous bool `json:"ambiguous"`
}

// Summarize sums up the results.
func Summarize(results []Result, anyN int) Summary {
	met, amb := 0, 0
	for _, r := range results {
		switch {
		case r.Outcome.Met():
			met++
		case r.Outcome == Ambiguous:
			amb++
		}
	}
	need := len(results)
	if anyN > 0 {
		need = anyN
	}
	if len(results) == 0 {
		return Summary{}
	}
	return Summary{Met: met >= need, Ambiguous: met < need && met+amb >= need}
}
