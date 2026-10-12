package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store"
)

// autoImportLockWait bounds the wait for the owner lock: enroll never
// waits behind a takeover.
const autoImportLockWait = 500 * time.Millisecond

// autoImport runs the automatic import of Bash-spawned agents
// (bindings.AutoImport) for every pane, under the shared owner lock and
// any owner. It never fails its caller: what goes wrong comes back as
// warnings, and the hooks import each pane again.
func autoImport(ctx context.Context, inst store.Instance, db *store.DB, h *herdr.Client) bindings.AutoImportResult {
	env := bindings.Env{DB: db, Bindings: bindings.NewStore(db), Herdr: h}
	lctx, cancel := context.WithTimeout(ctx, autoImportLockWait)
	defer cancel()
	var res bindings.AutoImportResult
	err := core.WithOwnerShared(lctx, inst.Dir, func(core.Owner) error {
		var ierr error
		res, ierr = bindings.AutoImport(ctx, env, projectOf, bindings.ImportRequest{ProjectAt: projectAt})
		return ierr
	})
	warnings := []string{}
	switch {
	case errors.Is(err, core.ErrOwnerLockTimeout):
		warnings = append(warnings, "import of Bash-spawned agents skipped: the owner lock is busy")
	case err != nil:
		warnings = append(warnings, fmt.Sprintf("import of Bash-spawned agents: %v", err))
	}
	for _, w := range res.Warnings {
		warnings = append(warnings, "import: "+w)
	}
	res.Warnings = warnings
	return res
}
