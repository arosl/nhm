package assignment

import (
	"strings"
	"testing"
)

// ClaimActorBase undoes StableClaimActor and leaves every other assignee as
// it is. StableClaimActor writes nhm's "/nhm-", and an older nhm's "/ntm-"
// still reads as its actor's (nhm-2p6.28.4).
func TestClaimActorBase(t *testing.T) {
	for _, key := range []string{"0123456789abcdef0123", "abc", AssignmentIdempotencyKey("SnowyDove")} {
		actor := StableClaimActor("SnowyDove", key)
		if !strings.HasPrefix(actor, "SnowyDove/nhm-") {
			t.Fatalf("key %q: actor=%q, want SnowyDove/nhm-<key>", key, actor)
		}
		if got := ClaimActorBase(actor); got != "SnowyDove" {
			t.Fatalf("key %q: base=%q", key, got)
		}
	}
	if actor := StableClaimActor("", "abc"); actor != "nhm/nhm-abc" {
		t.Fatalf("no actor: %q, want nhm/nhm-abc", actor)
	}
	for _, suffix := range []string{"/nhm-", "/ntm-"} {
		for _, assignee := range []string{"", "SnowyDove", suffix + "0123456789ab", "SnowyDove" + suffix,
			"SnowyDove" + suffix + "0123456789abc", "SnowyDove" + suffix + "01/x", "SnowyDove/other-0123456789ab"} {
			if got := ClaimActorBase(assignee); got != assignee {
				t.Fatalf("%q: base=%q, want it unchanged", assignee, got)
			}
		}
		if !ClaimedBy("SnowyDove"+suffix+"0123456789ab", "SnowyDove") || !ClaimedBy("SnowyDove", "SnowyDove") {
			t.Fatalf("%s: ClaimedBy misses the agent's own claims", suffix)
		}
		if ClaimedBy("SnowyDoveX"+suffix+"0123456789ab", "SnowyDove") || ClaimedBy("Snowy"+suffix+"0123456789ab", "SnowyDove") ||
			ClaimedBy("", "") || ClaimedBy("SnowyDove", "") {
			t.Fatalf("%s: ClaimedBy matches another actor", suffix)
		}
	}
}
