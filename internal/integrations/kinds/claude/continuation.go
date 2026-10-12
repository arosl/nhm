// Package claude holds what nhm knows about Claude Code as an agent kind.
package claude

import "strings"

// Claude Code's continuation rule (plan-v8 2.7), from ACFS's exp.4 with
// Claude Code 2.1.296:
//
//   - SessionStart carries source: startup, resume (also for --continue),
//     clear, compact, or fork (--fork-session).
//   - compact keeps the session id; PreCompact comes first and adds
//     nothing.
//   - resume reports the id it resumed.
//   - clear and fork start a new session id. The agent keeps no memory of
//     the old one, so the binding is displaced.
//   - A nested `claude -p` run from inside the agent inherits the pane's
//     HERDR_PANE_ID and sends SessionStart startup with a new id. Its
//     CLAUDE_CODE_ENTRYPOINT is "sdk-cli"; the interactive agent's is
//     "cli". The caller also checks herdr's session for the pane.

// Verdicts.
const (
	// Continue: the same occupant; the binding and its generation stay.
	Continue = "continue"
	// Displace: another occupant, or no proof of the same one.
	Displace = "displace"
	// Ignore: the event says nothing about the occupant.
	Ignore = "ignore"
)

// Event is one Claude Code hook call, as the rule needs it.
type Event struct {
	// HookEvent is the payload's hook_event_name.
	HookEvent string
	// Source is SessionStart's source.
	Source    string
	SessionID string
	// Entrypoint is CLAUDE_CODE_ENTRYPOINT in the hook's environment.
	Entrypoint string
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

// Nested reports whether the hook came from a non-interactive run inside
// the agent (claude -p, the SDK), not from the agent itself.
func Nested(e Event) bool {
	return e.Entrypoint != "" && e.Entrypoint != "cli"
}

// Classify applies the rule. bound is the binding's native session ("" if
// it has none); launchMatches is true when the hook ran in the pane that
// nhm's spawn prepared for this binding (NHM_LAUNCH_ID).
func Classify(e Event, bound string, launchMatches bool) Decision {
	if Nested(e) {
		return Decision{Verdict: Ignore, Reason: "nested run (entrypoint " + e.Entrypoint + ")"}
	}
	if e.HookEvent != "SessionStart" {
		return Decision{Verdict: Ignore, Reason: e.HookEvent + " is not a continuation cue for claude"}
	}
	same := bound != "" && bound == e.SessionID
	switch strings.ToLower(e.Source) {
	case "compact":
		if same || bound == "" {
			return Decision{Verdict: Continue, Reason: "compaction", Compacted: true, TakeSession: bound == ""}
		}
		return Decision{Verdict: Displace, Reason: "compaction of another session " + e.SessionID}
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
	case "clear":
		return Decision{Verdict: Displace, Reason: "cleared into new session " + e.SessionID}
	case "fork":
		return Decision{Verdict: Displace, Reason: "forked into new session " + e.SessionID}
	default:
		return Decision{Verdict: Displace, Reason: "unknown SessionStart source " + e.Source}
	}
}
