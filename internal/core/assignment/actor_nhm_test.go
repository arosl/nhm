package assignment

import "testing"

// ClaimActorBase undoes StableClaimActor and leaves every other assignee as
// it is.
func TestClaimActorBase(t *testing.T) {
	for _, key := range []string{"0123456789abcdef0123", "abc", AssignmentIdempotencyKey("SnowyDove")} {
		if got := ClaimActorBase(StableClaimActor("SnowyDove", key)); got != "SnowyDove" {
			t.Fatalf("key %q: base=%q", key, got)
		}
	}
	for _, assignee := range []string{"", "SnowyDove", "/ntm-0123456789ab", "SnowyDove/ntm-",
		"SnowyDove/ntm-0123456789abc", "SnowyDove/ntm-01/x", "SnowyDove/other-0123456789ab"} {
		if got := ClaimActorBase(assignee); got != assignee {
			t.Fatalf("%q: base=%q, want it unchanged", assignee, got)
		}
	}
	if !ClaimedBy("SnowyDove/ntm-0123456789ab", "SnowyDove") || !ClaimedBy("SnowyDove", "SnowyDove") {
		t.Fatal("ClaimedBy misses the agent's own claims")
	}
	if ClaimedBy("SnowyDoveX/ntm-0123456789ab", "SnowyDove") || ClaimedBy("Snowy/ntm-0123456789ab", "SnowyDove") ||
		ClaimedBy("", "") || ClaimedBy("SnowyDove", "") {
		t.Fatal("ClaimedBy matches another actor")
	}
}
