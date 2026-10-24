package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/core/owner"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
)

// switchLockWait bounds the wait for the exclusive owner lock: the
// mutators hold it shared through bounded steps only.
const switchLockWait = 60 * time.Second

const takeoverUsage = `Usage: nhm admin takeover (--dry-run | --yes) [--exclude NAME]… [--socket PATH] [--json]

takeover makes nhm the owner of this herdr server (the operator's step,
and ACFS's install and update; plan-v8 section 5 step 6). Under the
exclusive owner lock, so the commands of an old ACFS Bash wait and then
refuse, it imports what the Bash kept in $ACFS_HOME/state (default
~/.acfs/state): the registration tokens (state/tokens) and each agent's
wake state (state/wake). It reads those files itself and runs no acfs
command. 'admin recover reconcile' reads the agents the Bash spawned
into bindings. Each token is checked with Agent Mail first
(unretire_agent on the active identity, which changes nothing): a token
Agent Mail refuses, because the agent registered again after its spawn,
is reported stale and not kept, and 'nhm retire' then leaves that
identity active. Mail the Bash already woke an agent for is marked
announced, so nhm does not nudge for it again. Any entry that cannot be
imported refuses, the Bash stays the owner, and nothing is written: an
agent the import skips, or a token that no binding takes. --yes checks
everything as --dry-run does before it writes. --exclude leaves one
agent behind on purpose, and the report lists it (repeatable): NAME (an
Agent Mail or herdr name, when only one project has it), /project:NAME,
or the pane id of a skipped agent (its token goes with it). It deletes
nothing of the Bash's. With no Bash state, nothing of it is imported.
With no herdr server running (a fresh install), it goes on only when
the Bash keeps no token and no wake state: no pane holds an agent,
nothing is imported, and only the owner changes; otherwise start herdr
first. Nothing gives the server back: ACFS's Bash agent tools are gone.

--dry-run reads and checks everything and changes nothing. --yes does it.

Exit codes: 0 done (or the dry run passed); 1 an error; 2 refused.
`

func (ie *instanceEnv) ownerEnv() (owner.Env, error) {
	mail, err := agentmail.NewClient("")
	if err != nil {
		return owner.Env{}, err
	}
	// The Bash's token files are its spawn evidence for the import too.
	acfsHome := bindings.DefaultAcfsHome()
	return owner.Env{DB: ie.db, Bindings: bindings.NewStore(ie.db), InstanceDir: ie.inst.Dir,
		AcfsHome: acfsHome, Events: owner.EventsCLI{}, Mail: mail,
		ImportBindings: func(ctx context.Context, dry bool, snap *herdr.SessionSnapshot) (bindings.ImportReport, error) {
			return bindings.ImportOnly(ctx, ie.env(), projectOf, bindings.ImportRequest{DryRun: dry, AcfsHome: acfsHome,
				ProjectAt: projectAt, Snapshot: snap})
		},
		Keep: func() ([]string, error) { return lifecycle.LoadKeep(keepPaths()...) },
	}, nil
}

func runTakeover(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("admin takeover", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, takeoverUsage) }
	dryRun := fs.Bool("dry-run", false, "check everything; change nothing")
	yes := fs.Bool("yes", false, "do it")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	var exclude listFlag
	fs.Var(&exclude, "exclude", "an agent to leave behind (repeatable)")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) > 0 || *dryRun == *yes {
		if err == nil {
			fmt.Fprint(stderr, takeoverUsage)
		}
		return exitRefused
	}
	env := envelope{Command: "admin takeover"}
	ie, err := openInstance(*socket)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	oenv, err := ie.ownerEnv()
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	oenv.Exclude = exclude
	// SIGINT and SIGTERM cancel the run instead of killing the process,
	// so it ends as a failure that leaves the owner as it was.
	sctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The lock wait and the import (an inbox read per agent) together.
	ctx, cancel := context.WithTimeout(sctx, switchLockWait+5*time.Minute)
	defer cancel()
	rep, err := owner.Takeover(ctx, oenv, *dryRun)
	env.Result, env.Warnings = rep, rep.Notes
	code := exitOK
	if err != nil {
		// Every failure leaves the owner as it was; a rule's refusal
		// exits 2, a failure to read or write 1.
		code = exitError
		if errors.Is(err, owner.ErrRefused) {
			code = exitRefused
		}
	}
	return output(stdout, stderr, *asJSON, env, err, code, func(w io.Writer) { printTakeover(w, rep) })
}

func printTakeover(w io.Writer, rep owner.Report) {
	if rep.Bindings != nil {
		fmt.Fprintf(w, "agents the Bash spawned: %d imported\n", rep.Bindings.Imported)
	}
	for _, it := range rep.Tokens {
		line := fmt.Sprintf("token %s (%s): %s", it.Name, dash(it.ProjectKey), strings.ReplaceAll(it.Action, "_", " "))
		if it.Reason != "" {
			line += ": " + it.Reason
		}
		fmt.Fprintln(w, line)
	}
	for _, it := range rep.Wake {
		line := fmt.Sprintf("wake %s: %s", it.Name, strings.ReplaceAll(it.Action, "_", " "))
		if it.Handled+it.New > 0 {
			line += fmt.Sprintf(" (%d already woken for, %d new)", it.Handled, it.New)
		}
		if it.Reason != "" {
			line += ": " + it.Reason
		}
		fmt.Fprintln(w, line)
	}
	if len(rep.Keep) > 0 {
		fmt.Fprintf(w, "keep: %s\n", strings.Join(rep.Keep, ", "))
	}
	if len(rep.Excluded) > 0 {
		fmt.Fprintf(w, "left behind (--exclude): %s\n", strings.Join(rep.Excluded, ", "))
	}
	switch {
	case rep.DryRun:
		fmt.Fprintln(w, "the takeover would pass; nothing changed")
	default:
		fmt.Fprintf(w, "owner: %s (generation %d)\n", rep.Owner.Owner, rep.Owner.Generation)
	}
}
