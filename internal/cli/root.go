package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/arosl/nhm/internal/profiler"
	"github.com/arosl/nhm/internal/robot"
)

// nhm's grammar is ntm's (AGENTS.md rule 4): a cobra root with pflag, as
// ntm builds it (internal/cli/root.go). A verb on the shared parser is a
// *cobra.Command with ntm's Use, Aliases, Args and flags, and its RunE
// calls core. A verb that its bead has not moved yet keeps its own parser
// behind a pass-through command (legacyVerbs); moving it is one line here
// and the verb's own file.

// verbExit is a verb's exit code when the verb has written its own
// output, its error too: Run exits with the code and prints nothing more.
type verbExit struct{ code int }

func (e verbExit) Error() string { return "exit " + strconv.Itoa(e.code) }

// ExitCode is ntm's exit coder interface (cli.ExitCode).
func (e verbExit) ExitCode() int { return e.code }

// rootOptions are ntm's root persistent flags for one Run
// (internal/cli/root.go:4480-4530).
type rootOptions struct {
	json           bool
	config         string
	ssh            string
	noColor        bool
	redact         string
	allowSecret    bool
	profileStartup bool

	// set holds the root flags given, by name.
	set     map[string]bool
	restore []func()

	// args, stdout and stderr are the Run's, for --ssh: the call goes to
	// the host as it was given, less --ssh.
	args           []string
	stdout, stderr io.Writer

	// prof times the call for --profile-startup, and verb is the span of
	// the verb.
	prof *profiler.Profiler
	verb *profiler.Span
}

// rootFlag is one of ntm's root persistent flags; value is false for a
// boolean flag. notBuilt names the bead of a flag that parses but whose
// effect nhm does not have yet: a call that gives one is refused, never
// run as if the flag were not there (--ssh run here would act on the
// wrong host).
type rootFlag struct {
	name     string
	value    bool
	notBuilt string
}

var rootFlags = []rootFlag{
	{name: "json"},
	// --config F names nhm's config file for the call (NHM_CONFIG).
	{name: "config", value: true},
	// --ssh H hands the whole call to H once it is parsed (apply). An
	// empty --ssh is none, as in ntm.
	{name: "ssh", value: true},
	{name: "no-color"},
	{name: "redact", value: true, notBuilt: "nhm-2p6.20"},
	{name: "allow-secret", notBuilt: "nhm-2p6.20"},
	{name: "profile-startup"},
}

// notBuiltError refuses a root flag that nhm parses but cannot apply yet.
type notBuiltError struct{ flag, bead string }

func (e notBuiltError) Error() string {
	return fmt.Sprintf("--%s is not in this nhm yet (deferred: %s)", e.flag, e.bead)
}

// addRootFlags defines ntm's root persistent flags, with ntm's texts.
func addRootFlags(fs *pflag.FlagSet, o *rootOptions) {
	fs.StringVar(&o.config, "config", "", "config file (default ~/.config/nhm/config.toml)")
	fs.BoolVar(&o.json, "json", false, "Output in JSON format (machine-readable)")
	fs.StringVar(&o.ssh, "ssh", "", "Remote host for SSH execution (e.g. user@host)")
	fs.BoolVar(&o.noColor, "no-color", false, "Disable colored output")
	fs.StringVar(&o.redact, "redact", "", "Redaction mode override: off, warn, redact, block")
	fs.BoolVar(&o.allowSecret, "allow-secret", false, "Bypass 'block' mode for this invocation (use with caution)")
	fs.BoolVar(&o.profileStartup, "profile-startup", false, "Enable startup profiling (outputs timing data)")
}

// apply refuses a root flag whose effect nhm does not have yet, and sets
// up the others for this Run. --no-color sets NHM_NO_COLOR, where ntm sets
// its NTM_NO_COLOR; nhm's theme reads it. --config F sets NHM_CONFIG,
// which nhm's config readers take for their path (the operator's ruling,
// 2026-10-24: F replaces the path of nhm's config); an empty --config is
// none, as in ntm.
//
// --ssh H hands the whole call to H once it is parsed (the operator's
// ruling, 2026-10-24), less the --ssh words that pflag took: cmd's flags
// say which words are other flags' values. A legacy verb (its command
// does no flag parsing) parses its own flags, so only the root's value
// flags are known there.
//
// --profile-startup starts ntm's profiler: a cli_init span, as ntm's
// EnableProfilingIfRequested records it, and a span of the verb that the
// root's PersistentPostRun ends and prints (profileReport).
func (o *rootOptions) apply(cmd *cobra.Command) error {
	for _, f := range rootFlags {
		if f.notBuilt != "" && o.set[f.name] {
			return notBuiltError{flag: f.name, bead: f.notBuilt}
		}
	}
	if o.ssh != "" {
		v := rootValues
		if !cmd.DisableFlagParsing {
			v = pflagValues(cmd.Flags())
		}
		return verbExit{runOverSSH(o.ssh, stripSSH(o.args, v), o.stdout, o.stderr)}
	}
	if o.profileStartup && o.prof == nil {
		o.prof = profiler.New()
		o.prof.Enable()
		o.prof.StartWithPhase("cli_init", "startup").End()
		o.verb = o.prof.StartWithPhase(cmd.Name(), "command")
	}
	if o.noColor {
		o.setenv("NHM_NO_COLOR", "1")
	}
	if o.config != "" {
		path, err := filepath.Abs(o.config)
		if err != nil {
			return fmt.Errorf("--config %s: %w", o.config, err)
		}
		o.setenv("NHM_CONFIG", path)
	}
	return nil
}

// profileReport ends the verb's span and writes ntm's text profile on
// stderr, when --profile-startup started one and the call is not JSON.
func (o *rootOptions) profileReport() {
	if o.prof == nil {
		return
	}
	o.verb.End()
	if o.json || jsonInArgs(o.args) {
		return
	}
	_ = o.prof.WriteText(o.stderr)
}

func (o *rootOptions) setenv(key, value string) {
	old, had := os.LookupEnv(key)
	_ = os.Setenv(key, value)
	o.restore = append(o.restore, func() {
		if had {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

// takeRootFlags takes ntm's root flags out of a pass-through verb's
// arguments, before any "--", and returns the rest. --json stays, because
// every legacy verb parses its own; so does a root flag that the verb's
// own parser defines (own). These kept flags go after the other
// arguments: cobra leaves the root flags given before the verb in front,
// where a verb with sub-verbs (compatibility status) wants its sub-verb,
// and every legacy verb parses its flags interleaved.
func takeRootFlags(args []string, o *rootOptions, own []string) ([]string, error) {
	var rest, kept []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			rest = append(rest, kept...)
			return append(rest, args[i:]...), nil
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		f, ok := findRootFlag(name)
		if !strings.HasPrefix(a, "--") || !ok {
			rest = append(rest, a)
			continue
		}
		if f.name == "json" || slices.Contains(own, f.name) {
			kept = append(kept, a)
			if f.value && !hasValue && i+1 < len(args) {
				i++
				kept = append(kept, args[i])
			}
			continue
		}
		if f.value && !hasValue {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag needs an argument: --%s", f.name)
			}
			i++
			value, hasValue = args[i], true
		}
		if err := o.setFlag(f, value, hasValue); err != nil {
			return nil, err
		}
	}
	return append(rest, kept...), nil
}

func findRootFlag(name string) (rootFlag, bool) {
	for _, f := range rootFlags {
		if f.name == name {
			return f, true
		}
	}
	return rootFlag{}, false
}

// setFlag records one root flag that a pass-through verb was given, with
// pflag's text for a bad boolean.
func (o *rootOptions) setFlag(f rootFlag, value string, hasValue bool) error {
	b := true
	if !f.value && hasValue {
		var err error
		if b, err = strconv.ParseBool(value); err != nil {
			return fmt.Errorf("invalid argument %q for %q flag: %w", value, "--"+f.name, err)
		}
	}
	switch f.name {
	case "config":
		o.config = value
	case "ssh":
		o.ssh = value
	case "redact":
		o.redact = value
	case "no-color":
		o.noColor = b
	case "allow-secret":
		o.allowSecret = b
	case "profile-startup":
		o.profileStartup = b
	}
	o.set[f.name] = true
	return nil
}

// legacyVerb is a verb on its own parser, until its bead moves it to the
// shared one.
type legacyVerb struct {
	name    string
	aliases []string
	run     func(args []string, stdout, stderr io.Writer) int
	// own are the root flags besides --json that the verb's parser
	// defines; they reach the verb as given.
	own []string
}

// legacyVerbs are the verbs not on the shared parser yet, as cli.go's
// switch dispatched them. It is a function: the UI's verbs run the shared
// parser in turn (verbRun), and a variable would be an initialization
// cycle.
func legacyVerbs() []legacyVerb {
	return []legacyVerb{
		{name: "resolve", run: runResolve},
		{name: "adopt", run: runAdopt},
		{name: "move", run: runMove},
		{name: "arrange", run: runArrange},
		{name: "retire", run: runRetire},
		{name: "recycle", run: runRecycle},
		{name: "reap", run: runReap},
		{name: "attach", run: runAttach},
		{name: "new", run: runNew},
		{name: "admin", run: runAdmin},
		{name: "status", run: runStatus},
		{name: "snapshot", run: runSnapshot},
		{name: "attention", run: runAttention},
		{name: "read", run: runRead},
		// ntm's logs <session>; an agent's name runs read (the operator's
		// ruling, nhm-k9d, 2026-10-23).
		{name: "logs", run: runLogs},
		// ntm's watch and its alias w (the operator's ruling, nhm-zac,
		// 2026-10-23).
		{name: "watch", aliases: []string{"w"}, run: runWatch, own: []string{"no-color"}},
		{name: "activity", run: runActivity},
		{name: "health", run: runHealth},
		// ntm's deps, with its alias check (the operator's ruling, nhm-k9d,
		// 2026-10-23).
		{name: "deps", aliases: []string{"check"}, run: runDeps},
		{name: "tutorial", run: runTutorial},
		{name: "wake", run: runWake},
		{name: "maintain", run: runMaintain},
		{name: "capabilities", run: runCapabilities},
		{name: "compatibility", run: runCompatibility},
		{name: "quota", run: runQuota},
		{name: "limits", run: runLimits},
		// dashboard, with ntm's short names dash and d, is ntm's name for the
		// board (the operator's F.7 ruling, nhm-k9d); dashboard <session>
		// shows that session's workspace (the operator's ruling, 2026-10-23).
		{name: "board", aliases: []string{"dashboard", "dash", "d"}, run: runBoard},
		{name: "menu", run: runMenu},
		// bind's --config is herdr's config file.
		{name: "bind", run: runBind, own: []string{"config"}},
		{name: "ui", run: runUI},
		{name: "send", run: runSend},
		{name: "feedback", run: runFeedback},
		{name: "modes", run: runModes},
		{name: "ensemble", run: runEnsemble},
	}
}

// command is the pass-through command of a legacy verb: cobra finds it,
// and the verb parses the rest itself.
func (v legacyVerb) command(o *rootOptions, stdout, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:                v.name,
		Aliases:            v.aliases,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			args, err := takeRootFlags(args, o, v.own)
			if err != nil {
				return err
			}
			// A help request prints the usage and runs nothing (nhm-4cb),
			// as cobra's help flag comes before any hook.
			if u, ok := leafHelp(append([]string{v.name}, args...)); ok {
				fmt.Fprint(stdout, u)
				return nil
			}
			if err := o.apply(cmd); err != nil {
				return err
			}
			if code := v.run(args, stdout, stderr); code != exitOK {
				return verbExit{code}
			}
			return nil
		},
	}
}

// root is one Run's command tree.
type root struct {
	cmd  *cobra.Command
	opts *rootOptions
}

// newRoot builds the command tree for one Run, so no flag value outlives
// it.
func newRoot(stdout, stderr io.Writer) *root {
	o := &rootOptions{set: map[string]bool{}, stdout: stdout, stderr: stderr}
	cmd := &cobra.Command{
		Use: "nhm",
		// Version enables cobra's --version flag, as in ntm.
		Version:       Version,
		Short:         "the herdr wrapper of the ACFS flywheel (what ntm is to tmux)",
		SilenceUsage:  true,
		SilenceErrors: true,
		// ntm has its own completion verb (nhm-2p6.10), not cobra's.
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// A verb on the shared parser: cobra parsed the root flags.
			// A pass-through verb applies them itself.
			for _, f := range rootFlags {
				if cmd.Flags().Changed(f.name) {
					o.set[f.name] = true
				}
			}
			if cmd.DisableFlagParsing {
				return nil // a pass-through verb applies them itself
			}
			return o.apply(cmd)
		},
		// ntm prints the profile after a command that succeeded, and not
		// for a machine call (its PersistentPostRun).
		PersistentPostRun: func(cmd *cobra.Command, args []string) {
			o.profileReport()
		},
		// Bare nhm prints the usage and exits 0, as bare ntm prints its
		// help.
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprint(stdout, usage)
			return nil
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetIn(os.Stdin)
	addRootFlags(cmd.PersistentFlags(), o)
	// The -V shorthand for cobra's --version, as ntm sets it.
	cmd.Flags().BoolP("version", "V", false, "Print version information (same as 'nhm version')")
	cmd.SetVersionTemplate("nhm version {{.Version}}\n")
	cmd.SetFlagErrorFunc(flagError)
	defaultHelp := cmd.HelpFunc()
	// The usage texts are nhm's (the --help row): the root's, then each
	// verb's; a verb without one gets cobra's help.
	cmd.SetHelpFunc(func(c *cobra.Command, args []string) {
		if !c.HasParent() {
			fmt.Fprint(c.OutOrStdout(), usage)
			return
		}
		if c.Name() == "admin" {
			fmt.Fprint(c.OutOrStdout(), adminUsage(nil))
			return
		}
		// By the command's path below the root ("mail inbox"): a
		// sub-verb that only shares a verb's name (memory privacy status)
		// gets cobra's help, not that verb's.
		if u, ok := verbUsage[strings.TrimPrefix(c.CommandPath(), c.Root().Name()+" ")]; ok {
			fmt.Fprint(c.OutOrStdout(), u)
			return
		}
		defaultHelp(c, args)
	})
	cmd.AddCommand(newVersionCmd(), newMemoryCmd(), newTemplateCmd(), newMailCmd(), newPaletteCmd(), newSpawnCmd(), newAddCmd(), newInterruptCmd(), newKillCmd(), newWaitCmd())
	cmd.AddCommand(newDoctorCmd(), newCopyCmd(), newGrepCmd(), newErrorsCmd(), newGetAllSessionTextCmd(), newSwitchAgentCmd(), newExtractCmd(), newListCmd(), newSaveCmd(), newZoomCmd(), newViewCmd(), newDiffCmd())
	for _, v := range legacyVerbs() {
		cmd.AddCommand(v.command(o, stdout, stderr))
	}
	return &root{cmd: cmd, opts: o}
}

// execute runs args and returns the exit code: ntm's ExitCode, and ntm's
// Execute for an error that no verb has written (root.go:3364-3383):
// "Error: <msg>" on stderr, or under --json the robot error envelope on
// stdout.
func (r *root) execute(args []string, stdout, stderr io.Writer) int {
	defer func() {
		for i := len(r.opts.restore) - 1; i >= 0; i-- {
			r.opts.restore[i]()
		}
	}()
	if args == nil {
		args = []string{} // cobra reads os.Args when args is nil
	}
	r.opts.args = args
	r.cmd.SetArgs(args)
	err := r.cmd.Execute()
	if err == nil {
		return exitOK
	}
	var ve verbExit
	if errors.As(err, &ve) {
		return ve.code
	}
	if r.opts.json || jsonInArgs(args) {
		code, hint := rootErrorCode(err, r.cmd.Flags())
		exit := exitError
		var nb notBuiltError
		if errors.As(err, &nb) {
			// ntm's encoder exits 2 for NOT_IMPLEMENTED (ExitCodeForResponse).
			code, hint, exit = robot.ErrCodeNotImplemented, "its row in compat/ntm-contract.md names the bead", robotNotImplemented
		}
		out := robot.NewError(code, err.Error(), hint, "json", time.Now())
		out.Meta = &robot.ResponseMeta{ExitCode: exit, Command: "cli"}
		return robotWrite(stdout, out, exit)
	}
	fmt.Fprintln(stderr, "Error:", err)
	return exitError
}

// jsonInArgs reports whether args ask for --json before any "--": a flag
// error can come before cobra sets the flag.
func jsonInArgs(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--json" {
			return true
		}
		if v, ok := strings.CutPrefix(a, "--json="); ok {
			b, err := strconv.ParseBool(v)
			return err == nil && b
		}
	}
	return false
}

// verbRun is a verb on the shared parser in the shape of a legacy verb's
// run function, for the callers that run a verb in-process (the UI's
// views): it runs "nhm <name> args…" through a fresh root.
func verbRun(name string) func(args []string, stdout, stderr io.Writer) int {
	return func(args []string, stdout, stderr io.Writer) int {
		return Run(append([]string{name}, args...), stdout, stderr)
	}
}

// wantJSON reports whether a verb on the shared parser runs with --json.
func wantJSON(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("json")
	return v
}
