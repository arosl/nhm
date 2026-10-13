package spawn

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// --spawn-safety (Request.Safety) refuses a session whose workspace
// exists, before anything is created or claimed, a dry run too, as ntm
// refuses to reuse a session. A new session spawns, and without Safety
// an existing one takes the agents (nhm-lrx).
func TestSafetyRefusesAnExistingSession(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	first := req(Want{Kind: "claude", Count: 1})
	first.Safety = true
	if res, err := Spawn(ctx, w.env, first); err != nil || !res.WorkspaceNew {
		t.Fatalf("a new session with safety = %+v %v", res, err)
	}
	calls := len(w.h.calls)
	for _, dry := range []bool{false, true} {
		again := req(Want{Kind: "claude", Count: 1})
		again.Safety, again.DryRun, again.OperationID = true, dry, "op-safety"
		_, err := Spawn(ctx, w.env, again)
		if !errors.Is(err, ErrSessionExists) ||
			!strings.Contains(err.Error(), "session 'swarm' already exists (--spawn-safety mode prevents reuse; use 'nhm kill swarm' first)") {
			t.Fatalf("dry %v: an existing session with safety = %v", dry, err)
		}
	}
	if len(w.h.calls) != calls {
		t.Fatalf("a refused spawn called herdr: %v", w.h.calls[calls:])
	}
	if _, err := w.db.Operation(ctx, "op-safety"); err == nil {
		t.Fatal("a refused spawn claimed its operation id")
	}
	if res, err := Spawn(ctx, w.env, req(Want{Kind: "claude", Count: 1})); err != nil || res.WorkspaceNew {
		t.Fatalf("an existing session without safety = %+v %v", res, err)
	}
}
