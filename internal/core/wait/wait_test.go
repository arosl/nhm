package wait

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

func u64(v uint64) *uint64 { return &v }

// agent is one agent read: its state and herdr's two counters.
// completion is 0 for "absent".
func agent(st herdr.AgentStatus, seq, completion uint64) herdr.AgentInfo {
	a := herdr.AgentInfo{PaneID: "w1:p1", Agent: "claude", AgentStatus: st, StateChangeSeq: u64(seq)}
	if completion > 0 {
		a.CompletionSeq = u64(completion)
	}
	return a
}

// fakeHerdr plays a script of agent reads; the last step repeats. An
// error step makes AgentGet fail.
type fakeHerdr struct {
	mu     sync.Mutex
	steps  []step
	i      int
	keys   [][]string
	waits  int
	onRead func(i int)
}

type step struct {
	a   herdr.AgentInfo
	err error
}

func (f *fakeHerdr) AgentGet(context.Context, string) (herdr.AgentInfo, error) {
	f.mu.Lock()
	s := f.steps[min(f.i, len(f.steps)-1)]
	i := f.i
	f.i++
	hook := f.onRead
	f.mu.Unlock()
	if hook != nil {
		hook(i)
	}
	return s.a, s.err
}

func (f *fakeHerdr) AgentWait(ctx context.Context, p herdr.AgentWaitParams) (herdr.AgentInfo, error) {
	f.mu.Lock()
	f.waits++
	f.mu.Unlock()
	select {
	case <-ctx.Done():
		return herdr.AgentInfo{}, ctx.Err()
	case <-time.After(time.Duration(*p.TimeoutMS) * time.Millisecond):
	}
	return herdr.AgentInfo{}, &herdr.Error{Method: "agent.wait", Code: herdr.CodeTimeout}
}

func (f *fakeHerdr) AgentSendKeys(_ context.Context, _ string, keys ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, keys)
	return nil
}

func script(as ...herdr.AgentInfo) *fakeHerdr {
	f := &fakeHerdr{}
	for _, a := range as {
		f.steps = append(f.steps, step{a: a})
	}
	return f
}

// fakeBindings holds one binding whose generation a test can change.
type fakeBindings struct {
	mu sync.Mutex
	b  bindings.Binding
}

func (f *fakeBindings) Get(context.Context, string) (bindings.Binding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.b, nil
}

func (f *fakeBindings) set(fn func(*bindings.Binding)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(&f.b)
}

var bound = bindings.Binding{ID: "bnd-1", Generation: 3, Status: bindings.Active, MailName: "IcyKnoll",
	PaneID: "w1:p1", Kind: "claude", Slot: "0.1", HerdrAlias: "icyknoll"}

func baseAt(st herdr.AgentStatus, seq uint64) *Baseline {
	return &Baseline{BindingID: "bnd-1", Generation: 3, PaneID: "w1:p1", State: st, StateChangeSeq: seq,
		Working: st == herdr.StatusWorking}
}

func runOne(t *testing.T, h *fakeHerdr, fb *fakeBindings, tg Target, req Request, timeout time.Duration) Result {
	t.Helper()
	if req.Poll == 0 {
		req.Poll = time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return One(ctx, Env{Herdr: h, Bindings: fb}, tg, req)
}

func complete(base *Baseline, submission string) (Target, Request) {
	return Target{Binding: bound, Baseline: base, Submission: submission}, Request{Until: UntilComplete, AfterOp: true}
}

func TestCompleteNeedsTheStartAndACompletionAfterTheBaseline(t *testing.T) {
	cases := []struct {
		name       string
		base       *Baseline
		submission string
		steps      []herdr.AgentInfo
		want       Outcome
		corr       string
	}{
		{"start seen by a poll", baseAt(herdr.StatusIdle, 10), PossiblySubmitted,
			[]herdr.AgentInfo{agent(herdr.StatusWorking, 11, 0), agent(herdr.StatusDone, 12, 12)}, Complete, Strong},
		{"start seen by herdr's gate, fast finish", baseAt(herdr.StatusIdle, 10), Submitted,
			[]herdr.AgentInfo{agent(herdr.StatusIdle, 12, 12)}, Complete, Strong},
		{"stalled prompt, fast finish", baseAt(herdr.StatusIdle, 10), PossiblySubmitted,
			[]herdr.AgentInfo{agent(herdr.StatusIdle, 12, 12)}, Ambiguous, Weak},
		{"already working at submission", baseAt(herdr.StatusWorking, 10), Submitted,
			[]herdr.AgentInfo{agent(herdr.StatusWorking, 10, 0), agent(herdr.StatusDone, 11, 11)}, Ambiguous, Weak},
		{"an old completion does not count", baseAt(herdr.StatusIdle, 10), Submitted,
			[]herdr.AgentInfo{agent(herdr.StatusIdle, 10, 10), agent(herdr.StatusWorking, 11, 0), agent(herdr.StatusIdle, 12, 12)}, Complete, Strong},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tg, req := complete(c.base, c.submission)
			r := runOne(t, script(c.steps...), &fakeBindings{b: bound}, tg, req, 2*time.Second)
			if r.Outcome != c.want || r.Correlation != c.corr {
				t.Fatalf("= %s/%s (%s), want %s/%s", r.Outcome, r.Correlation, r.Reason, c.want, c.corr)
			}
			if r.CompletedSeq == nil || *r.CompletedSeq <= c.base.StateChangeSeq {
				t.Fatalf("completed_seq = %v", r.CompletedSeq)
			}
		})
	}
}

func TestBlockedIsNeverACompletion(t *testing.T) {
	tg, req := complete(baseAt(herdr.StatusIdle, 10), Submitted)
	r := runOne(t, script(agent(herdr.StatusBlocked, 11, 0)), &fakeBindings{b: bound}, tg, req, 50*time.Millisecond)
	if r.Outcome != Timeout || r.State != herdr.StatusBlocked {
		t.Fatalf("= %s %s (%s)", r.Outcome, r.State, r.Reason)
	}
}

func TestTimeoutIsNotACompletion(t *testing.T) {
	tg, req := complete(baseAt(herdr.StatusIdle, 10), PossiblySubmitted)
	r := runOne(t, script(agent(herdr.StatusWorking, 11, 0)), &fakeBindings{b: bound}, tg, req, 50*time.Millisecond)
	if r.Outcome != Timeout || r.StartedSeq == nil || *r.StartedSeq != 11 {
		t.Fatalf("= %s started %v (%s)", r.Outcome, r.StartedSeq, r.Reason)
	}
}

func TestGenerationChangeIsReplaced(t *testing.T) {
	fb := &fakeBindings{b: bound}
	h := script(agent(herdr.StatusWorking, 11, 0))
	h.onRead = func(i int) {
		if i == 2 {
			fb.set(func(b *bindings.Binding) { b.Generation = 4 })
		}
	}
	tg, req := complete(baseAt(herdr.StatusIdle, 10), Submitted)
	r := runOne(t, h, fb, tg, req, 2*time.Second)
	if r.Outcome != Replaced || r.Generation != 4 {
		t.Fatalf("= %s gen %d (%s)", r.Outcome, r.Generation, r.Reason)
	}
	// A receipt from an older generation is replaced at once.
	old := baseAt(herdr.StatusIdle, 10)
	old.Generation = 2
	tg, req = complete(old, Submitted)
	if r := runOne(t, script(agent(herdr.StatusIdle, 12, 12)), &fakeBindings{b: bound}, tg, req, time.Second); r.Outcome != Replaced {
		t.Fatalf("old generation = %s", r.Outcome)
	}
}

func TestReplacementSignsInHerdr(t *testing.T) {
	codex := agent(herdr.StatusIdle, 12, 12)
	codex.Agent = "codex"
	tg, req := complete(baseAt(herdr.StatusIdle, 10), Submitted)
	if r := runOne(t, script(codex), &fakeBindings{b: bound}, tg, req, time.Second); r.Outcome != Replaced {
		t.Fatalf("other kind = %s", r.Outcome)
	}
	other := agent(herdr.StatusIdle, 12, 12)
	other.AgentSession = &herdr.AgentSession{Value: "sess-new"}
	base := baseAt(herdr.StatusIdle, 10)
	base.NativeSession = "sess-old"
	tg, req = complete(base, Submitted)
	if r := runOne(t, script(other), &fakeBindings{b: bound}, tg, req, time.Second); r.Outcome != Replaced {
		t.Fatalf("other session = %s", r.Outcome)
	}
	fb := &fakeBindings{b: bound}
	fb.set(func(b *bindings.Binding) { b.Status = bindings.Displaced; b.StatusReason = "clear" })
	tg, req = complete(baseAt(herdr.StatusIdle, 10), Submitted)
	if r := runOne(t, script(agent(herdr.StatusIdle, 12, 12)), fb, tg, req, time.Second); r.Outcome != Replaced {
		t.Fatalf("displaced = %s", r.Outcome)
	}
}

func TestContinuityLost(t *testing.T) {
	tg, req := complete(baseAt(herdr.StatusIdle, 10), Submitted)
	if r := runOne(t, script(agent(herdr.StatusIdle, 3, 3)), &fakeBindings{b: bound}, tg, req, time.Second); r.Outcome != ContinuityLost {
		t.Fatalf("counters went back = %s", r.Outcome)
	}
	gone := &fakeHerdr{steps: []step{{err: &herdr.Error{Method: "agent.get", Code: herdr.CodeAgentNotRunning}}}}
	if r := runOne(t, gone, &fakeBindings{b: bound}, tg, req, time.Second); r.Outcome != ContinuityLost {
		t.Fatalf("agent gone = %s", r.Outcome)
	}
	// The agent is gone and the binding moved on: a replacement.
	fb := &fakeBindings{b: bound}
	fb.set(func(b *bindings.Binding) { b.Generation = 4 })
	gone.i = 0
	if r := runOne(t, gone, fb, Target{Binding: bound, Submission: Submitted, Baseline: nil},
		Request{Until: UntilComplete, Poll: time.Millisecond}, time.Second); r.Outcome != Replaced {
		t.Fatalf("gone and replaced = %s (%s)", r.Outcome, r.Reason)
	}
	// A new herdr socket: herdr restarted.
	base := baseAt(herdr.StatusIdle, 10)
	base.SocketInode = 7
	tg, req = complete(base, Submitted)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r := One(ctx, Env{Herdr: script(agent(herdr.StatusIdle, 12, 12)), Bindings: &fakeBindings{b: bound},
		Inode: func() uint64 { return 8 }}, tg, Request{Until: UntilComplete, AfterOp: true, Poll: time.Millisecond})
	if r.Outcome != ContinuityLost {
		t.Fatalf("new socket = %s", r.Outcome)
	}
}

func TestATransportErrorIsRetried(t *testing.T) {
	h := &fakeHerdr{steps: []step{
		{err: &herdr.TransportError{Method: "agent.get", Err: errors.New("connection refused")}},
		{a: agent(herdr.StatusWorking, 11, 0)},
		{a: agent(herdr.StatusDone, 12, 12)},
	}}
	tg, req := complete(baseAt(herdr.StatusIdle, 10), PossiblySubmitted)
	if r := runOne(t, h, &fakeBindings{b: bound}, tg, req, time.Second); r.Outcome != Complete {
		t.Fatalf("= %s (%s)", r.Outcome, r.Reason)
	}
}

func TestStateWaits(t *testing.T) {
	// Without --after-op, the current state counts, read once.
	h := script(agent(herdr.StatusIdle, 10, 10))
	r := runOne(t, h, &fakeBindings{b: bound}, Target{Binding: bound}, Request{Until: UntilIdle}, time.Second)
	if r.Outcome != Idle || r.BaselineSeq == nil || *r.BaselineSeq != 10 {
		t.Fatalf("idle = %s %v", r.Outcome, r.BaselineSeq)
	}
	// It sleeps in herdr's agent.wait while the state is not reached.
	h = script(agent(herdr.StatusWorking, 10, 0), agent(herdr.StatusWorking, 10, 0), agent(herdr.StatusDone, 11, 11))
	r = runOne(t, h, &fakeBindings{b: bound}, Target{Binding: bound}, Request{Until: UntilDone}, time.Second)
	if r.Outcome != Done || h.waits == 0 {
		t.Fatalf("done = %s, %d agent.wait calls", r.Outcome, h.waits)
	}
	// healthy is neither blocked nor unknown.
	h = script(agent(herdr.StatusBlocked, 10, 0), agent(herdr.StatusWorking, 11, 0))
	if r = runOne(t, h, &fakeBindings{b: bound}, Target{Binding: bound}, Request{Until: UntilHealthy}, time.Second); r.Outcome != Healthy {
		t.Fatalf("healthy = %s", r.Outcome)
	}
	// With --after-op, a state from before the baseline does not count.
	h = script(agent(herdr.StatusIdle, 10, 10), agent(herdr.StatusIdle, 10, 10), agent(herdr.StatusWorking, 11, 0), agent(herdr.StatusIdle, 12, 12))
	tg := Target{Binding: bound, Baseline: baseAt(herdr.StatusIdle, 10), Submission: Submitted}
	r = runOne(t, h, &fakeBindings{b: bound}, tg, Request{Until: UntilIdle, AfterOp: true}, time.Second)
	if r.Outcome != Idle || h.i < 4 {
		t.Fatalf("idle after op = %s after %d reads", r.Outcome, h.i)
	}
	// A target that the operation never submitted to.
	r = runOne(t, h, &fakeBindings{b: bound}, Target{Binding: bound, Submission: "refused"}, Request{Until: UntilIdle, AfterOp: true}, time.Second)
	if r.Outcome != NotSubmitted {
		t.Fatalf("refused target = %s", r.Outcome)
	}
}

func TestWatchWakesTheLoop(t *testing.T) {
	ch := make(chan struct{}, 1)
	h := script(agent(herdr.StatusWorking, 11, 0))
	// After the first read, the turn ends and the stream says so; the
	// poll interval is an hour, so only the watch can wake the loop.
	h.onRead = func(i int) {
		if i == 0 {
			go func() {
				h.mu.Lock()
				h.steps = append(h.steps, step{a: agent(herdr.StatusDone, 12, 12)})
				h.mu.Unlock()
				ch <- struct{}{}
			}()
		}
	}
	tg, _ := complete(baseAt(herdr.StatusIdle, 10), Submitted)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	r := One(ctx, Env{Herdr: h, Bindings: &fakeBindings{b: bound}, Watch: func(context.Context, string) <-chan struct{} { return ch }},
		tg, Request{Until: UntilComplete, AfterOp: true, Poll: time.Hour})
	if r.Outcome != Complete || time.Since(start) > 2*time.Second {
		t.Fatalf("= %s after %s", r.Outcome, time.Since(start))
	}
}

func TestAllAndSummarize(t *testing.T) {
	a, b := bound, bound
	b.ID, b.MailName = "bnd-2", "BlueLake"
	targets := []Target{{Binding: a}, {Binding: b}}
	fb := multiBindings{"bnd-1": a, "bnd-2": b}
	h := &paneHerdr{byPane: map[string]herdr.AgentInfo{}}
	h.byPane["w1:p1"] = agent(herdr.StatusIdle, 10, 10)
	// Both share w1:p1 in this fake; a second pane keeps working.
	targets[1].Binding.PaneID = "w1:p2"
	fb["bnd-2"] = targets[1].Binding
	w := agent(herdr.StatusWorking, 5, 0)
	w.PaneID = "w1:p2"
	h.byPane["w1:p2"] = w
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	env := Env{Herdr: h, Bindings: fb}
	res := All(ctx, env, targets, Request{Until: UntilIdle, Poll: time.Millisecond}, 1)
	if res[0].Outcome != Idle || res[1].Outcome != Pending {
		t.Fatalf("--any = %s %s", res[0].Outcome, res[1].Outcome)
	}
	if s := Summarize(res, 1); !s.Met {
		t.Fatalf("summary = %+v", s)
	}
	if s := Summarize(res, 0); s.Met || s.Ambiguous {
		t.Fatalf("all = %+v", s)
	}
	if s := Summarize([]Result{{Outcome: Complete}, {Outcome: Ambiguous}}, 0); s.Met || !s.Ambiguous {
		t.Fatalf("ambiguous = %+v", s)
	}
	if s := Summarize(nil, 0); s.Met {
		t.Fatalf("empty = %+v", s)
	}
}

type multiBindings map[string]bindings.Binding

func (m multiBindings) Get(_ context.Context, id string) (bindings.Binding, error) { return m[id], nil }

// paneHerdr answers each pane with a fixed agent.
type paneHerdr struct {
	fakeHerdr
	byPane map[string]herdr.AgentInfo
}

func (p *paneHerdr) AgentGet(_ context.Context, pane string) (herdr.AgentInfo, error) {
	return p.byPane[pane], nil
}

func TestParseUntil(t *testing.T) {
	for _, s := range []string{"idle", "done", "complete", "generating", "healthy"} {
		if _, err := ParseUntil(s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	if _, err := ParseUntil("finished"); err == nil {
		t.Fatal("finished parsed")
	}
}
