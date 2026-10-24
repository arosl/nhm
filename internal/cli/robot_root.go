package cli

import (
	"errors"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/arosl/nhm/internal/robot"
)

// ntm's robot flags are flags of its root, so its root flags work in a
// robot call too: `ntm --json --robot-status`. nhm's robot flags have
// their own grammar (runRobot); robotCall finds a call whose root flags
// come in front of the first --robot-* flag. After it, the robot flag
// sets do not take them yet (nhm-2p6.1.10).

// robotCall splits a call whose first word after ntm's root flags is a
// --robot-* flag into those root flags (lead) and the robot call (rest).
// ok is false for any other call. A root flag that takes a value takes
// the next word, as pflag does.
func robotCall(args []string) (lead, rest []string, ok bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--robot-") {
			return args[:i], args[i:], true
		}
		name, _, hasValue := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		f, isRoot := findRootFlag(name)
		if !strings.HasPrefix(a, "--") || !isRoot {
			return nil, nil, false
		}
		if f.value && !hasValue {
			i++ // its value
		}
	}
	return nil, nil, false
}

// runRobotCall applies the root flags in front of a robot call, as
// rootOptions.apply does for a verb, then runs the call. --config sets
// NHM_CONFIG for it; --json and --no-color change nothing that it writes;
// --profile-startup prints nothing, as ntm prints no profile after a
// machine call; --redact and --allow-secret are not built (ntm's
// NOT_IMPLEMENTED). remoteRobotCall takes --ssh first.
func runRobotCall(lead, rest []string, stdout, stderr io.Writer) int {
	now := time.Now()
	o := &rootOptions{set: map[string]bool{}, args: append(append([]string{}, lead...), rest...), stdout: stdout, stderr: stderr}
	defer func() {
		for i := len(o.restore) - 1; i >= 0; i-- {
			o.restore[i]()
		}
	}()
	for i := 0; i < len(lead); i++ {
		name, value, hasValue := strings.Cut(strings.TrimPrefix(lead[i], "--"), "=")
		f, _ := findRootFlag(name)
		if f.value && !hasValue {
			i++
			value, hasValue = lead[i], true
		}
		if err := o.setFlag(f, value, hasValue); err != nil {
			return robotFail(stdout, robot.ErrCodeInvalidFlag, err.Error(), "nhm --robot-capabilities lists the robot flags", "json", robotError, now)
		}
	}
	o.profileStartup = false
	if err := o.apply(&cobra.Command{Use: "robot", DisableFlagParsing: true}); err != nil {
		var ve verbExit
		var nb notBuiltError
		switch {
		case errors.As(err, &ve):
			return ve.code
		case errors.As(err, &nb):
			return robotFail(stdout, robot.ErrCodeNotImplemented, err.Error(), "its row in compat/ntm-contract.md names the bead", "json", robotNotImplemented, now)
		default:
			return robotFail(stdout, robot.ErrCodeInvalidFlag, err.Error(), "nhm --robot-capabilities lists the robot flags", "json", robotError, now)
		}
	}
	return runRobot(rest, stdout, stderr)
}
