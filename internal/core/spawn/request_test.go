package spawn

import "testing"

// A role is "reviewer" or nothing: reap retires a bead-less agent only
// when it was spawned as a reviewer.
func TestRequestRole(t *testing.T) {
	r := req(Want{Kind: "claude", Count: 1})
	r.Role = RoleReviewer
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	r.Role = "boss"
	if err := r.Validate(); err == nil {
		t.Fatal("an unknown role was accepted")
	}
	if !ValidModel("claude-opus-5-5") || ValidModel("--dangerously-skip-permissions") {
		t.Fatal("ValidModel")
	}
}
