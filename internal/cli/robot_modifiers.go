package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/robot"
)

// ntm's global robot modifiers are root flags, so every robot call takes
// them (newRobotFlagSet). --robot-format and its alias
// --robot-output-format are one value. ntm resolves the format before
// any dispatch (resolveRobotFormat, internal/cli/root.go at 9d5ed977):
// the flag, then the environment, then the config's [robot.output]
// format, else auto. nhm's environment names are NHM_ROBOT_FORMAT and
// NHM_OUTPUT_FORMAT, where ntm's are NTM_*; TOON_DEFAULT_FORMAT is
// toon's own name and stays (nhm-2p6.2.2; the operator, 2026-10-24).

// robotFormatEnv is the environment that sets the format when no flag
// does, the first set one winning.
var robotFormatEnv = []string{"NHM_ROBOT_FORMAT", "NHM_OUTPUT_FORMAT", "TOON_DEFAULT_FORMAT"}

// robotFormatHint is ntm's hint for a format it does not know.
const robotFormatHint = "Use json, toon, or auto"

// resolveRobotFormat is the format of a robot call: flag (the flag's
// value, "" when not given), else the environment, else, when useConfig
// is true, the config's [robot.output] format, else auto. auto and json
// write JSON, as ntm's auto does; toon is refused until nhm writes TOON
// (nhm-2p6.2.11). A value ntm does not know is ntm's error.
func resolveRobotFormat(flag string, useConfig bool) (string, error) {
	v := flag
	for _, name := range robotFormatEnv {
		if v == "" {
			v = os.Getenv(name)
		}
	}
	if v == "" && useConfig {
		v = robotConfig().Robot.Output.Format
	}
	switch v {
	case "", "auto":
		return "auto", nil
	case "json":
		return "json", nil
	case "toon":
		return "", errRobotTOON
	}
	return "", fmt.Errorf("invalid robot format %q: must be json, toon, or auto", v)
}

// errRobotTOON refuses --robot-format=toon, which nhm does not write yet.
var errRobotTOON = errors.New("--robot-format=toon: nhm writes json only")

// --robot-verbosity is resolved as the format is (ntm's
// resolveRobotVerbosity): the flag, then NHM_ROBOT_VERBOSITY (ntm's
// NTM_ROBOT_VERBOSITY), then the config's [robot] verbosity, else
// default (nhm-2p6.2.3).

// robotVerbosityHint is ntm's hint for a verbosity it does not know.
const robotVerbosityHint = "Use terse, default, or debug"

// resolveRobotVerbosity is the verbosity of a robot call: flag, else
// NHM_ROBOT_VERBOSITY, else, when useConfig is true, the config's [robot]
// verbosity, else default. A value ntm does not know is ntm's error.
func resolveRobotVerbosity(flag string, useConfig bool) (robot.RobotVerbosity, error) {
	v := flag
	if v == "" {
		v = os.Getenv("NHM_ROBOT_VERBOSITY")
	}
	if v == "" && useConfig {
		v = robotConfig().Robot.Verbosity
	}
	return robot.ParseRobotVerbosity(v)
}

// robotModifiers are ntm's global robot modifiers (isRobotGlobalModifier):
// flags of every robot call, never its command.
var robotModifiers = map[string]bool{"robot-format": true, "robot-output-format": true, "robot-verbosity": true,
	"robot-limit": true, "robot-offset": true}

// robotInvocation is ntm's robotInvocationFromArgs: the robot command a
// call names, its first --robot-* flag before any "--" that is not a
// global modifier, else its first one, without "--"; "" for none.
func robotInvocation(args []string) string {
	fallback := ""
	for _, a := range args {
		if a == "--" {
			break
		}
		name, _, _ := strings.Cut(a, "=")
		if !strings.HasPrefix(name, "--robot-") {
			continue
		}
		command := strings.TrimPrefix(name, "--")
		if fallback == "" {
			fallback = command
		}
		if !robotModifiers[command] {
			return command
		}
	}
	return fallback
}

// robotCommand is the running robot call's command (robotInvocation),
// which its error envelopes name in _meta.command, as ntm's
// EncodeErrorJSON does (nhm-2p6.2.5). runRobotCall sets it for the call.
var robotCommand string

// robotOutput is how the robot call that runs writes its successful
// answer: its format, which debug's _debug names, and its verbosity.
// applyRobotRoot sets it for the call, and the call's undo restores it,
// as ntm's process globals hold them for its one call.
type robotOutput struct {
	format    string
	verbosity robot.RobotVerbosity
}

// robotOut is the running robot call's output.
var robotOut = robotOutput{format: "auto", verbosity: robot.VerbosityDefault}

// robotPagingHint is ntm's hint for a negative --robot-limit or offset.
const robotPagingHint = "Pagination values must be zero or greater"

// pagination is ntm's resolveRobotPagination for a call whose flag set
// fs parsed: --robot-limit, else --limit when given; --robot-offset or
// --offset; a negative value is ntm's error. The calls that page call it
// (status and snapshot), so the others ignore the flags, as in ntm
// (nhm-2p6.2.4).
func (o *robotOptions) pagination(fs *flag.FlagSet) (robot.PaginationOptions, error) {
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	opts := robot.PaginationOptions{}
	if given["robot-limit"] {
		opts.Limit = o.robotLimit
	} else if given["limit"] {
		opts.Limit = o.limit
	}
	if given["robot-offset"] || given["offset"] {
		opts.Offset = o.offset
	}
	if opts.Limit < 0 || opts.Offset < 0 {
		return opts, errors.New("pagination values must be >= 0")
	}
	return opts, nil
}

// robotConfigFile is the part of nhm's config that robot calls read.
type robotConfigFile struct {
	Robot struct {
		Verbosity string `toml:"verbosity"`
		Output    struct {
			Format string `toml:"format"`
		} `toml:"output"`
	} `toml:"robot"`
}

// robotConfig reads the [robot] table of nhm's config file (--config's,
// else NHM_CONFIG's, else ~/.config/nhm/config.toml). A config that is
// absent or does not load gives no value, as nhm's kinds reader adds
// nothing from one; ntm refuses the call (the config row, nhm-2p6.10).
func robotConfig() robotConfigFile {
	var c robotConfigFile
	path, err := kinds.ConfigPath()
	if err != nil {
		return c
	}
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return robotConfigFile{}
	}
	return c
}
