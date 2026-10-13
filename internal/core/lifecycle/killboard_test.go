package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/uilaunch"
	"github.com/arosl/nhm/internal/herdr"
)

// writeBoard records a registered board launch in a pane, as
// uilaunch.Register leaves it. The fake's shells are all pid 100.
func writeBoard(t *testing.T, instanceDir, workspaceID, paneID string, pid int) {
	t.Helper()
	data, err := json.Marshal(uilaunch.Launch{Token: "bl-test", InstanceID: "inst-000000000001", WorkspaceID: workspaceID,
		State: uilaunch.Registered, OpenedAt: time.Date(2026, 10, 12, 6, 0, 0, 0, time.UTC), PaneID: paneID, PID: pid})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(instanceDir, uilaunch.Dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "board-"+workspaceID+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// herdr names an agent in nhm's live board pane. Kill's review and its
// last check leave the board out by its verified launch record: kill
// closes it with the workspace. Without the record, or with a record of
// another process, the same pane is an agent with no binding, and kill
// refuses (plan-v4 section 7).
func TestKillLeavesOutTheLiveBoard(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
	w.h.add("w1:p5", "claude", "ghost", "s-board", herdr.StatusIdle)

	res, err := KillWith(ctx, w.env, KillRequest{WorkspaceID: "w1"})
	if !errors.Is(err, ErrRefused) || len(res.Refusals) != 1 || !strings.Contains(res.Refusals[0], "pane w1:p5 holds claude agent") {
		t.Fatalf("review without a board record = %+v, %v", res, err)
	}
	writeBoard(t, w.env.InstanceDir, "w1", "w1:p5", 4242)
	if res, err = KillWith(ctx, w.env, KillRequest{WorkspaceID: "w1"}); len(res.Refusals) != 1 {
		t.Fatalf("review with another process's record = %+v, %v", res, err)
	}

	writeBoard(t, w.env.InstanceDir, "w1", "w1:p5", 100)
	res, err = KillWith(ctx, w.env, KillRequest{WorkspaceID: "w1"})
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "--yes") || len(res.Refusals) != 0 {
		t.Fatalf("review with the live board = %+v, %v", res, err)
	}
	res, err = KillWith(ctx, w.env, KillRequest{WorkspaceID: "w1", Yes: true})
	if err != nil || !res.Closed || len(w.h.wsClosed) != 1 {
		t.Fatalf("kill = %+v, %v (closed %v)", res, err, w.h.wsClosed)
	}
}
