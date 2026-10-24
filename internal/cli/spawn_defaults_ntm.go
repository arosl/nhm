// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/cli/spawn.go at commit 9d5ed977: canonicalSpawnAgentType,
// orderedSpawnAgentTypes, canonicalSpawnCountTotals,
// formatSpawnCountSummary and appendMissingCountMapAgentSpecs; and
// internal/config/project.go at the same commit: ProjectDefaults; see
// LICENSE in this directory and PROVENANCE.md. Local changes
// (nhm-2p6.4.14): this header; package and imports; the types are
// internal/ui/tui/agent's port of ntm's agent types; the counts become
// nhm's wants, a type by the kind whose short type it is, and a type nhm
// has no kind for refuses; the project's .ntm/config.toml is read here
// for its [defaults] table only.
package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/core/spawnassign"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/ui/tui/agent"
)

// projectDefaults holds default settings for the project: the [defaults]
// table of its .ntm/config.toml.
type projectDefaults struct {
	Agents map[string]int `toml:"agents"` // e.g., { cc = 2, cod = 1 }
}

func canonicalSpawnAgentType(raw string) (agent.AgentType, bool) {
	switch t := agent.AgentType(raw).Canonical(); t {
	case agent.AgentTypeClaudeCode, agent.AgentTypeCodex, agent.AgentTypeGemini, agent.AgentTypeAntigravity,
		agent.AgentTypeGrok, agent.AgentTypeCursor, agent.AgentTypeWindsurf, agent.AgentTypeAider,
		agent.AgentTypeOpencode, agent.AgentTypeOmp, agent.AgentTypeOllama:
		return t, true
	default:
		return "", false
	}
}

func orderedSpawnAgentTypes() []agent.AgentType {
	return []agent.AgentType{
		agent.AgentTypeClaudeCode,
		agent.AgentTypeCodex,
		agent.AgentTypeGemini,
		agent.AgentTypeAntigravity,
		agent.AgentTypeGrok,
		agent.AgentTypeCursor,
		agent.AgentTypeWindsurf,
		agent.AgentTypeAider,
		agent.AgentTypeOpencode,
		agent.AgentTypeOmp,
		agent.AgentTypeOllama,
	}
}

func canonicalSpawnCountTotals(counts map[string]int) map[agent.AgentType]int {
	totals := make(map[agent.AgentType]int)
	for rawType, count := range counts {
		if count <= 0 {
			continue
		}
		agentType, ok := canonicalSpawnAgentType(rawType)
		if !ok {
			continue
		}
		totals[agentType] += count
	}
	return totals
}

func formatSpawnCountSummary(counts map[string]int) string {
	totals := canonicalSpawnCountTotals(counts)
	parts := make([]string, 0, len(totals))
	for _, agentType := range orderedSpawnAgentTypes() {
		count := totals[agentType]
		if count <= 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%d %s", count, agentType))
	}
	return strings.Join(parts, ", ")
}

// countMapWants is appendMissingCountMapAgentSpecs for a spawn with no
// agent flag: the counts in ntm's order, each type by nhm's kind of that
// short type (claude is cc, codex cod, opencode oc).
func countMapWants(counts map[string]int) ([]spawn.Want, error) {
	totals := canonicalSpawnCountTotals(counts)
	var wants []spawn.Want
	for _, agentType := range orderedSpawnAgentTypes() {
		count := totals[agentType]
		if count <= 0 {
			continue
		}
		kind := kindOfShortType(string(agentType))
		if kind == "" {
			return nil, fmt.Errorf("[defaults] agents: %d %s, and nhm has no kind for %s", count, agentType, agentType)
		}
		wants = append(wants, spawn.Want{Kind: kind, Count: count})
	}
	return wants, nil
}

// kindOfShortType is the kind whose short type (its first selector flag,
// else its name) is t.
func kindOfShortType(t string) string {
	for _, a := range kinds.All() {
		short := a.Name
		if len(a.Flags) > 0 {
			short = a.Flags[0]
		}
		if short == t {
			return a.Name
		}
	}
	return ""
}

// loadProjectDefaults reads the [defaults] agents of the nearest
// .ntm/config.toml from dir; nil when there is none.
func loadProjectDefaults(dir string) (map[string]int, error) {
	path, err := spawnassign.FindProjectConfig(dir)
	if err != nil || path == "" {
		return nil, err
	}
	var c struct {
		Defaults projectDefaults `toml:"defaults"`
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := toml.Decode(string(data), &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c.Defaults.Agents, nil
}
