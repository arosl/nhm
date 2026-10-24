// Taken from ntm (github.com/Dicklesworthstone/ntm) internal/cli/spawn.go
// at commit 9d5ed977: validateSpawnPaneEnv, isSpawnEnvironmentName and
// expandSpawnPaneEnv; see LICENSE in this directory and PROVENANCE.md.
// Local changes (nhm-2p6.4.7): this header; package and imports; exported
// names; the role is the agent's short type as a string; the variables go
// into the pane's environment (herdr's env on pane.split and tab.create),
// not ahead of the agent's command line.
package spawn

import (
	"fmt"
	"strconv"
	"strings"
)

// ValidatePaneEnv checks --pane-env: each name an environment name, no
// value with a NUL.
func ValidatePaneEnv(env map[string]string) error {
	for key, value := range env {
		if !isSpawnEnvironmentName(key) {
			return fmt.Errorf("invalid --pane-env name %q", key)
		}
		if strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("invalid --pane-env value for %q: contains NUL", key)
		}
	}
	return nil
}

func isSpawnEnvironmentName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && r != '_' && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// ExpandPaneEnv fills --pane-env's templates for one agent: {project},
// {pane} (its number among the spawn's agents of its type) and {role}
// (its short type).
func ExpandPaneEnv(env map[string]string, project string, pane int, role string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	replacer := strings.NewReplacer("{project}", project, "{pane}", strconv.Itoa(pane), "{role}", role)
	expanded := make(map[string]string, len(env))
	for key, value := range env {
		expanded[key] = replacer.Replace(value)
	}
	return expanded
}
