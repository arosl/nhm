package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store"
)

// The menu starts kill in another process with an id it chose, and finds
// the record by that id.
func TestKillWithACallersOperationID(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
	w.bind(t, "BlueLake", "w1:p2", herdr.StatusIdle)
	// The review: the checks pass, so there are no refusals; nothing is
	// recorded without Yes.
	res, err := KillWith(ctx, w.env, KillRequest{WorkspaceID: "w1", OperationID: "op-menu-1"})
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "--yes") || len(res.Refusals) != 0 || res.OperationID != "" {
		t.Fatalf("review = %+v %v", res, err)
	}
	if ops, _ := RecentOperations(ctx, w.db, 0); len(ops) != 0 {
		t.Fatalf("a review left %+v", ops)
	}
	res, err = KillWith(ctx, w.env, KillRequest{WorkspaceID: "w1", Yes: true, OperationID: "op-menu-1"})
	if err != nil || !res.Closed || res.OperationID != "op-menu-1" {
		t.Fatalf("kill = %+v %v", res, err)
	}
	if op := killOp(t, w); op.OperationID != "op-menu-1" || op.Status != KillClosed {
		t.Fatalf("operation = %+v", op)
	}
	// The menu follows the record by its id.
	op, err := ReadOperation(ctx, w.db, "op-menu-1")
	if err != nil || op.Verb != VerbKill || op.Completed == nil || len(op.Targets) != 2 || string(op.Scope) != `{"workspace_id":"w1"}` {
		t.Fatalf("read = %+v %s %v", op, op.Scope, err)
	}
	if _, err := ReadOperation(ctx, w.db, "op-none"); !errors.Is(err, ErrNoOperation) {
		t.Fatalf("a missing id = %v", err)
	}
	// The same id again is refused before any effect, whatever it asks.
	w.bind(t, "RedStone", "w1:p3", herdr.StatusIdle)
	res, err = KillWith(ctx, w.env, KillRequest{WorkspaceID: "w1", Yes: true, OperationID: "op-menu-1"})
	if !errors.Is(err, ErrRefused) || res.Closed || len(w.h.wsClosed) != 1 || len(w.h.closed) != 2 {
		t.Fatalf("second kill under one id = %+v %v (closed %v)", res, err, w.h.closed)
	}
}

// A kill id as long as an operation id may be still gives each retire a
// valid id (nhm-ceu), so kill retires every agent and closes.
func TestKillWithTheLongestOperationID(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
	w.bind(t, "BlueLake", "w1:p2", herdr.StatusIdle)
	id := "op-" + strings.Repeat("m", 125)
	res, err := KillWith(ctx, w.env, KillRequest{WorkspaceID: "w1", Yes: true, OperationID: id})
	if err != nil || !res.Closed || len(res.Agents) != 2 || len(w.h.closed) != 2 {
		t.Fatalf("kill = %+v %v", res, err)
	}
	for i, r := range res.Agents {
		if r.OperationID != store.ChildOperationID(id, i) {
			t.Errorf("retire %d ran as %q", i, r.OperationID)
		}
	}
}

// An id that another verb holds is refused, and nothing runs.
func TestKillRefusesAnIDInUse(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
	if _, err := w.db.ClaimOperation(ctx, store.ClaimRequest{ID: "op-used", Verb: "send", Request: "x"}, w.clock); err != nil {
		t.Fatal(err)
	}
	res, err := KillWith(ctx, w.env, KillRequest{WorkspaceID: "w1", Yes: true, OperationID: "op-used"})
	if !errors.Is(err, ErrRefused) || !errors.Is(err, store.ErrOperationIDConflict) || res.Closed || len(w.h.closed) != 0 || len(w.h.wsClosed) != 0 {
		t.Fatalf("kill under a used id = %+v %v", res, err)
	}
}

// A review that refuses lists each refusal, so the menu offers no
// confirm.
func TestKillReviewListsRefusals(t *testing.T) {
	w := newWorld(t)
	w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusWorking)
	res, err := KillWith(context.Background(), w.env, KillRequest{WorkspaceID: "w1"})
	if !errors.Is(err, ErrRefused) || len(res.Refusals) != 1 || !strings.HasPrefix(res.Refusals[0], "IcyKnoll: ") {
		t.Fatalf("review = %+v %v", res, err)
	}
}
