package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/uilaunch"
)

// writeBoard records a registered board launch in a pane; the world's
// panes all have shell pid 10.
func writeBoard(t *testing.T, instanceDir, paneID string, pid int) {
	t.Helper()
	data, err := json.Marshal(uilaunch.Launch{Token: "bl-test", InstanceID: "inst-000000000001", WorkspaceID: "w1",
		State: uilaunch.Registered, OpenedAt: t0, PaneID: paneID, PID: pid})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(instanceDir, uilaunch.Dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "board-w1.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// An agent hook from nhm's live board pane, with the session herdr
// reports there, binds nothing: the reconcile leaves the board alone and
// says so (plan-v4 section 7). A record that cannot be read leaves the
// candidates waiting. Once the record names another process, the same
// hook binds.
func TestHookInTheBoardPaneBindsNothing(t *testing.T) {
	h := newHarness(t, core.OwnerNhm)
	project := t.TempDir()
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500})
	writeBoard(t, h.env.InstanceDir, "w1:p1", 10)
	ctx := context.Background()

	h.hook(registration("sess-a", "IcyKnoll", project), "e1")
	if list, err := h.env.Bindings.List(ctx, bindings.Filter{MailName: "IcyKnoll"}); err != nil || len(list) != 0 {
		t.Fatalf("the board pane got a binding: %+v %v", list, err)
	}
	r, err := ReconcilePane(ctx, h.env, "w1:p1")
	if err != nil || r.Ran || len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "nhm's board") {
		t.Fatalf("reconcile = %+v, %v", r, err)
	}
	if h.world.called("agent.rename") || h.world.called("pane.rename") || h.world.called("tab.rename") {
		t.Fatalf("the board pane was renamed: %v", h.world.calls)
	}

	if err := os.WriteFile(filepath.Join(h.env.InstanceDir, uilaunch.Dir, "board-w1.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err = ReconcilePane(ctx, h.env, "w1:p1")
	if err != nil || r.Ran || len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "cannot check") {
		t.Fatalf("reconcile with a broken record = %+v, %v", r, err)
	}

	writeBoard(t, h.env.InstanceDir, "w1:p1", 4242)
	h.hook(registration("sess-a", "IcyKnoll", project), "e2")
	if b := h.binding("IcyKnoll"); b.Status != bindings.Active || b.PaneID != "w1:p1" {
		t.Fatalf("binding after the board = %+v", b)
	}
}
