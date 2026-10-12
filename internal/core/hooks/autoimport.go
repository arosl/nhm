package hooks

import (
	"context"
	"errors"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
)

// importPane imports the Bash-spawned agent in a pane, while nhm does not
// own the instance (bindings.AutoImport, ACFS acfs-9hkh.41). A pane that
// already has an active binding costs one SQLite read; otherwise a dry
// run looks for verified evidence, and only an importable agent claims an
// operation. Failures are warnings: the next hook for the pane tries
// again.
func importPane(ctx context.Context, env Env, pane string, r *Reconcile) {
	if _, err := env.Bindings.ActiveByPane(ctx, pane); err == nil {
		return
	} else if !errors.Is(err, bindings.ErrNotFound) {
		r.warn("import: %v", err)
		return
	}
	benv := bindings.Env{DB: env.DB, Bindings: env.Bindings, Herdr: env.Herdr, ConfigHome: env.ConfigHome, Now: env.Now}
	res, err := bindings.AutoImport(ctx, benv, projectOf, bindings.ImportRequest{PaneID: pane, AcfsHome: env.AcfsHome})
	for _, w := range res.Warnings {
		r.warn("import: %s", w)
	}
	if err != nil {
		r.warn("import: %v", err)
		return
	}
	if res.Imported > 0 {
		r.act("imported the Bash-spawned agent in %s", pane)
	}
}

// projectOf maps a pane's working directory to its project, as the CLI's
// import does.
func projectOf(ctx context.Context, cwd string) (string, string, error) {
	s, err := core.ResolveScope(ctx, core.ScopeRequest{Cwd: cwd}, nil)
	if err != nil {
		return "", "", err
	}
	return s.MailProjectKey, s.ProjectID, nil
}
