package wait

import (
	"context"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

// ntm's wait on herdr's states (nhm-2p6.5.2). ntm polls the session: on
// each poll it lists the panes again, reads each agent's state from its
// screen, and checks the conditions over that one poll: every agent must
// meet them, or --count of them with --any. herdr reports one state per
// agent, from the agent's own hooks: idle or done at its prompt, working,
// blocked at a dialog. ntm's conditions on those states:
//   - idle: idle or done;
//   - complete: the same. ntm adds 5 s without output, against a screen
//     read in the middle of a turn; herdr's idle or done comes at the end
//     of the turn;
//   - generating: working;
//   - healthy: any state but blocked, ntm's stall; herdr has no error
//     state;
//   - done, nhm's own: herdr's done alone.

// NTMMeets reports whether an agent in state s meets every condition of
// conds. The names are checked before.
func NTMMeets(conds []string, s herdr.AgentStatus) bool {
	for _, c := range conds {
		var ok bool
		switch c {
		case "idle", "complete":
			ok = s == herdr.StatusIdle || s == herdr.StatusDone
		case "done":
			ok = s == herdr.StatusDone
		case "generating":
			ok = s == herdr.StatusWorking
		case "healthy":
			ok = s != herdr.StatusBlocked
		}
		if !ok {
			return false
		}
	}
	return true
}

// NTMRequest is ntm's wait: its conditions, --any with --count, and
// --exit-on-error. Poll is the pause between two polls.
type NTMRequest struct {
	Conds       []string
	Any         bool
	Count       int
	ExitOnError bool
	Poll        time.Duration
}

// NTMAgent is an agent that met the conditions in the last poll.
type NTMAgent struct {
	PaneID string
	State  herdr.AgentStatus
}

// NTMResult is how ntm's wait ended: Met holds the agents that met the
// conditions in that poll; Lost is the pane of an agent that can no
// longer meet them (ExitOnError); NoPanes: no agent is left.
type NTMResult struct {
	Met     []NTMAgent
	Lost    string
	NoPanes bool
}

// minNTMPoll keeps a zero --poll from a busy loop on herdr's socket.
const minNTMPoll = 10 * time.Millisecond

// NTMWait is ntm's wait loop. Each poll selects the agents again, as ntm
// lists the panes again, and reads each of them. An agent that is gone
// from its pane, or whose pane now holds another kind, drops out, as a
// closed pane drops out of ntm's poll; with ExitOnError it ends the wait
// (herdr has no error state). An agent that herdr cannot read otherwise
// stays out of that poll's check, as ntm skips a pane that it cannot
// classify. With no agent left the wait ends with NoPanes. NTMWait
// returns ctx's error when ctx ends first (the timeout), and sel's error.
func NTMWait(ctx context.Context, h Herdr, sel func(context.Context) ([]bindings.Binding, error), req NTMRequest) (NTMResult, error) {
	for {
		if err := ctx.Err(); err != nil {
			return NTMResult{}, err
		}
		list, err := sel(ctx)
		if err != nil {
			return NTMResult{}, err
		}
		var met []NTMAgent
		left, read := 0, 0
		for _, b := range list {
			a, err := h.AgentGet(ctx, b.PaneID)
			if gone(err) || err == nil && b.Kind != "" && a.Agent != "" && a.Agent != b.Kind {
				if req.ExitOnError {
					return NTMResult{Lost: b.PaneID}, nil
				}
				continue
			}
			left++
			if err != nil {
				if ctx.Err() != nil {
					return NTMResult{}, ctx.Err()
				}
				continue
			}
			read++
			if NTMMeets(req.Conds, a.AgentStatus) {
				met = append(met, NTMAgent{PaneID: b.PaneID, State: a.AgentStatus})
			}
		}
		if left == 0 {
			return NTMResult{NoPanes: true}, nil
		}
		if read > 0 && (req.Any && len(met) >= req.Count || !req.Any && len(met) == read) {
			return NTMResult{Met: met}, nil
		}
		timer := time.NewTimer(max(req.Poll, minNTMPoll))
		select {
		case <-ctx.Done():
			timer.Stop()
			return NTMResult{}, ctx.Err()
		case <-timer.C:
		}
	}
}

// gone reports herdr's answer for a pane that holds no agent now.
func gone(err error) bool {
	switch herdr.ErrorCode(err) {
	case herdr.CodeAgentNotRunning, herdr.CodeAgentNotFound, herdr.CodePaneNotFound:
		return true
	}
	return false
}
