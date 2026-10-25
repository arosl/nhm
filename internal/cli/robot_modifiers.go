package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"

	"github.com/arosl/nhm/internal/integrations/kinds"
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

// robotConfigFile is the part of nhm's config that robot calls read.
type robotConfigFile struct {
	Robot struct {
		Output struct {
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
