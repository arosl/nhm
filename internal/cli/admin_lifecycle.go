package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store"
)

const adminLifecycleUsage = `Usage: nhm admin restore [--socket PATH] [--json]
       nhm admin recover inspect [--socket PATH] [--json]
       nhm admin recover resume <binding|name> (--dry-run | --run) [--socket PATH] [--json]
       nhm admin export --project DIR --to DIR [--socket PATH] [--json]
       nhm admin import --from DIR [--socket PATH] [--json]

restore is the plugin's startup hook: it verifies each binding against the
restored pane and native session, applies the herdr names, the sidebar
tokens and Agent Mail's identity files again, marks the rest displaced,
and starts nothing. Under ACFS's Bash it does nothing, and with no herdr
server running (an install before herdr starts) there is nothing to
restore: it exits 0 and says so.

recover inspect lists live, displaced and orphan bindings, the operations
with no final outcome, and the native sessions two bindings claim.

recover resume shows the resume argv for a binding (--dry-run), or, as
the operator's call on a terminal, starts it in the binding's pane and
bumps the generation (--run).

export writes a project's bindings with their tokens, its transcripts and
the keep list into a directory (mode 0700; export.json is 0600). import
reads it on the new host: each identity becomes a displaced binding to
adopt. Socket paths and pane ids never travel.
`

// runAdminLifecycle routes the lifecycle's admin verbs; ok is false for
// another admin verb.
func runAdminLifecycle(args []string, stdout, stderr io.Writer) (int, bool) {
	switch {
	case len(args) >= 1 && args[0] == "restore":
		return runRestore(args[1:], stdout, stderr), true
	case len(args) >= 2 && args[0] == "recover" && args[1] == "inspect":
		return runRecoverInspect(args[2:], stdout, stderr), true
	case len(args) >= 2 && args[0] == "recover" && args[1] == "resume":
		return runRecoverResume(args[2:], stdout, stderr), true
	case len(args) >= 1 && args[0] == "export":
		return runExport(args[1:], stdout, stderr), true
	case len(args) >= 1 && args[0] == "import":
		return runImport(args[1:], stdout, stderr), true
	case len(args) >= 1 && args[0] == "takeover":
		return runTakeover(args[1:], stdout, stderr), true
	}
	return 0, false
}

// adminFlags parses the common flags of the admin lifecycle verbs.
func adminFlags(name string, args []string, stderr io.Writer, extra func(*flag.FlagSet)) (pos []string, socket string, asJSON bool, ok bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, adminLifecycleUsage) }
	s := fs.String("socket", "", "herdr API socket")
	j := fs.Bool("json", false, "nhm's JSON envelope")
	if extra != nil {
		extra(fs)
	}
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return nil, "", false, false
	}
	return pos, *s, *j, true
}

func runRestore(args []string, stdout, stderr io.Writer) int {
	pos, socket, asJSON, ok := adminFlags("admin restore", args, stderr, nil)
	if !ok || len(pos) > 0 {
		return exitRefused
	}
	env := envelope{Command: "admin restore"}
	ctx := context.Background()
	ie, err := openInstance(socket)
	if errors.Is(err, store.ErrNotEnrolled) {
		// A startup hook from a server nhm is not enrolled for exits 0.
		return output(stdout, stderr, asJSON, env, nil, exitOK, func(w io.Writer) { fmt.Fprintln(w, "not enrolled; nothing to restore") })
	}
	if err != nil {
		return output(stdout, stderr, asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	if o, err := core.ReadOwner(ie.inst.Dir); err == nil && o.Owner != core.OwnerNhm {
		return output(stdout, stderr, asJSON, env, nil, exitOK, func(w io.Writer) {
			fmt.Fprintf(w, "the instance is owned by %s; nhm restores nothing\n", o.Owner)
		})
	}
	lenv, err := ie.lifecycleEnv()
	if err != nil {
		return output(stdout, stderr, asJSON, env, err, exitError, nil)
	}
	var items []lifecycle.RestoreItem
	code, err := underOwner(ctx, ie, func() error {
		var rerr error
		items, rerr = lifecycle.Restore(ctx, lenv)
		return rerr
	})
	if err != nil && herdr.ServerDown(err) {
		// No herdr server runs (ACFS's install or update before herdr
		// starts), so no pane is back to verify and nothing was changed;
		// the plugin runs restore when herdr starts (nhm-brp).
		env.Result = map[string]any{"bindings": []lifecycle.RestoreItem{}}
		return output(stdout, stderr, asJSON, env, nil, exitOK, func(w io.Writer) {
			fmt.Fprintln(w, "herdr is not running; nothing to restore")
		})
	}
	env.Result = map[string]any{"bindings": items}
	return output(stdout, stderr, asJSON, env, err, code, func(w io.Writer) {
		for _, it := range items {
			line := fmt.Sprintf("%s (%s): %s", it.MailName, dash(it.PaneID), it.Action)
			if it.Reason != "" {
				line += ": " + it.Reason
			}
			if len(it.Redone) > 0 {
				line += " (again: " + strings.Join(it.Redone, ", ") + ")"
			}
			fmt.Fprintln(w, line)
		}
	})
}

func runRecoverInspect(args []string, stdout, stderr io.Writer) int {
	pos, socket, asJSON, ok := adminFlags("admin recover inspect", args, stderr, nil)
	if !ok || len(pos) > 0 {
		return exitRefused
	}
	env := envelope{Command: "admin recover inspect"}
	ie, err := openInstance(socket)
	if err != nil {
		return output(stdout, stderr, asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	lenv, err := ie.lifecycleEnv()
	if err != nil {
		return output(stdout, stderr, asJSON, env, err, exitError, nil)
	}
	in, err := lifecycle.Inspect(context.Background(), lenv)
	env.Result = in
	if err != nil {
		return output(stdout, stderr, asJSON, env, err, exitError, nil)
	}
	return output(stdout, stderr, asJSON, env, nil, exitOK, func(w io.Writer) {
		for _, group := range []struct {
			name string
			list []lifecycle.InspectBinding
		}{{"live", in.Live}, {"displaced", in.Displaced}, {"orphan", in.Orphans}} {
			for _, b := range group.list {
				line := fmt.Sprintf("%-9s %s (%s) %s %s", group.name, b.MailName, b.Kind, dash(b.PaneID), b.Verdict)
				if b.Reason != "" {
					line += ": " + b.Reason
				}
				fmt.Fprintln(w, line)
			}
		}
		for _, p := range in.Pending {
			fmt.Fprintf(w, "pending   %s %s (%s), %d receipts unknown\n", p.OperationID, p.Verb, p.Status, p.Unknown)
		}
		for s, names := range in.Duplicates {
			fmt.Fprintf(w, "duplicate session %s: %s\n", s, strings.Join(names, ", "))
		}
	})
}

func runRecoverResume(args []string, stdout, stderr io.Writer) int {
	var dryRun, run bool
	pos, socket, asJSON, ok := adminFlags("admin recover resume", args, stderr, func(fs *flag.FlagSet) {
		fs.BoolVar(&dryRun, "dry-run", false, "show the resume argv")
		fs.BoolVar(&run, "run", false, "start it (on a terminal, after your yes)")
	})
	if !ok || len(pos) != 1 || dryRun == run {
		fmt.Fprint(stderr, adminLifecycleUsage)
		return exitRefused
	}
	env := envelope{Command: "admin recover resume"}
	ctx := context.Background()
	ie, err := openInstance(socket)
	if err != nil {
		return output(stdout, stderr, asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	lenv, err := ie.lifecycleEnv()
	if err != nil {
		return output(stdout, stderr, asJSON, env, err, exitError, nil)
	}
	plan, err := lifecycle.Resume(ctx, lenv, pos[0], false)
	code := exitOK
	if err == nil && run {
		switch {
		case !term.IsTerminal(int(os.Stdin.Fd())):
			code, err = exitRefused, errors.New("--run is the operator's: it needs a terminal")
		case !confirm(stderr, fmt.Sprintf("Start %s in %s?", strings.Join(plan.Argv, " "), plan.PaneID)):
			code, err = exitRefused, errors.New("nothing started")
		default:
			code, err = underOwner(ctx, ie, func() error {
				var rerr error
				plan, rerr = lifecycle.Resume(ctx, lenv, pos[0], true)
				return rerr
			})
		}
	} else if err != nil {
		code = exitRefused
	}
	env.Result = plan
	return output(stdout, stderr, asJSON, env, err, code, func(w io.Writer) {
		fmt.Fprintf(w, "%s in %s: %s\n", plan.MailName, plan.PaneID, strings.Join(plan.Argv, " "))
		if plan.Reason != "" {
			fmt.Fprintf(w, "  not ready: %s\n", plan.Reason)
		}
		if plan.Started {
			fmt.Fprintln(w, "  started")
		}
	})
}

func runExport(args []string, stdout, stderr io.Writer) int {
	var project, to string
	pos, socket, asJSON, ok := adminFlags("admin export", args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&project, "project", "", "the project")
		fs.StringVar(&to, "to", "", "the export directory")
	})
	if !ok || len(pos) > 0 || project == "" || to == "" {
		fmt.Fprint(stderr, adminLifecycleUsage)
		return exitRefused
	}
	env := envelope{Command: "admin export"}
	ctx := context.Background()
	ie, err := openInstance(socket)
	if err != nil {
		return output(stdout, stderr, asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	s, err := projectScope(ctx, ie.inst.ID, project)
	if err != nil {
		return output(stdout, stderr, asJSON, env, err, exitRefused, nil)
	}
	keep, err := lifecycle.LoadKeep(keepPaths()...)
	if err != nil {
		return output(stdout, stderr, asJSON, env, err, exitError, nil)
	}
	lenv, err := ie.lifecycleEnv()
	if err != nil {
		return output(stdout, stderr, asJSON, env, err, exitError, nil)
	}
	x, err := lifecycle.ExportTo(ctx, lenv, s.MailProjectKey, to, keep)
	x.Bindings = nil // tokens never reach stdout
	env.Result = map[string]any{"directory": to, "transcripts": len(x.Transcripts), "keep": x.Keep}
	code := exitOK
	if err != nil {
		code = exitError
	}
	return output(stdout, stderr, asJSON, env, err, code, func(w io.Writer) {
		fmt.Fprintf(w, "exported %s to %s (export.json holds the tokens: keep it private)\n", s.MailProjectKey, to)
	})
}

func runImport(args []string, stdout, stderr io.Writer) int {
	var from string
	pos, socket, asJSON, ok := adminFlags("admin import", args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&from, "from", "", "the export directory")
	})
	if !ok || len(pos) > 0 || from == "" {
		fmt.Fprint(stderr, adminLifecycleUsage)
		return exitRefused
	}
	env := envelope{Command: "admin import"}
	ctx := context.Background()
	ie, err := openInstance(socket)
	if err != nil {
		return output(stdout, stderr, asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	lenv, err := ie.lifecycleEnv()
	if err != nil {
		return output(stdout, stderr, asJSON, env, err, exitError, nil)
	}
	var keep, notes []string
	code, err := underOwner(ctx, ie, func() error {
		var ierr error
		keep, notes, ierr = lifecycle.ImportFrom(ctx, lenv, from)
		return ierr
	})
	env.Result = map[string]any{"notes": notes, "keep": keep}
	return output(stdout, stderr, asJSON, env, err, code, func(w io.Writer) {
		for _, n := range notes {
			fmt.Fprintln(w, n)
		}
		if len(keep) > 0 {
			quoted := make([]string, len(keep))
			for i, k := range keep {
				quoted[i] = fmt.Sprintf("%q", k)
			}
			fmt.Fprintf(w, "the keep list: put [reap] keep = [%s] in ~/.config/nhm/config.toml\n", strings.Join(quoted, ", "))
		}
	})
}
