package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"golang.org/x/term"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/enrich"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/core/spawnassign"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/integrations/guards"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/integrations/quota"
	"github.com/arosl/nhm/internal/robot"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/ui"
)

// spawnLockWait bounds a spawn's wait for the owner lock. A spawn holds
// the shared lock through its starts, which are bounded by the ready
// timeout per agent.
const spawnLockWait = 10 * time.Second

const spawnUsage = `Usage: nhm spawn <session> [--cc=N[:MODEL]] [--cod=N[:MODEL]] [--agy=N] [--pi=N] [--omp=N] [--oc=N] [--grok=N]
                 [--agent KIND=N[:MODEL]]… [--label L] [--group G] [--per-tab N]
                 [--prompt T | --prompt-file F | --no-kickoff] [--assign [--yes]]
                 [--stagger-mode fixed|smart] [--stagger-delay D] [--ready-timeout D]
                 [--trust-folder] [--force] [--allow-unguarded] [--require-observations]
                 [--project DIR] [--op-id ID] [--dry-run] [--socket PATH] [--json]
       nhm add <session> …    the same, into an existing workspace
       nhm new <session>      only the workspace

Start agents in a herdr workspace (the session, by label). For each agent:
an Agent Mail identity first, a pane, the binding, agent.start, the labels,
then the kickoff: --prompt, or ACFS's default_new_agent palette prompt.
--assign appends next_bead after your explicit yes; nhm never picks a bead.

Agents of one project share a tab, a pane each, up to --per-tab (6): a new
agent splits the biggest pane of a tab of its project that has room, or
opens a tab. A tab with one agent has the agent's name; with more, the
project's directory name. --label or --group always opens new tabs, and a
tab from before this rule is never joined. --per-tab 1 gives each agent
its own tab.

Admission runs first: a quota at 90 % or a known limit refuses (--force
overrides the quota only); unknown quota is a warning (refused with
--require-observations); a red capacity refuses; a guard that is not
configured refuses unless --allow-unguarded, which records an exception for
this launch; pi warns that no guard protects it. --dry-run prints the plan
and the admission and creates nothing.

If an agent fails, the agents before it stay, the rest are not started,
and only what this spawn created and nothing uses is removed. Runs only
when nhm owns this herdr instance.

Exit codes: 0 every agent ready; 1 an error or a partial spawn; 2 refused
before anything started; 3 admission refused.
`

// spawnFlags are the flags of spawn and add.
type spawnFlags struct {
	counts                   map[string]*string
	agents                   listFlag
	label, group             string
	perTab                   int
	prompt, promptFile       string
	noKickoff, assign, yes   bool
	staggerMode              string
	staggerDelay, readyWait  time.Duration
	trustFolder, force       bool
	allowUnguarded, require  bool
	withMemory, withCASS     bool
	noCASSContext            bool
	project, opID, socket    string
	effort                   string
	role                     string
	dryRun, asJSON, interact bool
}

func addSpawnFlags(fs *flag.FlagSet) *spawnFlags {
	f := &spawnFlags{counts: map[string]*string{}}
	fs.Var(&f.agents, "agent", "KIND=N[:MODEL] (repeatable)")
	fs.StringVar(&f.effort, "effort", "", "reasoning effort (default: each kind's, ntm's xhigh for claude and codex)")
	fs.StringVar(&f.role, "role", "", "reviewer: the agents work no beads (reap may retire them)")
	fs.StringVar(&f.label, "label", "", "the tabs' label")
	fs.StringVar(&f.group, "group", "", "a group token on the panes")
	fs.IntVar(&f.perTab, "per-tab", spawn.DefaultPerTab, "the most agents of a project in one tab (1: a tab each)")
	fs.StringVar(&f.prompt, "prompt", "", "the kickoff prompt")
	fs.StringVar(&f.promptFile, "prompt-file", "", "read the kickoff prompt from a file (- is stdin)")
	fs.BoolVar(&f.noKickoff, "no-kickoff", false, "send no kickoff")
	fs.BoolVar(&f.assign, "assign", false, "append the next_bead prompt (asks for your yes)")
	fs.BoolVar(&f.yes, "yes", false, "with --assign: the yes, for a non-interactive call")
	fs.StringVar(&f.staggerMode, "stagger-mode", "", "fixed or smart")
	fs.DurationVar(&f.staggerDelay, "stagger-delay", 0, "the delay between starts")
	fs.DurationVar(&f.readyWait, "ready-timeout", 60*time.Second, "the wait for each agent to be ready")
	fs.BoolVar(&f.trustFolder, "trust-folder", false, "answer the kind's folder-trust dialog")
	fs.BoolVar(&f.force, "force", false, "start over a quota at 90 % (not over a limit)")
	fs.BoolVar(&f.allowUnguarded, "allow-unguarded", false, "start with a guard missing, recorded as an exception")
	fs.BoolVar(&f.require, "require-observations", false, "the robot admission profile: unknown quota refuses")
	fs.BoolVar(&f.withMemory, "with-memory", true, "CM rules in the kickoff (best-effort; the default)")
	fs.BoolVar(&f.withCASS, "with-cass", false, "CASS context in the kickoff (best-effort)")
	fs.BoolVar(&f.noCASSContext, "no-cass-context", false, "no CASS context in the kickoff")
	fs.BoolVar(&f.interact, "interactive", false, "the gum wizard: questions, the plan, then your yes")
	fs.StringVar(&f.project, "project", "", "the project (default: the git work tree of the cwd)")
	fs.StringVar(&f.opID, "op-id", "", "operation id, for an idempotent retry")
	fs.StringVar(&f.socket, "socket", "", "herdr API socket")
	fs.BoolVar(&f.dryRun, "dry-run", false, "print the plan and the admission; create nothing")
	fs.BoolVar(&f.asJSON, "json", false, "nhm's JSON envelope")
	// The kinds' count flags last: a flag spawn has stays spawn's, so a
	// kind named after one never redefines it (kinds.Reserved refuses it
	// too).
	for _, a := range kinds.All() {
		for _, name := range append([]string{a.Name}, a.Flags...) {
			if fs.Lookup(name) == nil {
				f.counts[name] = fs.String(name, "", a.Name+" agents: N[:MODEL]")
			}
		}
	}
	return f
}

func (f *spawnFlags) request(session string, add bool) (spawn.Request, error) {
	req := spawn.Request{Session: session, Add: add, Label: f.label, Group: f.group, PerTab: f.perTab, Effort: f.effort, Role: f.role,
		NoKickoff: f.noKickoff, Assign: f.assign, StaggerMode: f.staggerMode, StaggerDelay: f.staggerDelay,
		ReadyTimeout: f.readyWait, TrustFolder: f.trustFolder, Force: f.force, AllowUnguarded: f.allowUnguarded,
		RequireObservations: f.require, DryRun: f.dryRun, OperationID: f.opID}
	switch {
	case f.prompt != "" && f.promptFile != "":
		return req, errors.New("give --prompt or --prompt-file, not both")
	case f.noKickoff && (f.prompt != "" || f.promptFile != ""):
		return req, errors.New("--no-kickoff and a prompt contradict")
	}
	enrichment, err := spawnEnrichment(f)
	if err != nil {
		return req, err
	}
	req.Enrich = enrichment
	req.Prompt = f.prompt
	if f.promptFile != "" {
		text, err := readInput(f.promptFile)
		if err != nil {
			return req, fmt.Errorf("--prompt-file: %w", err)
		}
		req.Prompt = text
	}
	for flagName, v := range f.counts {
		if *v == "" {
			continue
		}
		a, _ := kinds.Default().ByFlag(flagName)
		w, err := spawn.ParseCount(a.Name, *v)
		if err != nil {
			return req, err
		}
		req.Agents = append(req.Agents, w)
	}
	for _, s := range f.agents {
		w, err := spawn.ParseAgent(s)
		if err != nil {
			return req, err
		}
		req.Agents = append(req.Agents, w)
	}
	sortWants(req.Agents)
	return req, nil
}

// spawnEnrichment is the kickoff's enrichment (plan-v8 2.13): CM
// best-effort by default for a kickoff (--with-memory=false turns it off),
// CASS from the user's config unless --with-cass or --no-cass-context.
func spawnEnrichment(f *spawnFlags) (send.Enrichment, error) {
	cfg, err := loadEnrichConfig()
	if err != nil {
		return send.Enrichment{}, err
	}
	e := send.Enrichment{DefaultCM: enrich.BestEffort, DefaultCASS: cfg.CASS.Context, CM: enrich.Off}
	if f.withMemory {
		e.CM = enrich.BestEffort
	}
	switch {
	case f.withCASS && f.noCASSContext:
		return e, errors.New("--with-cass and --no-cass-context contradict")
	case f.withCASS:
		e.CASS = enrich.BestEffort
	case f.noCASSContext:
		e.CASS = enrich.Off
	}
	return e, nil
}

// sortWants orders the agents by the registry's kind order, so that a
// plan is the same whatever order the flags came in.
func sortWants(ws []spawn.Want) {
	order := map[string]int{}
	for i, n := range kinds.Names() {
		order[n] = i
	}
	for i := 1; i < len(ws); i++ {
		for j := i; j > 0 && order[ws[j].Kind] < order[ws[j-1].Kind]; j-- {
			ws[j], ws[j-1] = ws[j-1], ws[j]
		}
	}
}

func runSpawn(args []string, stdout, stderr io.Writer) int {
	return spawnVerb("spawn", args, stdout, stderr)
}
func runAdd(args []string, stdout, stderr io.Writer) int {
	return spawnVerb("add", args, stdout, stderr)
}

func spawnVerb(verb string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, spawnUsage) }
	f := addSpawnFlags(fs)
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	env := envelope{Command: verb}
	refuse := func(err error) int { return output(stdout, stderr, f.asJSON, env, err, exitRefused, nil) }
	if len(pos) != 1 {
		fmt.Fprint(stderr, spawnUsage)
		return exitRefused
	}
	if f.interact {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return refuse(errors.New("--interactive needs a terminal"))
		}
		return runSpawnWizard(context.Background(), ui.Gum{}, pos[0], verb == "add", f.project, f.socket, stdout, stderr)
	}
	req, err := f.request(pos[0], verb == "add")
	if err != nil {
		return refuse(err)
	}
	if req.Assign && !req.DryRun && !f.yes {
		if !confirm(stderr, "--assign sends next_bead: each agent then picks and claims a bead itself. Continue?") {
			return refuse(errors.New("--assign needs your yes (or --yes)"))
		}
	}
	return runSpawnRequest(context.Background(), req, f.project, f.socket, f.asJSON, env, stdout, stderr)
}

// runSpawnWizard is the gum wizard (plan-v8 2.4): the questions, then the
// plan and the admission (a dry run), then the final yes before the paid
// start, then the spawn.
func runSpawnWizard(ctx context.Context, p ui.Prompter, session string, add bool, project, socket string, stdout, stderr io.Writer) int {
	verb := "spawn"
	if add {
		verb = "add"
	}
	fail := func(err error, code int) int {
		fmt.Fprintf(stderr, "nhm %s: %v\n", verb, err)
		return code
	}
	ans, err := ui.SpawnWizard(p, fmt.Sprintf("nhm %s in %s", verb, session), kinds.All())
	if err != nil {
		return fail(err, exitRefused)
	}
	f := &spawnFlags{withMemory: ans.Memory, withCASS: ans.CASS, noCASSContext: !ans.CASS}
	enrichment, err := spawnEnrichment(f)
	if err != nil {
		return fail(err, exitRefused)
	}
	req := spawn.Request{Session: session, Add: add, Label: ans.Label, Group: ans.Group, Prompt: ans.Prompt,
		NoKickoff: ans.NoKickoff, Enrich: enrichment, DryRun: true}
	for _, a := range ans.Agents {
		req.Agents = append(req.Agents, spawn.Want{Kind: a.Kind, Count: a.Count, Model: a.Model})
	}
	ie, err := openInstance(socket)
	if err != nil {
		return fail(err, exitError)
	}
	defer ie.db.Close()
	plan, code, err := spawnUnderOwner(ctx, ie, &req, project)
	printSpawn(stdout, plan)
	if err != nil {
		return fail(err, code)
	}
	fmt.Fprintf(stdout, "the same by hand: %s\n", strings.Join(ui.SpawnArgs(session, add, ans), " "))
	yes, err := p.Confirm(fmt.Sprintf("Start %d agents? This starts paid model work.", req.Count()))
	if err != nil || !yes {
		fmt.Fprintln(stderr, "nothing started")
		return exitRefused
	}
	req.DryRun = false
	res, code, err := spawnUnderOwner(ctx, ie, &req, project)
	printSpawn(stdout, res)
	if err != nil {
		return fail(err, code)
	}
	return spawnExit(res)
}

// runSpawnPopup is the spawn popup: the wizard adds agents to the
// workspace the action was invoked in, with that pane's cwd as the
// project.
func runSpawnPopup(stderr io.Writer) int {
	c, err := popupContext()
	if err != nil {
		fmt.Fprintf(stderr, "nhm spawn: %v\n", err)
		return exitError
	}
	if c.WorkspaceID == "" {
		fmt.Fprintln(stderr, "nhm spawn: the popup has no workspace; run it from a workspace")
		return exitRefused
	}
	fmt.Fprintln(os.Stdout, c.Header())
	project := ""
	if c.Cwd != "" {
		// The git work tree that holds the pane's cwd.
		if s, err := core.ResolveScope(context.Background(), core.ScopeRequest{Cwd: c.Cwd}, nil); err == nil {
			project = s.ProjectRoot
		}
	}
	return runSpawnWizard(context.Background(), ui.Gum{}, c.WorkspaceID, true, project, c.SocketPath, os.Stdout, stderr)
}

// confirm asks a yes/no question on the terminal; without a terminal the
// answer is no.
func confirm(stderr io.Writer, question string) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false
	}
	fmt.Fprintf(stderr, "%s [y/N] ", question)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

const newUsage = `Usage: nhm new <session> [--project DIR] [--socket PATH] [--json]

Create a herdr workspace with the session's label and the project as its
cwd, with no agent. A label that exists already is refused.
`

func runNew(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, newUsage) }
	project := fs.String("project", "", "the project (default: the git work tree of the cwd)")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 1 {
		if err == nil {
			fmt.Fprint(stderr, newUsage)
		}
		return exitRefused
	}
	env := envelope{Command: "new"}
	ctx := context.Background()
	ie, err := openInstance(*socket)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	res, s, code, err := newWorkspace(ctx, ie, pos[0], *project)
	if s != nil {
		env.Scope = *s
	}
	if code == exitOK || res.WorkspaceID != "" {
		env.Result = res
	}
	return output(stdout, stderr, *asJSON, env, err, code, func(w io.Writer) {
		fmt.Fprintf(w, "created workspace %s (%s) in %s\n", res.Session, res.WorkspaceID, s.ProjectRoot)
	})
}

// newResult is what nhm new created.
type newResult struct {
	Session     string `json:"session"`
	WorkspaceID string `json:"workspace_id"`
}

// newWorkspace creates a workspace with the label and the project (""
// is the cwd's) as its cwd, with no agent: nhm new and the menu's new
// job. A label in use is refused. The scope is nil when the project
// cannot be resolved.
func newWorkspace(ctx context.Context, ie *instanceEnv, label, project string) (newResult, *core.Scope, int, error) {
	res := newResult{Session: label}
	s, err := projectScope(ctx, ie.inst.ID, project)
	if err != nil {
		return res, nil, exitRefused, err
	}
	snap, err := ie.herdr.Snapshot(ctx)
	if err != nil {
		return res, &s, exitError, err
	}
	for _, w := range snap.Workspaces {
		if w.Label == label || w.WorkspaceID == label {
			return res, &s, exitRefused, fmt.Errorf("workspace %s exists (%s)", label, w.WorkspaceID)
		}
	}
	lctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	code := exitOK
	err = core.WithOwnerShared(lctx, ie.inst.Dir, func(o core.Owner) error {
		if err := core.RequireOwner(o, core.OwnerNhm); err != nil {
			code = exitRefused
			return err
		}
		focus := false
		c, err := ie.herdr.WorkspaceCreate(ctx, herdrWorkspace(label, s.ProjectRoot, &focus))
		if err != nil {
			return err
		}
		if c.Workspace != nil {
			res.WorkspaceID = c.Workspace.WorkspaceID
		}
		return nil
	})
	if err != nil && code == exitOK {
		code = exitError
	}
	return res, &s, code, err
}

func herdrWorkspace(label, cwd string, focus *bool) herdr.WorkspaceCreateParams {
	return herdr.WorkspaceCreateParams{Label: label, Cwd: cwd, Focus: focus}
}

// spawnConfig is the spawn part of ~/.config/nhm/config.toml:
//
//	[providers]
//	capacity = ["acfs", "capacity", "--guard", "--json"]   # default: none
//	[quota]
//	admit_unknown = ["agy", "pi"]   # pools a robot spawn admits without an observation
//
// Other keys belong to other parts of nhm.
type spawnConfig struct {
	Providers struct {
		Capacity []string `toml:"capacity"`
	} `toml:"providers"`
	Quota struct {
		AdmitUnknown []string `toml:"admit_unknown"`
	} `toml:"quota"`
}

func loadSpawnConfig() (spawnConfig, error) {
	var c spawnConfig
	path, err := kinds.ConfigPath()
	if err != nil {
		return c, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if _, err := toml.Decode(string(data), &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// spawnEnv is a spawn's environment on an open instance, for a project.
func (ie *instanceEnv) spawnEnv(projectRoot string) (spawn.Env, error) {
	mail, err := agentmail.NewClient("")
	if err != nil {
		return spawn.Env{}, err
	}
	configHome, _ := agentmail.ConfigHome()
	senv := ie.sendEnv()
	ad := spawn.AdmissionEnv{
		Quota:  func() []quota.Observation { return quota.Collect(quota.DefaultEnv(), time.Now().UTC(), nil) },
		Guards: func(kind string) []guards.Result { return guards.Check(kind, guards.Env{ProjectRoot: projectRoot}) },
		Screen: guards.ScreenArgv,
	}
	if cfg, err := loadSpawnConfig(); err == nil {
		if len(cfg.Providers.Capacity) > 0 {
			ad.Capacity = spawn.CommandCapacity(cfg.Providers.Capacity)
		}
		ad.AdmitUnknown = cfg.Quota.AdmitUnknown
		// ntm's launch: Claude gets the dcg and rch hooks on --settings.
		ad.Settings = spawn.HookSettings(exec.LookPath)
	} else {
		return spawn.Env{}, err
	}
	return spawn.Env{DB: ie.db, Bindings: bindings.NewStore(ie.db), Herdr: ie.herdr, Mail: mail, InstanceDir: ie.inst.Dir,
		Admission: ad, Send: func(ctx context.Context, r send.Request) (send.Result, error) { return send.Send(ctx, senv, r) },
		IdentityFile: func(projectKey, paneID, name string) error {
			if configHome == "" {
				return errors.New("no Agent Mail config home")
			}
			_, err := agentmail.WriteIdentity(configHome, projectKey, paneID, name, time.Now())
			return err
		},
		ProcStart: bindings.ProcStart}, nil
}

// runSpawnRequest resolves the project, opens the instance and runs the
// spawn under the owner lock (a dry run reads only and runs under any
// owner).
func runSpawnRequest(ctx context.Context, req spawn.Request, project, socket string, asJSON bool, env envelope, stdout, stderr io.Writer) int {
	ie, err := openInstance(socket)
	if err != nil {
		return output(stdout, stderr, asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	res, code, err := spawnUnderOwner(ctx, ie, &req, project)
	env.Scope, env.OperationID, env.Result = req.Scope, res.OperationID, res
	env.Warnings = append(res.Admission.Warnings, res.Warnings...)
	if err != nil {
		return output(stdout, stderr, asJSON, env, err, code, func(w io.Writer) { printSpawn(w, res) })
	}
	return output(stdout, stderr, asJSON, env, nil, spawnExit(res), func(w io.Writer) { printSpawn(w, res) })
}

// spawnUnderOwner fills the project scope and runs the spawn.
func spawnUnderOwner(ctx context.Context, ie *instanceEnv, req *spawn.Request, project string) (spawn.Result, int, error) {
	s, err := projectScope(ctx, ie.inst.ID, project)
	if err != nil {
		return spawn.Result{}, exitRefused, err
	}
	req.ProjectRoot, req.ProjectID, req.Scope = s.ProjectRoot, s.ProjectID, s
	senv, err := ie.spawnEnv(req.ProjectRoot)
	if err != nil {
		return spawn.Result{}, exitError, err
	}
	run := func() (spawn.Result, int, error) {
		res, err := spawn.Spawn(ctx, senv, *req)
		switch {
		case errors.Is(err, spawn.ErrRefused):
			return res, exitAdmission, err
		case err != nil && res.OperationID == "":
			return res, exitRefused, err
		case err != nil:
			return res, exitError, err
		}
		return res, exitOK, nil
	}
	if req.DryRun {
		return run()
	}
	lctx, cancel := context.WithTimeout(ctx, spawnLockWait)
	defer cancel()
	var res spawn.Result
	code := exitOK
	err = core.WithOwnerShared(lctx, ie.inst.Dir, func(o core.Owner) error {
		if err := core.RequireOwner(o, core.OwnerNhm); err != nil {
			code = exitRefused
			return err
		}
		var rerr error
		res, code, rerr = run()
		return rerr
	})
	if errors.Is(err, store.ErrOperationIDConflict) {
		code = exitRefused
	}
	return res, code, err
}

func spawnExit(res spawn.Result) int {
	if res.DryRun || res.Ready() {
		return exitOK
	}
	return exitError
}

func printSpawn(w io.Writer, res spawn.Result) {
	if res.Replayed {
		fmt.Fprintf(w, "operation %s was claimed before; nothing started. Its outcome:\n", res.OperationID)
	}
	if res.DryRun {
		ws := res.WorkspaceID
		if res.WorkspaceNew {
			ws = "a new workspace"
		}
		fmt.Fprintf(w, "would start %d agents in %s (%s), from %s; admission: %s\n", len(res.Targets), res.Session, ws,
			res.ProjectRoot, res.Admission.Decision)
	}
	for _, r := range res.Admission.Reasons {
		fmt.Fprintf(w, "  refused: %s\n", r)
	}
	for _, e := range res.Admission.Exceptions {
		fmt.Fprintf(w, "  exception: %s\n", e)
	}
	for _, t := range res.Targets {
		model := t.Model
		if model == "" {
			model = "default model"
		}
		line := fmt.Sprintf("%s (%s, %s) slot %s", t.MailName, t.Kind, model, t.Slot)
		if t.MailName == "" {
			line = fmt.Sprintf("%s agent (%s) slot %s", t.Kind, model, t.Slot)
		}
		if t.PaneID != "" {
			line += " pane " + t.PaneID
		}
		line += " " + strings.ReplaceAll(t.Status, "_", " ")
		if t.Error != "" {
			line += ": " + t.Error
		}
		fmt.Fprintln(w, line)
		if t.Cleanup != "" {
			fmt.Fprintf(w, "  cleanup: %s\n", t.Cleanup)
		}
	}
	if res.OperationID != "" {
		fmt.Fprintf(w, "operation %s\n", res.OperationID)
	}
}

// robotSpawnFlags are --robot-spawn's flags.
type robotSpawnFlags struct {
	session, dir, format, prompt, opID, socket *string
	counts                                     map[string]*int
	readyTimeout                               *time.Duration
	dryRun, assign                             *bool
	assignFlags                                robotAssignFlags
}

// addRobotSpawnFlags registers ntm's robot spawn flags, then a
// --spawn-<kind> count for each kind name and flag that is free.
func addRobotSpawnFlags(fs *flag.FlagSet) robotSpawnFlags {
	f := robotSpawnFlags{counts: map[string]*int{}}
	f.session = fs.String("robot-spawn", "", "")
	f.dir = fs.String("spawn-dir", "", "")
	f.format = fs.String("robot-format", "auto", "")
	fs.Bool("spawn-no-user", false, "")
	fs.Bool("spawn-safety", false, "")
	fs.Bool("spawn-wait", false, "")
	f.readyTimeout = fs.Duration("ready-timeout", 60*time.Second, "")
	fs.Duration("timeout", 60*time.Second, "")
	f.dryRun = fs.Bool("dry-run", false, "")
	f.assign = fs.Bool("spawn-assign-work", false, "")
	f.assignFlags = addRobotAssignFlags(fs)
	f.prompt = fs.String("spawn-prompt", "", "")
	f.opID = fs.String("op-id", "", "")
	f.socket = fs.String("socket", "", "")
	for _, a := range kinds.All() {
		for _, name := range append([]string{a.Name}, a.Flags...) {
			if fs.Lookup("spawn-"+name) == nil {
				f.counts[name] = fs.Int("spawn-"+name, 0, "")
			}
		}
	}
	return f
}

// hasRobotSpawn reports a --robot-spawn flag in args.
func hasRobotSpawn(args []string) bool {
	for _, a := range args {
		if name, _, _ := strings.Cut(a, "="); name == "--robot-spawn" {
			return true
		}
	}
	return false
}

// The robot form: --robot-spawn=S --spawn-dir=R --spawn-cc=N
// --spawn-cod=N [--spawn-agy=N] --spawn-no-user --spawn-safety
// --spawn-wait [--ready-timeout=Ns] --timeout=60s --robot-format=json
// [--dry-run] [--spawn-assign-work [--strategy=S]] [--spawn-prompt=T].
func runRobotSpawn(args []string, stdout, stderr io.Writer) int {
	now := time.Now()
	fs := flag.NewFlagSet("nhm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	rf := addRobotSpawnFlags(fs)
	session, dir, format, counts, readyTimeout := rf.session, rf.dir, rf.format, rf.counts, rf.readyTimeout
	dryRun, assign, prompt, opID, socket := rf.dryRun, rf.assign, rf.prompt, rf.opID, rf.socket
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		msg := "unexpected arguments"
		if err != nil {
			msg = err.Error()
		}
		return robotFail(stdout, robot.ErrCodeInvalidFlag, msg, "nhm --robot-capabilities lists the robot flags", "json", robotError, now)
	}
	switch *format {
	case "auto", "json":
		*format = "json"
	default:
		return robotFail(stdout, robot.ErrCodeInvalidFlag, fmt.Sprintf("--robot-format=%s: nhm writes json only", *format),
			"use --robot-format=json", "json", robotError, now)
	}
	// A robot spawn sends no kickoff unless --spawn-prompt. With
	// --spawn-assign-work (the caller's consent) the assignment phase
	// sends each agent its work, with --spawn-prompt ahead of it, so the
	// spawn itself sends nothing.
	req := spawn.Request{Session: *session, Prompt: *prompt, NoKickoff: *prompt == "",
		ReadyTimeout: *readyTimeout, RequireObservations: true, DryRun: *dryRun, OperationID: *opID}
	if *assign {
		req.Prompt, req.NoKickoff = "", true
	}
	for flagName, n := range counts {
		if *n == 0 {
			continue
		}
		a, _ := kinds.Default().ByFlag(flagName)
		req.Agents = append(req.Agents, spawn.Want{Kind: a.Name, Count: *n})
	}
	sortWants(req.Agents)
	ie, err := openInstance(*socket)
	if err != nil {
		return robotFail(stdout, robot.ErrCodeDependencyMissing, err.Error(), "run inside herdr or give --socket", *format, robotUnavailable, now)
	}
	defer ie.db.Close()
	ctx := context.Background()
	// ntm's assignment preflight runs before anything is created, in a dry
	// run too.
	var plan spawnassign.Plan
	var scope core.Scope
	if *assign {
		var refusal *robot.RobotResponse
		if plan, scope, refusal = robotAssignPreflight(ctx, ie, rf.assignFlags, *dir); refusal != nil {
			return robotWrite(stdout, robotAssignRefusal(refusal, req.Session, *dir, *format, now), robotError)
		}
	}
	res, _, err := spawnUnderOwner(ctx, ie, &req, *dir)
	if res.Session == "" {
		res.Session, res.ProjectRoot = req.Session, req.ProjectRoot
	}
	out := spawn.SpawnOutput(res, err, *format, now)
	if errors.Is(err, core.ErrNotOwner) {
		out.ErrorCode = robot.ErrCodeNotOwner
	}
	if *assign && !*dryRun && out.Success {
		robotAssignPhase(ctx, ie, res, scope, plan, *prompt, &out)
	}
	if !out.Success {
		return robotWrite(stdout, out, robotError)
	}
	return robotWrite(stdout, out, robotOK)
}
