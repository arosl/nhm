package spawn

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/herdr"
)

// waitLog is the fake herdr with agent.wait in its call log; failOn makes
// that wait (1-based) fail.
type waitLog struct {
	*fakeHerdr
	waits, failOn int
}

func (w *waitLog) AgentWait(ctx context.Context, p herdr.AgentWaitParams) (herdr.AgentInfo, error) {
	w.mu.Lock()
	w.waits++
	n := w.waits
	w.call("agent.wait " + p.Target)
	w.mu.Unlock()
	if n == w.failOn {
		return herdr.AgentInfo{}, &herdr.Error{Code: herdr.CodeTimeout, Message: "timeout"}
	}
	return w.fakeHerdr.AgentWait(ctx, p)
}

// clock gives the world a fake clock that its sleeps move on, and
// records the sleeps.
func clock(w *world) *[]time.Duration {
	now := time.Date(2026, 10, 24, 6, 0, 0, 0, time.UTC)
	var sleeps []time.Duration
	w.env.Now = func() time.Time { return now }
	w.env.Sleep = func(_ context.Context, d time.Duration) {
		sleeps = append(sleeps, d)
		now = now.Add(d)
	}
	return &sleeps
}

// As ntm's spawn: every agent starts first, then the spawn waits for them
// to be ready, then the kickoffs go out on the stagger schedule; the
// stagger paces the prompts, not the starts (nhm-2p6.4.10).
func TestSpawnStartsEveryAgentFirstThenPacesKickoffs(t *testing.T) {
	w := newWorld(t)
	log := &waitLog{fakeHerdr: w.h}
	w.env.Herdr = log
	sleeps := clock(w)
	r := req(Want{Kind: "claude", Count: 3})
	r.OperationID, r.Prompt, r.StaggerMode, r.StaggerDelay = "op-ph", "Do the bead.", StaggerFixed, 10*time.Second
	res, err := Spawn(context.Background(), w.env, r)
	if err != nil || !res.Ready() {
		t.Fatalf("spawn = %+v %v", res, err)
	}
	lastStart, firstWait := -1, -1
	for i, c := range w.h.calls {
		switch {
		case strings.HasPrefix(c, "agent.start "):
			lastStart = i
		case strings.HasPrefix(c, "agent.wait ") && firstWait < 0:
			firstWait = i
		}
	}
	if lastStart < 0 || firstWait < lastStart {
		t.Fatalf("a wait came before the last start: %v", w.h.calls)
	}
	if len(*sleeps) != 2 || (*sleeps)[0] != 10*time.Second || (*sleeps)[1] != 10*time.Second || len(w.sent) != 3 {
		t.Fatalf("sleeps %v, kickoffs %d", *sleeps, len(w.sent))
	}
	for i, tg := range res.Targets {
		if tg.DelayMS != int64(i)*10000 || tg.Status != StatusKickoffSubmitted {
			t.Fatalf("target %d = %+v", i, tg)
		}
	}
	if res.Stagger == nil || res.Stagger.Mode != StaggerFixed || res.Stagger.IntervalMs != 10000 || len(res.Stagger.Schedule) != 3 ||
		res.Stagger.Schedule[2].ScheduledAt != "2026-10-24T06:00:20Z" {
		t.Fatalf("stagger = %+v", res.Stagger)
	}
	if out := SpawnOutput(res, nil, "json", time.Now()); out.Stagger != res.Stagger {
		t.Fatalf("the answer's stagger = %+v", out.Stagger)
	}
}

// Without the wait, the spawn starts every agent and answers: no
// agent.wait, each agent started and not ready, and the robot answer is a
// success, as ntm's without --spawn-wait. A kickoff needs the wait.
func TestSpawnNoWait(t *testing.T) {
	w := newWorld(t)
	log := &waitLog{fakeHerdr: w.h}
	w.env.Herdr = log
	r := req(Want{Kind: "claude", Count: 1}, Want{Kind: "codex", Count: 1})
	r.OperationID, r.NoKickoff, r.NoWait = "op-nw", true, true
	res, err := Spawn(context.Background(), w.env, r)
	if err != nil || log.waits != 0 || res.Ready() || !res.Started() || !res.NoWait {
		t.Fatalf("spawn = %+v %v, waits %d", res, err, log.waits)
	}
	for _, tg := range res.Targets {
		if tg.Status != StatusStarted || tg.Ready || tg.BindingID == "" {
			t.Fatalf("target = %+v", tg)
		}
	}
	if out := SpawnOutput(res, nil, "json", time.Now()); !out.Success || out.Stagger != nil || out.Agents[0].Ready {
		t.Fatalf("answer = %+v", out)
	}
	r = req(Want{Kind: "claude", Count: 1})
	r.NoWait = true
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "a kickoff needs the agents ready") {
		t.Fatalf("a kickoff without the wait = %v", err)
	}
}

// An agent that does not get ready is unresolved and left in place; the
// others are ready. With KickoffAllReady (ntm's robot spawn) nobody gets
// the prompt; without it, the ready ones get their kickoff.
func TestSpawnKickoffAllReady(t *testing.T) {
	for _, all := range []bool{true, false} {
		w := newWorld(t)
		w.env.Herdr = &waitLog{fakeHerdr: w.h, failOn: 2}
		r := req(Want{Kind: "claude", Count: 2})
		r.OperationID, r.Prompt, r.KickoffAllReady = "op-all", "Do the bead.", all
		res, err := Spawn(context.Background(), w.env, r)
		if err != nil || res.Ready() || res.Targets[0].Status == StatusUnresolved || res.Targets[1].Status != StatusUnresolved ||
			!strings.Contains(res.Targets[1].Cleanup, "left in place") {
			t.Fatalf("all=%v: spawn = %+v %v", all, res.Targets, err)
		}
		want := 1
		if all {
			want = 0
		}
		if len(w.sent) != want || !strings.Contains(res.Targets[1].Error, "timeout") {
			t.Fatalf("all=%v: kickoffs %d (want %d), error %q", all, len(w.sent), want, res.Targets[1].Error)
		}
	}
}
