// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/cli/assign.go
// and internal/cli/activity.go at commit 9d5ed977 (in part); see LICENSE in
// this directory and PROVENANCE.md. Local changes: this header; the
// selection code of ntm's human assign moves from its CLI to this package,
// as pure functions:
//   - generateAssignmentsLegacy is LegacyAssignments. It takes LegacyAgent
//     (the fields of ntm's assignAgentInfo and tmux pane that it reads) in
//     place of assignAgentInfo, and returns LegacyAssignment (the selection
//     fields of ntm's AssignmentItem). The caller fills the agent name, pane
//     id, status, prompt flag and time, and prints the round-robin plan that
//     ntm prints with --verbose. The balanced branch reads LegacyLoad, which
//     the caller fills from its ledger, where ntm reads its AssignmentStore,
//     and calls now for each pick, where ntm calls time.Now (a nil now is
//     time.Now).
//   - detectAgentTypeFromPane (activity.go) is LegacyAgentType and takes the
//     agent type in place of the tmux pane; nhm knows the kind, so ntm's
//     fallback to the pane title is not taken.
//   - usesAllocationPlanner is UsesAllocationPlanner and takes the strategy
//     string in place of AssignCommandOptions.
//   - parsePriorityString and inferTaskTypeFromBead are exported as
//     ParsePriorityString and InferTaskTypeFromBead.
//   - calculateMatchConfidence keeps its task patterns in a fixed order,
//     where ntm ranges over a map. ntm gives a title that matches patterns
//     of two task types either type, at random; here the later type in the
//     list always wins, which is one of ntm's answers.
//   - buildReasoning is unchanged.

package assign

import (
	"fmt"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/integrations/bv"
	"github.com/arosl/nhm/internal/ui/tui/agent"
)

// LegacyAgent is a placeable agent for the legacy strategies. The caller
// passes the agents in ntm's order, the session's panes in pane order: the
// order decides speed and round-robin, and ties in quality and dependency.
type LegacyAgent struct {
	// Key identifies the agent: unique and not empty, and the same key the
	// caller's ledger counts its active assignments under (nhm: the binding
	// id; ntm: the pane's stable key). It keys the used set and LegacyLoad.
	Key string
	// Target is balanced's last tie-break, compared as a plain string. ntm
	// uses the pane's physical target, unpadded "W.P", so "0.10" < "0.2".
	Target string
	Index  int // ntm: the pane index
	// AgentType is ntm's long agent name (claude, codex, gemini, omp, oc,
	// ...), as LegacyAgentType gives it; the short kinds (cc, cod) score
	// every agent as unprofiled.
	AgentType string
}

// LegacyLoad is the active load that the balanced branch starts from, keyed
// by LegacyAgent.Key: the count of each agent's active assignments, and the
// latest time among them. ntm fills it only when the session is named, from
// its store's active assignments; a store it cannot read, or an assignment
// without a pane identity, adds nothing.
type LegacyLoad struct {
	Active       map[string]int
	LastAssigned map[string]time.Time
}

// LegacyAssignment is one bead-to-agent recommendation.
type LegacyAssignment struct {
	BeadID    string
	BeadTitle string
	Agent     LegacyAgent
	Score     float64
	Reasoning string
}

// UsesAllocationPlanner reports whether ntm plans the strategy with
// PlanAllocations: an empty strategy or balanced, in any case and with
// spaces trimmed. Every other string goes to LegacyAssignments.
func UsesAllocationPlanner(strategy string) bool {
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	return strategy == "" || strategy == "balanced"
}

// LegacyAssignments preserves the explicit non-balanced strategy behavior.
// It matches the strategy in lower case but untrimmed; a string that is not
// round-robin, quality, speed or dependency takes the balanced branch.
// The balanced branch calls now once for each pick; like ntm's time.Now,
// each call must return a later time, or the least-recent tie-break picks
// otherwise than in ntm. A nil now is time.Now.
func LegacyAssignments(strategy string, agents []LegacyAgent, beads []bv.BeadPreview, load LegacyLoad, now func() time.Time) []LegacyAssignment {
	var assignments []LegacyAssignment
	if now == nil {
		now = time.Now
	}

	switch strings.ToLower(strategy) {
	case "round-robin":
		// Deterministic round-robin: bead[i] -> agent[i % N]
		// Score is always 1.0 (all assignments equally valid in round-robin)
		// Distribution: beads evenly spread, first agents get +1 if uneven
		for i, bead := range beads {
			if len(agents) == 0 {
				break
			}
			agent := agents[i%len(agents)]
			assignments = append(assignments, LegacyAssignment{
				BeadID:    bead.ID,
				BeadTitle: bead.Title,
				Agent:     agent,
				Score:     1.0, // Round-robin: all assignments equally valid
				Reasoning: fmt.Sprintf("round-robin slot %d → agent %d", i+1, i%len(agents)),
			})
		}

	case "quality":
		// Quality: assign each bead to the best-matching available agent
		usedAgents := make(map[string]bool)
		for _, bead := range beads {
			var bestAgent *LegacyAgent
			var bestScore float64

			for i := range agents {
				if usedAgents[agents[i].Key] {
					continue
				}
				score := GetAgentScoreByString(agents[i].AgentType, InferTaskTypeFromBead(bead))
				if score > bestScore {
					bestScore = score
					bestAgent = &agents[i]
				}
			}

			if bestAgent != nil {
				assignments = append(assignments, LegacyAssignment{
					BeadID:    bead.ID,
					BeadTitle: bead.Title,
					Agent:     *bestAgent,
					Score:     bestScore,
					Reasoning: buildReasoning(bestAgent.AgentType, bead, "quality"),
				})
				usedAgents[bestAgent.Key] = true
			}
		}

	case "speed":
		// Speed: assign to first available agent
		usedAgents := make(map[string]bool)
		for _, bead := range beads {
			for i := range agents {
				if usedAgents[agents[i].Key] {
					continue
				}
				score := (calculateMatchConfidence(agents[i].AgentType, bead, "speed") + 0.9) / 2
				assignments = append(assignments, LegacyAssignment{
					BeadID:    bead.ID,
					BeadTitle: bead.Title,
					Agent:     agents[i],
					Score:     score,
					Reasoning: buildReasoning(agents[i].AgentType, bead, "speed"),
				})
				usedAgents[agents[i].Key] = true
				break
			}
		}

	case "dependency":
		// Dependency: prioritize by unblocks count (already sorted by bv)
		usedAgents := make(map[string]bool)
		for _, bead := range beads {
			var bestAgent *LegacyAgent
			var bestScore float64

			for i := range agents {
				if usedAgents[agents[i].Key] {
					continue
				}
				score := calculateMatchConfidence(agents[i].AgentType, bead, "dependency")
				// Boost for high priority
				priority := ParsePriorityString(bead.Priority)
				if priority <= 1 {
					score = min(score+0.1, 0.95)
				}
				if score > bestScore {
					bestScore = score
					bestAgent = &agents[i]
				}
			}

			if bestAgent != nil {
				assignments = append(assignments, LegacyAssignment{
					BeadID:    bead.ID,
					BeadTitle: bead.Title,
					Agent:     *bestAgent,
					Score:     bestScore,
					Reasoning: buildReasoning(bestAgent.AgentType, bead, "dependency"),
				})
				usedAgents[bestAgent.Key] = true
			}
		}

	default: // balanced
		// Balanced: spread work evenly, considering existing load from the
		// caller's ledger
		agentAssignCounts := make(map[string]int)
		agentLastAssigned := make(map[string]time.Time)

		// Pre-populate counts from the load (live tracking of active assignments)
		for key, count := range load.Active {
			agentAssignCounts[key] = count
		}
		for key, assignedAt := range load.LastAssigned {
			agentLastAssigned[key] = assignedAt
		}

		for _, bead := range beads {
			var bestAgent *LegacyAgent
			var bestScore float64
			minAssigns := int(^uint(0) >> 1)
			var leastRecentTime time.Time

			for i := range agents {
				paneKey := agents[i].Key
				count := agentAssignCounts[paneKey]
				score := calculateMatchConfidence(agents[i].AgentType, bead, "balanced")
				lastAssign := agentLastAssigned[paneKey]

				// Tie-breaker cascade:
				// 1. Prefer agents with fewer active assignments
				// 2. On tie: prefer higher capability score
				// 3. On tie: prefer least-recently assigned (or never assigned)
				// 4. On tie: prefer lower pane index (deterministic)
				shouldPick := false
				if count < minAssigns {
					shouldPick = true
				} else if count == minAssigns {
					if score > bestScore {
						shouldPick = true
					} else if score == bestScore {
						// Tie-breaker: least-recently assigned wins
						if bestAgent == nil {
							shouldPick = true
						} else if lastAssign.IsZero() && !leastRecentTime.IsZero() {
							// Never assigned beats previously assigned
							shouldPick = true
						} else if !lastAssign.IsZero() && !leastRecentTime.IsZero() && lastAssign.Before(leastRecentTime) {
							shouldPick = true
						} else if lastAssign.Equal(leastRecentTime) && agents[i].Target < bestAgent.Target {
							// Final tie-breaker: physical topology order for determinism.
							shouldPick = true
						}
					}
				}

				if shouldPick {
					minAssigns = count
					bestScore = score
					bestAgent = &agents[i]
					leastRecentTime = lastAssign
				}
			}

			if bestAgent != nil {
				assignments = append(assignments, LegacyAssignment{
					BeadID:    bead.ID,
					BeadTitle: bead.Title,
					Agent:     *bestAgent,
					Score:     bestScore,
					Reasoning: buildReasoning(bestAgent.AgentType, bead, "balanced"),
				})
				bestKey := bestAgent.Key
				agentAssignCounts[bestKey]++
				// Update last assigned time for this session's assignments
				agentLastAssigned[bestKey] = now()
			}
		}
	}

	return assignments
}

// LegacyAgentType gives ntm's long agent name for an agent type, the name
// that the legacy strategies score (ntm: detectAgentTypeFromPane).
func LegacyAgentType(agentType agent.AgentType) string {
	switch agentType.Canonical() {
	case agent.AgentTypeClaudeCode:
		return "claude"
	case agent.AgentTypeCodex:
		return "codex"
	case agent.AgentTypeGemini:
		return "gemini"
	case agent.AgentTypeAntigravity:
		return "antigravity"
	case agent.AgentTypeGrok:
		return "grok"
	case agent.AgentTypeCursor:
		return "cursor"
	case agent.AgentTypeWindsurf:
		return "windsurf"
	case agent.AgentTypeAider:
		return "aider"
	case agent.AgentTypeOpencode:
		return "oc"
	case agent.AgentTypeOmp:
		return "omp"
	case agent.AgentTypeOllama:
		return "ollama"
	case agent.AgentTypeUser:
		return "user"
	default:
		return "unknown"
	}
}

// calculateMatchConfidence calculates how well an agent matches a task
func calculateMatchConfidence(agentType string, bead bv.BeadPreview, strategy string) float64 {
	baseConfidence := 0.7

	// Task type inference
	title := strings.ToLower(bead.Title)
	taskType := "task"

	// The patterns in a fixed order (ntm: a map); the last match wins.
	taskPatterns := []struct {
		tt       string
		patterns []string
	}{
		{"bug", []string{"bug", "fix", "broken", "error", "crash"}},
		{"testing", []string{"test", "spec", "coverage"}},
		{"documentation", []string{"doc", "readme", "comment"}},
		{"refactor", []string{"refactor", "cleanup", "improve"}},
		{"analysis", []string{"analyze", "investigate", "research"}},
		{"feature", []string{"feature", "implement", "add", "new"}},
	}

	for _, row := range taskPatterns {
		for _, p := range row.patterns {
			if strings.Contains(title, p) {
				taskType = row.tt
				break
			}
		}
	}

	// Agent strengths
	strengths := map[string]map[string]float64{
		"claude": {"analysis": 0.9, "refactor": 0.9, "documentation": 0.8, "feature": 0.8, "bug": 0.7},
		"codex":  {"feature": 0.9, "bug": 0.8, "task": 0.8, "refactor": 0.6},
		"gemini": {"documentation": 0.9, "analysis": 0.8, "feature": 0.8},
		// omp follows whichever frontier model its roles select: the midpoint
		// of the claude and codex rows (0.7 base where a row is silent), the
		// same basis as its assign.DefaultCapabilities profile.
		"omp": {"analysis": 0.8, "refactor": 0.75, "documentation": 0.75, "feature": 0.85, "bug": 0.75, "task": 0.75},
	}

	if agentStrengths, ok := strengths[agentType]; ok {
		if strength, ok := agentStrengths[taskType]; ok {
			baseConfidence = strength
		}
	}

	// Strategy adjustments
	switch strategy {
	case "speed":
		baseConfidence = (baseConfidence + 0.9) / 2
	case "dependency":
		priority := ParsePriorityString(bead.Priority)
		if priority <= 1 {
			baseConfidence = min(baseConfidence+0.1, 0.95)
		}
	}

	return baseConfidence
}

// ParsePriorityString converts "P0"-"P4" to integer
func ParsePriorityString(p string) int {
	if len(p) == 2 && p[0] == 'P' {
		if n := p[1] - '0'; n <= 4 {
			return int(n)
		}
	}
	return 2
}

// buildReasoning creates explanation for assignment
func buildReasoning(agentType string, bead bv.BeadPreview, strategy string) string {
	var reasons []string

	title := strings.ToLower(bead.Title)
	priority := ParsePriorityString(bead.Priority)

	// Task-agent match
	if agentType == "claude" && (strings.Contains(title, "refactor") || strings.Contains(title, "analyze")) {
		reasons = append(reasons, "Claude excels at analysis/refactoring")
	} else if agentType == "codex" && (strings.Contains(title, "feature") || strings.Contains(title, "implement")) {
		reasons = append(reasons, "Codex excels at implementations")
	} else if agentType == "gemini" && strings.Contains(title, "doc") {
		reasons = append(reasons, "Gemini excels at documentation")
	}

	// Priority
	switch priority {
	case 0:
		reasons = append(reasons, "critical priority")
	case 1:
		reasons = append(reasons, "high priority")
	}

	// Strategy
	switch strategy {
	case "balanced":
		reasons = append(reasons, "balanced workload")
	case "speed":
		reasons = append(reasons, "optimizing for speed")
	case "quality":
		reasons = append(reasons, "optimizing for quality")
	case "dependency":
		reasons = append(reasons, "prioritizing unblocks")
	}

	if len(reasons) == 0 {
		return "available agent matched to available work"
	}

	return strings.Join(reasons, "; ")
}

// InferTaskTypeFromBead determines task type from bead metadata
func InferTaskTypeFromBead(bead bv.BeadPreview) string {
	title := strings.ToLower(bead.Title)
	rules := []struct {
		typ string
		kws []string
	}{
		{"bug", []string{"bug", "fix", "broken", "error", "crash"}},
		{"testing", []string{"test", "spec", "coverage"}},
		{"documentation", []string{"doc", "readme", "comment"}},
		{"refactor", []string{"refactor", "cleanup", "improve"}},
		{"analysis", []string{"analyze", "investigate", "research"}},
		{"feature", []string{"feature", "implement", "add", "new"}},
	}
	for _, r := range rules {
		for _, kw := range r.kws {
			if strings.Contains(title, kw) {
				return r.typ
			}
		}
	}
	return "task"
}
