package guards

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Screen decisions.
const (
	Allow = "allow"
	Deny  = "deny"
)

// Verdict is DCG's answer for one command line.
type Verdict struct {
	Command  string `json:"command"`
	Decision string `json:"decision"`
	RuleID   string `json:"rule_id,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// ErrNoDCG means dcg is not installed: nhm cannot screen, and the caller
// refuses (the guard is missing anyway).
var ErrNoDCG = errors.New("guards: dcg is not in PATH")

// DCGPath is the dcg binary; a variable so tests can replace it.
var DCGPath = "dcg"

// ScreenTimeout bounds one screen (nhm-eid, the operator's ruling of
// 2026-10-23). A dcg that gives no answer in time is an error, so the
// launch is refused: nhm never launches an argv that was not screened.
var ScreenTimeout = 30 * time.Second

// ScreenArgv screens nhm's own launch or resume argv against DCG's policy
// (plan-v8 2.2 step 3, 2.14) before nhm hands it to herdr: `dcg test
// --stdin --robot --dialect posix`, the dialect of the Bash hook. The
// command line reaches dcg on stdin, never on its own command line. dcg
// has ScreenTimeout to answer.
func ScreenArgv(ctx context.Context, argv []string) (Verdict, error) {
	if len(argv) == 0 {
		return Verdict{}, errors.New("guards: empty argv")
	}
	path, err := exec.LookPath(DCGPath)
	if err != nil {
		return Verdict{}, ErrNoDCG
	}
	line := ShellJoin(argv)
	ctx, cancel := context.WithTimeout(ctx, ScreenTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "test", "--stdin", "--robot", "--dialect", "posix")
	// A child of dcg (or of a wrapper script) that holds stdout open must
	// not hold the screen (nhm-fty): WaitDelay after ctx ends, or after
	// dcg exits, the pipes are closed.
	cmd.WaitDelay = time.Second
	cmd.Stdin = strings.NewReader(line)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	runErr := cmd.Run()
	if runErr != nil && ctx.Err() != nil {
		// dcg was killed at the limit (or the caller gave up): what it
		// wrote, even an allow, is no answer (nhm-eid).
		return Verdict{}, fmt.Errorf("guards: dcg test gave no answer (limit %s): %w", ScreenTimeout, ctx.Err())
	}
	var v Verdict
	if err := json.Unmarshal(out.Bytes(), &v); err != nil || v.Decision == "" {
		if runErr != nil {
			return Verdict{}, fmt.Errorf("guards: dcg test: %v: %s", runErr, strings.TrimSpace(errOut.String()))
		}
		return Verdict{}, fmt.Errorf("guards: dcg test answered no decision: %.200s", out.String())
	}
	if v.Decision != Allow {
		// Anything but an explicit allow (deny, warn, indeterminate) stops
		// the launch.
		if v.Decision != Deny {
			v.Reason = strings.TrimSpace(v.Decision + ": " + v.Reason)
			v.Decision = Deny
		}
	}
	v.Command = line
	return v, nil
}

// ShellJoin quotes argv for a POSIX shell: words of safe characters stay
// bare; others go in single quotes.
func ShellJoin(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && strings.Trim(a, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./=:,@%+") == "" {
			parts[i] = a
			continue
		}
		parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(parts, " ")
}
