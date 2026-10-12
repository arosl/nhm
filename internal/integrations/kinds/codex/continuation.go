// Package codex holds what nhm knows about the Codex CLI as an agent kind.
package codex

import "strings"

// Codex's continuation rule (plan-v8 2.7), from ACFS's exp.3 with
// codex-cli 0.162.1 started with --no-daemon:
//
//   - Hooks run in the pane's codex process, with HERDR_PANE_ID. In the
//     daemon mode they run in the shared app-server and name the wrong
//     pane; nhm spawns Codex with --no-daemon.
//   - SessionStart fires at the first turn, not at launch, with source
//     startup (a new thread) or resume (`codex resume <id>`, the same id).
//     Until that turn herdr has no session for the pane.
//   - /compact fires PreCompact and PostCompact with the same session id.
//     No SessionStart follows.

// Verdicts.
const (
	// Continue: the same occupant; the binding and its generation stay.
	Continue = "continue"
	// Displace: another occupant, or no proof of the same one.
	Displace = "displace"
	// Ignore: the event says nothing about the occupant.
	Ignore = "ignore"
)

// Event is one Codex hook call, as the rule needs it.
type Event struct {
	// HookEvent is the payload's hook_event_name.
	HookEvent string
	// Source is SessionStart's source.
	Source    string
	SessionID string
}

// Decision is what the rule says about an event.
type Decision struct {
	Verdict   string
	Reason    string
	Compacted bool
	Resumed   bool
	// TakeSession: record the event's session on the binding (the
	// binding had none).
	TakeSession bool
}

// Classify applies the rule. bound is the binding's native session ("" if
// it has none); launchMatches is true when the hook ran in the pane that
// nhm's spawn prepared for this binding (NHM_LAUNCH_ID).
func Classify(e Event, bound string, launchMatches bool) Decision {
	same := bound != "" && bound == e.SessionID
	switch e.HookEvent {
	case "PreCompact":
		return Decision{Verdict: Ignore, Reason: "PostCompact is the compaction cue"}
	case "PostCompact":
		if same || (bound == "" && launchMatches) {
			return Decision{Verdict: Continue, Reason: "compaction", Compacted: true, TakeSession: bound == ""}
		}
		if bound == "" {
			return Decision{Verdict: Ignore, Reason: "compaction of a session nhm never saw"}
		}
		return Decision{Verdict: Displace, Reason: "compaction of another session " + e.SessionID}
	case "SessionStart":
	default:
		return Decision{Verdict: Ignore, Reason: e.HookEvent + " is not a continuation cue for codex"}
	}
	switch strings.ToLower(e.Source) {
	case "resume":
		if same || bound == "" {
			return Decision{Verdict: Continue, Reason: "resume of " + e.SessionID, Resumed: true, TakeSession: bound == ""}
		}
		return Decision{Verdict: Displace, Reason: "resume of another session " + e.SessionID}
	case "startup":
		if same {
			return Decision{Verdict: Continue, Reason: "the bound session started again"}
		}
		if bound == "" && launchMatches {
			return Decision{Verdict: Continue, Reason: "first session of the spawned agent", TakeSession: true}
		}
		return Decision{Verdict: Displace, Reason: "new session " + e.SessionID}
	default:
		return Decision{Verdict: Displace, Reason: "unknown SessionStart source " + e.Source}
	}
}
