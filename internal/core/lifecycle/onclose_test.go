package lifecycle

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

// Recycle on close (nhm-abn): a generation's first check records the
// beads closed before; a recycle is due only when a bead closed since,
// the agent is between turns with no bead in progress, and the same holds
// a check later; then never again for the generation.
func TestOnCloseDue(t *testing.T) {
	ctx := context.Background()
	f := &fakeFacts{assigned: map[string][]Bead{}, closed: []string{"acfs-1"}}
	b := bindings.Binding{ID: "bnd-1", MailName: "IcyKnoll", MailProjectKey: "/p", Generation: 1}
	var seen OnCloseSeen
	now := time.Date(2026, 10, 13, 5, 0, 0, 0, time.UTC)
	check := func(st herdr.AgentStatus) []string {
		t.Helper()
		due, err := OnCloseDue(ctx, f, b, st, &seen, now)
		if err != nil {
			t.Fatal(err)
		}
		return due
	}
	step := func(d time.Duration) { now = now.Add(d) }

	if due := check(herdr.StatusIdle); due != nil || !slices.Equal(seen.Closed, []string{"acfs-1"}) || seen.Generation != 1 {
		t.Fatalf("first check = %v, seen %+v", due, seen)
	}
	// It works on a bead, then closes it.
	f.assigned["IcyKnoll"] = []Bead{{ID: "acfs-2"}}
	step(time.Minute)
	if due := check(herdr.StatusWorking); due != nil {
		t.Fatalf("working = %v", due)
	}
	f.assigned["IcyKnoll"], f.closed = nil, []string{"acfs-2", "acfs-1"}
	step(10 * time.Second)
	if due := check(herdr.StatusIdle); due != nil {
		t.Fatalf("a check within %s = %v", OnCloseEvery, due)
	}
	step(time.Minute)
	if due := check(herdr.StatusIdle); due != nil || !slices.Equal(seen.Pending, []string{"acfs-2"}) {
		t.Fatalf("first idle check = %v, pending %v", due, seen.Pending)
	}
	step(time.Minute)
	if due := check(herdr.StatusDone); !slices.Equal(due, []string{"acfs-2"}) {
		t.Fatalf("second idle check = %v", due)
	}
	seen.Recycled = "op-r1"
	step(time.Minute)
	if due := check(herdr.StatusIdle); due != nil {
		t.Fatalf("after the recycle started = %v", due)
	}

	// The successor is another generation: it starts afresh, and the
	// closed bead is no work of its.
	b.Generation = 2
	step(time.Minute)
	if due := check(herdr.StatusIdle); due != nil || seen.Recycled != "" || !slices.Equal(seen.Closed, []string{"acfs-1", "acfs-2"}) {
		t.Fatalf("new generation = %v, seen %+v", due, seen)
	}
}

// An agent that takes its next bead, or works, between the two checks is
// left alone.
func TestOnCloseLeavesAnAgentThatGoesOn(t *testing.T) {
	ctx := context.Background()
	f := &fakeFacts{assigned: map[string][]Bead{}}
	b := bindings.Binding{ID: "bnd-1", MailName: "IcyKnoll", MailProjectKey: "/p", Generation: 1}
	var seen OnCloseSeen
	now := time.Date(2026, 10, 13, 5, 0, 0, 0, time.UTC)
	if _, err := OnCloseDue(ctx, f, b, herdr.StatusIdle, &seen, now); err != nil {
		t.Fatal(err)
	}
	f.closed = []string{"acfs-2"}
	for i, st := range []herdr.AgentStatus{herdr.StatusIdle, herdr.StatusWorking, herdr.StatusIdle} {
		if i == 2 {
			f.assigned["IcyKnoll"] = []Bead{{ID: "acfs-3"}}
		}
		now = now.Add(time.Minute)
		if due, err := OnCloseDue(ctx, f, b, st, &seen, now); err != nil || due != nil {
			t.Fatalf("check %d (%s) = %v %v", i, st, due, err)
		}
	}
	if seen.Pending != nil {
		t.Fatalf("pending = %v", seen.Pending)
	}
}
