package send

import (
	"errors"
	"fmt"
	"net"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

// Gate is the delivery gate's answer for one target (plan-v8 2.3).
type Gate string

const (
	GateReady   Gate = "ready"
	GateBusy    Gate = "busy"
	GateBlocked Gate = "blocked"
	GateUnsafe  Gate = "unsafe"
	GateUnknown Gate = "unknown"
)

// DeliveryGate reads herdr's state of the agent in the binding's pane.
// herdr's state comes first; the kind adapters' readiness evidence
// (nhm.kinds) refines it later. Terminal recognition is never used here.
func DeliveryGate(b bindings.Binding, a *herdr.AgentInfo) (Gate, string) {
	switch {
	case a == nil:
		return GateUnknown, "the pane has no agent"
	case a.Agent != "" && b.Kind != "" && a.Agent != b.Kind:
		return GateUnsafe, fmt.Sprintf("the pane holds a %s agent; the binding is %s", a.Agent, b.Kind)
	case a.LaunchPending != nil && *a.LaunchPending:
		return GateBusy, "the agent's launch is pending"
	}
	switch a.AgentStatus {
	case herdr.StatusIdle, herdr.StatusDone:
		return GateReady, ""
	case herdr.StatusWorking:
		return GateBusy, "the agent is working"
	case herdr.StatusBlocked:
		return GateBlocked, "the agent waits at a dialog; answer it in its pane"
	}
	return GateUnknown, fmt.Sprintf("herdr reports the state %q", a.AgentStatus)
}

// Admit applies the gate: blocked and unsafe are always refused; busy
// and unknown only with --when any or --force.
func Admit(g Gate, whenAny, force bool) bool {
	switch g {
	case GateReady:
		return true
	case GateBusy, GateUnknown:
		return whenAny || force
	}
	return false
}

// Submission statuses of a receipt (plan-v8 2.3, 2.12).
const (
	StatusPending           = "pending"
	StatusAttemptStarted    = "attempt_started"
	StatusSubmitted         = "submitted"
	StatusPossiblySubmitted = "possibly_submitted"
	StatusRefused           = "refused"
	StatusFailed            = "failed"
)

// refusedBeforeTyping are herdr's errors that mean agent.prompt typed
// nothing.
var refusedBeforeTyping = map[string]bool{
	herdr.CodeAgentNotReady:      true,
	herdr.CodeAgentNotFound:      true,
	herdr.CodeAgentNotRunning:    true,
	herdr.CodeAgentLaunchPending: true,
	herdr.CodePaneNotFound:       true,
	herdr.CodeInvalidParams:      true,
	herdr.CodeInvalidRequest:     true,
	"agent_blocked":              true,
}

// classify maps agent.prompt's answer to a submission status. Only an
// error that proves nothing was typed is refused or failed; a timeout, a
// stall or a lost reply is possibly_submitted (exp.3: a stalled prompt
// often ran).
func classify(err error) (status, code string) {
	if err == nil {
		return StatusSubmitted, ""
	}
	if code := herdr.ErrorCode(err); code != "" {
		if refusedBeforeTyping[code] {
			return StatusRefused, code
		}
		return StatusPossiblySubmitted, code
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		// The connection never opened: nothing reached herdr.
		return StatusFailed, "connect"
	}
	return StatusPossiblySubmitted, "lost_reply"
}
