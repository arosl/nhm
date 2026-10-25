package send

import (
	"context"
	"time"

	"github.com/arosl/nhm/internal/herdr"
)

// ntm's send types into a pane whose agent has just started (nhm-2p6.5.12;
// AGENTS.md rule 4). herdr takes no prompt while it still launches an
// agent: agent.prompt answers agent_not_ready or agent_launch_pending,
// and nothing is typed. So a send to such an agent waits until herdr has
// it ready, within the start wait, and refuses only when that runs out.
// A spawn without a prompt returns before its agents are ready, as
// ntm's does, so a send right after it meets them starting (brenner's
// cockpit: spawn, then `nhm send THREAD --all`).

// DefaultStartWait is the start wait: the spawn's ready timeout.
const DefaultStartWait = 60 * time.Second

// startPoll is the pause between two reads of a starting agent.
const startPoll = 250 * time.Millisecond

func (e Env) startWait() time.Duration {
	if e.StartWait > 0 {
		return e.StartWait
	}
	return DefaultStartWait
}

// launching reports an agent whose launch herdr has not finished.
func launching(a herdr.AgentInfo) bool {
	return a.LaunchPending != nil && *a.LaunchPending
}

// notStarted reports agent.prompt's refusal of an agent that herdr still
// launches; it typed nothing.
func notStarted(err error) bool {
	code := herdr.ErrorCode(err)
	return code == herdr.CodeAgentNotReady || code == herdr.CodeAgentLaunchPending
}

// awaitStart reads the agent in the pane until its launch is done, or
// until deadline. It returns the last read; started is false when the
// deadline came first.
func awaitStart(ctx context.Context, env Env, paneID string, deadline time.Time) (a herdr.AgentInfo, started bool, err error) {
	for {
		a, err = env.Herdr.AgentGet(ctx, paneID)
		if err == nil && !launching(a) {
			return a, true, nil
		}
		if err != nil && !notStarted(err) {
			return a, true, err // gone, or herdr cannot be read: the caller decides
		}
		if !env.now().Add(startPoll).Before(deadline) {
			return a, false, err
		}
		timer := time.NewTimer(startPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return a, false, ctx.Err()
		case <-timer.C:
		}
	}
}
