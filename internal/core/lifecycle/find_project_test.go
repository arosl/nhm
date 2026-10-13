package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

// TestRetireStaysInTheNamedProject: with --project /acfs, IcyKnoll of /p
// is not found by its binding id, its herdr name or its pane, and nothing
// is retired; with /p, the same id is found (nhm-ktm).
func TestRetireStaysInTheNamedProject(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	b := w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
	for _, target := range []string{b.ID, "icyknoll", "IcyKnoll", "w1:p1"} {
		_, err := Retire(ctx, w.env, RetireRequest{Target: target, ProjectKey: "/acfs", DryRun: true})
		if !errors.Is(err, bindings.ErrNotFound) || !strings.Contains(err.Error(), "in /acfs") {
			t.Errorf("%s in /acfs: %v", target, err)
		}
	}
	if res, err := Retire(ctx, w.env, RetireRequest{Target: b.ID, ProjectKey: "/p", DryRun: true}); err != nil || len(res.Actions) == 0 {
		t.Fatalf("%s in /p: %+v %v", b.ID, res, err)
	}
	if len(w.h.closed) != 0 {
		t.Fatalf("closed %v", w.h.closed)
	}
}
