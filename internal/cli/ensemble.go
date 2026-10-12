package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/ensemble"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/store"
)

// nhm ensemble (plan-v8 2.11): ntm's presets, with ntm's grammar.

const ensembleUsage = `Usage: nhm ensemble <preset> "<question>" [run flags]
       nhm ensemble spawn <session> --preset P --question Q [run flags]
       nhm ensemble list [--json]
       nhm ensemble status <run> [--json]
       nhm ensemble synthesize <run> [--to NAME] [--partial] [--json]
       nhm ensemble stop <run> [--json]
       nhm ensemble presets [--tag TAG] [--verbose] [--format table|json] [--json]

Run flags: [--kind KIND] [--model M] [--project DIR] [--socket PATH] [--json]

A preset names angles, one reasoning mode each (nhm modes explain
<mode>). A run starts one agent per angle in the group ens-<run> (nhm's
spawn; the session is the run id unless spawn names one) and prompts each
with its angle (nhm's send). Each angle writes its result to a file in
the run's directory. synthesize checks that every result belongs to the
run and angle, refuses when one is missing unless --partial (which
records the omissions), and sends the synthesis prompt to one agent.
stop keeps every result and retires no agent. Runs need nhm to own the
herdr instance.
`

func runEnsemble(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, ensembleUsage)
		return exitRefused
	}
	switch args[0] {
	case "presets":
		return runEnsemblePresets(args[1:], stdout, stderr)
	case "spawn":
		return runEnsembleStart(args[1:], true, stdout, stderr)
	case "list", "ls":
		return runEnsembleList(args[1:], stdout, stderr)
	case "status":
		return runEnsembleRun("status", args[1:], stdout, stderr)
	case "synthesize":
		return runEnsembleRun("synthesize", args[1:], stdout, stderr)
	case "stop":
		return runEnsembleRun("stop", args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, ensembleUsage)
		return exitOK
	}
	// ntm's short form: nhm ensemble <preset> "<question>".
	if c, err := ensemble.Builtin(); err == nil {
		if _, err := c.Preset(args[0]); err == nil {
			return runEnsembleStart(args, false, stdout, stderr)
		}
	}
	fmt.Fprintf(stderr, "nhm ensemble: unknown verb or preset %q\n%s", args[0], ensembleUsage)
	return exitRefused
}

// runEnv is a run's environment on an open instance: nhm's spawn and
// send, each under the owner lock, as the spawn and send verbs run them.
func (ie *instanceEnv) runEnv(project string) ensemble.RunEnv {
	return ensemble.RunEnv{RunsDir: filepath.Join(ie.inst.Dir, store.RunsDir),
		Spawn: func(ctx context.Context, req spawn.Request) (spawn.Result, error) {
			res, _, err := spawnUnderOwner(ctx, ie, &req, project)
			return res, err
		},
		Send: func(ctx context.Context, req send.Request) (send.Result, error) {
			if err := ie.fillScope(ctx, &req, req.Session, project); err != nil {
				return send.Result{}, err
			}
			res, _, err := runSendCore(ctx, ie, req)
			return res, err
		}}
}

func runEnsembleStart(args []string, spawnForm bool, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ensemble", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, ensembleUsage) }
	preset := fs.String("preset", "", "the preset (spawn form)")
	question := fs.String("question", "", "the question (spawn form)")
	kind := fs.String("kind", "claude", "the agent kind of every angle")
	model := fs.String("model", "", "the model of every angle")
	project := fs.String("project", "", "the project (default: the git work tree of the cwd)")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	req := ensemble.StartRequest{Kind: *kind, Model: *model}
	switch {
	case spawnForm && len(pos) == 1:
		req.Session, req.Preset, req.Question = pos[0], *preset, *question
	case !spawnForm && len(pos) == 2 && *preset == "" && *question == "":
		req.Preset, req.Question = pos[0], pos[1]
	default:
		fmt.Fprint(stderr, ensembleUsage)
		return exitRefused
	}
	env := envelope{Command: "ensemble"}
	c, err := ensemble.Builtin()
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	ie, err := openInstance(*socket)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	ctx := context.Background()
	if s, err := projectScope(ctx, ie.inst.ID, *project); err == nil {
		req.ProjectRoot, req.ProjectID = s.ProjectRoot, s.ProjectID
	} else {
		return output(stdout, stderr, *asJSON, env, err, exitRefused, nil)
	}
	r, err := ensemble.Start(ctx, ie.runEnv(*project), c, req)
	env.Result = r
	code := exitOK
	switch {
	case errors.Is(err, ensemble.ErrNoQuestion), errors.Is(err, ensemble.ErrUnknownPreset), errors.Is(err, ensemble.ErrUnknownMode):
		code = exitRefused
	case err != nil:
		code = exitError
	}
	return output(stdout, stderr, *asJSON, env, err, code, func(w io.Writer) { printRun(w, r) })
}

func runEnsembleList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ensemble list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) > 0 {
		fmt.Fprint(stderr, ensembleUsage)
		return exitRefused
	}
	env := envelope{Command: "ensemble list"}
	ie, err := openInstance(*socket)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	runs, err := ensemble.List(filepath.Join(ie.inst.Dir, store.RunsDir))
	env.Result = runs
	return output(stdout, stderr, *asJSON, env, err, exitOK, func(w io.Writer) {
		for _, r := range runs {
			fmt.Fprintf(w, "%-30s %-20s %-12s %d angles  %s\n", r.ID, r.Preset, r.Status, len(r.Angles), r.CreatedAt.Format(time.RFC3339))
		}
		fmt.Fprintf(w, "%d run(s)\n", len(runs))
	})
}

// RunStatus is a run with the state of each angle's result.
type RunStatus struct {
	ensemble.Run
	Artifacts []ensemble.ArtifactState `json:"artifacts"`
}

func runEnsembleRun(verb string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ensemble "+verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, ensembleUsage) }
	to := fs.String("to", "", "the agent that writes the synthesis (synthesize)")
	partial := fs.Bool("partial", false, "synthesize without the angles that gave no result")
	project := fs.String("project", "", "the project (default: the git work tree of the cwd)")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	if len(pos) != 1 || (verb != "synthesize" && (*to != "" || *partial)) {
		fmt.Fprint(stderr, ensembleUsage)
		return exitRefused
	}
	env := envelope{Command: "ensemble " + verb}
	ie, err := openInstance(*socket)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	renv := ie.runEnv(*project)
	var r ensemble.Run
	switch verb {
	case "status":
		r, err = ensemble.Load(renv.RunsDir, pos[0])
	case "synthesize":
		r, err = ensemble.Synthesize(context.Background(), renv, ensemble.SynthesizeRequest{RunID: pos[0], To: *to, Partial: *partial})
	case "stop":
		r, err = ensemble.Stop(renv, pos[0])
	}
	st := RunStatus{Run: r, Artifacts: ensemble.Artifacts(r)}
	env.Result = st
	code := exitOK
	switch {
	case errors.Is(err, ensemble.ErrNoRun), errors.Is(err, ensemble.ErrMissingAngles), errors.Is(err, ensemble.ErrRunStopped):
		code = exitRefused
	case err != nil:
		code = exitError
	}
	return output(stdout, stderr, *asJSON, env, err, code, func(w io.Writer) { printRunStatus(w, st) })
}

func printRun(w io.Writer, r ensemble.Run) {
	fmt.Fprintf(w, "run %s: preset %s, %s, session %s, %d angles\n", r.ID, r.Preset, r.Status, r.Session, len(r.Angles))
	for _, a := range r.Angles {
		line := fmt.Sprintf("  %d %-24s %-12s %s", a.Index, a.Mode, dash(a.MailName), dash(a.PromptStatus))
		if a.Error != "" {
			line += "  " + a.Error
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintf(w, "results: %s\n", r.Dir)
}

func printRunStatus(w io.Writer, st RunStatus) {
	if st.ID == "" {
		return
	}
	printRun(w, st.Run)
	for _, a := range st.Artifacts {
		state := "ready"
		if !a.Valid {
			state = a.Reason
		}
		fmt.Fprintf(w, "  result %d %-24s %s\n", a.Index, a.Mode, state)
	}
	if st.SynthesisTo != "" {
		fmt.Fprintf(w, "synthesis: %s writes %s", st.SynthesisTo, st.Synthesis)
		if len(st.Omitted) > 0 {
			fmt.Fprintf(w, " (without angles %v)", st.Omitted)
		}
		fmt.Fprintln(w)
	}
}

// PresetView is a preset with its modes resolved, as presets prints it.
type PresetView struct {
	ensemble.EnsemblePreset
	ModeIDs []string `json:"mode_ids"`
}

func runEnsemblePresets(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ensemble presets", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, ensembleUsage) }
	ff := addFormatFlags(fs, "table")
	tag := fs.String("tag", "", "only presets with this tag")
	fs.StringVar(tag, "t", "", "tag (shorthand)")
	verbose := fs.Bool("verbose", false, "show each preset's modes, synthesis and budget")
	fs.BoolVar(verbose, "v", false, "verbose (shorthand)")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	env := envelope{Command: "ensemble presets"}
	if len(pos) > 0 {
		fmt.Fprint(stderr, ensembleUsage)
		return exitRefused
	}
	if err := ff.check(); err != nil {
		return output(stdout, stderr, *ff.asJSON, env, err, exitRefused, nil)
	}
	c, err := ensemble.Builtin()
	if err != nil {
		return output(stdout, stderr, *ff.asJSON, env, err, exitError, nil)
	}
	views := []PresetView{}
	for _, p := range c.Presets() {
		if *tag != "" && !hasTag(p.Tags, *tag) {
			continue
		}
		modes, err := c.Resolve(p)
		if err != nil {
			return output(stdout, stderr, *ff.asJSON, env, err, exitError, nil)
		}
		v := PresetView{EnsemblePreset: p}
		for _, m := range modes {
			v.ModeIDs = append(v.ModeIDs, m.ID)
		}
		views = append(views, v)
	}
	if ff.bareJSON() {
		return printJSON(stdout, stderr, "ensemble presets", views)
	}
	env.Result = views
	return output(stdout, stderr, *ff.asJSON, env, nil, exitOK, func(w io.Writer) {
		for _, v := range views {
			fmt.Fprintf(w, "%-24s %-2d modes  %-14s %s\n", v.Name, len(v.ModeIDs), v.Synthesis.Strategy, v.Description)
			if *verbose {
				fmt.Fprintf(w, "    modes: %s\n", strings.Join(v.ModeIDs, ", "))
				fmt.Fprintf(w, "    budget: %d tokens per mode, %d in total, %s per mode, %s in total\n",
					v.Budget.MaxTokensPerMode, v.Budget.MaxTotalTokens, v.Budget.TimeoutPerMode, v.Budget.TotalTimeout)
				if len(v.Tags) > 0 {
					fmt.Fprintf(w, "    tags: %s\n", strings.Join(v.Tags, ", "))
				}
			}
		}
	})
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if strings.EqualFold(t, want) {
			return true
		}
	}
	return false
}
