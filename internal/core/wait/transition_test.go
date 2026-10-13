package wait

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/herdr"
)

// The robot form's transition (ntm's --transition, nhm-hir): an agent in
// a target state at the start must leave the target states and come back.
// The first read is the start.
func TestTransitionNeedsTheAgentToLeaveAndComeBack(t *testing.T) {
	idle, done, working, blocked := herdr.StatusIdle, herdr.StatusDone, herdr.StatusWorking, herdr.StatusBlocked
	cases := []struct {
		name   string
		until  Until
		steps  []herdr.AgentInfo
		want   Outcome
		reason string
	}{
		{"a read shows the turn", UntilWaiting,
			[]herdr.AgentInfo{agent(idle, 5, 4), agent(idle, 5, 4), agent(working, 6, 4), agent(idle, 7, 7)}, Idle, ""},
		{"a turn between two reads", UntilWaiting,
			[]herdr.AgentInfo{agent(idle, 5, 4), agent(done, 7, 7)}, Done, ""},
		{"not in the target at the start", UntilWaiting,
			[]herdr.AgentInfo{agent(working, 5, 4), agent(idle, 6, 6)}, Idle, ""},
		{"done to idle is no turn", UntilWaiting,
			[]herdr.AgentInfo{agent(done, 5, 5), agent(idle, 6, 5)}, Timeout, "did not leave the target state"},
		{"a turn does not leave unblocked", UntilUnblocked,
			[]herdr.AgentInfo{agent(idle, 5, 4), agent(idle, 7, 7)}, Timeout, "did not leave the target state"},
		{"blocked leaves unblocked", UntilUnblocked,
			[]herdr.AgentInfo{agent(idle, 5, 4), agent(blocked, 6, 4), agent(idle, 7, 4)}, Healthy, ""},
		{"a turn leaves generating", UntilGenerating,
			[]herdr.AgentInfo{agent(working, 5, 4), agent(working, 8, 7)}, Generating, ""},
	}
	for _, c := range cases {
		h := script(c.steps...)
		r := runOne(t, h, &fakeBindings{b: bound}, Target{Binding: bound},
			Request{Until: c.until, Transition: true}, 200*time.Millisecond)
		if r.Outcome != c.want || !strings.Contains(r.Reason, c.reason) {
			t.Errorf("%s: %s (%s), want %s", c.name, r.Outcome, r.Reason, c.want)
		}
		// herdr's agent.wait returns at once for an agent already in
		// the target state, so a transition sleeps on the events.
		if h.waits != 0 {
			t.Errorf("%s: %d agent.wait calls", c.name, h.waits)
		}
	}
}

// Without a transition, an agent already at its prompt meets the robot
// form's idle, done included; herdr's unknown is healthy, as ntm's
// UNKNOWN is.
func TestRobotConditionsWithoutTransition(t *testing.T) {
	for _, c := range []struct {
		until Until
		st    herdr.AgentStatus
		want  Outcome
	}{
		{UntilWaiting, herdr.StatusDone, Done},
		{UntilWaiting, herdr.StatusIdle, Idle},
		{UntilUnblocked, herdr.StatusUnknown, Healthy},
		{UntilUnblocked, herdr.StatusBlocked, Timeout},
		{UntilWaiting, herdr.StatusWorking, Timeout},
	} {
		r := runOne(t, script(agent(c.st, 5, 4)), &fakeBindings{b: bound}, Target{Binding: bound},
			Request{Until: c.until}, 50*time.Millisecond)
		if r.Outcome != c.want {
			t.Errorf("%s at %s: %s (%s), want %s", c.until, c.st, r.Outcome, r.Reason, c.want)
		}
	}
}

// paneScripts gives each pane its own script.
type paneScripts map[string]*fakeHerdr

func (p paneScripts) AgentGet(ctx context.Context, pane string) (herdr.AgentInfo, error) {
	return p[pane].AgentGet(ctx, pane)
}

func (p paneScripts) AgentWait(ctx context.Context, w herdr.AgentWaitParams) (herdr.AgentInfo, error) {
	return p[w.Target].AgentWait(ctx, w)
}

func (p paneScripts) AgentSendKeys(ctx context.Context, pane string, keys ...string) error {
	return p[pane].AgentSendKeys(ctx, pane, keys...)
}

// ntm's --exit-on-error: the first target that cannot meet the condition
// ends the wait; without it, the others wait to the deadline.
func TestFailFastEndsTheOtherWaits(t *testing.T) {
	gone := &fakeHerdr{steps: []step{{err: &herdr.Error{Method: "agent.get", Code: herdr.CodeAgentNotFound}}}}
	for _, failFast := range []bool{true, false} {
		a, b := bound, bound
		b.ID, b.PaneID, b.Slot = "bnd-2", "w1:p2", "0.2"
		busy := agent(herdr.StatusWorking, 5, 4)
		busy.PaneID = b.PaneID
		h := paneScripts{"w1:p1": gone, "w1:p2": script(busy)}
		env := Env{Herdr: h, Bindings: multiBindings{a.ID: a, b.ID: b}}
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		start := time.Now()
		rs := All(ctx, env, []Target{{Binding: a}, {Binding: b}}, Request{Until: UntilWaiting, FailFast: failFast, Poll: time.Millisecond}, 0)
		cancel()
		want := Timeout
		if failFast {
			want = Pending
		}
		if rs[0].Outcome != ContinuityLost || rs[1].Outcome != want {
			t.Errorf("fail fast %v: %s, %s (%s), want continuity_lost, %s", failFast, rs[0].Outcome, rs[1].Outcome, rs[1].Reason, want)
		}
		if failFast && time.Since(start) > 100*time.Millisecond {
			t.Errorf("fail fast waited %s", time.Since(start))
		}
	}
}
