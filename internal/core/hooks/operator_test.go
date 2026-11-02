package hooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/store"
)

// A person's own session registers with Agent Mail to use mail; in a
// pane marked as an operator's, the registration binds nothing
// (nhm-h85k).
func TestRegistrationInAnOperatorsPaneIsRefused(t *testing.T) {
	h := newHarness(t, core.OwnerNhm)
	ctx := context.Background()
	project := t.TempDir()
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-op", pid: 500})
	if _, err := h.env.Bindings.MarkOperator(ctx, bindings.OperatorPane{PaneID: "w1:p1", WorkspaceID: "w1"}); err != nil {
		t.Fatal(err)
	}
	res := h.hook(registration("sess-op", "WindyPanther", project), "e1")
	if _, err := h.env.Bindings.ActiveByPane(ctx, "w1:p1"); !errors.Is(err, bindings.ErrNotFound) {
		t.Fatalf("the operator's pane is bound: %v (report %+v)", err, res.Report)
	}
	results, err := CandidateResults(ctx, h.env.DB, "w1:p1")
	if err != nil || len(results) != 1 || !results[0].Processed || !strings.HasPrefix(results[0].Result, "refused: ") ||
		!strings.Contains(results[0].Result, "operator") {
		t.Fatalf("candidates = %+v %v", results, err)
	}
	// Nothing of the bind's writes: no name, no label, no identity file,
	// and the candidate's token file is gone.
	if h.world.called("agent.rename") || h.world.called("pane.rename") || h.world.called("pane.report_metadata") {
		t.Fatalf("herdr calls: %v", h.world.calls)
	}
	if _, ok, _ := agentmail.ReadIdentity(h.env.ConfigHome, canonical(project), "w1:p1"); ok {
		t.Fatal("an identity file was written for the operator's pane")
	}
	if _, err := store.ReadSecret(h.env.InstanceDir, "cand-1"); !errors.Is(err, store.ErrNoSecret) {
		t.Fatalf("the candidate's token file stayed: %v", err)
	}
	// Without the mark, the next registration binds the pane.
	if _, err := h.env.Bindings.UnmarkOperator(ctx, "w1:p1"); err != nil {
		t.Fatal(err)
	}
	h.hook(registration("sess-op", "WindyPanther", project), "e2")
	if b := h.binding("WindyPanther"); b.Status != bindings.Active || b.PaneID != "w1:p1" {
		t.Fatalf("binding after the mark is gone = %+v", b)
	}
}

// Under the Bash, the hook pane's import skips an operator's pane, and
// claims no operation for it.
func TestBashOwnerDoesNotImportAnOperatorsPane(t *testing.T) {
	h := newHarness(t, core.OwnerBash)
	ctx := context.Background()
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	repo = canonical(repo)
	h.env.AcfsHome = t.TempDir()
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-op", pid: 500, cwd: repo})
	if _, err := agentmail.WriteIdentity(h.env.ConfigHome, repo, "w1:p1", "WindyPanther", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := h.env.Bindings.MarkOperator(ctx, bindings.OperatorPane{PaneID: "w1:p1", WorkspaceID: "w1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := RunPluginEvent(ctx, h.env, PaneEvent{PaneID: "w1:p1", Agent: "claude"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.env.Bindings.ActiveByPane(ctx, "w1:p1"); !errors.Is(err, bindings.ErrNotFound) {
		t.Fatalf("the operator's pane was imported: %v", err)
	}
	var ops int
	if err := h.env.DB.SQL().QueryRow(`SELECT count(*) FROM operations`).Scan(&ops); err != nil || ops != 0 {
		t.Fatalf("operations = %d %v", ops, err)
	}
	// The identity file stays: it names the person's own identity.
	if rec, ok, _ := agentmail.ReadIdentity(h.env.ConfigHome, repo, "w1:p1"); !ok || rec.Name != "WindyPanther" {
		t.Fatal("the identity file went")
	}
}
