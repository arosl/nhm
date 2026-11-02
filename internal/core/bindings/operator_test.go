package bindings

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/integrations/agentmail"
)

func TestOperatorMarks(t *testing.T) {
	_, s := openStore(t)
	ctx := context.Background()
	if ok, err := s.IsOperator(ctx, "w1:p2"); err != nil || ok {
		t.Fatalf("unmarked pane = %v %v", ok, err)
	}
	first, err := s.MarkOperator(ctx, OperatorPane{PaneID: "w1:p2", WorkspaceID: "w1", MarkedAt: t0})
	if err != nil || first.PaneID != "w1:p2" || !first.MarkedAt.Equal(t0) {
		t.Fatalf("mark = %+v %v", first, err)
	}
	// A second mark keeps the first.
	again, err := s.MarkOperator(ctx, OperatorPane{PaneID: "w1:p2", WorkspaceID: "w9", MarkedAt: t0.Add(time.Hour)})
	if err != nil || again.WorkspaceID != "w1" || !again.MarkedAt.Equal(t0) {
		t.Fatalf("second mark = %+v %v", again, err)
	}
	if _, err := s.MarkOperator(ctx, OperatorPane{PaneID: "w2:p1", WorkspaceID: "w2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkOperator(ctx, OperatorPane{}); err == nil {
		t.Fatal("a mark without a pane was accepted")
	}
	marks, err := s.Operators(ctx)
	if err != nil || len(marks) != 2 || marks[0].PaneID != "w1:p2" || marks[1].PaneID != "w2:p1" {
		t.Fatalf("marks = %+v %v", marks, err)
	}
	if ok, err := s.IsOperator(ctx, "w1:p2"); err != nil || !ok {
		t.Fatalf("marked pane = %v %v", ok, err)
	}
	// Prune keeps the panes herdr lists.
	dropped, err := s.PruneOperators(ctx, map[string]bool{"w1:p2": true})
	if err != nil || len(dropped) != 1 || dropped[0].PaneID != "w2:p1" {
		t.Fatalf("prune = %+v %v", dropped, err)
	}
	if ok, err := s.UnmarkOperator(ctx, "w1:p2"); err != nil || !ok {
		t.Fatalf("unmark = %v %v", ok, err)
	}
	if ok, err := s.UnmarkOperator(ctx, "w1:p2"); err != nil || ok {
		t.Fatalf("second unmark = %v %v", ok, err)
	}
	if marks, err := s.Operators(ctx); err != nil || len(marks) != 0 {
		t.Fatalf("marks after unmark = %+v %v", marks, err)
	}
}

// No path binds an operator's pane: a new binding, a handover into the
// pane, adopt and the import (nhm-h85k).
func TestOperatorPaneIsNeverBound(t *testing.T) {
	project := t.TempDir()
	real, _ := filepath.EvalSymlinks(project)
	h := session(
		paneSpec{pane: "w1:p1", kind: "claude", tabLabel: "1", cwd: real},
		paneSpec{pane: "w1:p2", kind: "claude", tabLabel: "2", cwd: real},
	)
	env, home := testEnv(t, h)
	ctx := context.Background()
	if _, err := env.Bindings.MarkOperator(ctx, OperatorPane{PaneID: "w1:p2", WorkspaceID: "w1"}); err != nil {
		t.Fatal(err)
	}
	_, err := env.Bindings.Create(ctx, newBinding("WindyPanther", real, "w1", "w1:t2", "w1:p2"))
	if !errors.Is(err, ErrOperatorPane) || !strings.Contains(err.Error(), "nhm operator --off w1:p2") {
		t.Fatalf("create in the operator's pane: %v", err)
	}
	other, err := env.Bindings.Create(ctx, newBinding("IcyKnoll", real, "w1", "w1:t1", "w1:p1"))
	if err != nil {
		t.Fatalf("create in another pane: %v", err)
	}
	// A handover into the operator's pane is refused too.
	if _, err := env.Bindings.Displace(ctx, other.ID, other.Generation, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Bindings.Occupy(ctx, other.ID, other.Generation, Occupant{WorkspaceID: "w1", TabID: "w1:t2",
		PaneID: "w1:p2", HerdrAlias: "icyknoll", Kind: "claude"}); !errors.Is(err, ErrOperatorPane) {
		t.Fatalf("occupy the operator's pane: %v", err)
	}
	res, err := Adopt(ctx, env, AdoptRequest{MailName: "WindyPanther", PaneID: "w1:p2", ProjectKey: real, ProjectID: "proj-x"})
	if !errors.Is(err, ErrOperatorPane) {
		t.Fatalf("adopt the operator's pane: %v", err)
	}
	if op, err := env.DB.Operation(ctx, res.OperationID); err != nil || op.Status != "refused" {
		t.Fatalf("adopt's operation = %+v %v", op, err)
	}
	if len(h.renamed) != 0 || len(h.paneLabels) != 0 {
		t.Fatalf("adopt changed herdr: %v %v", h.renamed, h.paneLabels)
	}
	// The import skips the pane before its dry run's verdict, so no run
	// claims an operation for it, even with Agent Mail's identity file.
	if _, err := agentmail.WriteIdentity(home, real, "w1:p2", "WindyPanther", t0); err != nil {
		t.Fatal(err)
	}
	projectOf := func(context.Context, string) (string, string, error) { return real, "proj-x", nil }
	dry, err := ImportOnly(ctx, env, projectOf, ImportRequest{DryRun: true, AcfsHome: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var item *ImportItem
	for i := range dry.Items {
		if dry.Items[i].PaneID == "w1:p2" {
			item = &dry.Items[i]
		}
	}
	if item == nil || item.Action != ActionSkipped || !strings.Contains(item.Reason, "operator's") || item.ProjectKey != real {
		t.Fatalf("import of the operator's pane = %+v", item)
	}
	auto, err := AutoImport(ctx, env, projectOf, ImportRequest{PaneID: "w1:p2", AcfsHome: t.TempDir()})
	if err != nil || auto.Imported != 0 {
		t.Fatalf("auto import = %+v %v", auto, err)
	}
	if _, err := env.Bindings.ActiveByPane(ctx, "w1:p2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the operator's pane is bound: %v", err)
	}
	// A run for another project leaves the pane out.
	only, err := ImportOnly(ctx, env, projectOf, ImportRequest{ProjectKey: "/elsewhere", DryRun: true, AcfsHome: t.TempDir()})
	if err != nil || len(only.Items) != 0 {
		t.Fatalf("other project = %+v %v", only, err)
	}
	// Without the mark, the pane binds again.
	if _, err := env.Bindings.UnmarkOperator(ctx, "w1:p2"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Bindings.Create(ctx, newBinding("WindyPanther", real, "w1", "w1:t2", "w1:p2")); err != nil {
		t.Fatalf("create after the mark is gone: %v", err)
	}
}
