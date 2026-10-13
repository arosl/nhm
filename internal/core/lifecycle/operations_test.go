package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

// hookHerdr runs onClose after a pane closes, and can fail the workspace
// close.
type hookHerdr struct {
	*fakeHerdr
	onClose func(pane string)
	wsErr   error
}

func (h *hookHerdr) PaneClose(ctx context.Context, pane string) error {
	err := h.fakeHerdr.PaneClose(ctx, pane)
	if h.onClose != nil {
		h.onClose(pane)
	}
	return err
}

func (h *hookHerdr) WorkspaceClose(ctx context.Context, ws string) error {
	if h.wsErr != nil {
		return h.wsErr
	}
	return h.fakeHerdr.WorkspaceClose(ctx, ws)
}

func killOp(t *testing.T, w *world) RecentOperation {
	t.Helper()
	ops, err := RecentOperations(context.Background(), w.db, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		if op.Verb == VerbKill {
			return op
		}
	}
	t.Fatalf("no kill operation in %+v", ops)
	return RecentOperation{}
}

func TestKillRecordsItsOperation(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
	w.bind(t, "BlueLake", "w1:p2", herdr.StatusIdle)
	// A refused kill records nothing: no effect ran.
	if _, err := Kill(ctx, w.env, "w1", false, false); !errors.Is(err, ErrRefused) {
		t.Fatal(err)
	}
	if ops, _ := RecentOperations(ctx, w.db, 0); len(ops) != 0 {
		t.Fatalf("a refused kill left %+v", ops)
	}
	res, err := Kill(ctx, w.env, "w1", true, false)
	if err != nil || !res.Closed || res.OperationID == "" {
		t.Fatalf("kill = %+v %v", res, err)
	}
	op := killOp(t, w)
	if op.OperationID != res.OperationID || op.Status != KillClosed || op.Completed == nil || len(op.Targets) != 2 {
		t.Fatalf("operation = %+v", op)
	}
	for _, tg := range op.Targets {
		if tg.Status != KillRetired || !strings.Contains(string(tg.Result), res.OperationID+".") {
			t.Fatalf("target = %+v %s", tg, tg.Result)
		}
	}
	// Each agent's retire is its own operation, under the kill's id.
	ops, err := RecentOperations(ctx, w.db, 0)
	if err != nil || len(ops) != 3 {
		t.Fatalf("operations = %+v %v", ops, err)
	}
}

// An agent recycled while the kill runs is refused, the kill stops, and
// the record keeps the partial outcome per agent.
func TestKillRecordsAPartialOutcome(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
	w.bind(t, "BlueLake", "w1:p2", herdr.StatusIdle)
	w.bind(t, "RedStone", "w1:p3", herdr.StatusIdle)
	h := &hookHerdr{fakeHerdr: w.h}
	w.env.Herdr = h
	h.onClose = func(closed string) {
		h.onClose = nil
		list, err := w.env.Bindings.List(ctx, bindings.Filter{WorkspaceID: "w1", Statuses: []bindings.Status{bindings.Active}})
		if err != nil {
			t.Error(err)
			return
		}
		for _, b := range list {
			if b.PaneID == closed {
				continue
			}
			if _, err := w.env.Bindings.Occupy(ctx, b.ID, b.Generation, bindings.Occupant{WorkspaceID: "w1", TabID: b.TabID,
				PaneID: b.PaneID, HerdrAlias: b.HerdrAlias, Kind: b.Kind, Model: b.Model, NativeSession: b.NativeSession}); err != nil {
				t.Error(err)
			}
		}
	}
	res, err := Kill(ctx, w.env, "w1", true, false)
	if !errors.Is(err, ErrRefused) || res.Closed || len(w.h.wsClosed) != 0 {
		t.Fatalf("kill = %+v %v", res, err)
	}
	op := killOp(t, w)
	if op.Status != KillStopped || op.Completed == nil {
		t.Fatalf("operation = %+v", op)
	}
	got := []string{}
	for _, tg := range op.Targets {
		got = append(got, tg.Status)
	}
	if strings.Join(got, ",") != KillRetired+","+KillRefused+",pending" {
		t.Fatalf("targets = %v", got)
	}
	if !strings.Contains(string(op.Targets[1].Result), "expected generation") {
		t.Fatalf("refusal = %s", op.Targets[1].Result)
	}
}

// A close that fails leaves the agents retired and the record stopped.
func TestKillRecordsAFailedClose(t *testing.T) {
	w := newWorld(t)
	w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
	w.env.Herdr = &hookHerdr{fakeHerdr: w.h, wsErr: errors.New("socket closed")}
	if _, err := Kill(context.Background(), w.env, "w1", true, false); err == nil {
		t.Fatal("a failed close succeeded")
	}
	op := killOp(t, w)
	if op.Status != KillStopped || op.Targets[0].Status != KillRetired {
		t.Fatalf("operation = %+v", op)
	}
}
