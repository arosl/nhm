package cli

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/session"
	"github.com/arosl/nhm/internal/core/wait"
)

const interruptUsage = `Usage: nhm interrupt <session> [--tag T]… [--json]
                     [--name N]… [--pane N.M]… [--type K]… [--include-self]
                     [--project DIR] [--op-id ID] [--socket PATH]

ntm's interrupt: send each agent of the session its interrupt key,
whatever it is doing: Ctrl+C, or Escape for omp and pi. agy is left out,
as ntm leaves out antigravity, and so is the calling pane, nhm's user
pane. --tag (repeatable, or a comma list) keeps the agents whose herdr
pane label ends in one of the tags: "IcyKnoll opus[frontend,api]".

nhm's selectors narrow the agents further: --name, --pane (the slot),
--type, --project; --include-self adds the calling pane. One operation
id (--op-id) sends at most once. nhm sends no key to a kind whose key it
has not checked (opencode, grok): that agent fails. Runs only when nhm
owns this herdr instance.

Prints "Sent Ctrl+C to N agent pane(s)", or with an Escape among them
"Sent interrupt to N agent pane(s) (Ctrl+C: a, Escape: b)". --json gives
ntm's InterruptResponse, with nhm's operation under "_nhm".

Exit codes: 0 every target interrupted; 1 an error, or a target failed.
`

// runInterruptNTM is ntm's runInterrupt on nhm's bound agents. An error is
// ntm's: returned for the root's "Error: …", or under --json ntm's
// failure envelope; either way exit 1.
func runInterruptNTM(name string, f interruptFlags, asJSON bool, stdout, stderr io.Writer) error {
	fail := func(err error) error {
		if asJSON {
			return jsonFailure(stdout, err)
		}
		return err
	}
	ctx := context.Background()
	ie, err := openInstance(f.socket)
	if err != nil {
		return fail(err)
	}
	defer ie.db.Close()
	// ntm's interrupt takes a unique prefix of the name under --json too
	// (normalizeExplicitLiveSessionName(…, true)).
	r, err := resolveSession(ctx, ie, name, false, stdout, stderr)
	if err != nil {
		return fail(err)
	}
	if r.WorkspaceID == "" {
		return nil // a cancelled picker
	}
	ws := r.WorkspaceID
	store := bindings.NewStore(ie.db)
	projectKey := ""
	if f.project != "" {
		s, err := projectScope(ctx, ie.inst.ID, f.project)
		if err != nil {
			return fail(err)
		}
		projectKey = s.MailProjectKey
	}
	all, err := store.List(ctx, bindings.Filter{WorkspaceID: ws, Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return fail(err)
	}
	sel := wait.Selector{Names: f.names, Slots: f.slots, Kinds: f.kinds}
	if !f.includeSelf {
		sel.Exclude = ie.callerPane(ctx)
	}
	// In ntm's form a session without agents is no error: ntm sends to
	// none. nhm's selectors that match no agent stay one.
	targets, err := wait.Select(ctx, store, ws, projectKey, sel)
	if errors.Is(err, wait.ErrNoTargets) && len(sel.Names)+len(sel.Slots)+len(sel.Kinds) == 0 && projectKey == "" {
		targets, err = nil, nil
	}
	if err != nil {
		return fail(err)
	}
	targets, skipped, err := interruptTargets(ctx, ie, targets, f.tags)
	if err != nil {
		return fail(err)
	}
	scope := map[string]string{"instance_id": ie.inst.ID, "workspace_id": ws}
	if projectKey != "" {
		scope["mail_project_key"] = projectKey
	}
	lctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	var res wait.InterruptResult
	err = core.WithOwnerShared(lctx, ie.inst.Dir, func(o core.Owner) error {
		if err := core.RequireOwner(o, core.OwnerNhm); err != nil {
			return err
		}
		var ierr error
		res, ierr = wait.Interrupt(ctx, ie.db, ie.herdr, store, wait.InterruptRequest{OperationID: f.opID, Scope: scope, Targets: targets}, time.Now)
		return ierr
	})
	if err != nil {
		return fail(err)
	}
	resp := buildInterruptResponse(r.Session, res, skipped, len(session.AgentTabs(all)) > 1)
	if asJSON {
		if err := writeNTMJSON(stdout, resp); err != nil {
			return err
		}
		if !resp.Success {
			return verbExit{code: exitError}
		}
		return nil
	}
	return printInterruptText(stdout, resp)
}

// interruptTargets keeps the agents that ntm's interrupt reaches: not
// agy, and with tags, those whose pane label holds one. skipped counts
// the agents that the tags left out, as ntm counts them.
func interruptTargets(ctx context.Context, ie *instanceEnv, list []bindings.Binding, tags []string) ([]bindings.Binding, int, error) {
	labels := map[string]string{}
	if len(tags) > 0 {
		snap, err := ie.herdr.Snapshot(ctx)
		if err != nil {
			return nil, 0, err
		}
		for _, p := range snap.Panes {
			labels[p.PaneID] = p.Label
		}
	}
	var out []bindings.Binding
	skipped := 0
	for _, b := range list {
		if !wait.NTMInterrupts(b.Kind) {
			continue
		}
		if len(tags) > 0 && !session.HasAnyTag(session.PaneTags(labels[b.PaneID]), tags) {
			skipped++
			continue
		}
		out = append(out, b)
	}
	return out, skipped, nil
}
