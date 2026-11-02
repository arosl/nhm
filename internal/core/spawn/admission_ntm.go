// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/spawn.go at commit 9d5ed977: the spawn admission input
// (collectSpawnAdmissionInputWithPanes), spawnAdmissionAgentLimit,
// spawnAdmissionAgentTypeLimits, needsSpawnAdmissionFence and
// spawnAdmissionAgentType; internal/robot/spawn_pacing.go at the same
// commit: an add's pane request (beginAgentAddition); and
// internal/config/pacing.go at the same commit: SpawnPacingConfig,
// AgentPacingConfig and ValidateSpawnPacingConfig; see LICENSE in this
// directory and PROVENANCE.md. Local changes (nhm-a421): this header;
// package and imports; the fleet is the herdr instance's snapshot (its
// workspaces are the sessions; an agent is a pane with a detected agent
// or one in herdr's agent list, never nhm's board) in place of tmux's
// panes; nhm's defaults (DefaultPacing: no agent caps,
// and a large spawn is 16 agents, ntm's built-in budget, where ntm's
// config sets caps of 3, 2, 2 and 8 and a large spawn of 4); pi is a
// type agent_type_limits takes; nhm never makes a user pane, so a spawn
// asks for one pane per agent; a spawn into a workspace that exists asks
// for its panes plus the new ones, as ntm's add does.
package spawn

import (
	"fmt"
	"sort"

	"github.com/arosl/nhm/internal/core/pressure"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/kinds"
)

// Pacing is ntm's [spawn_pacing] table, in nhm's config.
type Pacing struct {
	// Enabled controls whether spawn admission control is active.
	Enabled bool `toml:"enabled"`
	// MaxConcurrentSpawns is the size of a large spawn: one of at least
	// this many agents is deferred at high pressure and refused at
	// critical pressure.
	MaxConcurrentSpawns int `toml:"max_concurrent_spawns"`
	// AgentCaps add up to one fleet-wide agent budget.
	AgentCaps AgentCaps `toml:"agent_caps"`
	// AgentTypeLimits bound each agent type across the fleet. Zero or an
	// omitted type means no limit.
	AgentTypeLimits map[string]int `toml:"agent_type_limits"`
}

// AgentCaps is ntm's [spawn_pacing.agent_caps]: each cap is its type's
// share of the shared budget.
type AgentCaps struct {
	ClaudeMaxConcurrent int `toml:"claude_max_concurrent"`
	CodexMaxConcurrent  int `toml:"codex_max_concurrent"`
	GeminiMaxConcurrent int `toml:"gemini_max_concurrent"`
	OmpMaxConcurrent    int `toml:"omp_max_concurrent"`
}

// DefaultPacing is nhm's [spawn_pacing] default: admission on, no agent
// caps, and a large spawn of pressure.DefaultLargeSpawnThreshold agents.
func DefaultPacing() Pacing {
	return Pacing{Enabled: true, MaxConcurrentSpawns: pressure.DefaultLargeSpawnThreshold}
}

// pacingTypes are the agent types agent_type_limits takes: ntm's, and
// nhm's pi.
var pacingTypes = []string{"claude", "codex", "gemini", "antigravity", "grok", "omp", "opencode", "pi"}

// ValidatePacing is ntm's ValidateSpawnPacingConfig.
func ValidatePacing(p Pacing) error {
	if !p.Enabled {
		return nil
	}
	if p.MaxConcurrentSpawns < 1 {
		return fmt.Errorf("max_concurrent_spawns must be at least 1, got %d", p.MaxConcurrentSpawns)
	}
	for _, c := range []struct {
		name string
		v    int
	}{
		{"claude_max_concurrent", p.AgentCaps.ClaudeMaxConcurrent}, {"codex_max_concurrent", p.AgentCaps.CodexMaxConcurrent},
		{"gemini_max_concurrent", p.AgentCaps.GeminiMaxConcurrent}, {"omp_max_concurrent", p.AgentCaps.OmpMaxConcurrent},
	} {
		if c.v < 0 {
			return fmt.Errorf("agent_caps: %s must be non-negative, got %d", c.name, c.v)
		}
	}
	types := make([]string, 0, len(p.AgentTypeLimits))
	for kind := range p.AgentTypeLimits {
		types = append(types, kind)
	}
	sort.Strings(types)
	for _, kind := range types {
		known := false
		for _, k := range pacingTypes {
			known = known || k == kind
		}
		if !known {
			return fmt.Errorf("agent_type_limits: unknown agent type %q; use claude, codex, gemini, antigravity, grok, omp, opencode, or pi", kind)
		}
		if p.AgentTypeLimits[kind] < 0 {
			return fmt.Errorf("agent_type_limits: %s must be non-negative, got %d", kind, p.AgentTypeLimits[kind])
		}
	}
	return nil
}

// agentLimit is ntm's spawnAdmissionAgentLimit: the caps add up to one
// budget, saturated.
func (p Pacing) agentLimit() int {
	total := 0
	for _, c := range []int{p.AgentCaps.ClaudeMaxConcurrent, p.AgentCaps.CodexMaxConcurrent,
		p.AgentCaps.GeminiMaxConcurrent, p.AgentCaps.OmpMaxConcurrent} {
		if c > 0 {
			if total > int(^uint(0)>>1)-c {
				return int(^uint(0) >> 1)
			}
			total += c
		}
	}
	return total
}

func (p Pacing) typeLimits() map[string]int {
	if len(p.AgentTypeLimits) == 0 {
		return nil
	}
	out := make(map[string]int, len(p.AgentTypeLimits))
	for kind, n := range p.AgentTypeLimits {
		out[kind] = n
	}
	return out
}

// needsFence is ntm's needsSpawnAdmissionFence: a real spawn under a cap
// counts and launches under one cross-process lock.
func (p Pacing) needsFence(dryRun bool) bool {
	if dryRun || !p.Enabled {
		return false
	}
	if p.agentLimit() > 0 {
		return true
	}
	for _, n := range p.AgentTypeLimits {
		if n > 0 {
			return true
		}
	}
	return false
}

// admissionType is an agent's type in ntm's configuration words: the
// kind's name, and antigravity for agy.
func admissionType(kind string) string {
	name := kinds.Lookup(kind).Name
	if name == "agy" {
		return "antigravity"
	}
	return name
}

// admissionInput is ntm's collectSpawnAdmissionInputWithPanes on herdr:
// the request, the pacing policy, the host's pressure and the fleet.
// A fleet that could not be read leaves the counts unknown. An agent is
// a pane where herdr detects one, or one in herdr's agent list (an
// agent.start still launching), never nhm's own board (boards, by pane).
func admissionInput(req Request, workspaceID string, p Pacing, snap pressure.Snapshot, fleet herdr.SessionSnapshot, fleetErr error,
	boards map[string]bool) pressure.SpawnAdmissionInput {
	in := pressure.SpawnAdmissionInput{Session: req.Session, RequestedByType: map[string]int{}, RunningByType: map[string]int{}}
	for _, w := range req.Agents {
		in.RequestedAgents += w.Count
		in.RequestedByType[admissionType(w.Kind)] += w.Count
	}
	in.RequestedPanes = in.RequestedAgents
	if p.Enabled {
		in.LargeSpawnThreshold = pressure.DefaultLargeSpawnThreshold
		if p.MaxConcurrentSpawns > 0 {
			in.LargeSpawnThreshold = p.MaxConcurrentSpawns
		}
		in.MaxAgents = p.agentLimit()
		in.MaxAgentsByType = p.typeLimits()
		in.Pressure = snap
	}
	if fleetErr != nil {
		in.InventoryError = "enumerate herdr fleet: " + fleetErr.Error()
		return in
	}
	in.RunningSessions = len(fleet.Workspaces)
	counted := map[string]bool{}
	count := func(paneID, agent string) {
		if agent == "" || boards[paneID] || counted[paneID] {
			return
		}
		counted[paneID] = true
		in.RunningAgents++
		in.RunningByType[admissionType(agent)]++
	}
	for _, pane := range fleet.Panes {
		if workspaceID != "" && pane.WorkspaceID == workspaceID {
			in.SessionPanes++
		}
		in.CurrentPanes++
		count(pane.PaneID, pane.Agent)
	}
	for _, a := range fleet.Agents {
		count(a.PaneID, a.Agent)
	}
	// The pane request is the session's total, not an increment: nhm
	// always adds new panes.
	in.RequestedPanes = in.SessionPanes + in.RequestedAgents
	return in
}
