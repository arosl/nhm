package send

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/herdr"
)

// launchFor makes the agent in pane launch-pending until d has passed.
func (w *world) launchFor(pane string, d time.Duration) {
	w.fake.mu.Lock()
	a := w.fake.agents[pane]
	pending := true
	a.LaunchPending = &pending
	w.fake.agents[pane] = a
	w.fake.mu.Unlock()
	if d <= 0 {
		return
	}
	go func() {
		time.Sleep(d)
		w.fake.mu.Lock()
		defer w.fake.mu.Unlock()
		a := w.fake.agents[pane]
		done := false
		a.LaunchPending = &done
		w.fake.agents[pane] = a
	}()
}

// ntm's send types into a pane whose agent has just started: brenner's
// cockpit spawns, then sends at once. herdr takes no prompt while it
// launches the agent, so the send waits for the launch, within the start
// wait, and then prompts (nhm-2p6.5.12).
func TestSendWaitsForAStartingAgent(t *testing.T) {
	w := newWorld(t)
	w.env.StartWait = 5 * time.Second
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	w.launchFor("w1:p1", 400*time.Millisecond)
	start := time.Now()
	res, err := Send(ctx, w.env, base(Request{Message: "Please check your Agent Mail inbox.", WhenAny: true, OperationID: "op-start"}))
	if err != nil {
		t.Fatal(err)
	}
	if rc := res.Targets[0]; rc.Status != StatusSubmitted || w.fake.promptCount() != 1 {
		t.Fatalf("receipt %+v, prompts %d", rc, w.fake.promptCount())
	}
	if waited := time.Since(start); waited < 400*time.Millisecond {
		t.Fatalf("prompted after %s, before the launch finished", waited)
	}
}

// An agent whose launch never finishes is refused when the start wait
// runs out; nothing is typed.
func TestSendRefusesAnAgentThatNeverStarts(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	w.launchFor("w1:p1", 0)
	res, err := Send(ctx, w.env, base(Request{Message: "hi", WhenAny: true, OperationID: "op-never"}))
	if err != nil {
		t.Fatal(err)
	}
	rc := res.Targets[0]
	if rc.Status != StatusRefused || rc.ErrorCode != herdr.CodeAgentNotReady ||
		!strings.Contains(rc.Result.Reason, "launch did not finish within 300ms") || w.fake.promptCount() != 0 {
		t.Fatalf("receipt %+v, prompts %d", rc, w.fake.promptCount())
	}
}

// herdr can refuse a prompt as not ready just after the launch; it typed
// nothing, so the prompt goes again.
func TestSendRetriesAPromptHerdrRefusedAsNotReady(t *testing.T) {
	w := newWorld(t)
	w.env.StartWait = 5 * time.Second
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	w.fake.promptErr["w1:p1"] = &herdr.Error{Method: "agent.prompt", Code: herdr.CodeAgentNotReady}
	w.fake.onErr = func(pane string) { delete(w.fake.promptErr, pane) }
	res, err := Send(ctx, w.env, base(Request{Message: "hi", WhenAny: true, OperationID: "op-retry"}))
	if err != nil {
		t.Fatal(err)
	}
	if rc := res.Targets[0]; rc.Status != StatusSubmitted || w.fake.promptCount() != 2 || rc.Baseline == nil {
		t.Fatalf("receipt %+v, prompts %d", rc, w.fake.promptCount())
	}
}

// The wait comes before nhm's refusals: the input box is read once the
// agent is ready, and a "!" draft still refuses the target.
func TestSendReadsTheBoxAfterTheStart(t *testing.T) {
	w := newWorld(t)
	w.env.StartWait = 5 * time.Second
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	w.fake.screens["w1:p1"] = drafts["claude"].screen
	w.launchFor("w1:p1", 300*time.Millisecond)
	res, err := Send(ctx, w.env, base(Request{Message: "hi", WhenAny: true, OperationID: "op-draft"}))
	if err != nil {
		t.Fatal(err)
	}
	if rc := res.Targets[0]; rc.Status != StatusRefused || !strings.Contains(rc.Result.Reason, drafts["claude"].shows) || w.fake.promptCount() != 0 {
		t.Fatalf("receipt %+v, prompts %d", rc, w.fake.promptCount())
	}
}
