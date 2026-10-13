// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/spawn.go at commit 9d5ed977: composeSpawnWorkPrompt,
// spawnCancellationError, normalizeAssignStrategyStrict,
// assignWorkToAgentsWithError through finalizeSpawnAssignmentOutput,
// enrichSpawnWorkPrompt through spawnDurableAssignmentReplay, and workItem
// through generateWorkPrompt; see LICENSE in this directory and
// PROVENANCE.md. Local changes, each at the backend boundary (nhm-i85, ACFS
// acfs-f2mc.2): this header; package and imports; ntm's config.Config is
// the strictly loaded Policy; the topology is nhm's verified occupants
// (ListOccupants, resolved by slot) in place of tmux panes, the occupancy
// key is the binding id and the dispatch target "<binding id>@<generation>"
// (Occupant.Target), the agent's claim name its binding's Mail name, the
// legacy pane index 0, the pane reference the slot; the dispatch-time
// observation is ObserveOccupant (nhm's delivery gate) in place of tmux's
// SafeToDispatch; delivery is the Dispatch port (nhm's send core) in place
// of the tmux dispatch service and its redaction config; ntm's Agent Mail
// reservation branch is not taken (a reservation request is refused before
// the loop); the coverage key is the slot; replays compare the occupancy
// key with the binding id; enrichSpawnWorkPrompt prepares the context
// through nhm's enrichment for the occupant and the intent's key
// (prepared.go); an optional per-occupant Preamble goes ahead of the
// initial prompt (occupantInitialPrompt), and an optional SelectWork picks
// the work in place of the strategies (selectedWork), each item with its
// own work prompt if it has one (workItem.prompt, workPromptFor) (nhm-cvw).

package spawnassign

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/assignment"
	"github.com/arosl/nhm/internal/core/spawnassign/assign"
	"github.com/arosl/nhm/internal/integrations/bv"
)

// composeSpawnWorkPrompt prefixes a work-assignment prompt with the initial
// --spawn-prompt so both reach the agent in one paced, atomic delivery.
func composeSpawnWorkPrompt(initialPrompt, workPrompt string) string {
	if strings.TrimSpace(initialPrompt) == "" {
		return workPrompt
	}
	return initialPrompt + "\n\n" + workPrompt
}

func spawnCancellationError(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

func normalizeAssignStrategyStrict(strategy string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(strategy))
	switch s {
	case "top-n", "topn":
		return "top-n", nil
	case "diverse":
		return "diverse", nil
	case "dependency-aware", "dependency":
		return "dependency-aware", nil
	case "skill-matched", "skill":
		return "skill-matched", nil
	case "":
		return "", errors.New("assignment strategy is required when --spawn-assign-work is enabled")
	default:
		return "", fmt.Errorf("unsupported assignment strategy %q (expected top-n, diverse, dependency-aware, or skill-matched)", strategy)
	}
}

// assignWorkToAgentsWithError claims and dispatches one work item per agent.
// Each agent's atomic claim+dispatch waits for its stagger slot (pacer; nil
// never waits) so work prompts reach agents paced like `ntm spawn` prompts,
// and initialPrompt, when set, prefixes every work prompt.
func assignWorkToAgentsWithError(ctx context.Context, output *SpawnOutput, workDir, session, strategy string, cfg *Policy, requireReservation bool, reservationPaths []string, customDeps *SpawnAssignmentDependencies, verifiedPlan *bv.TriageResponse, initialPrompt string, promptContext PromptContextOptions, pacer *spawnStaggerPacer) ([]SpawnAssignment, error) {
	var assignments []SpawnAssignment
	if ctx == nil {
		return []SpawnAssignment{{ClaimError: "spawn assignment context is required"}}, nil
	}
	if err := ctx.Err(); err != nil {
		return []SpawnAssignment{{ClaimError: fmt.Sprintf("spawn assignment canceled: %v", err)}}, err
	}
	deps := spawnAssignmentDeps(customDeps)

	// Get non-user agents that are ready
	var readyAgents []SpawnedAgent
	for _, agent := range output.Agents {
		if agent.Type == "user" {
			continue
		}
		// Include agents even if not marked ready (best effort)
		readyAgents = append(readyAgents, agent)
	}

	if len(readyAgents) == 0 {
		return assignments, nil
	}
	if requireReservation || len(reservationPaths) > 0 {
		return spawnAgentPlanErrors(readyAgents, errReservationsNotTaken), nil
	}

	// Production preflights the verified actionable plan before creating the
	// session. Focused helper tests may still inject a complete triage fixture.
	triage := verifiedPlan
	if triage == nil {
		var err error
		triage, err = deps.FetchTriage(ctx, workDir)
		if err != nil {
			wrapped := fmt.Errorf("load bv triage: %w", err)
			return spawnAgentPlanErrors(readyAgents, wrapped), spawnCancellationError(ctx, wrapped)
		}
	}
	if triage == nil {
		return spawnAgentPlanErrors(readyAgents, errors.New("load bv triage: empty response")), nil
	}
	if err := ctx.Err(); err != nil {
		return spawnAgentPlanErrors(readyAgents, fmt.Errorf("spawn assignment canceled after triage: %w", err)), err
	}

	// Get work items based on strategy, or nhm's SelectWork
	var workItems []workItem
	if deps.SelectWork != nil {
		readyAgents, workItems = selectedWork(deps, triage, readyAgents)
	} else {
		workItems = getWorkItemsForStrategy(triage, strategy, readyAgents)
	}
	if len(workItems) == 0 {
		ledgerExists, err := deps.AssignmentLedgerExists(session)
		if err != nil {
			wrapped := fmt.Errorf("inspect assignment ledger: %w", err)
			return spawnAgentPlanErrors(readyAgents, wrapped), spawnCancellationError(ctx, wrapped)
		}
		if !ledgerExists {
			return assignments, nil
		}
		store, err := deps.LoadStore(session)
		if err != nil {
			wrapped := fmt.Errorf("load assignment ledger for replay: %w", err)
			return spawnAgentPlanErrors(readyAgents, wrapped), spawnCancellationError(ctx, wrapped)
		}
		if err := ctx.Err(); err != nil {
			return spawnAgentPlanErrors(readyAgents, fmt.Errorf("spawn assignment canceled after replay ledger load: %w", err)), err
		}
		occupants, err := deps.ListOccupants(ctx, session)
		if err != nil {
			wrapped := fmt.Errorf("load pane topology for assignment replay: %w", err)
			return spawnAgentPlanErrors(readyAgents, wrapped), spawnCancellationError(ctx, wrapped)
		}
		if err := ctx.Err(); err != nil {
			return spawnAgentPlanErrors(readyAgents, fmt.Errorf("spawn assignment canceled after replay topology load: %w", err)), err
		}
		return spawnDurableAssignmentReplays(
			ctx, workDir, readyAgents, occupants, store, requireReservation, reservationPaths, deps,
		)
	}

	store, err := deps.LoadStore(session)
	if err != nil {
		wrapped := fmt.Errorf("load assignment ledger: %w", err)
		return spawnAssignmentPlanErrors(readyAgents, workItems, wrapped), spawnCancellationError(ctx, wrapped)
	}
	if err := ctx.Err(); err != nil {
		return spawnAssignmentPlanErrors(readyAgents, workItems, fmt.Errorf("spawn assignment canceled after ledger load: %w", err)), err
	}
	dispatchPort := deps.Dispatch
	if dispatchPort == nil || deps.ObserveOccupant == nil || deps.ListOccupants == nil {
		return spawnAssignmentPlanErrors(readyAgents, workItems, errors.New("spawn assignment delivery ports are unavailable")), nil
	}
	if deps.ClaimBeadWithOperatorGatedLabels == nil {
		return spawnAssignmentPlanErrors(readyAgents, workItems, errors.New("spawn assignment guarded claim policy is unavailable")), nil
	}
	operatorGatedLabels := []string(nil)
	if cfg != nil {
		operatorGatedLabels = append(operatorGatedLabels, cfg.OperatorGatedLabels...)
	}
	claimPort := newRobotAtomicClaimPort(workDir, func(ctx context.Context, dir, beadID, actor string) (bv.BeadClaimResult, error) {
		return deps.ClaimBeadWithOperatorGatedLabels(ctx, dir, beadID, actor, operatorGatedLabels)
	})
	occupants, err := deps.ListOccupants(ctx, session)
	if err != nil {
		wrapped := fmt.Errorf("load pane topology: %w", err)
		return spawnAssignmentPlanErrors(readyAgents, workItems, wrapped), spawnCancellationError(ctx, wrapped)
	}
	if err := ctx.Err(); err != nil {
		return spawnAssignmentPlanErrors(readyAgents, workItems, fmt.Errorf("spawn assignment canceled after topology load: %w", err)), err
	}
	var terminalErr error
	stopForTerminalError := func(err error) bool {
		cancelErr := spawnCancellationError(ctx, err)
		if cancelErr == nil {
			return false
		}
		terminalErr = cancelErr
		return true
	}

	// Assign work to agents
	for i, agent := range readyAgents {
		if i >= len(workItems) {
			break
		}
		if err := ctx.Err(); err != nil {
			assignments = append(assignments, spawnAgentPlanErrors(readyAgents[i:], fmt.Errorf("spawn assignment canceled: %w", err))...)
			terminalErr = err
			break
		}
		// Hold the whole claim+dispatch until this agent's stagger slot, so the
		// claim is never held across the wait and the dispatch-time observation
		// below is fresh.
		if err := pacer.await(ctx, i); err != nil {
			assignments = append(assignments, spawnAgentPlanErrors(readyAgents[i:], fmt.Errorf("spawn assignment stagger wait: %w", err))...)
			terminalErr = spawnCancellationError(ctx, err)
			break
		}

		item := workItems[i]
		spawnAssignment := SpawnAssignment{
			Pane:         agent.Pane,
			AgentType:    agent.Type,
			BeadID:       item.ID,
			BeadTitle:    item.Title,
			Priority:     fmt.Sprintf("P%d", item.Priority),
			AssignReason: item.MatchReason,
		}
		occupant, resolveErr := resolveOccupant(occupants, agent.Pane)
		if resolveErr != nil {
			spawnAssignment.ClaimError = fmt.Sprintf("resolve pane %s: %v", agent.Pane, resolveErr)
			assignments = append(assignments, spawnAssignment)
			continue
		}
		spawnAssignment.Pane = occupant.Slot
		target := occupant.Target()
		occupancy := occupant.BindingID
		basePrompt := composeSpawnWorkPrompt(occupantInitialPrompt(deps, occupant, initialPrompt), workPromptFor(item))
		prompt := basePrompt
		agentName := ""
		idempotencyKey := ""
		baseIntentSHA256 := ""
		recoveredIntentSHA256 := ""
		// A live row recorded from this same template intent with an enriched
		// prompt is replayed or recovered with its exact recorded prompt;
		// cass and cm are never queried again for it.
		recorded := robotAtomicContextIntent(store, item.ID, target, occupancy, agent.Type, basePrompt, requireReservation, reservationPaths)
		if recorded != nil {
			prompt = bulkRecoveryPrompt(recorded)
			recoveredIntentSHA256 = strings.TrimSpace(recorded.IntentSHA256)
			if recoveredIntentSHA256 == "" {
				recoveredIntentSHA256 = strings.TrimSpace(recorded.PromptSHA256)
			}
		}
		var replay *assignment.Assignment
		switch {
		case recorded != nil && recorded.DispatchState == assignment.DispatchSent:
			replay = recorded
		case recorded == nil:
			replay = robotAtomicReplayIntent(store, item.ID, target, occupancy, 0, agent.Type, prompt, requireReservation, reservationPaths)
		}
		if replay != nil {
			agentName = replay.AgentName
			idempotencyKey = replay.IdempotencyKey
		} else {
			safe, observeErr := deps.ObserveOccupant(ctx, occupant)
			if observeErr != nil {
				spawnAssignment.ClaimError = fmt.Sprintf("observe pane %s before assignment: %v", spawnAssignment.Pane, observeErr)
				assignments = append(assignments, spawnAssignment)
				if stopForTerminalError(observeErr) {
					assignments = append(assignments, spawnAgentPlanErrors(readyAgents[i+1:], observeErr)...)
					break
				}
				continue
			}
			if err := ctx.Err(); err != nil {
				spawnAssignment.ClaimError = fmt.Sprintf("spawn assignment canceled after observation: %v", err)
				assignments = append(assignments, spawnAssignment)
				assignments = append(assignments, spawnAgentPlanErrors(readyAgents[i+1:], err)...)
				terminalErr = err
				break
			}
			if !safe {
				spawnAssignment.ClaimError = fmt.Sprintf("pane %s (%s) is not safe to dispatch", spawnAssignment.Pane, target)
				assignments = append(assignments, spawnAssignment)
				continue
			}

			if recorded != nil {
				// Recover the recorded, not yet delivered, enriched intent
				// under its own identity and key.
				agentName = recorded.AgentName
				idempotencyKey = recorded.IdempotencyKey
			} else {
				agentName = strings.TrimSpace(occupant.MailName)
				if agentName == "" {
					spawnAssignment.ClaimError = fmt.Sprintf("pane %s (%s) has no canonical assignment identity", spawnAssignment.Pane, target)
					assignments = append(assignments, spawnAssignment)
					continue
				}
				var keyErr error
				idempotencyKey, keyErr = robotAtomicIdempotencyKey(
					store, item.ID, target, 0, agent.Type, agentName, prompt,
					requireReservation, reservationPaths, deps.NewIdempotencyKey,
				)
				if keyErr != nil {
					spawnAssignment.ClaimError = keyErr.Error()
					assignments = append(assignments, spawnAssignment)
					if stopForTerminalError(keyErr) {
						assignments = append(assignments, spawnAgentPlanErrors(readyAgents[i+1:], keyErr)...)
						break
					}
					continue
				}
				if promptContext.Enabled() && !robotAtomicLiveKey(store, item.ID, idempotencyKey) {
					// A fresh intent: enrich now, before Execute records it, so
					// the ledger hashes and persists exactly what the agent gets.
					prompt, baseIntentSHA256 = enrichSpawnWorkPrompt(ctx, deps.GetBeadDetails, deps.Enrich, workDir, occupant, idempotencyKey, item, prompt, promptContext, &spawnAssignment)
				}
			}
		}
		spawnAssignment.IdempotencyKey = idempotencyKey
		if err := ctx.Err(); err != nil {
			spawnAssignment.ClaimError = fmt.Sprintf("spawn assignment canceled before atomic claim: %v", err)
			assignments = append(assignments, spawnAssignment)
			assignments = append(assignments, spawnAgentPlanErrors(readyAgents[i+1:], err)...)
			terminalErr = err
			break
		}
		if deps.GetBeadDetails == nil {
			spawnAssignment.ClaimError = "spawn assignment exact eligibility reader is unavailable"
			assignments = append(assignments, spawnAssignment)
			continue
		}
		coordinator := assignment.NewAtomicCoordinator(store, claimPort, nil, dispatchPort, dispatchPort).
			WithWorkItemStatusPort(assignment.WorkItemStatusFunc(func(statusCtx context.Context, beadID string) (string, error) {
				if err := statusCtx.Err(); err != nil {
					return "", err
				}
				return deps.GetBeadStatus(statusCtx, workDir, beadID)
			}))
		coordinator = coordinator.WithAssignmentEligibilityAuthorizationPort(
			newRobotAtomicEligibilityAuthorizationPort(workDir, operatorGatedLabels, deps.GetBeadDetails),
		)
		request := spawnAtomicRequest(
			item, target, occupancy, 0, agent.Type, agentName, prompt, idempotencyKey, requireReservation, reservationPaths,
		)
		request.BaseIntentSHA256 = baseIntentSHA256
		request.RecoveredIntentSHA256 = recoveredIntentSHA256
		result, executeErr := coordinator.Execute(ctx, request)
		if result.Assignment != nil && result.Assignment.IdempotencyKey == idempotencyKey {
			spawnAssignment.Claimed = result.Assignment.ClaimState == assignment.ClaimClaimed
			spawnAssignment.ClaimActor = result.Assignment.ClaimActor
			spawnAssignment.DispatchReceiptID = result.Assignment.DispatchReceiptID
			spawnAssignment.ReservationIDs = append([]int(nil), result.Assignment.ReservationIDs...)
		}
		if executeErr != nil {
			if spawnAssignment.Claimed {
				spawnAssignment.PromptError = executeErr.Error()
			} else {
				spawnAssignment.ClaimError = executeErr.Error()
			}
		} else {
			spawnAssignment.PromptSent = result.Sent
			if result.Sent && !result.Replayed {
				spawnAssignment.DeliveredAt = pacer.stamp()
			}
		}

		assignments = append(assignments, spawnAssignment)
		if stopForTerminalError(executeErr) {
			assignments = append(assignments, spawnAgentPlanErrors(readyAgents[i+1:], executeErr)...)
			break
		}
		if err := ctx.Err(); err != nil {
			assignments = append(assignments, spawnAgentPlanErrors(readyAgents[i+1:], err)...)
			terminalErr = err
			break
		}
	}

	return assignments, terminalErr
}

func spawnAgentPlanErrors(agents []SpawnedAgent, err error) []SpawnAssignment {
	result := make([]SpawnAssignment, 0, len(agents))
	for _, agent := range agents {
		result = append(result, SpawnAssignment{
			Pane: agent.Pane, AgentType: agent.Type, ClaimError: err.Error(),
		})
	}
	return result
}

func ensureSpawnAssignmentCoverage(output *SpawnOutput) {
	if output == nil {
		return
	}
	multiWindow := spawnCoverageSpansMultipleWindows(output.Agents)
	represented := make(map[string]struct{}, len(output.Assignments))
	for _, spawnAssignment := range output.Assignments {
		represented[spawnCoveragePaneKey(spawnAssignment.Pane, multiWindow)] = struct{}{}
	}
	for _, agent := range output.Agents {
		if agent.Type == "user" {
			continue
		}
		coverageKey := spawnCoveragePaneKey(agent.Pane, multiWindow)
		if _, ok := represented[coverageKey]; ok {
			continue
		}
		output.Assignments = append(output.Assignments, SpawnAssignment{
			Pane:       agent.Pane,
			AgentType:  agent.Type,
			ClaimError: "no work assignment was produced for this eligible agent",
		})
		represented[coverageKey] = struct{}{}
	}
}

// spawnCoverageSpansMultipleWindows: nhm's pane reference is the slot "N.M",
// unique in its workspace across tabs, so the key never narrows.
func spawnCoverageSpansMultipleWindows(agents []SpawnedAgent) bool {
	return false
}

// spawnCoveragePaneKey is the slot itself (see above).
func spawnCoveragePaneKey(raw string, multiWindow bool) string {
	return strings.TrimSpace(raw)
}

func finalizeSpawnAssignmentOutput(output *SpawnOutput) {
	if output == nil {
		return
	}
	ensureSpawnAssignmentCoverage(output)
	failed := 0
	for _, spawnAssignment := range output.Assignments {
		if spawnAssignment.ClaimError != "" || spawnAssignment.PromptError != "" || !spawnAssignment.Claimed || !spawnAssignment.PromptSent {
			failed++
		}
	}
	if failed == 0 {
		return
	}
	output.Error = fmt.Sprintf("%d of %d spawn work assignments failed", failed, len(output.Assignments))
	output.Success = false
	output.ErrorCode = ErrorCodeAssignmentFailed
	output.Hint = "Inspect assignments[].claim_error and assignments[].prompt_error; failed targets were not dispatched"
}

// enrichSpawnWorkPrompt enriches one fresh work prompt with CASS history and
// CM rules (queried by the bead's title, labels and description), records
// what happened on out, and returns the enriched prompt with the base
// prompt's checksum for the ledger. nhm: the enrichment is the occupant's
// (enricher, prepared.go), keyed by the intent's idempotency key; one that
// cannot be prepared leaves the prompt as it is, as ntm's best-effort
// sources do.
func enrichSpawnWorkPrompt(
	ctx context.Context,
	readBead func(context.Context, string, string) (*bv.BeadAssignmentDetails, error),
	enricher EnrichmentPort,
	workDir string,
	occupant Occupant, key string,
	item workItem,
	prompt string,
	promptContext PromptContextOptions,
	out *SpawnAssignment,
) (string, string) {
	skipped := func(reason string) (string, string) {
		if promptContext.WithCASS {
			out.CASSInjection = &CASSInjectionInfo{Enabled: true, SkippedReason: reason}
		}
		if promptContext.WithMemory {
			out.MemoryInjection = &CMInjectionInfo{Enabled: true, SkippedReason: reason}
		}
		return prompt, ""
	}
	if enricher == nil {
		return skipped("no enrichment for this assignment")
	}
	var labels []string
	var description string
	if readBead != nil {
		if details, err := readBead(ctx, workDir, item.ID); err == nil && details != nil {
			labels, description = details.Labels, details.Description
		}
	}
	prepared, err := enricher.Prepare(ctx, occupant, key, item.ID, retrievalQuery(item.Title, labels, description), prompt, promptContext)
	if err != nil {
		return skipped("work prompt context not prepared: " + err.Error())
	}
	out.CASSInjection, out.MemoryInjection = prepared.CASS, prepared.Memory
	return prepared.Prompt, assignment.PromptSHA256(prompt)
}

func spawnAtomicRequest(item workItem, target, occupancy string, pane int, agentType, agentName, prompt, key string, requireReservation bool, reservationPaths []string) assignment.AtomicRequest {
	return assignment.AtomicRequest{
		BeadID: item.ID, BeadTitle: item.Title, Target: target, OccupancyKey: occupancy, Pane: pane,
		AgentType: agentType, AgentName: agentName, Actor: agentName, Prompt: prompt,
		IdempotencyKey: key, RequireReservation: requireReservation, ReservationTTL: time.Hour,
		RequestedPaths: append([]string(nil), reservationPaths...),
		// Without explicit paths the shared Agent Mail port discovers the
		// bead's file scope, as --robot-bulk-assign and ntm assign do.
		AllowReservationDiscovery: requireReservation && len(reservationPaths) == 0,
	}
}

func spawnAssignmentPlanErrors(agents []SpawnedAgent, items []workItem, err error) []SpawnAssignment {
	limit := len(agents)
	if len(items) < limit {
		limit = len(items)
	}
	result := make([]SpawnAssignment, 0, limit)
	for i := 0; i < limit; i++ {
		result = append(result, SpawnAssignment{
			Pane: agents[i].Pane, AgentType: agents[i].Type, BeadID: items[i].ID,
			BeadTitle: items[i].Title, Priority: fmt.Sprintf("P%d", items[i].Priority),
			ClaimError: err.Error(),
		})
	}
	return result
}

func spawnAssignmentLedgerExists(session string) (bool, error) {
	path := filepath.Join(assignment.StorageDir(), session, "assignments.json")
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("assignment ledger %s is not a regular file", path)
	}
	return true, nil
}

// spawnDurableAssignmentReplays reports exact completed dispatch receipts when
// the freshly verified actionable plan is empty. It never claims, reserves, or
// dispatches; the active Beads owner and durable intent must still agree.
func spawnDurableAssignmentReplays(
	ctx context.Context,
	workDir string,
	agents []SpawnedAgent,
	occupants []Occupant,
	store *assignment.AssignmentStore,
	requireReservation bool,
	reservationPaths []string,
	deps SpawnAssignmentDependencies,
) ([]SpawnAssignment, error) {
	if store == nil {
		return spawnAgentPlanErrors(agents, errors.New("assignment replay ledger is unavailable")), nil
	}

	durable := store.GetAll()
	result := make([]SpawnAssignment, 0, len(agents))
	for i, agent := range agents {
		if err := ctx.Err(); err != nil {
			wrapped := fmt.Errorf("spawn assignment replay canceled: %w", err)
			result = append(result, spawnAgentPlanErrors(agents[i:], wrapped)...)
			return result, err
		}

		occupant, err := resolveOccupant(occupants, agent.Pane)
		if err != nil {
			result = append(result, SpawnAssignment{
				Pane: agent.Pane, AgentType: agent.Type,
				ClaimError: fmt.Sprintf("resolve pane %s for assignment replay: %v", agent.Pane, err),
			})
			continue
		}
		target := occupant.Target()

		candidates := make([]assignment.Assignment, 0, 1)
		for _, existing := range durable {
			if robotAtomicAssignmentTerminal(existing.Status) {
				continue
			}
			occupancy := strings.TrimSpace(existing.OccupancyKey)
			if occupancy == "" {
				occupancy = strings.TrimSpace(existing.DispatchTarget)
			}
			if strings.TrimSpace(existing.DispatchTarget) == target || occupancy == occupant.BindingID {
				candidates = append(candidates, existing)
			}
		}
		paneRef := occupant.Slot
		if len(candidates) == 0 {
			continue
		}
		if len(candidates) != 1 {
			result = append(result, SpawnAssignment{
				Pane: paneRef, AgentType: agent.Type,
				ClaimError: fmt.Sprintf("assignment replay target %s has %d active durable records", target, len(candidates)),
			})
			continue
		}

		replay, err := spawnDurableAssignmentReplay(
			ctx, workDir, agent, occupant, paneRef, target, candidates[0],
			requireReservation, reservationPaths, deps.GetBeadDetails,
		)
		if err != nil {
			result = append(result, SpawnAssignment{
				Pane: paneRef, AgentType: agent.Type, BeadID: candidates[0].BeadID,
				BeadTitle: candidates[0].BeadTitle, ClaimError: err.Error(),
			})
			if cancelErr := spawnCancellationError(ctx, err); cancelErr != nil {
				result = append(result, spawnAgentPlanErrors(agents[i+1:], err)...)
				return result, cancelErr
			}
			continue
		}
		result = append(result, replay)
	}
	return result, nil
}

func spawnDurableAssignmentReplay(
	ctx context.Context,
	workDir string,
	agent SpawnedAgent,
	occupant Occupant,
	paneRef, target string,
	existing assignment.Assignment,
	requireReservation bool,
	reservationPaths []string,
	getBeadDetails func(context.Context, string, string) (*bv.BeadAssignmentDetails, error),
) (SpawnAssignment, error) {
	occupancy := strings.TrimSpace(existing.OccupancyKey)
	if occupancy == "" {
		occupancy = strings.TrimSpace(existing.DispatchTarget)
	}
	promptChecksum := assignment.PromptSHA256(existing.PromptSent)
	if existing.Pane != 0 || strings.TrimSpace(existing.DispatchTarget) != target || occupancy != occupant.BindingID ||
		normalizeAgentType(existing.AgentType) != normalizeAgentType(agent.Type) {
		return SpawnAssignment{}, fmt.Errorf("durable assignment %s does not match pane %s identity", existing.BeadID, target)
	}
	if strings.TrimSpace(existing.IdempotencyKey) == "" || strings.TrimSpace(existing.ClaimActor) == "" ||
		existing.ClaimState != assignment.ClaimClaimed || existing.DispatchState != assignment.DispatchSent ||
		strings.TrimSpace(existing.DispatchReceiptID) == "" {
		return SpawnAssignment{}, fmt.Errorf("durable assignment %s has no complete claim and dispatch receipt", existing.BeadID)
	}
	if existing.ReservationRequired != requireReservation ||
		!stringSlicesEqualRobot(existing.ReservationInputPaths, reservationPaths) {
		return SpawnAssignment{}, fmt.Errorf("durable assignment %s does not match the requested reservation contract", existing.BeadID)
	}
	if requireReservation && (existing.ReservationState != assignment.ReservationReserved || !existing.ReservationCompleted) {
		return SpawnAssignment{}, fmt.Errorf("durable assignment %s has no complete reservation receipt", existing.BeadID)
	}
	if existing.PromptSent == "" || strings.TrimSpace(existing.IntentSHA256) == "" ||
		strings.TrimSpace(existing.PromptSHA256) == "" || existing.PromptSHA256 != promptChecksum {
		return SpawnAssignment{}, fmt.Errorf("durable assignment %s has no verifiable sent prompt checksum", existing.BeadID)
	}
	if getBeadDetails == nil {
		return SpawnAssignment{}, errors.New("assignment replay requires an exact Beads state reader")
	}
	details, err := getBeadDetails(ctx, workDir, existing.BeadID)
	if err != nil {
		return SpawnAssignment{}, fmt.Errorf("verify durable assignment %s owner: %w", existing.BeadID, err)
	}
	if details == nil || details.ID != existing.BeadID ||
		!strings.EqualFold(strings.TrimSpace(details.Status), "in_progress") ||
		strings.TrimSpace(details.Assignee) != strings.TrimSpace(existing.ClaimActor) {
		return SpawnAssignment{}, fmt.Errorf("durable assignment %s is not owned in_progress by %s", existing.BeadID, existing.ClaimActor)
	}

	return SpawnAssignment{
		Pane:              paneRef,
		AgentType:         agent.Type,
		BeadID:            existing.BeadID,
		BeadTitle:         existing.BeadTitle,
		Priority:          fmt.Sprintf("P%d", details.Priority),
		Claimed:           true,
		PromptSent:        true,
		ClaimActor:        existing.ClaimActor,
		IdempotencyKey:    existing.IdempotencyKey,
		DispatchReceiptID: existing.DispatchReceiptID,
		ReservationIDs:    append([]int(nil), existing.ReservationIDs...),
	}, nil
}

// workItem represents a work item from triage for assignment.
type workItem struct {
	ID          string
	Title       string
	Priority    int
	Score       float64
	Type        string
	Reasons     []string
	MatchReason string // How the strategy paired this item with its agent slot (recorded in the envelope)
	prompt      string // nhm: SelectWork's own work prompt (workPromptFor)
}

// getWorkItemsForStrategy returns work items based on the selected strategy.
// The returned slice is positional: workItems[i] is assigned to agents[i].
func getWorkItemsForStrategy(triage *bv.TriageResponse, strategy string, agents []SpawnedAgent) []workItem {
	count := len(agents)

	switch strategy {
	case "diverse":
		// Get a mix of different task types
		return getDiverseWorkItems(triage, count)
	case "dependency-aware":
		// Prioritize items that unblock others
		return getDependencyAwareItems(triage, count)
	case "skill-matched":
		// Match agent capabilities (capability matrix) to bead task types.
		return getSkillMatchedWorkItems(triage, agents)
	case "top-n":
		// Get top N recommendations by score
		return getTopNWorkItems(triage, count)
	default:
		// Strict normalization upstream should make this unreachable. If a new
		// strategy value ever leaks through, fall back loudly instead of
		// silently pretending it ran.
		items := getTopNWorkItems(triage, count)
		for i := range items {
			items[i].MatchReason = fmt.Sprintf("strategy %q not implemented at dispatch; fell back to top-n", strategy)
		}
		return items
	}
}

// spawnTaskTypeForRecommendation resolves the capability-matrix task type for
// a triage recommendation. An explicit bead type wins unless it is the
// generic "task"; then a label naming a specific type; then title keyword
// inference (same ladder the C4 assign planners use).
func spawnTaskTypeForRecommendation(rec bv.TriageRecommendation) assign.TaskType {
	if t := strings.ToLower(strings.TrimSpace(rec.Type)); t != "" && t != "task" {
		return assign.ParseTaskType(t)
	}
	for _, label := range rec.Labels {
		if tt := assign.ParseTaskType(label); tt != assign.TaskTask {
			return tt
		}
	}
	return assign.ParseTaskType(inferTaskType(bv.BeadPreview{ID: rec.ID, Title: rec.Title, Type: rec.Type}))
}

// getSkillMatchedWorkItems pairs each agent slot with the unassigned
// recommendation its agent type scores highest on in the capability matrix.
// Triage order (best score first) is the tie-break, so identical capability
// scores degrade to top-n order — and that degenerate case is recorded
// loudly in MatchReason rather than hidden.
func getSkillMatchedWorkItems(triage *bv.TriageResponse, agents []SpawnedAgent) []workItem {
	recs := triage.Triage.Recommendations
	if len(recs) == 0 || len(agents) == 0 {
		return nil
	}

	taskTypes := make([]assign.TaskType, len(recs))
	for j, rec := range recs {
		taskTypes[j] = spawnTaskTypeForRecommendation(rec)
	}

	used := make([]bool, len(recs))
	items := make([]workItem, 0, len(agents))
	for _, agent := range agents {
		best := -1
		bestScore := -1.0
		minScore, maxScore := 2.0, -1.0
		for j := range recs {
			if used[j] {
				continue
			}
			score := assign.GetAgentScoreByString(agent.Type, string(taskTypes[j]))
			if score < minScore {
				minScore = score
			}
			if score > maxScore {
				maxScore = score
			}
			// Strictly greater keeps triage score order as the tie-break:
			// recommendations arrive sorted best-first.
			if score > bestScore {
				best = j
				bestScore = score
			}
		}
		if best < 0 {
			break
		}
		used[best] = true
		rec := recs[best]

		matchReason := fmt.Sprintf("skill-matched: %s capability %.2f for %s tasks", agent.Type, bestScore, taskTypes[best])
		if minScore == maxScore {
			// No capability signal discriminated between the remaining beads
			// (e.g. unknown agent type, or uniform task types): the pick is
			// triage order. Say so instead of silently looking matched.
			matchReason = fmt.Sprintf("skill-matched: no discriminating capability signal for agent type %q (uniform score %.2f); kept triage order", agent.Type, bestScore)
		}

		reasons := append([]string(nil), rec.Reasons...)
		reasons = append(reasons, matchReason)
		items = append(items, workItem{
			ID:          rec.ID,
			Title:       rec.Title,
			Priority:    rec.Priority,
			Score:       rec.Score,
			Type:        rec.Type,
			Reasons:     reasons,
			MatchReason: matchReason,
		})
	}

	return items
}

// getTopNWorkItems returns the top N recommendations by score.
func getTopNWorkItems(triage *bv.TriageResponse, count int) []workItem {
	var items []workItem

	for i, rec := range triage.Triage.Recommendations {
		if i >= count {
			break
		}
		items = append(items, workItem{
			ID:       rec.ID,
			Title:    rec.Title,
			Priority: rec.Priority,
			Score:    rec.Score,
			Type:     rec.Type,
			Reasons:  rec.Reasons,
		})
	}

	return items
}

// getDiverseWorkItems returns a diverse set of work items by type.
func getDiverseWorkItems(triage *bv.TriageResponse, count int) []workItem {
	var items []workItem
	seenTypes := make(map[string]bool)

	// First pass: get one of each type
	for _, rec := range triage.Triage.Recommendations {
		if len(items) >= count {
			break
		}
		if !seenTypes[rec.Type] {
			items = append(items, workItem{
				ID:       rec.ID,
				Title:    rec.Title,
				Priority: rec.Priority,
				Score:    rec.Score,
				Type:     rec.Type,
				Reasons:  rec.Reasons,
			})
			seenTypes[rec.Type] = true
		}
	}

	// Second pass: fill remaining slots with top items
	if len(items) < count {
		for _, rec := range triage.Triage.Recommendations {
			if len(items) >= count {
				break
			}
			// Check if already included
			found := false
			for _, existing := range items {
				if existing.ID == rec.ID {
					found = true
					break
				}
			}
			if !found {
				items = append(items, workItem{
					ID:       rec.ID,
					Title:    rec.Title,
					Priority: rec.Priority,
					Score:    rec.Score,
					Type:     rec.Type,
					Reasons:  rec.Reasons,
				})
			}
		}
	}

	return items
}

// getDependencyAwareItems prioritizes items that unblock the most work.
func getDependencyAwareItems(triage *bv.TriageResponse, count int) []workItem {
	var items []workItem

	// First, add blockers to clear (these unblock other work)
	for _, blocker := range triage.Triage.BlockersToClear {
		if len(items) >= count {
			break
		}
		if blocker.Actionable {
			items = append(items, workItem{
				ID:       blocker.ID,
				Title:    blocker.Title,
				Priority: 0, // Blockers get high priority
				Score:    float64(blocker.UnblocksCount),
				Type:     "blocker",
				Reasons:  []string{fmt.Sprintf("Unblocks %d items", blocker.UnblocksCount)},
			})
		}
	}

	// Then fill with top recommendations
	if len(items) < count {
		for _, rec := range triage.Triage.Recommendations {
			if len(items) >= count {
				break
			}
			// Check if already included
			found := false
			for _, existing := range items {
				if existing.ID == rec.ID {
					found = true
					break
				}
			}
			if !found {
				items = append(items, workItem{
					ID:       rec.ID,
					Title:    rec.Title,
					Priority: rec.Priority,
					Score:    rec.Score,
					Type:     rec.Type,
					Reasons:  rec.Reasons,
				})
			}
		}
	}

	return items
}

// generateWorkPrompt creates a prompt for an agent to work on a bead.
func generateWorkPrompt(item workItem) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Work on bead %s: %s\n\n", item.ID, item.Title))
	sb.WriteString("Use `br show " + item.ID + "` to see full details.\n")
	sb.WriteString("This bead has been marked as in_progress.\n")

	if len(item.Reasons) > 0 {
		sb.WriteString("\nContext:\n")
		for _, reason := range item.Reasons {
			sb.WriteString("- " + reason + "\n")
		}
	}

	sb.WriteString("\nWhen done, close it with: `br close " + item.ID + " --reason \"Completed\"`")

	return sb.String()
}
