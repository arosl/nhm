// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/agent_names.go at commit 9d5ed977: NATOAlphabet,
// agentTypePrefix, the naming of generateNameLocked and ParseCustomNames;
// and internal/robot/spawn.go at the same commit: the pane title
// "<session>__<short type>_<n>" of GetSpawn; see LICENSE in this directory
// and PROVENANCE.md. Local changes (nhm-2p6.4.2): this header; package
// and imports; agentNames holds only what naming needs (no pane map, no
// lock, no user pane, as nhm keeps no pane for the user); NameAgents
// names the agents of a --robot-spawn answer in launch order; the short
// type is the kind's first selector flag, else its name.
package spawn

import (
	"fmt"
	"strings"

	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/robot"
)

// NATOAlphabet is the NATO phonetic alphabet used for generating agent names.
var NATOAlphabet = []string{
	"alpha", "bravo", "charlie", "delta", "echo", "foxtrot",
	"golf", "hotel", "india", "juliet", "kilo", "lima",
	"mike", "november", "oscar", "papa", "quebec", "romeo",
	"sierra", "tango", "uniform", "victor", "whiskey", "xray",
	"yankee", "zulu",
}

// agentTypePrefix maps agent type strings to their name prefixes.
var agentTypePrefix = map[string]string{
	"claude": "claude",
	"codex":  "codex",
	"gemini": "gemini",
	"user":   "user",
}

// agentNames gives agents their names in launch order: the custom names
// first, then the NATO alphabet with the agent type as prefix.
type agentNames struct {
	natoIndex    int
	customNames  []string
	customOffset int
}

func (m *agentNames) next(agentType string) string {
	if m.customOffset < len(m.customNames) {
		name := m.customNames[m.customOffset]
		m.customOffset++
		return name
	}

	// Use NATO phonetic alphabet with agent type prefix
	prefix, ok := agentTypePrefix[agentType]
	if !ok {
		prefix = agentType
	}

	var natoWord string
	if m.natoIndex < len(NATOAlphabet) {
		natoWord = NATOAlphabet[m.natoIndex]
	} else {
		// Wrap around with numeric suffix for >26 agents
		idx := m.natoIndex % len(NATOAlphabet)
		cycle := m.natoIndex / len(NATOAlphabet)
		natoWord = fmt.Sprintf("%s-%d", NATOAlphabet[idx], cycle+1)
	}
	m.natoIndex++

	return fmt.Sprintf("%s-%s", prefix, natoWord)
}

// ParseCustomNames parses a comma-separated list of agent names
// (--spawn-names); blank entries are dropped.
func ParseCustomNames(namesList string) []string {
	if namesList == "" {
		return nil
	}
	parts := strings.Split(namesList, ",")
	names := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			names = append(names, trimmed)
		}
	}
	return names
}

// NameAgents gives each agent of a --robot-spawn answer (agents[], or
// would_create[] in a dry run) ntm's name and pane title, in launch order:
// the custom names first, then "<type>-<NATO word>"; the title is
// "<session>__<short type>_<n>", n counting the agents of the type. The
// agents of out are res.Targets, in the same order.
func NameAgents(out *robot.SpawnOutput, res Result, custom []string) {
	agents := out.Agents
	if res.DryRun {
		agents = out.WouldCreate
	}
	names := &agentNames{customNames: custom}
	perType := map[string]int{}
	for i := range agents {
		if i >= len(res.Targets) {
			break
		}
		a := kinds.Lookup(res.Targets[i].Kind)
		short := a.Name
		if len(a.Flags) > 0 {
			short = a.Flags[0]
		}
		perType[short]++
		agents[i].Name = names.next(a.NTMType)
		agents[i].Title = fmt.Sprintf("%s__%s_%d", res.Session, short, perType[short])
	}
}
