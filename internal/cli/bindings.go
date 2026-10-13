package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store"
)

// Exit codes of the native verbs (plan-v8 2.12).
const (
	exitOK      = 0
	exitError   = 1
	exitRefused = 2
)

// lockWait bounds the wait for the owner lock.
const lockWait = 10 * time.Second

// envelope is nhm's --json output for the native verbs.
type envelope struct {
	Schema      string   `json:"schema"`
	Command     string   `json:"command"`
	OperationID string   `json:"operation_id,omitempty"`
	Scope       any      `json:"scope,omitempty"`
	Result      any      `json:"result"`
	Warnings    []string `json:"warnings"`
	Error       *string  `json:"error"`
}

// instanceEnv is an enrolled instance, opened.
type instanceEnv struct {
	inst  store.Instance
	db    *store.DB
	herdr *herdr.Client
}

// openInstance finds the instance enrolled for the herdr socket
// (--socket, or HERDR_SOCKET_PATH) and opens its database.
func openInstance(socket string) (*instanceEnv, error) {
	if socket == "" {
		socket = os.Getenv("HERDR_SOCKET_PATH")
	}
	if socket == "" {
		return nil, errors.New("no herdr socket: give --socket or run inside herdr (HERDR_SOCKET_PATH)")
	}
	root, err := store.StateRoot()
	if err != nil {
		return nil, err
	}
	inst, err := store.Lookup(root, socket)
	if err != nil {
		return nil, err
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		return nil, err
	}
	return &instanceEnv{inst: inst, db: db, herdr: herdr.New(socket)}, nil
}

func (e *instanceEnv) env() bindings.Env {
	return bindings.Env{DB: e.db, Bindings: bindings.NewStore(e.db), Herdr: e.herdr}
}

// callerPane is the calling pane as the bindings know it: HERDR_PANE_ID,
// or herdr's new id for it after a move to another workspace
// (bindings.CallerPane).
func (e *instanceEnv) callerPane(ctx context.Context) string {
	return bindings.CallerPane(ctx, bindings.NewStore(e.db), e.herdr, os.Getenv("HERDR_PANE_ID"))
}

// parseInterleaved parses flags that may come before or after the
// positional arguments, and returns the positional ones.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		if args[0] == "--" {
			return append(pos, args[1:]...), nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// output writes a verb's result: the envelope with --json, the text
// otherwise. It returns the exit code.
func output(stdout, stderr io.Writer, asJSON bool, env envelope, err error, code int, text func(io.Writer)) int {
	if env.Warnings == nil {
		env.Warnings = []string{}
	}
	if asJSON {
		env.Schema = "nhm.v1"
		if err != nil {
			msg := err.Error()
			env.Error = &msg
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if e := enc.Encode(env); e != nil {
			fmt.Fprintf(stderr, "nhm %s: %v\n", env.Command, e)
			return exitError
		}
		return code
	}
	for _, w := range env.Warnings {
		fmt.Fprintf(stderr, "nhm %s: warning: %s\n", env.Command, w)
	}
	if err != nil {
		fmt.Fprintf(stderr, "nhm %s: %v\n", env.Command, err)
		return code
	}
	if text != nil {
		text(stdout)
	}
	return code
}

// projectScope resolves --project or the cwd to a scope, without a
// workspace.
func projectScope(ctx context.Context, instanceID, project string) (core.Scope, error) {
	cwd, _ := os.Getwd()
	return core.ResolveScope(ctx, core.ScopeRequest{InstanceID: instanceID, ProjectRoot: project, Cwd: cwd}, nil)
}

// projectOf maps a pane's cwd to its project for the import: the git
// work tree that holds it, or, outside git, the folder itself, as the
// Bash keys an agent spawned there (acfs-9hkh.80). The import still binds
// only on verified evidence under that key: Agent Mail's identity file or
// the Bash's spawn token.
func projectOf(ctx context.Context, cwd string) (string, string, error) {
	s, err := core.ResolveScope(ctx, core.ScopeRequest{Cwd: cwd}, nil)
	if errors.Is(err, core.ErrNoProject) && cwd != "" {
		s, err = core.ResolveScope(ctx, core.ScopeRequest{ProjectRoot: cwd}, nil)
	}
	if err != nil {
		return "", "", err
	}
	return s.MailProjectKey, s.ProjectID, nil
}

// projectAt is the project rooted exactly at dir, as --project takes it:
// the import's second candidate for a pane in a subfolder of a work tree,
// which the Bash keys by the folder (bindings.ImportRequest.ProjectAt).
func projectAt(ctx context.Context, dir string) (string, string, error) {
	s, err := core.ResolveScope(ctx, core.ScopeRequest{ProjectRoot: dir}, nil)
	if err != nil {
		return "", "", err
	}
	return s.MailProjectKey, s.ProjectID, nil
}

const resolveUsage = `Usage: nhm resolve <name|herdr-name|pane-id|--current> [--project DIR] [--socket PATH] [--json]

Print the binding of an Agent Mail name, a herdr name or a pane, its
generation, and how nhm verified it against herdr.
`

func runResolve(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, resolveUsage) }
	current := fs.Bool("current", false, "the calling pane (HERDR_PANE_ID)")
	project := fs.String("project", "", "limit a name to this project")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	if (len(pos) == 1) == *current || len(pos) > 1 {
		fmt.Fprint(stderr, resolveUsage)
		return exitRefused
	}
	q := bindings.Query{Current: *current}
	if len(pos) == 1 {
		q.Target = pos[0]
	}
	env := envelope{Command: "resolve"}
	ctx := context.Background()
	ie, err := openInstance(*socket)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	if *project != "" {
		s, err := projectScope(ctx, ie.inst.ID, *project)
		if err != nil {
			return output(stdout, stderr, *asJSON, env, err, exitRefused, nil)
		}
		q.ProjectKey, env.Scope = s.MailProjectKey, s
	}
	r, err := bindings.Resolve(ctx, bindings.NewStore(ie.db), ie.herdr, q, os.Getenv)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	env.Result = r
	return output(stdout, stderr, *asJSON, env, nil, exitOK, func(w io.Writer) { printResolution(w, r) })
}

func printResolution(w io.Writer, r bindings.Resolution) {
	b := r.Binding
	fmt.Fprintf(w, "%s (%s) %s generation %d, %s\n", b.MailName, b.Kind, b.ID, b.Generation, b.Status)
	if b.StatusReason != "" {
		fmt.Fprintf(w, "  reason: %s\n", b.StatusReason)
	}
	fmt.Fprintf(w, "  pane %s, slot %s, herdr name %s, project %s\n", dash(b.PaneID), dash(b.Slot), dash(b.HerdrAlias), b.MailProjectKey)
	fmt.Fprintf(w, "  verification: %s\n", r.Verification.Verdict)
	for _, c := range r.Verification.Checks {
		fmt.Fprintf(w, "    %-8s %-7s %s\n", c.Name, c.Result, c.Detail)
	}
	for _, e := range b.Evidence {
		fmt.Fprintf(w, "  evidence: %s: %s\n", e.Kind, e.Detail)
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

const adoptUsage = `Usage: nhm adopt <MailName> [--pane ID] [--handoff-from BINDING] [--kind K] [--model M]
                 [--project DIR] [--op-id ID] [--socket PATH] [--json]

Bind an existing Agent Mail identity to the agent in a pane, by hand. The
pane is --pane or the calling pane. --handoff-from gives the pane's agent
the identity of that binding (for example a session casr converted) and
bumps its generation. As spawn does, it labels the pane "<MailName>
<model>" and, when the pane is alone in its tab, the tab "<MailName>".
Runs only when nhm owns this herdr instance.
`

func runAdopt(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("adopt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, adoptUsage) }
	pane := fs.String("pane", "", "the agent's pane (default HERDR_PANE_ID)")
	handoff := fs.String("handoff-from", "", "the binding whose identity the agent takes over")
	kind := fs.String("kind", "", "the kind herdr must see in the pane")
	model := fs.String("model", "", "the agent's model")
	project := fs.String("project", "", "the project (default: the git work tree of the cwd)")
	opID := fs.String("op-id", "", "operation id, for an idempotent retry")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	if len(pos) > 1 || (len(pos) == 0 && *handoff == "") {
		fmt.Fprint(stderr, adoptUsage)
		return exitRefused
	}
	req := bindings.AdoptRequest{PaneID: *pane, HandoffFrom: *handoff, Kind: *kind, Model: *model, OperationID: *opID}
	if len(pos) == 1 {
		req.MailName = pos[0]
	}
	env := envelope{Command: "adopt"}
	ctx := context.Background()
	ie, err := openInstance(*socket)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	if req.PaneID == "" {
		req.PaneID = ie.callerPane(ctx)
	}
	if req.HandoffFrom == "" {
		s, err := projectScope(ctx, ie.inst.ID, *project)
		if err != nil {
			return output(stdout, stderr, *asJSON, env, err, exitRefused, nil)
		}
		req.ProjectKey, req.ProjectID, env.Scope = s.MailProjectKey, s.ProjectID, s
	}
	res, code, err := adoptUnderOwner(ctx, ie, req)
	env.OperationID, env.Result, env.Warnings = res.OperationID, res, res.Warnings
	return output(stdout, stderr, *asJSON, env, err, code, func(w io.Writer) {
		b := res.Binding
		verb := "adopted"
		if !res.Created {
			verb = "handed over"
		}
		fmt.Fprintf(w, "%s %s (%s) in %s: %s generation %d, slot %s\n", verb, b.MailName, b.Kind, b.PaneID, b.ID, b.Generation, dash(b.Slot))
	})
}

// adoptUnderOwner runs adopt under the owner lock, for nhm adopt and the
// surfaces' adopt job.
func adoptUnderOwner(ctx context.Context, ie *instanceEnv, req bindings.AdoptRequest) (bindings.AdoptResult, int, error) {
	lctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	var res bindings.AdoptResult
	code := exitOK
	err := core.WithOwnerShared(lctx, ie.inst.Dir, func(o core.Owner) error {
		if err := core.RequireOwner(o, core.OwnerNhm); err != nil {
			code = exitRefused
			return err
		}
		var aerr error
		res, aerr = bindings.Adopt(ctx, ie.env(), req)
		return aerr
	})
	switch {
	case errors.Is(err, bindings.ErrAgentChanged):
		// Another agent is in the pane since the review: a refusal.
		code = exitRefused
	case err != nil && code == exitOK:
		code = exitError
	}
	return res, code, err
}

const recoverUsage = `Usage: nhm admin recover reconcile --import-only [--project DIR] [--dry-run] [--op-id ID] [--socket PATH] [--json]

Read the agents that ACFS's Bash spawned into nhm's bindings, on verified
evidence only: Agent Mail's identity file for the pane, or the Bash's
token, tab label and herdr name together. A name match alone is never
enough. Changes nothing in herdr; runs under any owner.
`

func runAdmin(args []string, stdout, stderr io.Writer) int {
	if len(args) >= 2 && args[0] == "recover" && args[1] == "reconcile" {
		return runRecoverReconcile(args[2:], stdout, stderr)
	}
	if len(args) >= 2 && args[0] == "instance" && args[1] == "enroll" {
		return runInstanceEnroll(args[2:], stdout, stderr)
	}
	if code, ok := runAdminLifecycle(args, stdout, stderr); ok {
		return code
	}
	fmt.Fprint(stderr, enrollUsage+"\n"+recoverUsage)
	return exitRefused
}

func runRecoverReconcile(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("admin recover reconcile", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, recoverUsage) }
	importOnly := fs.Bool("import-only", false, "import Bash-spawned agents; required")
	project := fs.String("project", "", "only this project")
	dryRun := fs.Bool("dry-run", false, "say what would be imported")
	opID := fs.String("op-id", "", "operation id, for an idempotent retry")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	if len(pos) > 0 || !*importOnly {
		fmt.Fprint(stderr, recoverUsage)
		return exitRefused
	}
	env := envelope{Command: "admin recover reconcile"}
	ctx := context.Background()
	ie, err := openInstance(*socket)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	req := bindings.ImportRequest{DryRun: *dryRun, OperationID: *opID, ProjectAt: projectAt}
	if *project != "" {
		s, err := projectScope(ctx, ie.inst.ID, *project)
		if err != nil {
			return output(stdout, stderr, *asJSON, env, err, exitRefused, nil)
		}
		req.ProjectKey, env.Scope = s.MailProjectKey, s
	}
	lctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	var rep bindings.ImportReport
	err = core.WithOwnerShared(lctx, ie.inst.Dir, func(core.Owner) error {
		var ierr error
		rep, ierr = bindings.ImportOnly(ctx, ie.env(), projectOf, req)
		return ierr
	})
	env.OperationID, env.Result, env.Warnings = rep.OperationID, rep, rep.Warnings
	code := exitOK
	if err != nil {
		code = exitError
	}
	return output(stdout, stderr, *asJSON, env, err, code, func(w io.Writer) {
		for _, it := range rep.Items {
			line := fmt.Sprintf("%-13s %-8s %-7s %s", it.Action, it.PaneID, it.Kind, dash(it.MailName))
			if it.Reason != "" {
				line += ": " + it.Reason
			}
			fmt.Fprintln(w, strings.TrimRight(line, " "))
		}
		fmt.Fprintf(w, "%d imported\n", rep.Imported)
	})
}
