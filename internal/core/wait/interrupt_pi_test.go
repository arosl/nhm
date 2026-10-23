package wait

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
)

// pi stops a turn on esc: its TUI reads "esc to abort" while it works
// (pi 0.7.1; nhm-y7q).
func TestInterruptSendsEscToPi(t *testing.T) {
	db, s := openDB(t)
	pi := create(t, s, "IcyKnoll", "w1:t1", "w1:p1", "pi")
	h := &kindHerdr{kinds: map[string]string{"w1:p1": "pi"}}
	res, err := Interrupt(context.Background(), db, h, s, InterruptRequest{Targets: []bindings.Binding{pi}}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "done" || res.Targets[0].Status != InterruptSent || len(h.keys) != 1 || !slices.Equal(h.keys[0], []string{"esc"}) {
		t.Fatalf("status %s, targets %+v, keys %v", res.Status, res.Targets, h.keys)
	}
}
