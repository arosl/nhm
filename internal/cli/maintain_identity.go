package cli

import (
	"context"
	"fmt"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/integrations/agentmail"
)

// identityPass is the maintain pass's check of the bindings against
// Agent Mail and of the operator marks against herdr (nhm-h85k): it
// retires the binding of an identity that Agent Mail reports retired or
// deregistered (bindings.ReleaseGoneIdentities), and drops the mark of
// an operator pane that herdr no longer has. It runs only when nhm owns
// the instance; what fails is a warning.
func (ie *instanceEnv) identityPass(ctx context.Context, mail agentmail.Caller) ([]bindings.GoneIdentity, []bindings.OperatorPane, []string) {
	gone, dropped, warnings := []bindings.GoneIdentity{}, []bindings.OperatorPane{}, []string{}
	lctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	err := core.WithOwnerShared(lctx, ie.inst.Dir, func(o core.Owner) error {
		if err := core.RequireOwner(o, core.OwnerNhm); err != nil {
			return nil
		}
		env := ie.env()
		open, err := env.Bindings.List(ctx, bindings.Filter{Statuses: []bindings.Status{bindings.Active, bindings.Displaced}})
		if err != nil {
			return err
		}
		if mail != nil && len(open) > 0 {
			var w []string
			gone, w = bindings.ReleaseGoneIdentities(ctx, env, mail, open)
			warnings = append(warnings, w...)
		}
		marks, err := env.Bindings.Operators(ctx)
		if err != nil || len(marks) == 0 {
			return err
		}
		// One snapshot, only when a mark exists: a pane that herdr lists
		// keeps its mark.
		snap, err := ie.herdr.Snapshot(ctx)
		if err != nil {
			warnings = append(warnings, "operator marks kept: herdr: "+err.Error())
			return nil
		}
		if len(snap.Panes) == 0 {
			// A server that is still restoring its panes lists none.
			return nil
		}
		live := map[string]bool{}
		for _, p := range snap.Panes {
			live[p.PaneID] = true
		}
		dropped, err = env.Bindings.PruneOperators(ctx, live)
		return err
	})
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("identity check: %v", err))
	}
	return gone, dropped, warnings
}
