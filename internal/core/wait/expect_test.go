package wait

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
)

// A recycle after the caller reviewed the target: the core reselects the
// binding at its new generation, and Expect refuses it before the key.
func TestInterruptExpectRefusesAStaleGeneration(t *testing.T) {
	db, s := openDB(t)
	ctx := context.Background()
	icy := create(t, s, "IcyKnoll", "w1:t1", "w1:p1", "claude")
	blue := create(t, s, "BlueLake", "w1:t2", "w1:p2", "claude")
	expect := map[string]int64{icy.ID: icy.Generation, "gone": 2}
	cur, err := s.Occupy(ctx, icy.ID, icy.Generation, bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1",
		PaneID: "w1:p1", HerdrAlias: "icyknoll", Kind: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	h := &kindHerdr{kinds: map[string]string{"w1:p1": "claude", "w1:p2": "claude"}}
	res, err := Interrupt(ctx, db, h, s, InterruptRequest{Targets: []bindings.Binding{cur, blue}, Expect: expect}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.keys) != 0 || res.Status != "refused" || len(res.Targets) != 3 {
		t.Fatalf("= %+v, keys %v", res, h.keys)
	}
	if r := res.Targets[0]; r.Status != InterruptRefused || !strings.Contains(r.Reason, "expected generation") {
		t.Fatalf("stale = %+v", r)
	}
	if r := res.Targets[1]; r.Status != InterruptRefused || r.Reason != ReasonNotReviewed {
		t.Fatalf("unreviewed = %+v", r)
	}
	if r := res.Targets[2]; r.BindingID != "gone" || r.Reason != ReasonNotSelected {
		t.Fatalf("missing = %+v", r)
	}
	// The current generation is accepted.
	res, err = Interrupt(ctx, db, h, s, InterruptRequest{Targets: []bindings.Binding{cur},
		Expect: map[string]int64{cur.ID: cur.Generation}}, time.Now)
	if err != nil || res.Status != "done" || len(h.keys) != 1 {
		t.Fatalf("= %+v %v, keys %v", res, err, h.keys)
	}
}
