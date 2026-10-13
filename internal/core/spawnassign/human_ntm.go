// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/cli/assign.go at commit 9d5ed977: classifyTriageRecForAssignmentWithGate,
// normalizeBeadStatus, partitionActionableRecommendationsForAssignment, the
// cycle validation of CheckCycles, IsBeadInCycle, the bead preparation and
// the limit of getAssignOutputEnhanced, buildAssignAllocationInput,
// buildAssignAllocationBeads, assignBeadResourceCost,
// assignAgentResourceHeadroom, assignScrollbackBacklog, clampAssignScore,
// assignmentItemsFromAllocationPlan, the default template of
// promptTemplateSource through renderPromptTemplate, and
// printAssignmentPromptContext; internal/cli/spawn.go at the same commit:
// resolveSpawnAssignAgentType; internal/robot/robot.go at the same commit:
// ResolveAgentType; see LICENSE in this directory and PROVENANCE.md. Local
// changes (nhm-cvw, the human spawn --assign): this header; package and
// imports; the input is the verified plan and spawnassign's ready agents
// in place of bv and tmux panes: an
// agent's pane target and stable key are its slot, its pane index the
// slot's M, its name ntm's <session>_<type>_<slot>; the cycles, the active
// beads and the session's load come from the caller; context use and
// scrollback count as none, and ntm's system pressure probe gives way to
// the reading it gets at normal load; a new binding has no active
// assignment; the planned items become one WorkItem per agent for
// SelectWork, each with ntm's work prompt, and the items beyond an agent's
// first fail without a claim, with the words of ntm's observation gate,
// which refuses them once the first is sent; with no agent, every ready
// bead is left out as no_placeable_agents; the two resolvers are exported
// for the CLI;
// printAssignmentPromptContext returns its lines (HumanPromptContextLines).
package spawnassign

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/spawnassign/assign"
	"github.com/arosl/nhm/internal/integrations/bv"
	"github.com/arosl/nhm/internal/robot"
	"github.com/arosl/nhm/internal/ui/tui/agent"
)

// HumanReadyBeadLimit is ntm's cut of the ready beads before selection.
const HumanReadyBeadLimit = 50

// ntm's reasons for a bead left out of the human selection.
const (
	HumanSkipCycle        = "in_dependency_cycle"
	HumanSkipLimit        = "limit_reached"
	HumanSkipPressure     = string(assign.AllocationReasonCriticalPressure)
	HumanSkipNoPlaceable  = "no_placeable_agents"
	humanSkipNotOpen      = "not_open_status"
	humanSkipBlocked      = "blocked_by_dependency"
	humanSkipContainer    = "container_issue_type"
	humanSkipGated        = "operator_gated"
	humanSkipInProgress   = "already_in_progress"
	humanSkipBlockedState = "blocked_status"
	humanSkipClosed       = "closed_status"
	humanSkipAssigned     = "already_assigned"
)

// HumanSkip is a bead that the human selection gives no agent, with ntm's
// reason (ntm's SkippedItem).
type HumanSkip struct {
	BeadID    string   `json:"bead_id"`
	BeadTitle string   `json:"bead_title"`
	Reason    string   `json:"reason"`
	BlockedBy []string `json:"blocked_by_ids,omitempty"`
}

// HumanFailure is a planned item that fails before its claim: an item for
// an agent that already has one (ntm's coordinator refuses it).
type HumanFailure struct {
	BeadID    string `json:"bead_id"`
	BeadTitle string `json:"bead_title"`
	Pane      string `json:"pane"`
	Error     string `json:"error"`
}

// HumanOptions are the human spawn --assign's choices and the facts its
// selection reads.
type HumanOptions struct {
	// Session is the herdr session, ntm's tmux session: the agents' names.
	Session string
	// Strategy is --strategy as given: "" and balanced plan with
	// PlanAllocations, anything else takes ntm's legacy switch.
	Strategy string
	// Limit is --limit (0: none).
	Limit int
	// Cycles are bv's dependency cycles, as bv reports them.
	Cycles [][]string
	// Active are the beads with an active assignment in the ledger.
	Active map[string]struct{}
	// ActiveTotal is the session's count of active assignments.
	ActiveTotal   int
	OperatorGated func(string) bool
	Now           func() time.Time
}

// HumanSelection is the selection: the agents in the order they are served
// and one item for each of the first len(Items) of them; the beads left out
// and the items that fail before their claim, with why.
type HumanSelection struct {
	Agents   []SpawnedAgent
	Items    []WorkItem
	Skipped  []HumanSkip
	Failures []HumanFailure
	// Planned are the planned beads after the limit, in plan order: the
	// items and the failures (ntm's assignments, which it runs in order).
	Planned []string
	// Actionable is the count of beads that ntm's partition keeps, before
	// the cycles and the cut (its "[DEP] Filtered" line).
	Actionable int
}

// SelectHuman is ntm's human spawn --assign selection (getAssignOutputEnhanced,
// from the actionable work to the planned items, and the limit) on the
// verified plan and the ready agents.
func SelectHuman(triage *bv.TriageResponse, agents []SpawnedAgent, opts HumanOptions) HumanSelection {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	gated := opts.OperatorGated
	if gated == nil {
		gated = func(string) bool { return false }
	}
	var recs []bv.TriageRecommendation
	if triage != nil {
		recs = triage.Triage.Recommendations
	}
	ready, skipped := partitionActionableRecommendationsForAssignment(recs, opts.Active, gated)
	actionable := len(ready)

	// Filter out beads in dependency cycles.
	if cycles := validCycles(opts.Cycles); len(cycles) > 0 {
		nonCyclic := make([]bv.BeadPreview, 0, len(ready))
		for _, bead := range ready {
			if IsBeadInCycle(bead.ID, cycles) {
				skipped = append(skipped, HumanSkip{BeadID: bead.ID, BeadTitle: bead.Title, Reason: HumanSkipCycle})
				continue
			}
			nonCyclic = append(nonCyclic, bead)
		}
		ready = nonCyclic
	}
	if len(ready) > HumanReadyBeadLimit {
		ready = ready[:HumanReadyBeadLimit]
	}
	// ntm lists the panes in their order: speed and round-robin follow it,
	// and so do ties in quality and dependency.
	agents = slices.Clone(agents)
	slices.SortStableFunc(agents, func(a, b SpawnedAgent) int {
		if c := cmp.Compare(slotTab(a.Pane), slotTab(b.Pane)); c != 0 {
			return c
		}
		return cmp.Compare(slotPane(a.Pane), slotPane(b.Pane))
	})
	sel := HumanSelection{Skipped: skipped, Actionable: actionable}
	if len(agents) == 0 {
		// No agent can take a bead: every ready bead is left out with ntm's
		// reason, as ntm writes it (the id only).
		for _, bead := range ready {
			sel.Skipped = append(sel.Skipped, HumanSkip{BeadID: bead.ID, Reason: HumanSkipNoPlaceable})
		}
		return sel
	}
	if len(ready) == 0 {
		sel.Agents = agents
		return sel
	}

	planned, deferred := humanPlannedItems(agents, ready, opts, now)
	if deferred && len(planned) == 0 {
		// PlanAllocations deferred everything: every ready bead is left out
		// with ntm's reason.
		for _, bead := range ready {
			sel.Skipped = append(sel.Skipped, HumanSkip{BeadID: bead.ID, BeadTitle: bead.Title, Reason: HumanSkipPressure})
		}
	}
	if opts.Limit > 0 && len(planned) > opts.Limit {
		for _, item := range planned[opts.Limit:] {
			sel.Skipped = append(sel.Skipped, HumanSkip{BeadID: item.bead.ID, BeadTitle: item.bead.Title, Reason: HumanSkipLimit})
		}
		planned = planned[:opts.Limit]
	}

	// One bead per agent, in plan order. ntm sends an agent's first item,
	// and its observation gate then refuses the second, as the agent works
	// (its coordinator's ErrTargetOccupied is only the backstop). nhm
	// decides it before the run, with the gate's words; an agent whose
	// first bead fails gets no second.
	served := map[int]bool{}
	for _, item := range planned {
		sel.Planned = append(sel.Planned, item.bead.ID)
		if served[item.agent] {
			pane := agents[item.agent].Pane
			sel.Failures = append(sel.Failures, HumanFailure{BeadID: item.bead.ID, BeadTitle: item.bead.Title,
				Pane: pane, Error: humanNotIdle(pane)})
			continue
		}
		served[item.agent] = true
		sel.Agents = append(sel.Agents, agents[item.agent])
		sel.Items = append(sel.Items, item.bead)
	}
	for i, a := range agents {
		if !served[i] {
			sel.Agents = append(sel.Agents, a)
		}
	}
	return sel
}

// humanNotIdle is ntm's observation-gate refusal of a pane
// (executeAssignmentsEnhanced).
func humanNotIdle(pane string) string {
	return fmt.Sprintf("pane %s is not freshly and confidently idle", pane)
}

// humanItem is one planned bead for one agent (by index).
type humanItem struct {
	agent int
	bead  WorkItem
}

// humanPlannedItems runs the strategy: PlanAllocations for "" and balanced,
// ntm's legacy switch for anything else. deferred is the planner's defer
// decision. Each item carries ntm's work prompt (the default template of
// promptTemplateSource, as spawn --assign gives no --template).
func humanPlannedItems(agents []SpawnedAgent, ready []bv.BeadPreview, opts HumanOptions, now func() time.Time) (out []humanItem, deferred bool) {
	byID := map[string]bv.BeadPreview{}
	for _, b := range ready {
		byID[b.ID] = b
	}
	workItem := func(beadID, title string, score float64, reason string) WorkItem {
		b := byID[beadID]
		if title == "" {
			title = b.Title
		}
		return WorkItem{ID: beadID, Title: title, Priority: assign.ParsePriorityString(b.Priority), Score: score,
			Type: assign.InferTaskTypeFromBead(b), MatchReason: reason, Prompt: humanWorkPrompt(beadID, title)}
	}
	if assign.UsesAllocationPlanner(opts.Strategy) {
		ids := map[string]int{}
		for i, a := range agents {
			ids[humanAgentID(opts.Session, a)] = i
		}
		plan := assign.PlanAllocations(buildAssignAllocationInput(agents, ready, opts))
		for _, rec := range plan.Recommendations {
			i, ok := ids[rec.AgentID]
			if !ok {
				continue
			}
			out = append(out, humanItem{agent: i, bead: workItem(rec.BeadID, rec.BeadTitle, rec.Score, rec.Reason)})
		}
		return out, plan.Decision == assign.AllocationDecisionDefer
	}
	legacy := make([]assign.LegacyAgent, 0, len(agents))
	bySlot := map[string]int{}
	for i, a := range agents {
		legacy = append(legacy, assign.LegacyAgent{Key: a.Pane, Target: a.Pane, Index: slotPane(a.Pane),
			AgentType: assign.LegacyAgentType(agent.AgentType(a.Type))})
		bySlot[a.Pane] = i
	}
	// A new binding has no active assignment: the load is empty.
	for _, rec := range assign.LegacyAssignments(opts.Strategy, legacy, ready, assign.LegacyLoad{}, now) {
		i, ok := bySlot[rec.Agent.Key]
		if !ok {
			continue
		}
		out = append(out, humanItem{agent: i, bead: workItem(rec.BeadID, rec.BeadTitle, rec.Score, rec.Reasoning)})
	}
	return out, false
}

// humanWorkPrompt is ntm's default assignment prompt (promptTemplateSource's
// default, through renderPromptTemplate).
func humanWorkPrompt(beadID, title string) string {
	return "Work on bead " + beadID + ": " + title + ". Check dependencies first with `br dep tree " + beadID + "`."
}

// ResolveSpawnAssignAgentType is ntm's resolveSpawnAssignAgentType:
// --assign-agent wins, then the first --assign-*-only alias that is set;
// "" assigns to every agent.
func ResolveSpawnAssignAgentType(agentFlag string, ccOnly, codOnly, gmiOnly, agyOnly bool) string {
	if strings.TrimSpace(agentFlag) != "" {
		return ResolveAgentType(agentFlag)
	}
	if ccOnly {
		return "claude"
	}
	if codOnly {
		return "codex"
	}
	if gmiOnly {
		return "gemini"
	}
	if agyOnly {
		return "antigravity"
	}
	return ""
}

// ResolveAgentType maps agent type aliases to canonical names.
// For example: "cc" -> "claude", "cod" -> "codex"
func ResolveAgentType(t string) string {
	trimmed := strings.TrimSpace(t)

	switch agent.AgentType(trimmed).Canonical() {
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
	case agent.AgentTypeUnknown:
		return "unknown"
	default:
		return strings.ToLower(trimmed)
	}
}

// HumanPromptContextLines is ntm's printAssignmentPromptContext: one
// assignment's enrichment in human output, worded like ntm send. It
// returns the lines in place of printing them.
func HumanPromptContextLines(beadID string, cassInjection *robot.CASSInjectionInfo, memoryInjection *robot.CMInjectionInfo) []string {
	var lines []string
	if cassInjection != nil {
		switch {
		case cassInjection.ItemsInjected > 0:
			lines = append(lines, fmt.Sprintf("  CASS context injected for %s: %d item(s), ~%d tokens", beadID, cassInjection.ItemsInjected, cassInjection.TokensAdded))
		case cassInjection.SkippedReason != "":
			lines = append(lines, fmt.Sprintf("  CASS context injection skipped for %s: %s", beadID, cassInjection.SkippedReason))
		}
	}
	if memoryInjection != nil {
		switch {
		case memoryInjection.TokensAdded > 0:
			lines = append(lines, fmt.Sprintf("  CM rules injected for %s: ~%d tokens", beadID, memoryInjection.TokensAdded))
		case memoryInjection.SkippedReason != "":
			lines = append(lines, fmt.Sprintf("  CM rule injection skipped for %s: %s", beadID, memoryInjection.SkippedReason))
		}
	}
	return lines
}

// humanAgentID is ntm's assignAllocationAgentID: <session>_<type>_<pane
// target>, the slot as the target.
func humanAgentID(session string, a SpawnedAgent) string {
	if session != "" {
		return fmt.Sprintf("%s_%s_%s", session, a.Type, a.Pane)
	}
	return fmt.Sprintf("%s_%s", a.Type, a.Pane)
}

// slotPane is a slot's M, ntm's pane index; 0 for a slot that does not read.
func slotPane(slot string) int {
	_, m, ok := strings.Cut(slot, ".")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(m)
	if err != nil {
		return 0
	}
	return n
}

// slotTab is a slot's N; 0 for a slot that does not read.
func slotTab(slot string) int {
	t, _, _ := strings.Cut(slot, ".")
	n, err := strconv.Atoi(t)
	if err != nil {
		return 0
	}
	return n
}

func buildAssignAllocationInput(agents []SpawnedAgent, beads []bv.BeadPreview, opts HumanOptions) assign.AllocationInput {
	session := opts.Session
	allocationAgents := make([]assign.AllocationAgent, 0, len(agents))
	totalActive := opts.ActiveTotal
	totalHeadroom := 0.0
	for _, a := range agents {
		// A new binding: no active assignment; nhm measures no context use
		// or scrollback.
		active := 0
		headroom := assignAgentResourceHeadroom(0, "", active)
		totalHeadroom += headroom
		allocationAgents = append(allocationAgents, assign.AllocationAgent{
			ID:                humanAgentID(session, a),
			Session:           session,
			PaneIndex:         slotPane(a.Pane),
			PaneTarget:        a.Pane,
			PaneID:            a.PaneID,
			AgentType:         agent.AgentType(a.Type).Canonical(),
			Idle:              true,
			ContextUsage:      0,
			ActiveAssignments: active,
			AssignmentLimit:   1,
			ResourceHeadroom:  headroom,
		})
	}
	sessionHeadroom := 0.0
	if len(allocationAgents) > 0 {
		sessionHeadroom = totalHeadroom / float64(len(allocationAgents))
	}
	return assign.AllocationInput{
		ReadyBeads: buildAssignAllocationBeads(beads),
		Agents:     allocationAgents,
		Sessions: []assign.AllocationSession{{
			Name:              session,
			ActiveAssignments: totalActive,
			AssignmentLimit:   totalActive + len(allocationAgents),
			ResourceHeadroom:  sessionHeadroom,
		}},
		Pressure:    humanAllocationPressure,
		Fairness:    assign.AllocationFairness{SessionRecentAssignments: map[string]int{session: opts.ActiveTotal}},
		BVAvailable: true,
	}
}

// humanAllocationPressure stands for ntm's system pressure probe, which
// nhm does not take: the reading ntm gets on a host at normal load
// (collectLiveAssignAllocationPressure, assignPressureAgentHeadroom).
var humanAllocationPressure = assign.AllocationPressure{Available: true, Level: "normal", AgentHeadroom: 8}

func buildAssignAllocationBeads(beads []bv.BeadPreview) []assign.AllocationReadyBead {
	out := make([]assign.AllocationReadyBead, 0, len(beads))
	for _, bead := range beads {
		taskType := assign.TaskType(assign.InferTaskTypeFromBead(bead))
		out = append(out, assign.AllocationReadyBead{
			ID:           bead.ID,
			Title:        bead.Title,
			TaskType:     taskType,
			Priority:     assign.ParsePriorityString(bead.Priority),
			ResourceCost: assignBeadResourceCost(bead, taskType),
		})
	}
	return out
}

func assignBeadResourceCost(bead bv.BeadPreview, taskType assign.TaskType) float64 {
	title := strings.ToLower(bead.Title)
	if strings.Contains(title, "performance") ||
		strings.Contains(title, "load") ||
		strings.Contains(title, "large") ||
		strings.Contains(title, "swarm") ||
		strings.Contains(title, "benchmark") {
		return 0.80
	}
	switch taskType {
	case assign.TaskBug, assign.TaskDocs, assign.TaskDocumentation, assign.TaskTesting, assign.TaskChore:
		return 0.35
	case assign.TaskFeature, assign.TaskRefactor, assign.TaskAnalysis:
		return 0.65
	default:
		return 0.50
	}
}

func assignAgentResourceHeadroom(contextUsage float64, scrollback string, activeAssignments int) float64 {
	contextPenalty := clampAssignScore(contextUsage) * 0.35
	backlogPenalty := assignScrollbackBacklog(scrollback) * 0.25
	activePenalty := min(float64(max(activeAssignments, 0))*0.20, 0.60)
	return clampAssignScore(1.0 - contextPenalty - backlogPenalty - activePenalty)
}

func assignScrollbackBacklog(scrollback string) float64 {
	if scrollback == "" {
		return 0
	}
	linePressure := min(float64(strings.Count(scrollback, "\n"))/800.0, 1.0)
	bytePressure := min(float64(len(scrollback))/200000.0, 1.0)
	return max(linePressure, bytePressure)
}

func clampAssignScore(score float64) float64 {
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}

func partitionActionableRecommendationsForAssignment(recommendations []bv.TriageRecommendation, activeAssignments map[string]struct{},
	operatorGated func(string) bool) ([]bv.BeadPreview, []HumanSkip) {
	ready := make([]bv.BeadPreview, 0, len(recommendations))
	skipped := make([]HumanSkip, 0)
	for _, rec := range recommendations {
		if skip := classifyTriageRecForAssignmentWithGate(rec, activeAssignments, operatorGated); skip != nil {
			skipped = append(skipped, *skip)
			continue
		}
		ready = append(ready, bv.BeadPreview{
			ID:       rec.ID,
			Title:    rec.Title,
			Priority: fmt.Sprintf("P%d", rec.Priority),
		})
	}
	return ready, skipped
}

func classifyTriageRecForAssignmentWithGate(rec bv.TriageRecommendation, activeAssignments map[string]struct{}, operatorGated func(string) bool) *HumanSkip {
	if len(rec.BlockedBy) > 0 {
		return &HumanSkip{BeadID: rec.ID, BeadTitle: rec.Title, Reason: humanSkipBlocked, BlockedBy: rec.BlockedBy}
	}
	// Container types (epic) are grouping nodes, not implementation work.
	if bv.IsContainerBeadType(rec.Type) {
		return &HumanSkip{BeadID: rec.ID, BeadTitle: rec.Title, Reason: humanSkipContainer}
	}
	for _, label := range rec.Labels {
		if operatorGated(label) {
			return &HumanSkip{BeadID: rec.ID, BeadTitle: rec.Title, Reason: humanSkipGated}
		}
	}
	switch normalizeBeadStatus(rec.Status) {
	case "open", "ready":
	case "in_progress":
		return &HumanSkip{BeadID: rec.ID, BeadTitle: rec.Title, Reason: humanSkipInProgress}
	case "blocked":
		return &HumanSkip{BeadID: rec.ID, BeadTitle: rec.Title, Reason: humanSkipBlockedState}
	case "closed", "resolved", "done":
		return &HumanSkip{BeadID: rec.ID, BeadTitle: rec.Title, Reason: humanSkipClosed}
	default:
		return &HumanSkip{BeadID: rec.ID, BeadTitle: rec.Title, Reason: humanSkipNotOpen}
	}
	if _, ok := activeAssignments[rec.ID]; ok {
		return &HumanSkip{BeadID: rec.ID, BeadTitle: rec.Title, Reason: humanSkipAssigned}
	}
	return nil
}

func normalizeBeadStatus(status string) string {
	canonical := strings.ToLower(strings.TrimSpace(status))
	canonical = strings.ReplaceAll(canonical, "-", "_")
	canonical = strings.ReplaceAll(canonical, " ", "_")
	return canonical
}

// validCycles is CheckCycles' validation: a cycle has two nodes or more,
// none twice.
func validCycles(cycles [][]string) [][]string {
	var valid [][]string
	for _, cycle := range cycles {
		if len(cycle) < 2 {
			continue
		}
		seen := map[string]bool{}
		ok := true
		for _, node := range cycle {
			if seen[node] {
				ok = false
				break
			}
			seen[node] = true
		}
		if ok {
			valid = append(valid, cycle)
		}
	}
	return valid
}

// IsBeadInCycle checks if a bead ID is part of any detected dependency cycle.
func IsBeadInCycle(beadID string, cycles [][]string) bool {
	for _, cycle := range cycles {
		for _, id := range cycle {
			if id == beadID {
				return true
			}
		}
	}
	return false
}
