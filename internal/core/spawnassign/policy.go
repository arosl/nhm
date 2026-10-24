// Derived from ntm (github.com/Dicklesworthstone/ntm)
// internal/config/merge.go (LoadAssignmentPolicyStrict, MergeConfig's
// [assign] rules, mergeOperatorGatedLabels whole), internal/config/project.go
// (FindProjectConfig, ProjectAssign) and internal/config/config.go
// (AssignConfig, WorkSourceConfig.Policy) at commit 9d5ed977; see LICENSE in
// this directory and PROVENANCE.md. Local changes: only the [assign] section
// is read; the global file is nhm's config; an unknown key under [assign]
// refuses; FindProjectConfig finds nhm's .nhm/config.toml, for spawn's
// [defaults] agents (nhm-2p6.4.14).

package spawnassign

// The assignment policy, strictly loaded: ntm's LoadAssignmentPolicyStrict
// (internal/config/merge.go at 9d5ed977) limited to the [assign] section.
// The global part is nhm's config file; the project part is the project's
// .ntm/config.toml, found as ntm finds it, merged by ntm's rules: operator
// gates only extend, a work-source policy only tightens. A file that does
// not load refuses the assignment (nhm-i85).

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/arosl/nhm/internal/integrations/worksource"
)

// assignSection is the [assign] table, ntm's keys.
type assignSection struct {
	Assign struct {
		OperatorGatedLabels []string `toml:"operator_gated_labels"`
		WorkSource          struct {
			RequiredRef   string   `toml:"required_ref"`
			RequireClean  bool     `toml:"require_clean"`
			ProgramLabels []string `toml:"program_labels"`
		} `toml:"work_source"`
	} `toml:"assign"`
}

func (s assignSection) policy() Policy {
	return Policy{
		OperatorGatedLabels: append([]string(nil), s.Assign.OperatorGatedLabels...),
		WorkSource: worksource.ProjectPolicy{
			RequiredRef:   strings.TrimSpace(s.Assign.WorkSource.RequiredRef),
			RequireClean:  s.Assign.WorkSource.RequireClean,
			ProgramLabels: append([]string(nil), s.Assign.WorkSource.ProgramLabels...),
		},
	}
}

// LoadPolicy loads the assignment policy for projectDir from nhm's config
// file globalPath (absent: the defaults) and the project's .ntm/config.toml.
func LoadPolicy(projectDir, globalPath string) (Policy, error) {
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return Policy{}, errors.New("assignment safety policy requires an authoritative project directory")
	}
	var global assignSection
	if globalPath != "" {
		if err := decodeAssign(globalPath, &global, true); err != nil {
			return Policy{}, fmt.Errorf("loading global config: %w", err)
		}
	}
	policy := global.policy()
	projectPath, err := findProjectConfig(projectDir)
	if err != nil {
		return Policy{}, err
	}
	if projectPath != "" {
		var project assignSection
		if err := decodeAssign(projectPath, &project, false); err != nil {
			return Policy{}, fmt.Errorf("loading project config %s: %w", projectPath, err)
		}
		policy = mergePolicy(policy, project.policy())
	}
	if ref := policy.WorkSource.RequiredRef; ref != "" {
		if err := worksource.ValidateRequiredRef(ref); err != nil {
			return Policy{}, err
		}
	}
	return policy, nil
}

// decodeAssign reads path's [assign] table into s; a missing global file is
// the defaults.
func decodeAssign(path string, s *assignSection, missingOK bool) error {
	data, err := os.ReadFile(path)
	if missingOK && errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	md, err := toml.Decode(string(data), s)
	if err != nil {
		return err
	}
	// A key under [assign] that nhm does not know refuses: a misspelled
	// gate would otherwise load as no gate.
	for _, key := range md.Undecoded() {
		if len(key) > 0 && key[0] == "assign" {
			return fmt.Errorf("unknown key %s", key.String())
		}
	}
	return nil
}

// FindProjectConfig is the path of the nearest .nhm/config.toml from
// startDir, "" when there is none: nhm's project config, where ntm's is
// .ntm/config.toml (nhm-2p6.4.14; the operator, 2026-10-24: never ntm's
// paths). Spawn reads its [defaults] agents.
func FindProjectConfig(startDir string) (string, error) { return findConfigIn(startDir, ".nhm") }

// findProjectConfig is the project config that [assign] reads, still
// .ntm/config.toml until nhm-2p6.28 moves it to nhm's.
func findProjectConfig(startDir string) (string, error) { return findConfigIn(startDir, ".ntm") }

// findConfigIn walks up from startDir to the nearest <folder>/config.toml,
// as ntm's FindProjectConfig does: a lookup failure other than absence is an
// error, and the entry must be a regular file.
func findConfigIn(startDir, folder string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}
	for {
		configPath := filepath.Join(dir, folder, "config.toml")
		info, statErr := os.Stat(configPath)
		switch {
		case statErr == nil:
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("project config %s is not a regular file", configPath)
			}
			return configPath, nil
		case errors.Is(statErr, fs.ErrNotExist):
		default:
			return "", fmt.Errorf("inspecting project config %s: %w", configPath, statErr)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// mergePolicy applies ntm's MergeConfig rules for [assign].
func mergePolicy(global, project Policy) Policy {
	if len(project.OperatorGatedLabels) > 0 {
		global.OperatorGatedLabels = mergeOperatorGatedLabels(project.OperatorGatedLabels, global.OperatorGatedLabels)
	}
	if project.WorkSource.RequireClean {
		global.WorkSource.RequireClean = true
	}
	if strings.TrimSpace(global.WorkSource.RequiredRef) == "" {
		global.WorkSource.RequiredRef = strings.TrimSpace(project.WorkSource.RequiredRef)
	}
	if len(global.WorkSource.ProgramLabels) == 0 {
		global.WorkSource.ProgramLabels = append([]string(nil), project.WorkSource.ProgramLabels...)
	}
	return global
}

// mergeOperatorGatedLabels is ntm's (internal/config/merge.go).
func mergeOperatorGatedLabels(primary, secondary []string) []string {
	if len(primary) == 0 && len(secondary) == 0 {
		return nil
	}

	seen := make(map[string]bool, len(primary)+len(secondary))
	out := make([]string, 0, len(primary)+len(secondary))
	for _, values := range [][]string{primary, secondary} {
		for _, value := range values {
			value = strings.TrimSpace(value)
			normalized := strings.ToLower(value)
			if normalized == "" || seen[normalized] {
				continue
			}
			seen[normalized] = true
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
