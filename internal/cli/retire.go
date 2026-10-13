package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/integrations/cass"
)

// lifecycleLockWait bounds the wait for the owner lock. The lifecycle
// verbs hold it through bounded steps only (a recycle's stop waits 30 s).
const lifecycleLockWait = 10 * time.Second

// lifecycleFacts reads Agent Mail, git and br for retire and reap; nil is
// the CLI with nhm's configured bead databases. Tests replace it, so they
// never reach the host's tools.
var lifecycleFacts lifecycle.Facts

// beadDBs are the bead databases of nhm's config ([lifecycle] bead_dbs)
// and of NHM_BEAD_DBS (absolute paths, separated by ':').
func beadDBs() ([]string, error) {
	var dbs []string
	if dir, err := os.UserConfigDir(); err == nil {
		list, err := lifecycle.LoadBeadDBs(dir + "/nhm/config.toml")
		if err != nil {
			return nil, err
		}
		dbs = append(dbs, list...)
	}
	for _, p := range strings.Split(os.Getenv("NHM_BEAD_DBS"), ":") {
		if p == "" {
			continue
		}
		if !strings.HasPrefix(p, "/") {
			return nil, fmt.Errorf("NHM_BEAD_DBS takes absolute paths, not %q", p)
		}
		dbs = append(dbs, p)
	}
	return dbs, nil
}

// lifecycleEnv is the lifecycle verbs' environment on an open instance.
func (ie *instanceEnv) lifecycleEnv() (lifecycle.Env, error) {
	mail, err := agentmail.NewClient("")
	if err != nil {
		return lifecycle.Env{}, err
	}
	facts := lifecycleFacts
	if facts == nil {
		facts = lifecycle.CLI{LoadBeadDBs: beadDBs}
	}
	home, _ := os.UserHomeDir()
	configHome, _ := agentmail.ConfigHome()
	senv := ie.sendEnv()
	return lifecycle.Env{DB: ie.db, Bindings: bindings.NewStore(ie.db), Herdr: ie.herdr, Mail: mail, Facts: facts,
		InstanceDir: ie.inst.Dir, Home: home, MailConfigHome: configHome, Caller: os.Getenv("HERDR_PANE_ID"),
		Send:           func(ctx context.Context, r send.Request) (send.Result, error) { return send.Send(ctx, senv, r) },
		Handoff:        lifecycle.DefaultHandoff("", ""),
		LaunchSettings: spawn.HookSettings(exec.LookPath),
		Index: func(ctx context.Context, path, project, key string) (string, error) {
			return cass.Client{Run: cass.Exec("")}.IndexSession(ctx, path, project, key)
		},
		ProcStart: bindings.ProcStart}, nil
}

// underOwner runs fn under the shared owner lock when nhm owns the
// instance; code is exitRefused when it does not.
func underOwner(ctx context.Context, ie *instanceEnv, fn func() error) (int, error) {
	lctx, cancel := context.WithTimeout(ctx, lifecycleLockWait)
	defer cancel()
	code := exitOK
	err := core.WithOwnerShared(lctx, ie.inst.Dir, func(o core.Owner) error {
		if err := core.RequireOwner(o, core.OwnerNhm); err != nil {
			code = exitRefused
			return err
		}
		return fn()
	})
	if err != nil && code == exitOK {
		code = exitError
	}
	return code, err
}

const retireUsage = `Usage: nhm retire <Name> [--keep-identity] [--cleanup-worktrees] [--no-beads] [--dry-run]
                  [--project DIR] [--op-id ID] [--socket PATH] [--json]

Close an agent's pane, preserve its transcript (and index it in CASS),
record the closed beads it worked, and retire its Agent Mail identity
with the kept token. It checks that token with Agent Mail first: when the
agent registered again since nhm kept it, the token is stale, and the
identity stays active; a warning says how to finish. It refuses, before
anything changes, when the agent
is working, blocked or unknown, is displaced or being recycled, holds a
reservation, has uncommitted changes in files it reserved, has a dirty
or unmerged worktree, has a bead in progress with no handoff comment, has
no bead at all in the bead databases searched (the project's own and
[lifecycle] bead_dbs in nhm's config), or when Agent Mail, br or git
cannot be read. --no-beads retires an agent that worked no beads, such
as a reviewer, also in a project with no bead database; a bead in
progress with no handoff still refuses. Worktrees are listed, and
removed only with --cleanup-worktrees (merged, clean, never forced).

Exit codes: 0 retired (or the dry run passed); 1 an error; 2 refused.
`

func runRetire(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("retire", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, retireUsage) }
	keep := fs.Bool("keep-identity", false, "leave the Agent Mail identity active")
	cleanup := fs.Bool("cleanup-worktrees", false, "remove the agent's merged, clean worktrees")
	noBeads := fs.Bool("no-beads", false, "the agent worked no beads; do not require one")
	dryRun := fs.Bool("dry-run", false, "check and list the actions; change nothing")
	project := fs.String("project", "", "the name's project")
	opID := fs.String("op-id", "", "operation id, for an idempotent retry")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 1 {
		if err == nil {
			fmt.Fprint(stderr, retireUsage)
		}
		return exitRefused
	}
	env := envelope{Command: "retire"}
	ctx := context.Background()
	ie, err := openInstance(*socket)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	lenv, err := ie.lifecycleEnv()
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	req := lifecycle.RetireRequest{Target: pos[0], KeepIdentity: *keep, CleanupWorktrees: *cleanup, NoBeads: *noBeads,
		DryRun: *dryRun, OperationID: *opID}
	if *project != "" {
		s, err := projectScope(ctx, ie.inst.ID, *project)
		if err != nil {
			return output(stdout, stderr, *asJSON, env, err, exitRefused, nil)
		}
		req.ProjectKey = s.MailProjectKey
	}
	var res lifecycle.RetireResult
	run := func() error {
		var rerr error
		res, rerr = lifecycle.Retire(ctx, lenv, req)
		return rerr
	}
	code := exitOK
	if *dryRun {
		err = run()
	} else {
		code, err = underOwner(ctx, ie, run)
	}
	if errors.Is(err, lifecycle.ErrRefused) {
		code = exitRefused
		err = fmt.Errorf("refused: %s", strings.Join(res.Refusals, "; "))
	}
	env.OperationID, env.Result, env.Warnings = res.OperationID, res, res.Warnings
	return output(stdout, stderr, *asJSON, env, err, code, func(w io.Writer) { printRetire(w, res) })
}

func printRetire(w io.Writer, res lifecycle.RetireResult) {
	verb := "retired"
	if res.DryRun {
		verb = "would retire"
		for _, a := range res.Actions {
			fmt.Fprintf(w, "would %s\n", a)
		}
	}
	for _, wt := range res.Worktrees {
		fmt.Fprintf(w, "worktree %s\n", wt)
	}
	if res.Transcript != nil {
		fmt.Fprintf(w, "transcript preserved: %s (%d bytes); reflect on it: cm reflect\n", res.Transcript.Path, res.Transcript.Bytes)
	}
	if len(res.ClosedBeads) > 0 {
		fmt.Fprintf(w, "closed beads: %s\n", strings.Join(res.ClosedBeads, ", "))
	}
	if res.Done || res.DryRun {
		fmt.Fprintf(w, "%s %s\n", verb, res.MailName)
	}
}

const recycleUsage = `Usage: nhm recycle <Name> [-t KEY | --prompt T] [--no-handoff]
                   [--project DIR] [--op-id ID] [--socket PATH] [--json]

Give the same agent a fresh session in the same pane, with the same name,
tab and slot: preserve its transcript, compose a handoff (the bead's
latest handoff comment, cass pack of the session, cm context), stop the
old agent with its kind's exit command, start the successor in the same
pane, bump the binding's generation, and send the kickoff with the
handoff. Sends and wakes skip the agent while it recycles. On a failure
it reports the stage and starts nothing twice.

Exit codes: 0 recycled; 1 stopped at a stage; 2 refused.
`

func runRecycle(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("recycle", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, recycleUsage) }
	var tmpl string
	fs.StringVar(&tmpl, "template", "", "the kickoff's template key")
	fs.StringVar(&tmpl, "t", "", "the kickoff's template key")
	prompt := fs.String("prompt", "", "the kickoff prompt")
	noHandoff := fs.Bool("no-handoff", false, "send no handoff")
	project := fs.String("project", "", "the name's project")
	opID := fs.String("op-id", "", "operation id, for an idempotent retry")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 1 {
		if err == nil {
			fmt.Fprint(stderr, recycleUsage)
		}
		return exitRefused
	}
	env := envelope{Command: "recycle"}
	ctx := context.Background()
	ie, err := openInstance(*socket)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	lenv, err := ie.lifecycleEnv()
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	req := lifecycle.RecycleRequest{Target: pos[0], Template: tmpl, Prompt: *prompt, NoHandoff: *noHandoff, OperationID: *opID}
	if *project != "" {
		s, err := projectScope(ctx, ie.inst.ID, *project)
		if err != nil {
			return output(stdout, stderr, *asJSON, env, err, exitRefused, nil)
		}
		req.ProjectKey = s.MailProjectKey
	}
	var res lifecycle.RecycleResult
	code, err := underOwner(ctx, ie, func() error {
		var rerr error
		res, rerr = lifecycle.Recycle(ctx, lenv, req)
		return rerr
	})
	if err != nil && res.Stage == lifecycle.StageCheck && code == exitError {
		code = exitRefused
	}
	env.OperationID, env.Result, env.Warnings = res.OperationID, res, res.Warnings
	return output(stdout, stderr, *asJSON, env, err, code, func(w io.Writer) {
		if res.Handoff != nil {
			fmt.Fprintf(w, "handoff: %s", strings.Join(res.Handoff.Sources, ", "))
			if len(res.Handoff.Degraded) > 0 {
				fmt.Fprintf(w, " (degraded: %s)", strings.Join(res.Handoff.Degraded, "; "))
			}
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "recycled %s in %s: generation %d, kickoff %s\n", res.MailName, res.PaneID, res.Generation, dash(res.KickoffStatus))
	})
}

const reapUsage = `Usage: nhm reap [session] [--idle MIN] [--all-workspaces] [--dry-run]
                [--project DIR] [--socket PATH] [--json]

Retire the agents between turns for at least --idle minutes (default 60)
whose work is done, with every retire check. It keeps the focused and the
calling pane, the keep list ([reap] keep in ~/.config/nhm/config.toml and
ACFS's agents.toml), agents with unread mail or pending acks, agents with
a bead in progress, and any binding in an uncertain state. Idle time is
how long nhm has seen the agent idle at the same herdr state; run reap
again (or let nhm maintain do it) to let the time pass.
`

func runReap(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reap", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, reapUsage) }
	idle := fs.Int("idle", 60, "minutes between turns")
	all := fs.Bool("all-workspaces", false, "every workspace of the instance")
	dryRun := fs.Bool("dry-run", false, "say who would be retired")
	project := fs.String("project", "", "only this project")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) > 1 || *idle < 0 {
		if err == nil {
			fmt.Fprint(stderr, reapUsage)
		}
		return exitRefused
	}
	env := envelope{Command: "reap"}
	ctx := context.Background()
	ie, err := openInstance(*socket)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	lenv, err := ie.lifecycleEnv()
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	keep, err := lifecycle.LoadKeep(keepPaths()...)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, fmt.Errorf("the keep list: %w; nobody is reaped", err), exitError, nil)
	}
	req := lifecycle.ReapRequest{Idle: time.Duration(*idle) * time.Minute, AllWorkspaces: *all, Keep: keep, DryRun: *dryRun}
	if len(pos) == 1 {
		if req.WorkspaceID, err = sessionWorkspace(ctx, ie.herdr, pos[0]); err != nil {
			return output(stdout, stderr, *asJSON, env, err, exitRefused, nil)
		}
	} else if !*all {
		req.WorkspaceID = os.Getenv("HERDR_WORKSPACE_ID")
	}
	if *project != "" {
		s, err := projectScope(ctx, ie.inst.ID, *project)
		if err != nil {
			return output(stdout, stderr, *asJSON, env, err, exitRefused, nil)
		}
		req.ProjectKey = s.MailProjectKey
	}
	items, code, err := reapSweep(ctx, ie, lenv, req)
	env.Result = map[string]any{"agents": items}
	return output(stdout, stderr, *asJSON, env, err, code, func(w io.Writer) {
		for _, it := range items {
			line := fmt.Sprintf("%s: %s", it.MailName, strings.ReplaceAll(it.Action, "_", " "))
			if it.Reason != "" {
				line += ": " + it.Reason
			}
			fmt.Fprintln(w, line)
		}
	})
}

// reapSweep runs reap for nhm reap and the menu's reap job: a dry run as
// it is, a sweep under the owner lock. Agents with unread mail or pending
// acks are kept.
func reapSweep(ctx context.Context, ie *instanceEnv, lenv lifecycle.Env, req lifecycle.ReapRequest) ([]lifecycle.ReapItem, int, error) {
	mail, _ := agentmail.NewClient("")
	state := func(ctx context.Context, project, agent string) (int, int, error) {
		msgs, err := mail.FetchInbox(ctx, project, agent, 50)
		if err != nil {
			return 0, 0, err
		}
		u := agentmail.Summarize(msgs)
		return u.Unread, u.PendingAcks, nil
	}
	var items []lifecycle.ReapItem
	run := func() error {
		var rerr error
		items, rerr = lifecycle.Reap(ctx, lenv, state, req)
		return rerr
	}
	code := exitOK
	var err error
	if req.DryRun {
		if err = run(); err != nil {
			code = exitError
		}
	} else {
		code, err = underOwner(ctx, ie, run)
	}
	// An empty reviewed set is a refusal.
	if errors.Is(err, lifecycle.ErrRefused) {
		code = exitRefused
	}
	return items, code, err
}

// keepPaths are nhm's config and ACFS's agents.toml.
func keepPaths() []string {
	var paths []string
	if dir, err := os.UserConfigDir(); err == nil {
		paths = append(paths, dir+"/nhm/config.toml")
	}
	if p := os.Getenv("ACFS_AGENTS_CONFIG"); p != "" {
		paths = append(paths, p)
	} else if dir, err := os.UserConfigDir(); err == nil {
		paths = append(paths, dir+"/acfs/agents.toml")
	}
	return paths
}

const killUsage = `Usage: nhm kill <session> [--yes | -f | --force] [--no-beads] [--op-id ID] [--socket PATH] [--json]

Close a workspace. It runs the retire checks for every bound agent first
and never bypasses them: one refusal refuses the kill and nothing closes.
Then it retires each agent (its transcript preserved, its identity
retired) and closes the workspace. Without --yes it only reports. -f and
--force are ntm's spelling of --yes: they skip no check.
--no-beads is retire's, for every agent: the workspace's agents worked no
beads (agents spawned with --role reviewer never need it).
--op-id names the kill's operation record. An id used before is refused:
the kill does not run again (nhm menu's recent operations shows it).
`

func runKill(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("kill", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, killUsage) }
	yes := fs.Bool("yes", false, "close it")
	// ntm's kill -f and --force skip its confirmation, as --yes does here;
	// ru calls both spellings.
	fs.BoolVar(yes, "force", false, "ntm's spelling of --yes")
	fs.BoolVar(yes, "f", false, "ntm's spelling of --yes")
	noBeads := fs.Bool("no-beads", false, "the agents worked no beads; do not require any")
	opID := fs.String("op-id", "", "the id of the kill's operation record")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 1 {
		if err == nil {
			fmt.Fprint(stderr, killUsage)
		}
		return exitRefused
	}
	env := envelope{Command: "kill"}
	ctx := context.Background()
	ie, err := openInstance(*socket)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	ws, err := sessionWorkspace(ctx, ie.herdr, pos[0])
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitRefused, nil)
	}
	lenv, err := ie.lifecycleEnv()
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	res, code, err := killUnderOwner(ctx, ie, lenv, lifecycle.KillRequest{WorkspaceID: ws, Yes: *yes, NoBeads: *noBeads, OperationID: *opID})
	if res.OperationID != "" {
		env.OperationID = res.OperationID
	}
	env.Result = res
	return output(stdout, stderr, *asJSON, env, err, code, func(w io.Writer) {
		for _, a := range res.Agents {
			fmt.Fprintf(w, "retired %s\n", a.MailName)
		}
		fmt.Fprintf(w, "closed workspace %s\n", ws)
	})
}

// killUnderOwner runs kill under the owner lock, for nhm kill and the
// menu's kill job. Only a rule's refusal exits 2; herdr or Agent Mail
// failing is 1.
func killUnderOwner(ctx context.Context, ie *instanceEnv, lenv lifecycle.Env, req lifecycle.KillRequest) (lifecycle.KillResult, int, error) {
	var res lifecycle.KillResult
	code, err := underOwner(ctx, ie, func() error {
		var kerr error
		res, kerr = lifecycle.KillWith(ctx, lenv, req)
		return kerr
	})
	if errors.Is(err, lifecycle.ErrRefused) {
		code = exitRefused
	}
	return res, code, err
}
