package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/arosl/nhm/internal/core/ensemble"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/store"
)

// nhm ensemble (plan-v8 2.11): ntm's presets, with ntm's grammar.

const ensembleUsage = `Usage: nhm ensemble <preset> "<question>" [run flags]
       nhm ensemble spawn <session> --preset P --question Q [run flags]
       nhm ensemble presets|list [--tag TAG] [--verbose] [--format table|json|yaml] [--json]
       nhm ensemble status <session|run> [--format text|json] [--json]
       nhm ensemble synthesize <session|run> [--to NAME] [--partial] [--format text|json] [--json]
       nhm ensemble stop <session|run> [--format text|json] [--json]
       nhm ensemble runs [--json]

Run flags: [--kind KIND] [--model M] [--project DIR] [--socket PATH] [--json]

presets (ntm's alias: list) prints ntm's preset table and types. A
preset names angles, one reasoning mode each (nhm modes explain <mode>).
A run starts one agent per angle in the group ens-<run> (nhm's spawn; the
session is the run id unless spawn names one) and prompts each with its
angle (nhm's send). Each angle writes its result to a file in the run's
directory. status, synthesize and stop take the session (its newest run)
or a run id. synthesize checks that every result belongs to the run and
angle, refuses when one is missing unless --partial (which records the
omissions), and sends the synthesis prompt to one agent. stop keeps
every result and retires no agent. runs lists the runs. Runs need nhm to
own the herdr instance.
`

func runEnsemble(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, ensembleUsage)
		return exitRefused
	}
	switch args[0] {
	case "presets", "list":
		// ntm's `ensemble list` is its alias of presets.
		return runEnsemblePresets(args[1:], stdout, stderr)
	case "spawn":
		return runEnsembleStart(args[1:], true, stdout, stderr)
	case "runs":
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
	fs := flag.NewFlagSet("ensemble runs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) > 0 {
		fmt.Fprint(stderr, ensembleUsage)
		return exitRefused
	}
	env := envelope{Command: "ensemble runs"}
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

// resolveRun takes ntm's argument, a session, or nhm's run id: a run id
// names that run; a session names its newest run.
func resolveRun(runsDir, ref string) (string, error) {
	runs, err := ensemble.List(runsDir)
	if err != nil {
		return "", err
	}
	newest := ""
	var at time.Time
	for _, r := range runs {
		if r.ID == ref {
			return ref, nil
		}
		if r.Session == ref && (newest == "" || r.CreatedAt.After(at)) {
			newest, at = r.ID, r.CreatedAt
		}
	}
	if newest == "" {
		return "", fmt.Errorf("%w: no run and no session %q (nhm ensemble runs lists them)", ensemble.ErrNoRun, ref)
	}
	return newest, nil
}

// printJSON writes v as indented JSON, without nhm's envelope.
func printJSON(stdout, stderr io.Writer, command string, v any) int {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(stderr, "nhm %s: %v\n", command, err)
		return exitError
	}
	return exitOK
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
	format := fs.String("format", "text", "text, or json: the run as JSON (ntm's run output is not reproduced)")
	fs.StringVar(format, "f", "text", "format (shorthand)")
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
	if *format != "text" && *format != "json" {
		return output(stdout, stderr, *asJSON, env, fmt.Errorf("--format %q: give text or json", *format), exitRefused, nil)
	}
	ie, err := openInstance(*socket)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	renv := ie.runEnv(*project)
	var r ensemble.Run
	id, err := resolveRun(renv.RunsDir, pos[0])
	if err == nil {
		switch verb {
		case "status":
			r, err = ensemble.Load(renv.RunsDir, id)
		case "synthesize":
			r, err = ensemble.Synthesize(context.Background(), renv, ensemble.SynthesizeRequest{RunID: id, To: *to, Partial: *partial})
		case "stop":
			r, err = ensemble.Stop(renv, id)
		}
	}
	st := RunStatus{Run: r, Artifacts: ensemble.Artifacts(r)}
	env.Result = st
	if *format == "json" && !*asJSON && err == nil {
		return printJSON(stdout, stderr, env.Command, st)
	}
	code := exitOK
	switch {
	case errors.Is(err, ensemble.ErrNoRun), errors.Is(err, ensemble.ErrMissingAngles), errors.Is(err, ensemble.ErrRunStopped),
		errors.Is(err, ensemble.ErrSynthesizing), errors.Is(err, ensemble.ErrNotRunning), errors.Is(err, ensemble.ErrRunBusy):
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
	fmt.Fprintf(w, "results: %s\n", r.ResultsDir())
	if r.StaleClaim(time.Now()) {
		fmt.Fprintf(w, "synthesis: claimed with no prompt sent for %s or more; synthesize may run again\n", ensemble.SynthesisClaimTimeout)
	}
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
	result := ensemble.PresetsList(c, *tag, *verbose, time.Now())
	return renderNTM(stdout, stderr, ff, env, result, func(w io.Writer, format string) error {
		return ensemble.RenderPresets(w, result, *verbose, format)
	})
}
