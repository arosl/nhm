// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/bulk_assign.go at commit 9d5ed977: bulkRecoveryPrompt,
// restrictTriageToAssignable, newRobotAtomicClaimPort,
// newRobotAtomicEligibilityAuthorizationPort, robotAtomicIdempotencyKey,
// robotAtomicReplayIntent, robotAtomicContextIntent, robotAtomicLiveKey,
// robotAtomicAssignmentTerminal and stringSlicesEqualRobot; see LICENSE in
// this directory and PROVENANCE.md. Local changes: this header; package and
// imports; robotAtomicReplayIntent and robotAtomicContextIntent take the
// occupancy key (nhm's binding id) beside the dispatch target
// ("<binding id>@<generation>") and compare the recorded occupancy key with
// it, where ntm's tmux pane id served as both (nhm-i85).

package spawnassign

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/arosl/nhm/internal/core/assignment"
	"github.com/arosl/nhm/internal/integrations/bv"
)

func bulkRecoveryPrompt(recovery *assignment.Assignment) string {
	if recovery == nil {
		return ""
	}
	if prompt := strings.TrimSpace(recovery.PendingPrompt); prompt != "" {
		return recovery.PendingPrompt
	}
	return recovery.PromptSent
}

func restrictTriageToAssignable(triage *bv.TriageResponse, actionable []bv.TriageRecommendation) *bv.TriageResponse {
	if triage == nil {
		triage = &bv.TriageResponse{}
	}
	result := *triage
	result.Triage = triage.Triage
	result.Triage.Recommendations = append([]bv.TriageRecommendation(nil), actionable...)

	actionableByID := make(map[string]bv.TriageRecommendation, len(actionable))
	for _, rec := range actionable {
		if id := strings.TrimSpace(rec.ID); id != "" {
			actionableByID[id] = rec
		}
	}
	result.Triage.BlockersToClear = make([]bv.BlockerToClear, 0, len(triage.Triage.BlockersToClear))
	seenBlockers := make(map[string]struct{}, len(triage.Triage.BlockersToClear))
	for _, blocker := range triage.Triage.BlockersToClear {
		id := strings.TrimSpace(blocker.ID)
		rec, authorized := actionableByID[id]
		if !authorized {
			continue
		}
		blocker.ID = id
		blocker.Actionable = true
		blocker.BlockedBy = nil
		if strings.TrimSpace(blocker.Title) == "" {
			blocker.Title = rec.Title
		}
		if len(rec.UnblocksIDs) > 0 {
			blocker.UnblocksIDs = append([]string(nil), rec.UnblocksIDs...)
			blocker.UnblocksCount = len(rec.UnblocksIDs)
		}
		seenBlockers[id] = struct{}{}
		result.Triage.BlockersToClear = append(result.Triage.BlockersToClear, blocker)
	}
	for _, rec := range actionable {
		id := strings.TrimSpace(rec.ID)
		if len(rec.UnblocksIDs) == 0 {
			continue
		}
		if _, exists := seenBlockers[id]; exists {
			continue
		}
		result.Triage.BlockersToClear = append(result.Triage.BlockersToClear, bv.BlockerToClear{
			ID:            id,
			Title:         rec.Title,
			UnblocksCount: len(rec.UnblocksIDs),
			UnblocksIDs:   append([]string(nil), rec.UnblocksIDs...),
			Actionable:    true,
		})
	}
	return &result
}

func newRobotAtomicClaimPort(workDir string, claim func(context.Context, string, string, string) (bv.BeadClaimResult, error)) assignment.ClaimPort {
	return assignment.ClaimFunc(func(ctx context.Context, beadID, actor string) (assignment.ClaimReceipt, error) {
		claimed, err := claim(ctx, workDir, beadID, actor)
		if err != nil {
			switch {
			case errors.Is(err, bv.ErrBeadAssignmentIneligible):
				return assignment.ClaimReceipt{}, fmt.Errorf("%w: %v", assignment.ErrClaimIneligible, err)
			case errors.Is(err, bv.ErrBeadAlreadyClaimed):
				return assignment.ClaimReceipt{}, fmt.Errorf("%w: %v", assignment.ErrClaimConflict, err)
			}
			return assignment.ClaimReceipt{}, err
		}
		return assignment.ClaimReceipt{
			BeadID: claimed.ID, Actor: claimed.Actor, Status: claimed.Status, ClaimedAt: claimed.ClaimedAt,
		}, nil
	})
}

func newRobotAtomicEligibilityAuthorizationPort(
	workDir string,
	operatorGatedLabels []string,
	readDetails func(context.Context, string, string) (*bv.BeadAssignmentDetails, error),
) assignment.AssignmentEligibilityAuthorizationPort {
	capturedLabels := append([]string(nil), operatorGatedLabels...)
	return assignment.AssignmentEligibilityAuthorizationFunc(func(ctx context.Context, request assignment.AssignmentEligibilityAuthorizationRequest) error {
		details, err := readDetails(ctx, workDir, request.BeadID)
		if err != nil {
			return err
		}
		if details == nil {
			return errors.New("live work-item details are missing")
		}
		err = bv.ValidateBeadAssignmentAuthorizationWithOperatorGatedLabels(details, bv.BeadAssignmentAuthorization{
			BeadID: request.BeadID, ExpectedAssignee: request.ClaimActor,
			AllowUnassignedOpen:  request.AllowUnassignedOpen,
			AllowOwnedOpen:       request.AllowOwnedOpen,
			AllowOwnedInProgress: request.AllowOwnedInProgress,
		}, capturedLabels)
		if errors.Is(err, bv.ErrBeadAssignmentIneligible) {
			return fmt.Errorf("%w: %v", assignment.ErrClaimIneligible, err)
		}
		return err
	})
}

func robotAtomicIdempotencyKey(
	store *assignment.AssignmentStore,
	beadID, target string,
	_ int,
	agentType, agentName, prompt string,
	requireReservation bool,
	requestedPaths []string,
	newKey func() (string, error),
) (string, error) {
	promptMatches := func(existing *assignment.Assignment) bool {
		if existing.IntentSHA256 != "" {
			return existing.IntentSHA256 == assignment.PromptSHA256(prompt)
		}
		return existing.PendingPrompt == prompt || existing.PromptSent == prompt
	}
	if existing := store.Get(beadID); existing != nil && !robotAtomicAssignmentTerminal(existing.Status) && existing.IdempotencyKey != "" &&
		existing.DispatchTarget == target &&
		existing.AgentType == agentType && existing.AgentName == agentName &&
		existing.ReservationRequired == requireReservation &&
		stringSlicesEqualRobot(existing.ReservationInputPaths, requestedPaths) &&
		promptMatches(existing) {
		return existing.IdempotencyKey, nil
	}
	return newKey()
}

func robotAtomicReplayIntent(
	store *assignment.AssignmentStore,
	beadID, target, occupancy string,
	_ int,
	agentType, prompt string,
	requireReservation bool,
	requestedPaths []string,
) *assignment.Assignment {
	if store == nil {
		return nil
	}
	existing := store.Get(beadID)
	if existing == nil || robotAtomicAssignmentTerminal(existing.Status) || existing.IdempotencyKey == "" || existing.DispatchState != assignment.DispatchSent {
		return nil
	}
	occupancyKey := strings.TrimSpace(existing.OccupancyKey)
	if occupancyKey == "" {
		occupancyKey = strings.TrimSpace(existing.DispatchTarget)
	}
	intentMatches := existing.IntentSHA256 != "" && existing.IntentSHA256 == assignment.PromptSHA256(prompt)
	if existing.IntentSHA256 == "" {
		intentMatches = existing.PendingPrompt == prompt || existing.PromptSent == prompt
	}
	if existing.DispatchTarget != target || occupancyKey != occupancy ||
		existing.AgentType != agentType || !intentMatches ||
		existing.ReservationRequired != requireReservation ||
		!stringSlicesEqualRobot(existing.ReservationInputPaths, requestedPaths) {
		return nil
	}
	return existing
}

// robotAtomicContextIntent returns the live row a re-run of one enriched
// template intent must replay. The row's prompt was enriched with cass/cm
// context, so its intent checksum covers the enriched payload; only its
// recorded pre-enrichment checksum can recognize the caller's template
// prompt. Rows recorded without enrichment keep matching by prompt in
// robotAtomicReplayIntent and robotAtomicIdempotencyKey.
func robotAtomicContextIntent(
	store *assignment.AssignmentStore,
	beadID, target, occupancy, agentType, basePrompt string,
	requireReservation bool,
	requestedPaths []string,
) *assignment.Assignment {
	if store == nil {
		return nil
	}
	existing := store.Get(beadID)
	if existing == nil || robotAtomicAssignmentTerminal(existing.Status) || existing.IdempotencyKey == "" ||
		existing.BaseIntentSHA256 == "" || existing.BaseIntentSHA256 != assignment.PromptSHA256(basePrompt) {
		return nil
	}
	occupancyKey := strings.TrimSpace(existing.OccupancyKey)
	if occupancyKey == "" {
		occupancyKey = strings.TrimSpace(existing.DispatchTarget)
	}
	if existing.DispatchTarget != target || occupancyKey != occupancy || existing.AgentType != agentType ||
		existing.ReservationRequired != requireReservation ||
		!stringSlicesEqualRobot(existing.ReservationInputPaths, requestedPaths) {
		return nil
	}
	return existing
}

// robotAtomicLiveKey reports whether key already identifies the bead's live
// ledger row, i.e. robotAtomicIdempotencyKey reused a recorded intent rather
// than minting a fresh one.
func robotAtomicLiveKey(store *assignment.AssignmentStore, beadID, key string) bool {
	if store == nil {
		return false
	}
	existing := store.Get(beadID)
	return existing != nil && !robotAtomicAssignmentTerminal(existing.Status) && existing.IdempotencyKey == key
}

func robotAtomicAssignmentTerminal(status assignment.AssignmentStatus) bool {
	switch status {
	case assignment.StatusCompleted, assignment.StatusFailed, assignment.StatusReassigned:
		return true
	default:
		return false
	}
}

func stringSlicesEqualRobot(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
