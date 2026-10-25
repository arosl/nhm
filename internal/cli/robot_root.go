package cli

import (
	"errors"
	"flag"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/arosl/nhm/internal/robot"
)

// ntm's robot flags are flags of its root, so its root flags work in a
// robot call too, in front of the robot flag or after it: `ntm --json
// --robot-status`, `ntm --robot-status --json`. nhm's robot flags have
// their own grammar (runRobot); robotCall finds a call whose root flags
// come in front of the first --robot-* flag, and each robot flag set
// takes the root flags after it (newRobotFlagSet).

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

// runRobotCall applies the root flags in front of a robot call, then runs
// the call. remoteRobotCall takes --ssh first.
func runRobotCall(lead, rest []string, stdout, stderr io.Writer) int {
	now := time.Now()
	o := &rootOptions{set: map[string]bool{}, args: append(append([]string{}, lead...), rest...), stdout: stdout, stderr: stderr}
	defer o.undo()
	for i := 0; i < len(lead); i++ {
		name, value, hasValue := strings.Cut(strings.TrimPrefix(lead[i], "--"), "=")
		f, _ := findRootFlag(name)
		if f.value && !hasValue {
			i++
			value, hasValue = lead[i], true
		}
		if err := o.setFlag(f, value, hasValue); err != nil {
			return robotFail(stdout, robot.ErrCodeInvalidFlag, err.Error(), "nhm --robot-capabilities lists the robot flags", "auto", robotError, now)
		}
	}
	if code, done := applyRootFlags(o, now); done {
		return code
	}
	return runRobot(rest, stdout, stderr)
}

// robotOptions is a robot call's root flags and its global robot
// modifiers (robot_modifiers.go), which in ntm are root flags too.
type robotOptions struct {
	*rootOptions
	// format is --robot-format or --robot-output-format as given, ""
	// when neither is; applyRobotRoot replaces it with the call's
	// resolved format, which the call's output names.
	format string
	// verbosity is --robot-verbosity as given, "" when it is not.
	verbosity string
	// robotLimit, limit and offset are --robot-limit, --limit and
	// --robot-offset or --offset (one value) as given; pagination reads
	// them for the calls that page.
	robotLimit, limit, offset int
	// noConfig leaves the config out of the format and the verbosity:
	// ntm's help, version and capabilities load no config.
	noConfig bool
}

// newRobotFlagSet is a robot call's flag set, with ntm's root flags and
// global robot modifiers in it, for those after the --robot-* flag. Go's
// flag reads the word after a robot flag that takes a value as its value,
// as pflag does, so `--msg --json` keeps --json as the message. After the
// parse, applyRobotRoot applies the root flags that the set took and
// resolves the modifiers, and o.undo ends what the flags set up. A --ssh
// H never gets here: remoteRobotCall took it, so stderr is not needed; an
// empty --ssh is none, as in ntm.
func newRobotFlagSet(args []string, stdout io.Writer) (*flag.FlagSet, *robotOptions) {
	fs := flag.NewFlagSet("nhm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &robotOptions{rootOptions: &rootOptions{set: map[string]bool{}, args: args, stdout: stdout, stderr: io.Discard}}
	for _, f := range rootFlags {
		set := func(value string) error { return o.setFlag(f, value, true) }
		if f.value {
			fs.Func(f.name, "", set)
		} else {
			fs.BoolFunc(f.name, "", set)
		}
	}
	setFormat := func(value string) error { o.format = value; return nil }
	fs.Func("robot-format", "", setFormat)
	fs.Func("robot-output-format", "", setFormat)
	fs.Func("robot-verbosity", "", func(value string) error { o.verbosity = value; return nil })
	fs.IntVar(&o.robotLimit, "robot-limit", 0, "")
	fs.IntVar(&o.limit, "limit", 0, "")
	fs.IntVar(&o.offset, "robot-offset", 0, "")
	fs.IntVar(&o.offset, "offset", 0, "")
	return fs, o
}

// applyRobotRoot applies a robot call's root flags (applyRootFlags), then
// resolves its format and its verbosity, in ntm's order, before any
// dispatch: a value ntm does not know is ntm's INVALID_FLAG. The
// verbosity shapes the call's successful answer (robotWrite) until
// o.undo. done is true when the call ends here, with code.
func applyRobotRoot(o *robotOptions, now time.Time) (code int, done bool) {
	if code, done := applyRootFlags(o.rootOptions, now); done {
		return code, true
	}
	format, err := resolveRobotFormat(o.format, !o.noConfig)
	switch {
	case errors.Is(err, errRobotTOON):
		return robotFail(o.stdout, robot.ErrCodeInvalidFlag, err.Error(), "use --robot-format=json", "auto", robotError, now), true
	case err != nil:
		return robotFail(o.stdout, robot.ErrCodeInvalidFlag, err.Error(), robotFormatHint, "auto", robotError, now), true
	}
	o.format = format
	verbosity, err := resolveRobotVerbosity(o.verbosity, !o.noConfig)
	if err != nil {
		return robotFail(o.stdout, robot.ErrCodeInvalidFlag, err.Error(), robotVerbosityHint, format, robotError, now), true
	}
	prev := robotOut
	robotOut = robotOutput{format: format, verbosity: verbosity}
	o.restore = append(o.restore, func() { robotOut = prev })
	return 0, false
}

// applyRootFlags applies a robot call's root flags, as rootOptions.apply
// does for a verb. --config sets NHM_CONFIG for the call; --json and
// --no-color change nothing that it writes; --profile-startup prints
// nothing, as ntm prints no profile after a machine call; --redact and
// --allow-secret are not built (ntm's NOT_IMPLEMENTED). done is true when
// the call ends here, with code.
func applyRootFlags(o *rootOptions, now time.Time) (code int, done bool) {
	o.profileStartup = false
	err := o.apply(&cobra.Command{Use: "robot", DisableFlagParsing: true})
	var ve verbExit
	var nb notBuiltError
	switch {
	case err == nil:
		return 0, false
	case errors.As(err, &ve):
		return ve.code, true
	case errors.As(err, &nb):
		return robotFail(o.stdout, robot.ErrCodeNotImplemented, err.Error(), "its row in compat/ntm-contract.md names the bead", "auto", robotNotImplemented, now), true
	default:
		return robotFail(o.stdout, robot.ErrCodeInvalidFlag, err.Error(), "nhm --robot-capabilities lists the robot flags", "auto", robotError, now), true
	}
}

// undo ends what the root flags set up for the call, the last first.
func (o *rootOptions) undo() {
	for i := len(o.restore) - 1; i >= 0; i-- {
		o.restore[i]()
	}
	o.restore = nil
}

// robotConfigFirst sets NHM_CONFIG from a --config F after the robot flag
// before a robot flag set that reads the kinds is built, and returns what
// restores it, as configFirst does for a verb: robot spawn's
// --spawn-<kind> flags come from F's kinds. A first flag set (define's
// flags, from the config read now) parses the call. A flag that it does
// not know is one of F's kind flags, which all take a value: it adds the
// flag as one that takes a value and parses again, so the words are read
// as the real parse reads them.
func robotConfigFirst(args []string, define func(*flag.FlagSet)) (restore func()) {
	var kindFlags []string
	for {
		fs, o := newRobotFlagSet(args, io.Discard)
		define(fs)
		for _, name := range kindFlags {
			fs.String(name, "", "")
		}
		err := fs.Parse(args)
		if err != nil {
			if name, ok := strings.CutPrefix(err.Error(), "flag provided but not defined: -"); ok {
				kindFlags = append(kindFlags, name)
				continue
			}
		}
		if o.config == "" {
			return func() {}
		}
		path, err := filepath.Abs(o.config)
		if err != nil {
			return func() {} // applyRobotRoot reports it
		}
		r := &rootOptions{}
		r.setenv("NHM_CONFIG", path)
		return r.undo
	}
}
