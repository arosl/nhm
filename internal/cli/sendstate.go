package cli

import (
	"fmt"
	"slices"

	"github.com/arosl/nhm/internal/herdr"
)

// stateBetween is send's --state alias for an agent between turns:
// herdr's idle or done. An agent that finished its turn and was looked
// at is idle, so --state done alone misses it (nhm-96x).
const stateBetween = "between"

// stateValues is --state's help text.
const stateValues = "idle, done, working, blocked, or between (idle or done)"

// parseStates reads send's --state values: herdr's states, and the
// alias between for idle and done. Each state is kept once, in the
// order given. Like --state done, between leaves out the kinds whose
// code_review is false unless they are named.
func parseStates(values []string) ([]herdr.AgentStatus, error) {
	var out []herdr.AgentStatus
	add := func(st herdr.AgentStatus) {
		if !slices.Contains(out, st) {
			out = append(out, st)
		}
	}
	for _, v := range values {
		switch st := herdr.AgentStatus(v); {
		case v == stateBetween:
			add(herdr.StatusIdle)
			add(herdr.StatusDone)
		case st == herdr.StatusIdle, st == herdr.StatusDone, st == herdr.StatusWorking, st == herdr.StatusBlocked:
			add(st)
		default:
			return nil, fmt.Errorf("--state is %s, not %q", stateValues, v)
		}
	}
	return out, nil
}
