// Taken from ntm (github.com/Dicklesworthstone/ntm)
// internal/config/config.go at commit 9d5ed977: PromptsConfig and
// ResolveForType; see LICENSE in this directory and PROVENANCE.md. Local
// changes (nhm-2p6.4.6): this header; package and imports; unexported
// names; the table is the [prompts] table of nhm's config; defaultPrompts
// resolves the defaults of a spawn's kinds by their short types.
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/integrations/kinds"
)

// promptsConfig is ntm's [prompts] table: a default prompt for each agent
// type that spawn sends ahead of --prompt, inline or from a file.
type promptsConfig struct {
	CCDefault      string `toml:"cc_default"`       // Default prompt for Claude agents
	CCDefaultFile  string `toml:"cc_default_file"`  // File path for Claude default prompt
	CodDefault     string `toml:"cod_default"`      // Default prompt for Codex agents
	CodDefaultFile string `toml:"cod_default_file"` // File path for Codex default prompt
	GmiDefault     string `toml:"gmi_default"`      // Default prompt for Gemini agents
	GmiDefaultFile string `toml:"gmi_default_file"` // File path for Gemini default prompt
	AgyDefault     string `toml:"agy_default"`      // Default prompt for Antigravity (agy) agents
	AgyDefaultFile string `toml:"agy_default_file"` // File path for Antigravity default prompt
	OmpDefault     string `toml:"omp_default"`      // Default prompt for Oh My Pi (omp) agents
	OmpDefaultFile string `toml:"omp_default_file"` // File path for Oh My Pi default prompt
}

// resolveForType returns the default prompt for a given agent type string
// (cc, cod, gmi, agy, omp). It reads from the inline string first,
// falling back to the file if configured.
func (p promptsConfig) resolveForType(agentType string) (string, error) {
	var val, filePath string
	switch agentType {
	case "cc":
		val, filePath = p.CCDefault, p.CCDefaultFile
	case "cod":
		val, filePath = p.CodDefault, p.CodDefaultFile
	case "gmi":
		val, filePath = p.GmiDefault, p.GmiDefaultFile
	case "agy":
		val, filePath = p.AgyDefault, p.AgyDefaultFile
	case "omp":
		val, filePath = p.OmpDefault, p.OmpDefaultFile
	default:
		return "", nil
	}
	if val != "" {
		return strings.TrimSpace(val), nil
	}
	if filePath != "" {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return "", fmt.Errorf("reading prompts.%s_default_file: %w", agentType, err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	return "", nil
}

// defaultPrompts is the [prompts] default of each kind the spawn starts,
// by kind: a kind's short type is its first selector flag (cc, cod), else
// its name (agy, omp), as ntm's agent types are.
func (p promptsConfig) defaultPrompts(wants []spawn.Want) (map[string]string, error) {
	out := map[string]string{}
	for _, w := range wants {
		a := kinds.Lookup(w.Kind)
		short := a.Name
		if len(a.Flags) > 0 {
			short = a.Flags[0]
		}
		d, err := p.resolveForType(short)
		if err != nil {
			return nil, err
		}
		if d != "" {
			out[w.Kind] = d
		}
	}
	return out, nil
}
