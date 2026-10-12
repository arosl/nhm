package guards

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
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

// ScreenArgv screens nhm's own launch or resume argv against DCG's policy
// (plan-v8 2.2 step 3, 2.14) before nhm hands it to herdr: `dcg test
// --stdin --robot --dialect posix`, the dialect of the Bash hook. The
// command line reaches dcg on stdin, never on its own command line.
func ScreenArgv(ctx context.Context, argv []string) (Verdict, error) {
	if len(argv) == 0 {
		return Verdict{}, errors.New("guards: empty argv")
	}
	path, err := exec.LookPath(DCGPath)
	if err != nil {
		return Verdict{}, ErrNoDCG
	}
	line := ShellJoin(argv)
	cmd := exec.CommandContext(ctx, path, "test", "--stdin", "--robot", "--dialect", "posix")
	cmd.Stdin = strings.NewReader(line)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	runErr := cmd.Run()
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
