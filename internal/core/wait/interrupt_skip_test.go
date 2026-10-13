package wait

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store"
)

func resting(pane, kind string, st herdr.AgentStatus) herdr.AgentInfo {
	return herdr.AgentInfo{PaneID: pane, Agent: kind, AgentStatus: st, StateChangeSeq: u64(4)}
}

// ntm's robot interrupt without --force (nhm-hir): an agent at its
// prompt, idle or done, gets no key and counts as done; a working one
// gets its key. With --force every agent gets it. The two are different
// requests for one operation id.
func TestInterruptSkipsAnAgentAtItsPrompt(t *testing.T) {
	db, s := openDB(t)
	ctx := context.Background()
	icy := create(t, s, "IcyKnoll", "w1:t1", "w1:p1", "claude")
	blue := create(t, s, "BlueLake", "w1:t2", "w1:p2", "claude")
	red := create(t, s, "RedStone", "w1:t3", "w1:p3", "codex")
	targets := []bindings.Binding{icy, blue, red}
	h := &paneHerdr{byPane: map[string]herdr.AgentInfo{"w1:p1": resting("w1:p1", "claude", herdr.StatusIdle),
		"w1:p2": resting("w1:p2", "claude", herdr.StatusDone), "w1:p3": resting("w1:p3", "codex", herdr.StatusWorking)}}
	res, err := Interrupt(ctx, db, h, s, InterruptRequest{OperationID: "op-skip", Targets: targets, SkipResting: true}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{InterruptSkipped, InterruptSkipped, InterruptSent}
	for i, tg := range res.Targets {
		if tg.Status != want[i] {
			t.Errorf("%s = %s (%s), want %s", tg.MailName, tg.Status, tg.Reason, want[i])
		}
	}
	if res.Status != "done" || len(h.keys) != 1 || res.Targets[0].Keys != nil || res.Targets[1].StateBefore != herdr.StatusDone {
		t.Fatalf("status %s, keys %v, targets %+v", res.Status, h.keys, res.Targets)
	}
	if _, err := Interrupt(ctx, db, h, s, InterruptRequest{OperationID: "op-skip", Targets: targets}, time.Now); !errors.Is(err, store.ErrOperationIDConflict) {
		t.Fatalf("forced retry of the same id = %v", err)
	}
	res, err = Interrupt(ctx, db, h, s, InterruptRequest{OperationID: "op-force", Targets: targets}, time.Now)
	if err != nil || res.Status != "done" || len(h.keys) != 4 {
		t.Fatalf("force = %+v %v, keys %v", res, err, h.keys)
	}
}

// A dry run checks each target as a real interrupt does, but claims no
// operation and sends no key.
func TestInterruptDryRun(t *testing.T) {
	db, s := openDB(t)
	ctx := context.Background()
	icy := create(t, s, "IcyKnoll", "w1:t1", "w1:p1", "claude")
	blue := create(t, s, "BlueLake", "w1:t2", "w1:p2", "claude")
	red := create(t, s, "RedStone", "w1:t3", "w1:p3", "codex")
	h := &paneHerdr{byPane: map[string]herdr.AgentInfo{"w1:p1": resting("w1:p1", "claude", herdr.StatusWorking),
		"w1:p2": resting("w1:p2", "claude", herdr.StatusIdle), "w1:p3": resting("w1:p3", "claude", herdr.StatusWorking)}}
	res, err := Interrupt(ctx, db, h, s, InterruptRequest{OperationID: "op-dry", Targets: []bindings.Binding{icy, blue, red},
		SkipResting: true, DryRun: true}, time.Now)
	if err != nil || res.Status != "dry_run" || res.OperationID != "" || len(h.keys) != 0 {
		t.Fatalf("dry run = %+v %v, keys %v", res, err, h.keys)
	}
	want := []string{InterruptWouldSend, InterruptSkipped, InterruptRefused}
	for i, tg := range res.Targets {
		if tg.Status != want[i] {
			t.Errorf("%s = %s (%s), want %s", tg.MailName, tg.Status, tg.Reason, want[i])
		}
	}
	if _, err := db.Operation(ctx, "op-dry"); err == nil {
		t.Fatal("a dry run claimed its operation id")
	}
}
