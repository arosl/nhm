package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/wait"
)

const interruptUsage = `Usage: nhm interrupt [session] [--name N]… [--pane N.M]… [--type K]… [--include-self]
                     [--project DIR] [--op-id ID] [--socket PATH] [--json]

Send each selected agent its kind's interrupt key (Esc for Claude, Codex
and omp). The session is a workspace id or label; without it, the calling
pane's workspace. Without a selector, every bound agent of the session
except the caller. nhm refuses a kind whose key it has not checked, and an
agent whose binding changed. One operation id sends at most once. Runs
only when nhm owns this herdr instance.

Exit codes: 0 sent to every target; 1 an error or a partial result;
2 refused (bad arguments, no agent, not the owner, or every target
refused).
`

func runInterrupt(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("interrupt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, interruptUsage) }
	sel := addSelectorFlags(fs)
	includeSelf := fs.Bool("include-self", false, "also interrupt the calling pane")
	project := fs.String("project", "", "only this project's agents")
	opID := fs.String("op-id", "", "operation id, for an idempotent retry")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	if len(pos) > 1 {
		fmt.Fprint(stderr, interruptUsage)
		return exitRefused
	}
	session := ""
	if len(pos) == 1 {
		session = pos[0]
	}
	env := envelope{Command: "interrupt"}
	ctx := context.Background()
	ie, err := openInstance(*socket)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	store := bindings.NewStore(ie.db)
	projectKey := ""
	if *project != "" {
		s, err := projectScope(ctx, ie.inst.ID, *project)
		if err != nil {
			return output(stdout, stderr, *asJSON, env, err, exitRefused, nil)
		}
		projectKey, env.Scope = s.MailProjectKey, s
	}
	ws, err := sessionWorkspace(ctx, ie.herdr, session)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitRefused, nil)
	}
	s := sel.selector()
	if !*includeSelf {
		s.Exclude = ie.callerPane(ctx)
	}
	targets, err := wait.Select(ctx, store, ws, projectKey, s)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitRefused, nil)
	}
	scope := map[string]string{"instance_id": ie.inst.ID, "workspace_id": ws}
	if projectKey != "" {
		scope["mail_project_key"] = projectKey
	}
	lctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	var res wait.InterruptResult
	code := exitOK
	err = core.WithOwnerShared(lctx, ie.inst.Dir, func(o core.Owner) error {
		if err := core.RequireOwner(o, core.OwnerNhm); err != nil {
			code = exitRefused
			return err
		}
		var ierr error
		res, ierr = wait.Interrupt(ctx, ie.db, ie.herdr, store, wait.InterruptRequest{OperationID: *opID, Scope: scope, Targets: targets}, time.Now)
		return ierr
	})
	switch {
	case err != nil && code == exitOK:
		code = exitError
	case err != nil:
	case res.Existing, res.Status == "done":
		code = exitOK
	case res.Status == "refused":
		code = exitRefused
	default:
		code = exitError
	}
	env.OperationID, env.Result = res.OperationID, res
	return output(stdout, stderr, *asJSON, env, err, code, func(w io.Writer) { printInterrupt(w, res) })
}

func printInterrupt(w io.Writer, res wait.InterruptResult) {
	if res.Existing {
		fmt.Fprintf(w, "operation %s was claimed before (%s); nothing sent\n", res.OperationID, res.Status)
		return
	}
	for _, t := range res.Targets {
		line := fmt.Sprintf("%s (%s): %s", t.MailName, dash(t.Slot), t.Status)
		if len(t.Keys) > 0 && t.Status == wait.InterruptSent {
			line += " " + strings.Join(t.Keys, " ")
		}
		if t.Reason != "" {
			line += ": " + t.Reason
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintf(w, "operation %s: %s\n", res.OperationID, res.Status)
}
