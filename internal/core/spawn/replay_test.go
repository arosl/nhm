package spawn

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/wake"
	"github.com/arosl/nhm/internal/store"
)

// A replay of spawn --assign (nhm-vcf) counts an agent as reached when a
// receipt of another operation names its binding; the spawn's own
// receipts, other agents' receipts and a mail nudge's receipt (nhm-qaa)
// do not count.
func TestReachedBindings(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	now := time.Now()
	claim := func(id, verb string) {
		if _, err := w.db.ClaimOperation(ctx, store.ClaimRequest{ID: id, Verb: verb, Scope: map[string]any{}, Request: map[string]any{}}, now); err != nil {
			t.Fatal(err)
		}
	}
	claim("sp-1", Verb)
	claim("send-1", "send")
	claim("sp-2", Verb)
	claim("wake-1", wake.Verb)
	for i, b := range []string{"bnd-a", "bnd-b", "bnd-c", ""} {
		if err := writeTarget(ctx, w.db, "sp-1", Target{Index: i, BindingID: b, Status: StatusReady}, now); err != nil {
			t.Fatal(err)
		}
	}
	// A send reached bnd-b; a nudge told bnd-c of its mail; another
	// spawn's receipt names bnd-x only.
	if _, err := w.db.SQL().ExecContext(ctx, `INSERT INTO receipts (operation_id, target_index, binding_id, generation, status, updated_at)
		VALUES ('send-1', 0, 'bnd-b', 1, 'pending', ?), ('send-1', 1, NULL, NULL, 'refused', ?), ('wake-1', 0, 'bnd-c', 1, 'submitted', ?)`,
		store.FormatTime(now), store.FormatTime(now), store.FormatTime(now)); err != nil {
		t.Fatal(err)
	}
	if err := writeTarget(ctx, w.db, "sp-2", Target{Index: 0, BindingID: "bnd-x", Status: StatusReady}, now); err != nil {
		t.Fatal(err)
	}
	got, err := ReachedBindings(ctx, w.db, "sp-1")
	if err != nil || !reflect.DeepEqual(got, map[string]bool{"bnd-b": true}) {
		t.Fatalf("reached = %v, %v", got, err)
	}
	if got, err := ReachedBindings(ctx, w.db, "sp-unknown"); err != nil || len(got) != 0 {
		t.Fatalf("an unknown operation = %v, %v", got, err)
	}
}

// Kickoff takes the agent in any state, as after the assignment;
// KickoffGated sends through the delivery gate, frozen at the binding's
// first generation. Both record the kickoff in the target.
func TestKickoffGated(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if _, err := w.db.ClaimOperation(ctx, store.ClaimRequest{ID: "sp-1", Verb: Verb, Scope: map[string]any{}, Request: map[string]any{}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	res := &Result{OperationID: "sp-1", WorkspaceID: "w1"}
	req := Request{Session: "swarm", ProjectRoot: "/p"}
	plain := Target{Index: 0, MailName: "IcyKnoll", BindingID: "bnd-a", Ready: true, Status: StatusReady}
	gated := Target{Index: 1, MailName: "BlueLake", BindingID: "bnd-b", Ready: true, Status: StatusReady}
	Kickoff(ctx, w.env, req, res, &plain, "Read AGENTS.md.")
	KickoffGated(ctx, w.env, req, res, &gated, "Read AGENTS.md.")
	if len(w.sent) != 2 {
		t.Fatalf("sends = %+v", w.sent)
	}
	if r := w.sent[0]; !r.WhenAny || r.Expect != nil {
		t.Fatalf("Kickoff: when any %v, expect %v", r.WhenAny, r.Expect)
	}
	if r := w.sent[1]; r.WhenAny || !reflect.DeepEqual(r.Expect, map[string]int64{"bnd-b": FirstGeneration}) ||
		!reflect.DeepEqual(r.Selector.Names, []string{"BlueLake"}) {
		t.Fatalf("KickoffGated: when any %v, expect %v, names %v", r.WhenAny, r.Expect, r.Selector.Names)
	}
	stored, err := stored(ctx, w.db, "sp-1")
	if err != nil || len(stored.Targets) != 2 {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
	for _, tg := range stored.Targets {
		if tg.KickoffOp == "" || tg.Status != StatusKickoffSubmitted {
			t.Fatalf("target %d: kickoff not recorded: %+v", tg.Index, tg)
		}
	}
}
