package bindings

import (
	"context"
	"errors"
	"testing"

	"github.com/arosl/nhm/internal/integrations/agentmail"
)

func operationCount(t *testing.T, env Env) int {
	t.Helper()
	var n int
	if err := env.DB.SQL().QueryRow(`SELECT count(*) FROM operations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The automatic import claims an operation only when an agent is
// importable, and the pane filter keeps the hook path to its own pane.
func TestAutoImportClaimsAnOperationOnlyWhenItImports(t *testing.T) {
	real := t.TempDir()
	h := session(
		paneSpec{pane: "w1:p1", kind: "claude", tabLabel: "1", cwd: real},
		paneSpec{pane: "w1:p2", kind: "codex", tabLabel: "2", cwd: real},
		paneSpec{pane: "w1:p3", kind: "claude", tabLabel: "3", cwd: real},
	)
	env, home := testEnv(t, h)
	ctx := context.Background()
	for pane, name := range map[string]string{"w1:p1": "IcyKnoll", "w1:p2": "BlueLake"} {
		if _, err := agentmail.WriteIdentity(home, real, pane, name, t0); err != nil {
			t.Fatal(err)
		}
	}
	projectOf := func(_ context.Context, cwd string) (string, string, error) {
		if cwd == real {
			return real, "proj-x", nil
		}
		return "", "", errors.New("not a git work tree")
	}
	acfs := t.TempDir()

	// Nothing importable in w1:p3 (no evidence): a dry run only.
	res, err := AutoImport(ctx, env, projectOf, ImportRequest{PaneID: "w1:p3", AcfsHome: acfs})
	if err != nil || res.Imported != 0 || operationCount(t, env) != 0 {
		t.Fatalf("w1:p3 = %+v %v, %d operations", res, err, operationCount(t, env))
	}
	// One pane: only that pane is imported.
	res, err = AutoImport(ctx, env, projectOf, ImportRequest{PaneID: "w1:p1", AcfsHome: acfs})
	if err != nil || res.Imported != 1 || operationCount(t, env) != 1 {
		t.Fatalf("w1:p1 = %+v %v, %d operations", res, err, operationCount(t, env))
	}
	if _, err := env.Bindings.ActiveByPane(ctx, "w1:p2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("w1:p2 was imported by a run for w1:p1: %v", err)
	}
	// Every pane: the rest is imported.
	res, err = AutoImport(ctx, env, projectOf, ImportRequest{AcfsHome: acfs})
	if err != nil || res.Imported != 1 || operationCount(t, env) != 2 {
		t.Fatalf("all = %+v %v, %d operations", res, err, operationCount(t, env))
	}
	// Nothing left: no operation.
	res, err = AutoImport(ctx, env, projectOf, ImportRequest{AcfsHome: acfs})
	if err != nil || res.Imported != 0 || operationCount(t, env) != 2 {
		t.Fatalf("again = %+v %v, %d operations", res, err, operationCount(t, env))
	}
	b, err := env.Bindings.ActiveByPane(ctx, "w1:p2")
	if err != nil || b.MailName != "BlueLake" || b.Provenance != Imported {
		t.Fatalf("w1:p2 binding = %+v %v", b, err)
	}
}
