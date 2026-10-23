// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/assign.go at commit 9d5ed977, with
// assignmentDependencyMissing from internal/robot/bulk_assign.go; see
// LICENSE in this directory and PROVENANCE.md. Local changes: this header;
// package and imports. --robot-assign's code (nhm-y55): AssignOptions
// gains Ledger, the instance id that names the assignment ledger, and its
// Session is a herdr workspace;
// GetAssign takes nhm's ports (RobotAssignDependencies), leaves the
// session's check to the caller, which resolved its workspace, and reads
// the Beads work through the ports; observeAssignAgents and
// excludeDurablyOccupiedAssignAgents are nhm's (robot_assign.go), so
// assignAgentInfo gains occupancy, the binding id the ledger keys by;
// NewRobotResponse and NewErrorResponse are nhm's newRobotResponse and
// newErrorResponse; the session hint names nhm in place of ntm;
// generateAssignHints suggests no command, as nhm has no
// --robot-bulk-assign; staleWorkHint is spawnassign's; UnassignableBead and
// the other output types are internal/robot's (aliases in nhm.go). Not
// taken: PrintAssign (the CLI writes the answer), DistributeRecommendation
// and GetAssignRecommendations (ntm's distribute mode),
// getAssignableActionableRecommendations, assignAgentsFromObservation
// (tmux's observation) and loadAuthoritativeAssignmentPolicy (nhm's is
// LoadPolicy).

package spawnassign

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/assignment"
	"github.com/arosl/nhm/internal/core/spawnassign/assign"
	"github.com/arosl/nhm/internal/integrations/bv"
	"github.com/arosl/nhm/internal/integrations/worksource"
	"github.com/arosl/nhm/internal/robot"
)

// AssignOptions configures work assignment analysis
type AssignOptions struct {
	Session    string   // the session as given (a herdr workspace)
	ProjectDir string   // Explicit project directory for Beads reads
	Beads      []string // Specific bead IDs to assign (empty = all ready)
	Strategy   string   // simple, balanced, speed, quality, dependency
	// Ledger is the instance id that names the assignment ledger (nhm).
	Ledger string
}

// assignStrategyDefault is the strategy used when none is requested.
// "simple" is the honest name for the historical sequential pairing
// (next ready bead -> next idle agent); every other strategy routes
// through the real planner in internal/assign. Pinned by test: changing
// this default silently changes assignment output for users who never
// pass --strategy / --dist-strategy.
const assignStrategyDefault = "simple"

// assignStrategyNames lists the valid robot assignment strategies.
var assignStrategyNames = []string{"simple", "balanced", "speed", "quality", "dependency"}

// normalizeAssignStrategy lowercases the requested strategy and applies
// the pinned default when empty.
func normalizeAssignStrategy(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return assignStrategyDefault
	}
	return s
}

// isValidAssignStrategy reports whether s is a recognized strategy name.
func isValidAssignStrategy(s string) bool {
	for _, name := range assignStrategyNames {
		if s == name {
			return true
		}
	}
	return false
}

// AgentStrength returns the task type affinity score for an agent/task combination.
// This delegates to the assign package's capability matrix which supports
// configuration overrides and learned score adjustments.
func AgentStrength(agentType, taskType string) float64 {
	return assign.GetAgentScoreByString(agentType, taskType)
}

// unblocksIndex extracts the dependency-graph "unblocks" signal from triage
// recommendations, keyed by bead ID, for the graph-aware planner strategies.
func unblocksIndex(recommendations []bv.TriageRecommendation) map[string][]string {
	index := make(map[string][]string, len(recommendations))
	for _, recommendation := range recommendations {
		if len(recommendation.UnblocksIDs) > 0 {
			index[recommendation.ID] = recommendation.UnblocksIDs
		}
	}
	return index
}

// GetAssign generates work assignment recommendations and returns the result.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetAssign(ctx context.Context, opts AssignOptions, deps *RobotAssignDependencies) (*AssignOutput, error) {
	output := &AssignOutput{
		RobotResponse:   newRobotResponse(true),
		Session:         opts.Session,
		Recommendations: make([]AssignRecommend, 0),
		BlockedBeads:    make([]BlockedBead, 0),
		IdleAgents:      []string{},
	}
	if ctx == nil {
		return nil, fmt.Errorf("robot assignment context is required")
	}
	if err := ctx.Err(); err != nil {
		output.RobotResponse = newErrorResponse(err, ErrCodeTimeout, "Retry the command after cancellation")
		return output, nil
	}

	if opts.Session == "" {
		output.RobotResponse = newErrorResponse(
			fmt.Errorf("session name is required"),
			robot.ErrCodeInvalidFlag,
			"Provide session name: nhm --robot-assign=myproject",
		)
		return output, nil
	}

	// Normalize strategy
	strategy := normalizeAssignStrategy(opts.Strategy)
	if !isValidAssignStrategy(strategy) {
		output.RobotResponse = newErrorResponse(
			fmt.Errorf("invalid strategy '%s'", opts.Strategy),
			robot.ErrCodeInvalidFlag,
			"Valid strategies: "+strings.Join(assignStrategyNames, ", "),
		)
		return output, nil
	}

	output.Strategy = strategy
	output.GeneratedAt = time.Now().UTC()

	d := robotAssignDeps(deps)
	agents, idleAgentPanes, err := observeAssignAgents(ctx, opts.Session, d)
	if err != nil {
		setAssignError(output, fmt.Errorf("failed to observe assignment candidates: %w", err), "Retry after pane state can be observed freshly and confidently")
		return output, nil
	}
	idleAgentPanes, err = excludeDurablyOccupiedAssignAgents(opts.Ledger, d, agents, idleAgentPanes)
	if err != nil {
		output.RobotResponse = newErrorResponse(
			fmt.Errorf("failed to exclude active assignment occupancy: %w", err),
			robot.ErrCodeInternalError,
			"Repair or migrate the durable assignment ledger before distributing more work",
		)
		return output, nil
	}

	output.IdleAgents = idleAgentPanes

	projectDir, err := assignOptionsProjectDir(opts)
	if err != nil {
		output.RobotResponse = newErrorResponse(err, robot.ErrCodeInternalError, "Provide a readable project directory for Beads")
		return output, nil
	}
	actionable, err := d.GetActionable(ctx, projectDir, 0)
	if err != nil {
		setAssignError(output, fmt.Errorf("read actionable Beads work: %w", err), "Ensure bv and br can verify the target project's actionable work and labels")
		return output, nil
	}
	inProgress, err := d.GetInProgress(ctx, projectDir, 50)
	if err != nil {
		setAssignError(output, fmt.Errorf("read in-progress Beads work: %w", err), "Ensure br can read the target project's Beads database")
		return output, nil
	}

	// Filter to specific beads if requested
	if len(opts.Beads) > 0 {
		beadSet := make(map[string]bool)
		for _, b := range opts.Beads {
			beadSet[b] = true
		}
		var filtered []bv.TriageRecommendation
		for _, recommendation := range actionable {
			if beadSet[recommendation.ID] {
				filtered = append(filtered, recommendation)
			}
		}
		actionable = filtered
	}
	assignable, unassignable := classifyAssignableRecommendationsForProject(projectDir, actionable, 50)
	readyBeads := filterAssignableBeadPreviewsForProject(projectDir, assignable, 0)

	// Build working agents set from in-progress beads
	workingAgents := len(agents) - len(idleAgentPanes)

	// Generate recommendations through the strategy planner, preserving the
	// dependency graph (unblocks) signal for the graph-aware strategies.
	recommendations := planAssignments(agents, readyBeads, unblocksIndex(assignable), strategy, idleAgentPanes)
	output.Recommendations = recommendations
	unassignable = append(unassignable, unassignedBeadsBeyondRecommendations(readyBeads, recommendations, len(idleAgentPanes))...)
	output.UnassignableBeads = unassignable

	// Add blocked beads (beads with unmet dependencies)
	blockedBeads, err := d.GetBlocked(ctx, projectDir, 20)
	if err != nil {
		setAssignError(output, fmt.Errorf("read blocked Beads work: %w", err), "Ensure br can read the target project's Beads database")
		return output, nil
	}
	output.BlockedBeads, err = resolveAssignBlockedBeads(ctx, projectDir, blockedBeads, d.GetDetails)
	if err != nil {
		setAssignError(output, err, "Ensure br can read blocked-bead dependencies")
		return output, nil
	}

	// Build summary
	output.Summary = AssignSummary{
		TotalAgents:       len(agents),
		IdleAgents:        len(idleAgentPanes),
		WorkingAgents:     workingAgents,
		ReadyBeads:        len(readyBeads),
		BlockedBeads:      len(output.BlockedBeads),
		Recommendations:   len(recommendations),
		UnassignableBeads: len(output.UnassignableBeads),
	}

	// Generate agent hints
	output.AgentHints = generateAssignHints(opts.Session, recommendations, idleAgentPanes, readyBeads, inProgress)

	return output, nil
}

func resolveAssignBlockedBeads(
	ctx context.Context,
	projectDir string,
	previews []bv.BeadPreview,
	getDetails func(context.Context, string, string) (*bv.BeadAssignmentDetails, error),
) ([]BlockedBead, error) {
	blocked := make([]BlockedBead, 0, len(previews))
	for _, preview := range previews {
		details, err := getDetails(ctx, projectDir, preview.ID)
		if err != nil {
			return nil, fmt.Errorf("read blocked bead %s details: %w", preview.ID, err)
		}
		if details == nil {
			return nil, fmt.Errorf("read blocked bead %s details: empty response", preview.ID)
		}

		title := preview.Title
		if strings.TrimSpace(details.Title) != "" {
			title = details.Title
		}
		blocked = append(blocked, BlockedBead{
			ID:        preview.ID,
			Title:     title,
			BlockedBy: append([]string{}, details.BlockedBy...),
		})
	}
	return blocked, nil
}

// staleWorkError reports whether err is a work-source mismatch, with the code
// and hint every assignment surface returns for one.
func staleWorkError(err error) (string, string, bool) {
	if errors.Is(err, worksource.ErrStale) {
		return ErrCodeStaleWorkCoordination, staleWorkHint, true
	}
	return "", "", false
}

// staleWorkReceipt is the remediation receipt of a work-source mismatch: the
// expected and observed JSONL digest and HEAD, as far as the source layer
// recorded them. Assignment surfaces report it as work_source_mismatch.
func staleWorkReceipt(err error) *worksource.StaleError {
	var receipt *worksource.StaleError
	if errors.As(err, &receipt) {
		return receipt
	}
	return nil
}

func setAssignError(output *AssignOutput, err error, hint string) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		output.RobotResponse = newErrorResponse(err, ErrCodeTimeout, "Retry the command after cancellation")
		return
	}
	if code, staleHint, ok := staleWorkError(err); ok {
		output.RobotResponse = newErrorResponse(err, code, staleHint)
		output.WorkSourceMismatch = staleWorkReceipt(err)
		return
	}
	if assignmentDependencyMissing(err) {
		output.RobotResponse = newErrorResponse(err, robot.ErrCodeDependencyMissing, hint)
		return
	}
	output.RobotResponse = newErrorResponse(err, robot.ErrCodeInternalError, hint)
}

// assignmentDependencyMissing is ntm's (internal/robot/bulk_assign.go): bv
// or br is not installed.
func assignmentDependencyMissing(err error) bool {
	if errors.Is(err, bv.ErrNotInstalled) || errors.Is(err, exec.ErrNotFound) {
		return true
	}
	var executableError *exec.Error
	return errors.As(err, &executableError)
}

// assignAgentInfo holds agent data for assignment processing
type assignAgentInfo struct {
	paneID     string
	paneTarget string
	agentType  string
	model      string
	state      string
	// occupancy is the agent's binding id, the ledger's occupancy key (nhm).
	occupancy string
}

func assignOptionsProjectDir(opts AssignOptions) (string, error) {
	if projectDir := strings.TrimSpace(opts.ProjectDir); projectDir != "" {
		return projectDir, nil
	}
	projectDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve assignment project directory: %w", err)
	}
	return projectDir, nil
}

func filterAssignableBeadPreviewsForProject(projectDir string, recommendations []bv.TriageRecommendation, limit int) []bv.BeadPreview {
	return filterAssignableBeadPreviewsWithGate(recommendations, limit, func(label string) bool {
		return bv.IsOperatorGatedLabelForProject(projectDir, label)
	})
}

func filterAssignableBeadPreviewsWithGate(recommendations []bv.TriageRecommendation, limit int, operatorGated func(string) bool) []bv.BeadPreview {
	assignable := filterAssignableActionableRecommendationsWithGate(recommendations, limit, operatorGated)
	previews := make([]bv.BeadPreview, 0, len(assignable))
	for _, recommendation := range assignable {
		previews = append(previews, bv.BeadPreview{
			ID:       recommendation.ID,
			Title:    recommendation.Title,
			Priority: fmt.Sprintf("P%d", recommendation.Priority),
			Type:     recommendation.Type,
		})
	}
	return previews
}

func filterAssignableActionableRecommendationsForProject(projectDir string, recommendations []bv.TriageRecommendation, limit int) []bv.TriageRecommendation {
	return filterAssignableActionableRecommendationsWithGate(recommendations, limit, func(label string) bool {
		return bv.IsOperatorGatedLabelForProject(projectDir, label)
	})
}

func filterAssignableActionableRecommendationsWithGate(recommendations []bv.TriageRecommendation, limit int, operatorGated func(string) bool) []bv.TriageRecommendation {
	filtered, _ := classifyAssignableActionableRecommendationsWithGate(recommendations, limit, operatorGated)
	return filtered
}

func classifyAssignableRecommendationsForProject(projectDir string, recommendations []bv.TriageRecommendation, limit int) ([]bv.TriageRecommendation, []UnassignableBead) {
	return classifyAssignableActionableRecommendationsWithGate(recommendations, limit, func(label string) bool {
		return bv.IsOperatorGatedLabelForProject(projectDir, label)
	})
}

// unassignedBeadsBeyondRecommendations reports ready beads the planner left
// unassigned. The planner may skip earlier beads in favor of better graph or
// capability matches, so this is keyed by bead ID rather than by position.
// idleAgents is how many idle agents the planner had: when idle agents remain
// beyond the recommendation count, the honest reason is that the planner's
// confidence gate skipped the bead, not that no agent was available.
func unassignedBeadsBeyondRecommendations(readyBeads []bv.BeadPreview, recommendations []AssignRecommend, idleAgents int) []UnassignableBead {
	if len(recommendations) >= len(readyBeads) {
		return nil
	}
	assigned := make(map[string]struct{}, len(recommendations))
	for _, recommendation := range recommendations {
		assigned[recommendation.AssignBead] = struct{}{}
	}
	reason := "no idle agent available"
	if len(recommendations) < idleAgents {
		reason = "skipped by planner: no agent matched above the confidence threshold"
	}

	unassignable := make([]UnassignableBead, 0, len(readyBeads)-len(recommendations))
	for _, bead := range readyBeads {
		if _, ok := assigned[bead.ID]; ok {
			continue
		}
		unassignable = append(unassignable, UnassignableBead{ID: bead.ID, Title: bead.Title, Reason: reason})
	}
	return unassignable
}

func classifyAssignableActionableRecommendationsWithGate(recommendations []bv.TriageRecommendation, limit int, operatorGated func(string) bool) ([]bv.TriageRecommendation, []UnassignableBead) {
	filtered := make([]bv.TriageRecommendation, 0, len(recommendations))
	unassignable := make([]UnassignableBead, 0)
	seen := make(map[string]struct{}, len(recommendations))
	for _, recommendation := range recommendations {
		beadID := strings.TrimSpace(recommendation.ID)
		if beadID == "" {
			unassignable = append(unassignable, UnassignableBead{Title: strings.TrimSpace(recommendation.Title), Reason: "missing bead ID"})
			continue
		}
		if len(recommendation.BlockedBy) > 0 {
			unassignable = append(unassignable, UnassignableBead{ID: beadID, Title: strings.TrimSpace(recommendation.Title), Reason: "blocked by " + strings.Join(recommendation.BlockedBy, ", ")})
			continue
		}

		gatedLabel := ""
		for _, label := range recommendation.Labels {
			if operatorGated(label) {
				gatedLabel = strings.TrimSpace(label)
				break
			}
		}
		if gatedLabel != "" {
			unassignable = append(unassignable, UnassignableBead{ID: beadID, Title: strings.TrimSpace(recommendation.Title), Reason: "operator-gated label: " + gatedLabel})
			continue
		}

		status := strings.ToLower(strings.TrimSpace(recommendation.Status))
		if status != "open" && status != "ready" {
			unassignable = append(unassignable, UnassignableBead{ID: beadID, Title: strings.TrimSpace(recommendation.Title), Reason: "status is " + status})
			continue
		}
		if _, duplicate := seen[beadID]; duplicate {
			unassignable = append(unassignable, UnassignableBead{ID: beadID, Title: strings.TrimSpace(recommendation.Title), Reason: "duplicate recommendation"})
			continue
		}
		if limit > 0 && len(filtered) >= limit {
			unassignable = append(unassignable, UnassignableBead{ID: beadID, Title: strings.TrimSpace(recommendation.Title), Reason: "recommendation limit reached"})
			continue
		}
		seen[beadID] = struct{}{}

		recommendation.ID = beadID
		recommendation.Title = strings.TrimSpace(recommendation.Title)
		recommendation.Type = strings.TrimSpace(recommendation.Type)
		filtered = append(filtered, recommendation)
	}
	return filtered, unassignable
}

func filterDurablyOccupiedAssignAgents(idleAgentPanes []string, activeAssignments []*assignment.Assignment) ([]string, error) {
	occupied := make(map[string]struct{}, len(activeAssignments))
	for _, current := range activeAssignments {
		if current == nil {
			continue
		}
		paneID, err := assignment.CanonicalPaneIdentity(current)
		if err != nil {
			return nil, fmt.Errorf("active assignment %s: %w", current.BeadID, err)
		}
		occupied[paneID] = struct{}{}
	}
	available := make([]string, 0, len(idleAgentPanes))
	for _, paneID := range idleAgentPanes {
		if _, active := occupied[strings.TrimSpace(paneID)]; !active {
			available = append(available, paneID)
		}
	}
	return available, nil
}

// planAssignments routes assignment planning through the requested strategy.
// The "simple" strategy is the honest name for sequential pairing (next ready
// bead -> next idle agent, no scoring). Every other strategy runs the real
// planner in internal/assign, which scores agent/bead pairs against the
// capability matrix and, for "dependency", the bead graph's unblocks fan-out.
func planAssignments(agents []assignAgentInfo, beads []bv.BeadPreview, unblocks map[string][]string, strategy string, idleAgents []string) []AssignRecommend {
	// Create a map of idle agents for quick lookup
	idleSet := make(map[string]bool)
	for _, a := range idleAgents {
		idleSet[a] = true
	}

	// Get idle agent details
	var idleAgentDetails []assignAgentInfo
	for _, a := range agents {
		if idleSet[a.paneID] {
			idleAgentDetails = append(idleAgentDetails, a)
		}
	}

	if normalizeAssignStrategy(strategy) == "simple" {
		return sequentialAssignments(idleAgentDetails, beads)
	}
	return plannerAssignments(idleAgentDetails, beads, unblocks, strategy)
}

// sequentialAssignments implements the "simple" strategy: pair beads with
// idle agents in order, with no strategy scoring. Confidence is the raw
// capability score for the pairing and the reasoning says exactly that.
func sequentialAssignments(idleAgentDetails []assignAgentInfo, beads []bv.BeadPreview) []AssignRecommend {
	var recommendations []AssignRecommend
	beadIdx := 0
	for _, agent := range idleAgentDetails {
		if beadIdx >= len(beads) {
			break // No more beads to assign
		}

		bead := beads[beadIdx]
		confidence := calculateConfidence(agent.agentType, bead)
		reasoning := generateReasoning(agent.agentType, bead)

		recommendations = append(recommendations, AssignRecommend{
			PaneID:     agent.paneID,
			PaneTarget: agent.paneTarget,
			AgentType:  agent.agentType,
			Model:      agent.model,
			AssignBead: bead.ID,
			BeadTitle:  bead.Title,
			Priority:   bead.Priority,
			Confidence: confidence,
			Reasoning:  reasoning,
		})

		beadIdx++
	}

	return recommendations
}

// plannerAssignments routes the graph-aware strategies through the real
// planner in internal/assign and adapts its output back to the robot
// envelope, keeping at most one bead per pane (the bulk-assign contract).
func plannerAssignments(idleAgentDetails []assignAgentInfo, beads []bv.BeadPreview, unblocks map[string][]string, strategy string) []AssignRecommend {
	if len(idleAgentDetails) == 0 || len(beads) == 0 {
		return nil
	}

	agentByID := make(map[string]assignAgentInfo, len(idleAgentDetails))
	planAgents := make([]assign.Agent, 0, len(idleAgentDetails))
	for _, a := range idleAgentDetails {
		agentByID[a.paneID] = a
		planAgents = append(planAgents, assign.Agent{
			ID:        a.paneID,
			AgentType: assign.ParseAgentType(a.agentType),
			Model:     a.model,
			Idle:      true,
		})
	}

	previewByID := make(map[string]bv.BeadPreview, len(beads))
	planBeads := make([]assign.Bead, 0, len(beads))
	for _, b := range beads {
		previewByID[b.ID] = b
		planBeads = append(planBeads, assign.Bead{
			ID:          b.ID,
			Title:       b.Title,
			Priority:    parsePriority(b.Priority),
			TaskType:    planTaskType(b),
			UnblocksIDs: unblocks[b.ID],
		})
	}

	planned := assign.AssignTasksFunc(planBeads, planAgents, strategy)

	recommendations := make([]AssignRecommend, 0, len(planned))
	usedAgents := make(map[string]struct{}, len(planAgents))
	usedBeads := make(map[string]struct{}, len(planBeads))
	for _, p := range planned {
		agent, ok := agentByID[p.Agent.ID]
		if !ok {
			continue
		}
		if _, taken := usedAgents[p.Agent.ID]; taken {
			continue // one recommendation per pane
		}
		if _, taken := usedBeads[p.Bead.ID]; taken {
			continue
		}
		usedAgents[p.Agent.ID] = struct{}{}
		usedBeads[p.Bead.ID] = struct{}{}

		priority := fmt.Sprintf("P%d", p.Bead.Priority)
		title := p.Bead.Title
		if preview, ok := previewByID[p.Bead.ID]; ok {
			priority = preview.Priority
			title = preview.Title
		}
		recommendations = append(recommendations, AssignRecommend{
			PaneID:     agent.paneID,
			PaneTarget: agent.paneTarget,
			AgentType:  agent.agentType,
			Model:      agent.model,
			AssignBead: p.Bead.ID,
			BeadTitle:  title,
			Priority:   priority,
			Confidence: p.Confidence,
			Reasoning:  p.Reason,
		})
	}

	return recommendations
}

// planTaskType resolves the capability-matrix task type for a bead. An
// explicit bead type wins unless it is the generic "task", in which case
// the title heuristics may find something more specific.
func planTaskType(bead bv.BeadPreview) assign.TaskType {
	if t := strings.ToLower(strings.TrimSpace(bead.Type)); t != "" && t != "task" {
		return assign.ParseTaskType(t)
	}
	return assign.ParseTaskType(inferTaskType(bead))
}

// calculateConfidence determines assignment confidence for the "simple"
// strategy: the raw capability score for the agent/task pairing.
func calculateConfidence(agentType string, bead bv.BeadPreview) float64 {
	return AgentStrength(agentType, inferTaskType(bead))
}

// inferTaskType attempts to determine task type from bead metadata
func inferTaskType(bead bv.BeadPreview) string {
	title := strings.ToLower(bead.Title)

	// Check for common keywords in priority order
	// Order matters! Check specific types before generic ones.
	type rule struct {
		typ string
		kws []string
	}

	rules := []rule{
		{"bug", []string{"bug", "fix", "broken", "error", "crash"}},
		{"testing", []string{"test", "spec", "coverage"}},
		{"documentation", []string{"doc", "readme", "comment", "documentation"}},
		{"refactor", []string{"refactor", "cleanup", "improve", "consolidate"}},
		{"analysis", []string{"analyze", "investigate", "research", "design"}},
		{"feature", []string{"feature", "implement", "add", "new"}},
	}

	for _, r := range rules {
		for _, kw := range r.kws {
			if strings.Contains(title, kw) {
				return r.typ
			}
		}
	}

	return "task" // Default
}

// parsePriority converts "P0"-"P4" to integer
func parsePriority(p string) int {
	if len(p) == 2 && p[0] == 'P' {
		if n := p[1] - '0'; n <= 4 { // n is byte (unsigned), so >= 0 is always true
			return int(n)
		}
	}
	return 2 // Default to P2
}

// generateReasoning creates an honest explanation for a "simple" strategy
// assignment: sequential pairing, annotated with match and priority context.
func generateReasoning(agentType string, bead bv.BeadPreview) string {
	taskType := inferTaskType(bead)
	priority := parsePriority(bead.Priority)

	var reasons []string

	// Add task-agent match reasoning
	strength := AgentStrength(agentType, taskType)
	if strength >= 0.8 {
		reasons = append(reasons, fmt.Sprintf("%s excels at %s tasks", agentType, taskType))
	}

	// Add priority reasoning
	switch priority {
	case 0:
		reasons = append(reasons, "critical priority")
	case 1:
		reasons = append(reasons, "high priority")
	}

	// Simple strategy is sequential and does not score pairings.
	reasons = append(reasons, "simple sequential pairing (next ready bead to next idle agent)")

	return strings.Join(reasons, "; ")
}

// generateAssignHints creates actionable hints for AI agents
func generateAssignHints(session string, recs []AssignRecommend, idleAgents []string, readyBeads []bv.BeadPreview, inProgress []bv.BeadInProgress) *AssignAgentHints {
	hints := &AssignAgentHints{}

	// Build summary
	if len(recs) == 0 && len(readyBeads) == 0 {
		hints.Summary = "No work available to assign"
	} else if len(recs) == 0 && len(idleAgents) == 0 {
		hints.Summary = fmt.Sprintf("%d beads ready but no idle agents available", len(readyBeads))
	} else if len(recs) > 0 {
		hints.Summary = fmt.Sprintf("%d assignments recommended for %d idle agents", len(recs), len(idleAgents))
	}

	// ntm suggests an `ntm --robot-bulk-assign=<session> --allocation=...`
	// command per recommendation. nhm has no --robot-bulk-assign, so it
	// suggests none (nhm-y55).

	// Add warnings
	if len(readyBeads) > len(idleAgents) {
		diff := len(readyBeads) - len(idleAgents)
		hints.Warnings = append(hints.Warnings,
			fmt.Sprintf("%d beads won't be assigned - not enough idle agents", diff))
	}

	if len(inProgress) > 0 {
		staleCount := 0
		for _, b := range inProgress {
			if time.Since(b.UpdatedAt) > 24*time.Hour {
				staleCount++
			}
		}
		if staleCount > 0 {
			hints.Warnings = append(hints.Warnings,
				fmt.Sprintf("%d in-progress beads are stale (>24h since update)", staleCount))
		}
	}

	return hints
}
