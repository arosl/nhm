package assignment

import "strings"

// claimSuffix starts the key that StableClaimActor adds to the actor:
// nhm's "/nhm-", where ntm writes "/ntm-" (nhm-2p6.28.4; the operator,
// 2026-10-24: never ntm's names).
const claimSuffix = "/nhm-"

// legacyClaimSuffix is the "/ntm-" that nhm releases before nhm-2p6.28.4
// wrote. A bead claimed then still carries it, and it stays its agent's.
const legacyClaimSuffix = "/ntm-"

// ClaimActorBase is the actor a bead's assignee names: the assignee itself,
// or, for a StableClaimActor (actor + "/nhm-" + up to 12 bytes of the
// idempotency key, or an older nhm's "/ntm-"), the actor. A spawn
// assignment claims as the agent's Mail name with that suffix, and nhm
// reads an agent's beads by its Mail name (nhm-i85, ACFS acfs-f2mc.2).
func ClaimActorBase(assignee string) string {
	for _, suffix := range []string{claimSuffix, legacyClaimSuffix} {
		i := strings.LastIndex(assignee, suffix)
		if i <= 0 {
			continue
		}
		key := assignee[i+len(suffix):]
		if key == "" || len(key) > 12 || strings.Contains(key, "/") {
			continue
		}
		return assignee[:i]
	}
	return assignee
}

// ClaimedBy reports whether a bead whose assignee is assignee is actor's:
// assigned to actor, or claimed for actor by a spawn assignment.
func ClaimedBy(assignee, actor string) bool {
	return actor != "" && ClaimActorBase(assignee) == actor
}
