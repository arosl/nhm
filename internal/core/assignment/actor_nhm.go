package assignment

import "strings"

// ClaimActorBase is the actor a bead's assignee names: the assignee itself,
// or, for a StableClaimActor (actor + "/ntm-" + up to 12 bytes of the
// idempotency key), the actor. A spawn assignment claims as the agent's Mail
// name with that suffix, and nhm reads an agent's beads by its Mail name
// (nhm-i85, ACFS acfs-f2mc.2).
func ClaimActorBase(assignee string) string {
	i := strings.LastIndex(assignee, "/ntm-")
	if i <= 0 {
		return assignee
	}
	key := assignee[i+len("/ntm-"):]
	if key == "" || len(key) > 12 || strings.Contains(key, "/") {
		return assignee
	}
	return assignee[:i]
}

// ClaimedBy reports whether a bead whose assignee is assignee is actor's:
// assigned to actor, or claimed for actor by a spawn assignment.
func ClaimedBy(assignee, actor string) bool {
	return actor != "" && ClaimActorBase(assignee) == actor
}
