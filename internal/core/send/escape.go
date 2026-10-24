package send

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/robot"
)

// ErrShellEscape refuses a send, before its claim, whose prompt an agent
// would run as a shell command that no guard screens (nhm-ubi, the
// operator's ruling of 2026-10-24). ntm's send has no such refusal. Send
// checks by the binding's kind; the delivery gate refuses a pane where
// herdr reports an agent of another kind.
var ErrShellEscape = errors.New("refused before any prompt")

// ShellEscapeError is an ErrShellEscape refusal, with the keys of every
// target the send selected (the robot answer's targets, as ntm's).
type ShellEscapeError struct {
	msg     string
	Targets []string
}

func (e *ShellEscapeError) Error() string { return e.msg }
func (e *ShellEscapeError) Unwrap() error { return ErrShellEscape }

// controlChar finds a control character that a prompt may not hold: any
// but tab, newline and carriage return. herdr types a prompt as one
// bracketed paste and passes control characters through; an ESC [201~
// ends the paste, so the text after it reaches the agent as typed keys,
// and Codex 0.164.0 and pi 0.8.0 ran a "!" after it or after other
// control characters (the sandbox, nhm-ubi). A prompt to a kind with a
// shell escape is refused with one; a kind proved to have none takes it
// (the operator's ruling, 2026-10-24; nhm-gb0).
func controlChar(r rune) bool { return unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r' }

// shellReason says why an agent of kind a would run payload as a shell
// command that no guard screens, or "" when it would not: what follows
// "the prompt for <agent> (<kind>)".
func shellReason(payload string, a kinds.Adapter) string {
	esc := a.Escape()
	if esc == "" {
		return ""
	}
	if i := strings.IndexFunc(payload, controlChar); i >= 0 {
		return fmt.Sprintf("holds the control character %U: herdr types the prompt as one paste and passes it through, "+
			"so the text after it can reach the agent as typed keys, which can submit a shell command that no guard screens; "+
			"remove control characters (ESC, ANSI colours) but tab, newline and carriage return", []rune(payload[i:])[0])
	}
	p := a.ShellPrefix(payload)
	switch {
	case p == "":
		return ""
	case p == kinds.DefaultShellEscape && a.EscapeIsDefault():
		return fmt.Sprintf("starts with %q (nhm skips white space before it): nhm takes %q as the shell escape of every kind "+
			"that the sandbox has not proved to have none, because such a prompt may run as a shell command that no guard "+
			"such as dcg screens; start the prompt with other text", p, p)
	}
	return fmt.Sprintf("starts with %q (nhm skips white space before it): %q is a shell escape of each agent's kind, "+
		"which can run such text as a command at once, with no approval question, so no guard such as dcg screens it; "+
		"start the prompt with other text", p, p)
}

// refuseShell refuses the send when any target's prompt would run as a
// shell command: one error that names each such target, by reason.
func refuseShell(targets []Target, payloads []string) error {
	var order []string
	who := map[string][]string{}
	keys := make([]string, 0, len(targets))
	for i, t := range targets {
		keys = append(keys, t.Key)
		why := shellReason(payloads[i], kinds.Lookup(t.Binding.Kind))
		if why == "" {
			continue
		}
		if _, ok := who[why]; !ok {
			order = append(order, why)
		}
		name := t.Binding.MailName
		if name == "" {
			name = t.Key
		}
		who[why] = append(who[why], fmt.Sprintf("%s (%s)", name, t.Binding.Kind))
	}
	if len(order) == 0 {
		return nil
	}
	parts := make([]string, 0, len(order))
	for _, why := range order {
		parts = append(parts, fmt.Sprintf("the prompt for %s %s", strings.Join(who[why], ", "), why))
	}
	return &ShellEscapeError{msg: ErrShellEscape.Error() + ": " + strings.Join(parts, "; "), Targets: keys}
}

// CheckShell is Send's check for a message that goes as given, with no
// template or enrichment, to the agents of these bindings: robot
// interrupt's --msg checks it before it interrupts them.
func CheckShell(msg string, targets []bindings.Binding) error {
	if msg == "" {
		return nil
	}
	ts := make([]Target, len(targets))
	payloads := make([]string, len(targets))
	for i, b := range targets {
		ts[i], payloads[i] = Target{Binding: b, Key: b.PaneID}, msg
	}
	return refuseShell(ts, payloads)
}

// GuardHint is the robot hint of an ErrShellEscape refusal.
const GuardHint = "nhm types no prompt that the agent would run as a shell command: start the message with other text, " +
	"and leave out control characters but tab, newline and carriage return"

// GuardOutput is the --robot-send answer for an ErrShellEscape refusal,
// in the shape of ntm's for a send that its dcg guard refused before the
// claim: blocked, DESTRUCTIVE_COMMAND_BLOCKED and a "guard" failure,
// with the selected targets, the preview and the enrichment reports.
func GuardOutput(res Result, err error, dryRun bool, format string, now time.Time) robot.SendOutput {
	out := robot.SendOutput{RobotResponse: robot.NewResponse(false, format, now), Session: res.Session, SentAt: now.UTC(),
		Blocked: true, Redaction: redactionInfo(res), Warnings: res.Warnings, Targets: []string{}, Successful: []string{},
		Failed: []robot.SendError{{Pane: "guard", Error: err.Error()}}, MessagePreview: res.Preview, DryRun: dryRun,
		CASSInjection: cassInfo(res), MemoryInjection: memoryInfo(res)}
	var se *ShellEscapeError
	if errors.As(err, &se) && se.Targets != nil {
		out.Targets = se.Targets
	}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	out.Error, out.ErrorCode, out.Hint = err.Error(), robot.ErrCodeDestructiveCommandBlocked, GuardHint
	return out
}
