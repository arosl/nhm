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
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/enrich"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/sessionlabel"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/core/spawnassign"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/integrations/guards"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/integrations/quota"
	"github.com/arosl/nhm/internal/robot"
	"github.com/arosl/nhm/internal/store"
)

// spawnLockWait bounds a spawn's wait for the owner lock. A spawn holds
// the shared lock through its starts, which are bounded by the ready
// timeout per agent.
const spawnLockWait = 10 * time.Second

const spawnUsage = `Usage: nhm spawn <session> [--cc|--cod|--grok|--omp|--pi=N[:MODEL[:EFFORT]]]… [--oc=N[:MODEL]]… [--agy=N]…
                 [--agent KIND=N[:MODEL[:EFFORT]]]… [-l|--label L] [--tab-label L] [--group G] [--per-tab N]
                 [--prompt T | --prompt-file F | --no-kickoff] [--no-user]
                 [--assign [--strategy S] [--limit N] [--init-prompt T] [--assign-agent TYPE]
                  [--assign-cc-only|--assign-cod-only|--assign-gmi-only|--assign-agy-only]
                  [--assign-verbose|--assign-quiet] [--assign-timeout D] [--yes]]
                 [--stagger-mode fixed|smart] [--stagger-delay D] [--ready-timeout D]
                 [--trust-folder] [--force] [--ignore-capacity] [--allow-unguarded]
                 [--require-observations] [--project DIR] [--op-id ID] [--dry-run]
                 [--socket PATH] [--json]
       nhm add <session> …    the same, into an existing workspace
       nhm new <session>      only the workspace

Each agent flag takes ntm's spec: N, N:MODEL, N:MODEL:EFFORT, or
N:MODEL@EFFORT for a kind that takes an effort; N is at least 1. Repeated
flags add up, and the agents start in the order of their flags. agy's
model is pinned, so it takes a count only.

Start agents in a herdr workspace (the session, by label). -l L makes the
session <session>--L, as ntm's goal label; a session name may not hold
"--". Every agent starts first: an Agent Mail identity, a pane, the
binding, agent.start, the labels. As in ntm, an agent of spawn gets its
kind's [prompts] default (cc_default, cod_default, agy_default,
omp_default, or the *_default_file), then --prompt; add sends --prompt
only. An agent with neither gets no prompt. When an agent gets one, the
spawn waits for the agents to be ready and sends the prompts, paced by
--stagger-mode; when none does, it does not wait, except with --assign
or --trust-folder.

--assign claims a bead for each agent and sends it, after your yes, as
ntm's spawn --assign does, with ntm's choice of beads. The work is
verified before anything starts, in a dry run too. --strategy is ntm's:
balanced (the default), speed, quality, dependency or round-robin; any
other word takes ntm's load-balanced default. --limit caps the beads
assigned; --assign-agent (or an --assign-*-only flag) assigns to one type
of agent only; --role reviewer agents get no bead. Each agent gets one bead: a bead the strategy plans for an
agent that already has one fails, as in ntm. An agent with a bead gets its
identity line, the kickoff, --init-prompt and the bead's work prompt in one
delivery, since a working agent takes no second prompt; every other agent
gets its identity line, the kickoff and --init-prompt after the
assignment. If an agent does not start, the others get the kickoff alone
and nothing is assigned. A planned bead that is not claimed and sent makes
the exit 1; a delivery whose outcome is unknown is never sent again. A
rerun of the same --op-id starts nothing, and refuses while the first run
still runs. If the first run reached none of its agents (it ended between
the spawn and the assignment), the rerun assigns as a first run does.
Otherwise each agent that no prompt or bead reached, and that is still
ready for a prompt, gets its kickoff alone; no bead is assigned again.

Agents of one project share a tab, a pane each, up to --per-tab (6): a new
agent joins a tab of its project that has room, or opens a tab. A tab with
one agent has the agent's name; with more, the project's directory name.
--tab-label or --group always opens new tabs, and a tab from before this
rule is never joined. --per-tab 1 gives each agent its own tab. A
--tab-label that is the project's directory name reads as the shared
label: a later spawn may join that tab, and nhm maintain gives it its last
agent's name.

The panes of a tab fill an even grid in reading order, also across adds:
--per-tab 6 is 2 rows of 3, slots N.1 to N.3 on top and N.4 to N.6 below.
The top row fills from left to right, and its last pane keeps the room of
the columns still empty: a tab of 2 is a third and two thirds of the
width. Then each pane of the next row splits the pane above it: a tab of 4
has the first column cut in two. --per-tab N has the integer square root
of N rows and as many columns as N needs: 2 and 3 are one row, 4 is 2x2,
8 is 2 rows of 4, 9 is 3x3. A new pane takes the first cell of the grid
that no pane holds as its own, so after a retire it fills the gap. When
every cell is held, the biggest pane is cut in half. A tab whose panes
are off the grid (laid out by nhm 0.1.8 to 0.1.10, by hand, for another
--per-tab, or resized by hand by more than 2 columns or rows) is not
joined: the agent opens a tab, so no pane gets narrower than a column.

Admission runs first: a quota at 90 % or a known limit refuses (--force
overrides the quota only); unknown quota is a warning (refused with
--require-observations). A red capacity refuses, and so does an unknown
one with --require-observations; --ignore-capacity starts anyway, with a
warning. The capacity provider is [providers] capacity in
~/.config/nhm/config.toml: none by default, and capacity = [] turns it
off. A guard that is not configured refuses unless --allow-unguarded,
which records an exception for this launch. pi's guard is nhm's own pi
extension, which runs dcg on each bash call; it needs dcg. --dry-run
prints the plan and the admission and creates nothing.

If an agent fails, the agents before it stay, the rest are not started,
and only what this spawn created and nothing uses is removed. Runs only
when nhm owns this herdr instance.

Exit codes, ntm's: 0 every agent ready; 1 an error, a refusal (admission
too) or a partial spawn, with "Error: <msg>" on stderr.
`

// specFlag is a kind's count flag (--cc, --cod, …): each value is an
// agent spec (spawn.ParseCount), and the specs of every count flag and
// --agent add up in command-line order, as ntm's AgentSpecs do.
type specFlag struct {
	kind  string
	wants *[]spawn.Want
}

func (s *specFlag) String() string { return "" }

func (s *specFlag) Set(v string) error {
	w, err := spawn.ParseCount(s.kind, v)
	if err != nil {
		return err
	}
	*s.wants = append(*s.wants, w)
	return nil
}

// Type is the value's name in pflag's usage, ntm's.
func (s *specFlag) Type() string { return "N[:model[:effort]]" }

// agentFlag is --agent KIND=SPEC, into the same list as the count flags.
type agentFlag struct{ wants *[]spawn.Want }

func (a *agentFlag) String() string { return "" }

func (a *agentFlag) Set(v string) error {
	w, err := spawn.ParseAgent(v)
	if err != nil {
		return err
	}
	*a.wants = append(*a.wants, w)
	return nil
}

func (a *agentFlag) Type() string { return "KIND=N[:model[:effort]]" }

// spawnFlags are the flags of spawn and add.
type spawnFlags struct {
	wants                   []spawn.Want
	label, tabLabel, group  string
	perTab                  int
	prompt, promptFile      string
	noKickoff, assign, yes  bool
	strategy, initPrompt    string
	limit                   int
	assignAgent             string
	assignVerbose           bool
	assignQuiet             bool
	assignCCOnly            bool
	assignCodOnly           bool
	assignGmiOnly           bool
	assignAgyOnly           bool
	assignTimeout           time.Duration
	staggerMode             string
	staggerDelay, readyWait time.Duration
	trustFolder, force      bool
	allowUnguarded, require bool
	ignoreCapacity          bool
	withMemory, withCASS    bool
	noCASSContext           bool
	project, opID, socket   string
	effort                  string
	role                    string
	dryRun, asJSON          bool
	safety                  bool
}

func addSpawnFlags(fs *pflag.FlagSet) *spawnFlags {
	f := &spawnFlags{}
	fs.Var(&agentFlag{wants: &f.wants}, "agent", "KIND=N[:MODEL[:EFFORT]] (repeatable)")
	fs.StringVar(&f.effort, "effort", "", "reasoning effort (default: each kind's, ntm's xhigh for claude and codex)")
	fs.StringVar(&f.role, "role", "", "reviewer: the agents work no beads (reap may retire them)")
	// ntm's goal label (bd-1933u): the session <session>--L, in the same
	// project. nhm's tabs take --tab-label.
	fs.StringVarP(&f.label, "label", "l", "", "Goal label for multi-session support (e.g., --label frontend makes the session PROJECT--frontend)")
	fs.StringVar(&f.tabLabel, "tab-label", "", "the tabs' label")
	fs.StringVar(&f.group, "group", "", "a group token on the panes")
	fs.IntVar(&f.perTab, "per-tab", spawn.DefaultPerTab, "the most agents of a project in one tab (1: a tab each)")
	fs.StringVar(&f.prompt, "prompt", "", "the kickoff prompt")
	fs.StringVar(&f.promptFile, "prompt-file", "", "read the kickoff prompt from a file (- is stdin)")
	fs.BoolVar(&f.noKickoff, "no-kickoff", false, "send no kickoff")
	fs.BoolVar(&f.assign, "assign", false, "claim a bead for each agent and send it (asks for your yes)")
	fs.BoolVar(&f.yes, "yes", false, "with --assign: the yes, for a non-interactive call")
	// ntm's --no-user keeps no pane for the user. nhm keeps none
	// anyway, so the flag changes nothing (ru's spawn, nhm-hir).
	fs.Bool("no-user", false, "ntm's flag; nhm keeps no pane for the user")
	// ntm's assignment flags of spawn --assign (ntm 9d5ed977 spawn.go).
	fs.StringVar(&f.strategy, "strategy", "balanced", "with --assign: balanced, speed, quality, dependency or round-robin")
	fs.IntVar(&f.limit, "limit", 0, "with --assign: the most beads to assign (0: no limit)")
	fs.StringVar(&f.initPrompt, "init-prompt", "", "with --assign: a prompt for the ready agents, ahead of each one's work")
	fs.BoolVar(&f.assignVerbose, "assign-verbose", false, "with --assign: the scoring and the beads left out")
	fs.BoolVar(&f.assignQuiet, "assign-quiet", false, "with --assign: no phase lines")
	fs.DurationVar(&f.assignTimeout, "assign-timeout", 30*time.Second, "with --assign: the limit of each external call (bv, br)")
	fs.StringVar(&f.assignAgent, "assign-agent", "", "with --assign: only agents of this type (claude, codex, gemini, antigravity)")
	fs.BoolVar(&f.assignCCOnly, "assign-cc-only", false, "with --assign: only Claude agents")
	fs.BoolVar(&f.assignCodOnly, "assign-cod-only", false, "with --assign: only Codex agents")
	fs.BoolVar(&f.assignGmiOnly, "assign-gmi-only", false, "with --assign: only Gemini agents")
	fs.BoolVar(&f.assignAgyOnly, "assign-agy-only", false, "with --assign: only Antigravity agents")
	fs.StringVar(&f.staggerMode, "stagger-mode", "", "fixed or smart")
	fs.DurationVar(&f.staggerDelay, "stagger-delay", 0, "the delay between starts")
	fs.DurationVar(&f.readyWait, "ready-timeout", 60*time.Second, "the wait for each agent to be ready")
	fs.BoolVar(&f.trustFolder, "trust-folder", false, "answer the kind's folder-trust dialog")
	fs.BoolVar(&f.force, "force", false, "start over a quota at 90 % (not over a limit)")
	fs.BoolVar(&f.ignoreCapacity, "ignore-capacity", false, "start over a red or unknown capacity, with a warning")
	fs.BoolVar(&f.allowUnguarded, "allow-unguarded", false, "start with a guard missing, recorded as an exception")
	fs.BoolVar(&f.require, "require-observations", false, "the robot admission profile: unknown quota or capacity refuses")
	fs.BoolVar(&f.withMemory, "with-memory", true, "CM rules in the kickoff (best-effort; the default)")
	fs.BoolVar(&f.withCASS, "with-cass", false, "CASS context in the kickoff (best-effort)")
	fs.BoolVar(&f.noCASSContext, "no-cass-context", false, "no CASS context in the kickoff")
	fs.StringVar(&f.project, "project", "", "the project (default: the git work tree of the cwd)")
	fs.StringVar(&f.opID, "op-id", "", "operation id, for an idempotent retry")
	fs.StringVar(&f.socket, "socket", "", "herdr API socket")
	fs.BoolVar(&f.dryRun, "dry-run", false, "print the plan and the admission; create nothing")
	// The kinds' count flags last: a flag spawn has stays spawn's, so a
	// kind named after one never redefines it (kinds.Reserved refuses it
	// too).
	for _, a := range kinds.All() {
		for _, name := range append([]string{a.Name}, a.Flags...) {
			if fs.Lookup(name) == nil {
				fs.Var(&specFlag{kind: a.Name, wants: &f.wants}, name, a.Name+" agents: N, N:model, N:model:effort or N:model@effort (repeats add up)")
			}
		}
	}
	return f
}

func (f *spawnFlags) request(session string, add bool) (spawn.Request, error) {
	// ntm's checks: "--" is reserved for labels, and -l makes the session
	// <session>--<label> (config.ValidateProjectName, ValidateLabel).
	if err := sessionlabel.ValidateProjectName(session); err != nil {
		return spawn.Request{}, err
	}
	if f.label != "" {
		if err := sessionlabel.ValidateLabel(f.label); err != nil {
			return spawn.Request{}, fmt.Errorf("invalid label: %w", err)
		}
		session = sessionlabel.FormatSessionName(session, f.label)
	}
	req := spawn.Request{Session: session, Add: add, Label: f.tabLabel, Group: f.group, PerTab: f.perTab, Effort: f.effort, Role: f.role,
		NoKickoff: f.noKickoff, StaggerMode: f.staggerMode, StaggerDelay: f.staggerDelay,
		ReadyTimeout: f.readyWait, TrustFolder: f.trustFolder, Force: f.force, AllowUnguarded: f.allowUnguarded,
		IgnoreCapacity: f.ignoreCapacity, RequireObservations: f.require, DryRun: f.dryRun, OperationID: f.opID,
		Safety: f.safety, SafetyFlag: "--safety"}
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
	// The agents start in the order of their flags, as ntm's do.
	req.Agents = append([]spawn.Want(nil), f.wants...)
	// ntm's spawn sends each kind's [prompts] default ahead of --prompt;
	// its add sends --prompt only. --assign sends --prompt ahead of the
	// work (AssignPrefix).
	if !add && !f.noKickoff && !f.assign {
		cfg, err := loadSpawnConfig()
		if err != nil {
			return req, err
		}
		if req.DefaultPrompts, err = cfg.Prompts.defaultPrompts(req.Agents); err != nil {
			return req, err
		}
	}
	// As ntm's spawn and add: with no prompt to send, they start the
	// agents and do not wait for them. --assign waits (its work goes to
	// ready agents), and so does nhm's --trust-folder, which answers the
	// folder-trust dialog during the wait.
	req.NoWait = !f.assign && !f.trustFolder && (f.noKickoff || (req.Prompt == "" && len(req.DefaultPrompts) == 0))
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

// verbOutput is output for spawn and add with --assign on the shared
// parser (without it, runSpawnNTM's): nhm's envelope with --json (until
// nhm-2p6.4.11 gives them ntm's), else the text; an error is ntm's
// "Error: <msg>" on stderr; and every error or partial outcome exits 1,
// ntm's code.
func verbOutput(stdout, stderr io.Writer, asJSON bool, env envelope, err error, code int, text func(io.Writer)) int {
	if code != exitOK || err != nil {
		code = exitError
	}
	if asJSON || err == nil {
		return output(stdout, stderr, asJSON, env, err, code, text)
	}
	_ = output(stdout, stderr, false, env, nil, code, nil) // its warnings
	fmt.Fprintln(stderr, "Error:", err)
	return code
}

// newSpawnCmd and newAddCmd are ntm's spawn and add on the shared parser:
// ntm's flag and argument errors, and exit 1 for every error.
func newSpawnCmd() *cobra.Command {
	return newSpawnVerbCmd("spawn", "Create session and spawn AI agents in panes")
}

func newAddCmd() *cobra.Command {
	return newSpawnVerbCmd("add", "Add more agents to an existing session")
}

func newSpawnVerbCmd(verb, short string) *cobra.Command {
	var f *spawnFlags
	cmd := &cobra.Command{
		Use:   verb + " <session-name>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f.asJSON = wantJSON(cmd)
			code, err := spawnVerb(verb, args[0], f, cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if code != exitOK {
				return verbExit{code: code}
			}
			return nil
		},
	}
	f = addSpawnFlags(cmd.Flags())
	if verb == "spawn" {
		// ntm's spawn only; its add has no --safety.
		cmd.Flags().BoolVar(&f.safety, "safety", false, "Fail if session already exists (prevents accidental reuse)")
	}
	return cmd
}

// spawnVerb runs spawn or add. An argument error is returned, as ntm's
// RunE returns it: the root writes "Error: <msg>", or under --json ntm's
// error envelope, and exits 1.
func spawnVerb(verb, session string, f *spawnFlags, stdout, stderr io.Writer) (int, error) {
	req, err := f.request(session, verb == "add")
	if err != nil {
		return exitError, err
	}
	if !f.assign {
		return runSpawnNTM(context.Background(), verb, req, f.project, f.socket, f.asJSON, stdout, stderr), nil
	}
	env := envelope{Command: verb}
	// ntm takes the assignment flags without --assign and does nothing
	// with them; so does nhm.
	var assign *humanAssign
	if f.assign {
		assign = newHumanAssign(f, &req)
		if !req.DryRun && !f.yes {
			assign.ask = func() bool {
				return confirm(stderr, "--assign claims a bead for each agent and sends it, after the kickoff. Continue?")
			}
		}
	}
	return runSpawnRequest(context.Background(), req, f.project, f.socket, f.asJSON, env, stdout, stderr, assign), nil
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
//	[spawn]                         # ntm's [spawn] table
//	stagger_mode = "none"           # none (the default), fixed or smart
//	stagger_delay = "30s"           # 0 to 5m; 30s by default
//	[prompts]                       # ntm's [prompts] table: spawn's defaults
//	cc_default = "Read AGENTS.md."  # also cod_, gmi_, agy_, omp_; or *_default_file
//
// Other keys belong to other parts of nhm.
type spawnConfig struct {
	Providers struct {
		Capacity []string `toml:"capacity"`
	} `toml:"providers"`
	Quota struct {
		AdmitUnknown []string `toml:"admit_unknown"`
	} `toml:"quota"`
	Spawn struct {
		StaggerMode string `toml:"stagger_mode"`
		// StaggerDelay is nil when the key is not set (ntm's default 30s).
		StaggerDelay *time.Duration `toml:"stagger_delay"`
	} `toml:"spawn"`
	Prompts promptsConfig `toml:"prompts"`
}

// resolveSpawnStaggerDefaults is ntm's: a stagger setting the caller did
// not set explicitly comes from the [spawn] table, else its built-in
// default (none, 30s). A config value is checked here, so its error names
// the config key, not a flag.
func resolveSpawnStaggerDefaults(cfg spawnConfig, mode string, modeSet bool, delay time.Duration, delaySet bool) (string, time.Duration, error) {
	if !modeSet {
		if err := spawn.ValidateStaggerMode(cfg.Spawn.StaggerMode); err != nil {
			return "", 0, fmt.Errorf("[spawn] stagger_mode %w", err)
		}
		if cfg.Spawn.StaggerMode != "" {
			mode = cfg.Spawn.StaggerMode
		}
	}
	if !delaySet {
		delay = spawn.DefaultStaggerDelay
		if cfg.Spawn.StaggerDelay != nil {
			delay = *cfg.Spawn.StaggerDelay
		}
		if err := spawn.ValidateStaggerDelay(delay); err != nil {
			return "", 0, fmt.Errorf("[spawn] stagger_delay %w", err)
		}
	}
	return mode, delay, nil
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
		// ntm's launch: Claude gets the dcg and rch hooks on --settings;
		// pi gets nhm's guard extension on -e (nhm-dyr).
		ad.Settings = spawn.LaunchSettings(exec.LookPath, ie.inst.Dir)
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
func runSpawnRequest(ctx context.Context, req spawn.Request, project, socket string, asJSON bool, env envelope, stdout, stderr io.Writer,
	assign *humanAssign) int {
	// spawn and add are on the shared parser: every output below is
	// verbOutput's, with ntm's error text and exit 1.
	output := verbOutput
	ie, err := openInstance(socket)
	if err != nil {
		return output(stdout, stderr, asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	// --assign: the preflight runs before anything is created, in a dry
	// run too, and the yes comes after it.
	var plan spawnassign.Plan
	var scope core.Scope
	var kickoff string
	if assign != nil {
		if plan, scope, kickoff, err = assign.preflight(ctx, ie, project); err != nil {
			return output(stdout, stderr, asJSON, env, err, exitRefused, nil)
		}
		if assign.ask != nil && !assign.ask() {
			return output(stdout, stderr, asJSON, env, errors.New("--assign needs your yes (or --yes)"), exitRefused, nil)
		}
	}
	// --assign under an --op-id holds the operation's lock to its last
	// send: a rerun that gets it knows that this run is over, and that its
	// records are final (nhm-vcf).
	if assign != nil && !req.DryRun && req.OperationID != "" {
		unlock, err := store.LockOperation(ie.inst.Dir, req.OperationID)
		if errors.Is(err, store.ErrOperationBusy) {
			err = fmt.Errorf("operation %s still runs in another nhm process; nothing started (rerun it when that one ends)", req.OperationID)
		}
		if err != nil {
			return output(stdout, stderr, asJSON, env, err, exitRefused, nil)
		}
		defer unlock()
	}
	res, code, err := spawnUnderOwner(ctx, ie, &req, project)
	env.Scope, env.OperationID, env.Result = req.Scope, res.OperationID, res
	env.Warnings = append(res.Admission.Warnings, res.Warnings...)
	show := func(w io.Writer) { printSpawn(w, res) }
	if err != nil {
		if assign != nil && !req.DryRun {
			// The spawn sent no kickoff for --assign: an agent that started
			// before the error (the operation's last write) gets it now.
			if _, kerr := assign.kickoffs(ctx, ie, req, &res, nil, kickoff, nil); kerr != nil {
				err = fmt.Errorf("%w; kickoff: %v", err, kerr)
			}
		}
		return output(stdout, stderr, asJSON, env, err, code, show)
	}
	switch {
	case assign == nil:
		return output(stdout, stderr, asJSON, env, nil, spawnExit(res), show)
	case req.DryRun:
		env.Result = spawnAssignResult{Result: res, AssignStrategy: plan.Strategy, Assignments: []robot.SpawnAssignment{},
			Skipped: []spawnassign.HumanSkip{}}
		return output(stdout, stderr, asJSON, env, nil, spawnExit(res), func(w io.Writer) {
			printSpawn(w, res)
			fmt.Fprintf(w, "assignment (%s): the work is verified; a dry run claims nothing\n", plan.Strategy)
		})
	}
	ready := 0
	for _, t := range res.Targets {
		if t.Ready {
			ready++
		}
	}
	// fail prints the text before the error, as ntm does.
	fail := func(err error) int {
		if !asJSON {
			show(stdout)
		}
		return output(stdout, stderr, asJSON, env, err, exitError, nil)
	}
	// untouched is a replay's agents that the first run never reached; nil
	// on a first run.
	var untouched []int
	switch {
	case res.Replayed:
		// Nothing started. The first run is over (the lock), so its records
		// say which agents it reached. A run that reached none ended
		// between the spawn and the assignment: the ready path below runs,
		// as it would have. Otherwise each agent it did not reach gets its
		// kickoff alone, as an agent with no bead does: no bead is
		// assigned again.
		var uerr error
		untouched, uerr = replayUntouched(ctx, ie, &res)
		env.Result = spawnAssignResult{Result: res, AssignStrategy: assign.strategy, Assignments: []robot.SpawnAssignment{},
			Skipped: []spawnassign.HumanSkip{}}
		if uerr != nil {
			return fail(fmt.Errorf("assignment: the operation ran before, and its agents' records cannot be read; nothing is sent: %w", uerr))
		}
		if len(untouched) == 0 {
			return output(stdout, stderr, asJSON, env, nil, spawnExit(res), func(w io.Writer) {
				printSpawn(w, res)
				fmt.Fprintln(w, "assignment: the operation ran before; nothing is assigned again")
			})
		}
		// A target that is not ready is never untouched: a partial spawn
		// takes this branch too, and gets the kickoff alone, as at first.
		if len(untouched) < len(res.Targets) {
			text := kickoff
			if res.Ready() {
				text = assign.prefix(kickoff)
			}
			kicked, kerr := assign.kickoffs(ctx, ie, req, &res, nil, text, untouched)
			if kerr != nil {
				env.Warnings = append(env.Warnings, "kickoff of the agents the first run did not reach: "+kerr.Error())
			}
			env.Result = spawnAssignResult{Result: res, AssignStrategy: assign.strategy, Assignments: []robot.SpawnAssignment{},
				Skipped: []spawnassign.HumanSkip{}}
			return output(stdout, stderr, asJSON, env, nil, spawnExit(res), func(w io.Writer) {
				printSpawn(w, res)
				fmt.Fprintf(w, "assignment: the operation ran before; %d of its agents had no prompt, and %d got the kickoff now; "+
					"no bead is assigned again\n", len(untouched), kicked)
			})
		}
	case !res.Ready():
		// ntm stops when an agent is not ready, before the init prompt:
		// the agents that are up get the kickoff the spawn would have
		// sent, and nothing is assigned.
		_, kerr := assign.kickoffs(ctx, ie, req, &res, nil, kickoff, nil)
		env.Result = spawnAssignResult{Result: res, AssignStrategy: assign.strategy, Assignments: []robot.SpawnAssignment{},
			Skipped: []spawnassign.HumanSkip{}}
		show = func(w io.Writer) { printSpawn(w, res) }
		err := fmt.Errorf("assignment failed: ready wait failed: %d of %d agents ready", ready, len(res.Targets))
		if kerr != nil {
			err = fmt.Errorf("%w; kickoff: %v", err, kerr)
		}
		return fail(err)
	}
	// Every agent is up: ntm's selection picks the beads, and each agent
	// that gets one gets its identity, the kickoff, --init-prompt and its
	// bead in one delivery; every other agent then gets the kickoff and
	// --init-prompt. As in ntm, a planned bead that is not claimed and sent
	// fails the command; an agent that got none is no failure.
	prefix := assign.prefix(kickoff)
	var sel spawnassign.HumanSelection
	hooks := assignHooks{selectWork: assign.selectWork(ctx, ie, res.Session, scope.ProjectRoot, &sel)}
	if prefix != "" {
		hooks.preamble = identityPreamble(ctx, ie)
	}
	out := spawn.SpawnOutput(res, nil, "json", time.Now())
	assignPhase(ctx, ie, res, scope, plan, prefix, hooks, &out)
	outcome := newHumanOutcome(&out, sel)
	kicked, kerr := assign.kickoffs(ctx, ie, req, &res, out.Assignments, prefix, untouched)
	if kerr != nil {
		env.Warnings = append(env.Warnings, "kickoff of the agents without a bead: "+kerr.Error())
	}
	planned := []robot.SpawnAssignment{}
	for _, a := range out.Assignments {
		if a.BeadID != "" {
			planned = append(planned, a)
		}
	}
	env.Result = spawnAssignResult{Result: res, AssignStrategy: assign.strategy, Assignments: planned,
		Skipped: append([]spawnassign.HumanSkip{}, sel.Skipped...)}
	show = func(w io.Writer) {
		printSpawn(w, res)
		if res.Replayed {
			fmt.Fprintln(w, "assignment: the operation ran before, but none of its agents had a prompt; assigning now")
		}
		assign.print(w, stderr, ready, outcome.assigned+kicked, outcome, sel)
	}
	if err := outcome.err(); err != nil {
		return fail(err)
	}
	return output(stdout, stderr, asJSON, env, nil, spawnExit(res), show)
}

// ntmWords is a core refusal in ntm's words, without the core's sentinel
// ("spawn safety: ", "no such workspace: ").
type ntmWords struct{ err, sentinel error }

func (e ntmWords) Error() string {
	return strings.TrimPrefix(e.err.Error(), e.sentinel.Error()+": ")
}

func (e ntmWords) Unwrap() error { return e.err }

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
		for _, sentinel := range []error{spawn.ErrSessionExists, spawn.ErrNoWorkspace} {
			if errors.Is(err, sentinel) {
				err = ntmWords{err, sentinel}
			}
		}
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
	promptFile, names, label                   *string
	staggerMode, staggerDelay                  *string
	// counts are the --spawn-<kind> specs by flag (without "spawn-"),
	// in ntm's launch order.
	counts                              []robotCount
	spawnTimeout, timeout               *string
	dryRun, assign, safety, trustFolder *bool
	wait, ignoreCapacity                *bool
	assignFlags                         robotAssignFlags
}

// robotCount is one --spawn-<kind> flag: its name and its value.
type robotCount struct {
	flag, kind string
	value      *string
}

// ntmSpawnOrder is the order of ntm's robot spawn flags, which is its
// launch order (internal/robot/spawn.go, GetSpawn): the agents of nhm's
// own kinds (pi, the config's) come after them.
var ntmSpawnOrder = []string{"cc", "cod", "gmi", "agy", "grok", "omp", "oc"}

// addRobotSpawnFlags registers ntm's robot spawn flags and nhm's
// --trust-folder, then a --spawn-<kind> spec for each kind: ntm's short
// name for a built-in kind (cc, cod, agy, grok, omp, oc, and nhm's pi), the
// name and the flags of a config kind, when the flag is free.
func addRobotSpawnFlags(fs *flag.FlagSet) robotSpawnFlags {
	f := robotSpawnFlags{}
	f.session = fs.String("robot-spawn", "", "")
	f.dir = fs.String("spawn-dir", "", "")
	f.format = fs.String("robot-format", "auto", "")
	fs.Bool("spawn-no-user", false, "")
	f.safety = fs.Bool("spawn-safety", false, "")
	// As ntm's: the spawn waits for the agents to be ready only with
	// --spawn-wait or a prompt; otherwise it answers once they started.
	f.wait = fs.Bool("spawn-wait", false, "")
	// ntm's ready wait: --spawn-timeout, with --ready-timeout as its
	// deprecated alias (one value), else --timeout when given; 30s. A bare
	// number is seconds (resolveRobotSpawnTimeout).
	f.spawnTimeout = new(string)
	fs.StringVar(f.spawnTimeout, "spawn-timeout", "30s", "")
	fs.StringVar(f.spawnTimeout, "ready-timeout", "30s", "")
	f.timeout = fs.String("timeout", "5m", "")
	f.names = fs.String("spawn-names", "", "")
	f.label = fs.String("spawn-label", "", "")
	f.promptFile = fs.String("spawn-prompt-file", "", "")
	f.staggerMode = fs.String("spawn-stagger-mode", "", "")
	f.staggerDelay = fs.String("spawn-stagger-delay", "", "")
	// nhm's own, as spawn's --trust-folder: ntm 9d5ed977 has no such
	// flag. It answers the kind's folder-trust dialog during the ready
	// wait; without it an agent at the dialog ends unresolved (nhm-cvg).
	f.trustFolder = fs.Bool("trust-folder", false, "")
	// nhm's own, as spawn's --ignore-capacity: ntm 9d5ed977 overrides no
	// spawn admission. ACFS newproj passes it on a host whose capacity
	// provider answers red, as ACFS's old spawn took --force (nhm-1pq).
	f.ignoreCapacity = fs.Bool("ignore-capacity", false, "")
	f.dryRun = fs.Bool("dry-run", false, "")
	f.assign = fs.Bool("spawn-assign-work", false, "")
	f.assignFlags = addRobotAssignFlags(fs)
	f.prompt = fs.String("spawn-prompt", "", "")
	f.opID = fs.String("op-id", "", "")
	f.socket = fs.String("socket", "", "")
	var names []string
	for _, a := range kinds.All() {
		switch {
		case !a.Builtin:
			names = append([]string{a.Name}, a.Flags...)
		case len(a.Flags) > 0:
			names = a.Flags[:1]
		default:
			names = []string{a.Name}
		}
		for _, name := range names {
			if fs.Lookup("spawn-"+name) == nil {
				f.counts = append(f.counts, robotCount{flag: name, kind: a.Name, value: fs.String("spawn-"+name, "", "")})
			}
		}
	}
	rank := func(flag string) int {
		if i := slices.Index(ntmSpawnOrder, flag); i >= 0 {
			return i
		}
		return len(ntmSpawnOrder)
	}
	slices.SortStableFunc(f.counts, func(a, b robotCount) int { return rank(a.flag) - rank(b.flag) })
	return f
}

// robotReadyWait is a robot spawn's ready wait, as ntm takes it
// (resolveRobotSpawnTimeout): --spawn-timeout or --ready-timeout when
// given, else --timeout when given, else --spawn-timeout's default; a
// bare number is seconds. fs is parsed.
func robotReadyWait(fs *flag.FlagSet, rf robotSpawnFlags) (time.Duration, error) {
	given := map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { given[fl.Name] = true })
	value := *rf.spawnTimeout
	if !given["spawn-timeout"] && !given["ready-timeout"] && given["timeout"] {
		value = *rf.timeout
	}
	return parseNTMDurationWithDefault(value, time.Second)
}

// robotSpawnRequest is the spawn request of a parsed robot spawn. It
// sends no kickoff unless --spawn-prompt. With --spawn-assign-work (the
// caller's consent) the assignment phase sends each agent its work, with
// --spawn-prompt ahead of it, so the spawn itself sends nothing. Only
// --trust-folder answers a folder-trust dialog, and only
// --ignore-capacity admits a red or unknown capacity. The agents are in
// ntm's launch order. It also returns the prompt (--spawn-prompt, or the
// text of --spawn-prompt-file), which the assignment phase puts ahead of
// each work prompt. A refusal is ntm's: its code, text and hint.
func robotSpawnRequest(fs *flag.FlagSet, rf robotSpawnFlags) (spawn.Request, string, *robot.RobotResponse) {
	refuse := func(err error, hint string) (spawn.Request, string, *robot.RobotResponse) {
		return spawn.Request{}, "", &robot.RobotResponse{Success: false, ErrorCode: robot.ErrCodeInvalidFlag, Error: err.Error(), Hint: hint}
	}
	wait, err := robotReadyWait(fs, rf)
	if err != nil {
		return refuse(err, "Use a duration such as 30s or 500ms")
	}
	var wants []spawn.Want
	for _, c := range rf.counts {
		w, err := spawn.ParseRobotCount("--spawn-"+c.flag, c.kind, *c.value)
		if err != nil {
			return refuse(err, "Use count[:model[:effort]] (effort also as model@effort), e.g. --spawn-cod=2 or --spawn-cod=8:gpt-5.3-codex:high")
		}
		wants = append(wants, w)
	}
	// ntm's applyRobotSpawnPromptFlags: the prompt, then the stagger over
	// the [spawn] config's defaults, flags winning.
	const promptHint = "Use one of --spawn-prompt/--spawn-prompt-file, --spawn-stagger-mode=none|fixed|smart, and a " +
		"--spawn-stagger-delay such as 20s (0-5m); fix any invalid [spawn] config value"
	prompt, err := loadRobotMessageInput(*rf.prompt, *rf.promptFile, "--spawn-prompt", "--spawn-prompt-file")
	if err != nil {
		return refuse(err, promptHint)
	}
	given := map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { given[fl.Name] = true })
	delay := spawn.DefaultStaggerDelay
	if given["spawn-stagger-delay"] {
		if delay, err = time.ParseDuration(strings.TrimSpace(*rf.staggerDelay)); err != nil {
			return refuse(fmt.Errorf("invalid --spawn-stagger-delay %q: %w", *rf.staggerDelay, err), promptHint)
		}
	}
	cfg, err := loadSpawnConfig()
	if err != nil {
		return refuse(err, promptHint)
	}
	mode, delay, err := resolveSpawnStaggerDefaults(cfg, strings.TrimSpace(*rf.staggerMode), given["spawn-stagger-mode"], delay,
		given["spawn-stagger-delay"])
	if err != nil {
		return refuse(err, promptHint)
	}
	session := *rf.session
	if err := sessionlabel.ValidateProjectName(session); err != nil {
		return refuse(err, "Project names cannot contain '--' (reserved as label separator)")
	}
	if *rf.label != "" {
		if err := sessionlabel.ValidateLabel(*rf.label); err != nil {
			return refuse(fmt.Errorf("invalid label: %w", err), "Use a valid label (alphanumeric, dash, underscore)")
		}
		session = sessionlabel.FormatSessionName(session, *rf.label)
	}
	// ntm's validateSpawnStagger.
	const staggerHint = "Use --spawn-stagger-mode=none|fixed|smart and a --spawn-stagger-delay between 0 and 5m (e.g. 20s)"
	if err := spawn.ValidateStaggerMode(mode); err != nil {
		return refuse(fmt.Errorf("--spawn-stagger-mode %w", err), staggerHint)
	}
	if err := spawn.ValidateStaggerDelay(delay); err != nil {
		return refuse(fmt.Errorf("--spawn-stagger-delay %w", err), staggerHint)
	}
	if mode == "" {
		mode = spawn.StaggerNone
	}
	// ntm's validateSpawnRequest: each count is zero or more, and one is
	// more.
	total := 0
	for i, w := range wants {
		if w.Count < 0 {
			return refuse(fmt.Errorf("--spawn-%s must be zero or greater, got %d", rf.counts[i].flag, w.Count),
				"Use non-negative agent counts and a supported assignment strategy")
		}
		total += w.Count
	}
	if total == 0 {
		return refuse(errors.New("no agents specified (use cc, cod, gmi, agy, grok, omp, or oc counts)"),
			"Use non-negative agent counts and a supported assignment strategy")
	}
	// ntm waits for ready only with --spawn-wait or a prompt, and sends
	// no prompt after a failed wait.
	req := spawn.Request{Session: session, Prompt: prompt, NoKickoff: prompt == "",
		ReadyTimeout: wait, NoWait: !*rf.wait && prompt == "", KickoffAllReady: true, StaggerMode: mode, StaggerDelay: delay,
		TrustFolder: *rf.trustFolder, RequireObservations: true, DryRun: *rf.dryRun,
		OperationID: *rf.opID, Safety: *rf.safety, IgnoreCapacity: *rf.ignoreCapacity}
	if *rf.assign {
		req.Prompt, req.NoKickoff, req.AssignOptions = "", true, rf.assignFlags.options(prompt)
	}
	for _, w := range wants {
		if w.Count > 0 {
			req.Agents = append(req.Agents, w)
		}
	}
	return req, prompt, nil
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
// [--dry-run] [--spawn-assign-work [--strategy=S]] [--spawn-prompt=T]
// [--trust-folder] [--ignore-capacity].
func runRobotSpawn(args []string, stdout, stderr io.Writer) int {
	now := time.Now()
	fs := flag.NewFlagSet("nhm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	rf := addRobotSpawnFlags(fs)
	dir, format := rf.dir, rf.format
	dryRun, assign, socket := rf.dryRun, rf.assign, rf.socket
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
	req, prompt, refusal := robotSpawnRequest(fs, rf)
	if refusal != nil {
		return robotWrite(stdout, robotAssignRefusal(refusal, *rf.session, *dir, *format, now), robotError)
	}
	ie, err := openInstance(*socket)
	if err != nil {
		return robotFail(stdout, robot.ErrCodeDependencyMissing, err.Error(), "run inside herdr or give --socket", *format, robotError, now)
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
	// --spawn-assign-work under an --op-id holds the operation's lock to
	// its last send, as spawn --assign does: a rerun that gets it knows
	// that the first run is over (nhm-88n).
	if *assign && !*dryRun && req.OperationID != "" {
		unlock, err := store.LockOperation(ie.inst.Dir, req.OperationID)
		if errors.Is(err, store.ErrOperationBusy) {
			return robotFail(stdout, robot.ErrCodeOperationInProgress, err.Error(),
				"another nhm process still runs this operation; retry when it ends", *format, robotError, now)
		}
		if err != nil {
			return robotFail(stdout, robot.ErrCodeInternalError, err.Error(), "", *format, robotError, now)
		}
		defer unlock()
	}
	res, _, err := spawnUnderOwner(ctx, ie, &req, *dir)
	if res.Session == "" {
		res.Session, res.ProjectRoot = req.Session, req.ProjectRoot
	}
	// A replay assigns new work only when the first run reached none of
	// its agents (nhm-88n).
	if *assign && err == nil && res.Replayed && res.Ready() {
		plan, err = robotReplayPlan(ctx, ie, &res, plan)
	}
	out := spawn.SpawnOutput(res, err, *format, now)
	spawn.NameAgents(&out, res, spawn.ParseCustomNames(*rf.names))
	switch {
	case errors.Is(err, core.ErrNotOwner):
		out.ErrorCode = robot.ErrCodeNotOwner
	case errors.Is(err, store.ErrOperationIDConflict):
		// ntm's code for an --op-id reused with another request.
		out.ErrorCode = robot.ErrCodeIdempotencyConflict
	}
	if errors.Is(err, spawn.ErrSessionExists) {
		// ntm's answer (internal/robot/spawn.go:242-243): INVALID_FLAG,
		// its sentence without nhm's sentinel, and its hint.
		out.ErrorCode, out.Hint = robot.ErrCodeInvalidFlag, "Choose a new session name or disable --spawn-safety"
		out.Error = strings.TrimPrefix(err.Error(), spawn.ErrSessionExists.Error()+": ")
	}
	// --spawn-assign-work's work prompts go out on ntm's stagger schedule,
	// in launch order; a dry run reports the plan.
	if *assign && (*dryRun || out.Success) {
		stagger, splan := spawn.PlanStagger(req.StaggerMode, req.StaggerDelay, res.ProjectRoot, res.Targets)
		out.Stagger = splan
		if !*dryRun {
			plan.Pacer = spawn.NewPacer(stagger, splan, nil, nil)
		}
	}
	if *assign && !*dryRun && out.Success {
		assignPhase(ctx, ie, res, scope, plan, prompt, assignHooks{}, &out)
	}
	if !out.Success {
		return robotWrite(stdout, out, robotError)
	}
	return robotWrite(stdout, out, robotOK)
}
