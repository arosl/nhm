package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/arosl/nhm/internal/core/ensemble"
)

// nhm modes (plan-v8 2.11): ntm's reasoning-mode catalog, with ntm's
// grammar. --json gives nhm's envelope; --format json gives the bare data,
// as ntm prints it.

const modesUsage = `Usage: nhm modes list [--tier core|advanced|experimental] [--all] [--category NAME] [--format text|json] [--json]
       nhm modes explain <mode> [--format text|json] [--json]

The reasoning modes of ensemble presets: ntm's catalog of 80 modes in
twelve categories (A-L). list shows the core tier unless --tier or --all
says otherwise. explain takes a mode id (deductive) or its code (A1).
`

func runModes(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, modesUsage)
		return exitRefused
	}
	switch args[0] {
	case "list", "ls":
		return runModesList(args[1:], stdout, stderr)
	case "explain", "show":
		return runModesExplain(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, modesUsage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "nhm modes: unknown verb %q\n%s", args[0], modesUsage)
		return exitRefused
	}
}

// formatFlags are ntm's --format and nhm's --json.
type formatFlags struct {
	format *string
	asJSON *bool
}

func addFormatFlags(fs *flag.FlagSet, def string) formatFlags {
	f := formatFlags{format: fs.String("format", def, "output format: text or json")}
	fs.StringVar(f.format, "f", def, "output format (shorthand)")
	f.asJSON = fs.Bool("json", false, "nhm's JSON envelope")
	return f
}

// bareJSON reports whether --format json asks for the data alone.
func (f formatFlags) bareJSON() bool { return !*f.asJSON && *f.format == "json" }

func (f formatFlags) check() error {
	switch *f.format {
	case "text", "table", "json":
		return nil
	}
	return fmt.Errorf("--format %q: give text or json", *f.format)
}

func runModesList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("modes list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, modesUsage) }
	ff := addFormatFlags(fs, "text")
	tier := fs.String("tier", "", "core, advanced or experimental")
	fs.StringVar(tier, "t", "", "tier (shorthand)")
	all := fs.Bool("all", false, "every tier")
	fs.BoolVar(all, "a", false, "every tier (shorthand)")
	category := fs.String("category", "", "a category, such as Formal or Causal")
	fs.StringVar(category, "c", "", "category (shorthand)")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	env := envelope{Command: "modes list"}
	if len(pos) > 0 {
		fmt.Fprint(stderr, modesUsage)
		return exitRefused
	}
	if err := ff.check(); err != nil {
		return output(stdout, stderr, *ff.asJSON, env, err, exitRefused, nil)
	}
	t := ensemble.ModeTier(strings.ToLower(*tier))
	switch {
	case *all:
		t = ""
	case t == "":
		t = ensemble.TierCore
	case !t.IsValid():
		return output(stdout, stderr, *ff.asJSON, env, fmt.Errorf("--tier %q: give core, advanced or experimental", *tier), exitRefused, nil)
	}
	c, err := ensemble.Builtin()
	if err != nil {
		return output(stdout, stderr, *ff.asJSON, env, err, exitError, nil)
	}
	modes := []ensemble.ReasoningMode{}
	for _, m := range c.Modes(t) {
		if *category == "" || strings.EqualFold(string(m.Category), *category) {
			modes = append(modes, m)
		}
	}
	if ff.bareJSON() {
		return printJSON(stdout, stderr, "modes list", modes)
	}
	env.Result = modes
	return output(stdout, stderr, *ff.asJSON, env, nil, exitOK, func(w io.Writer) {
		for _, m := range modes {
			fmt.Fprintf(w, "%-4s %-28s %-12s %-9s %s\n", m.Code, m.ID, m.Category, m.Tier, m.ShortDesc)
		}
		fmt.Fprintf(w, "%d mode(s)\n", len(modes))
	})
}

func runModesExplain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("modes explain", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, modesUsage) }
	ff := addFormatFlags(fs, "text")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	env := envelope{Command: "modes explain"}
	if len(pos) != 1 {
		fmt.Fprint(stderr, modesUsage)
		return exitRefused
	}
	if err := ff.check(); err != nil {
		return output(stdout, stderr, *ff.asJSON, env, err, exitRefused, nil)
	}
	c, err := ensemble.Builtin()
	if err != nil {
		return output(stdout, stderr, *ff.asJSON, env, err, exitError, nil)
	}
	m, err := c.Mode(pos[0])
	if err != nil {
		return output(stdout, stderr, *ff.asJSON, env, err, exitRefused, nil)
	}
	if ff.bareJSON() {
		return printJSON(stdout, stderr, "modes explain", m)
	}
	env.Result = m
	return output(stdout, stderr, *ff.asJSON, env, nil, exitOK, func(w io.Writer) { printModeCard(w, m) })
}

// printJSON writes v as indented JSON, without nhm's envelope: ntm's
// --format json.
func printJSON(stdout, stderr io.Writer, command string, v any) int {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(stderr, "nhm %s: %v\n", command, err)
		return exitError
	}
	return exitOK
}

func printModeCard(w io.Writer, m ensemble.ReasoningMode) {
	fmt.Fprintf(w, "%s (%s) %s\n", m.Name, m.Code, m.ID)
	fmt.Fprintf(w, "  category %s, tier %s\n", m.Category, m.Tier)
	fmt.Fprintf(w, "  %s\n\n", m.ShortDesc)
	fmt.Fprintf(w, "%s\n\n", m.Description)
	fmt.Fprintf(w, "Outputs: %s\n", m.Outputs)
	if len(m.BestFor) > 0 {
		fmt.Fprintf(w, "Best for: %s\n", strings.Join(m.BestFor, "; "))
	}
	if len(m.FailureModes) > 0 {
		fmt.Fprintf(w, "Failure modes: %s\n", strings.Join(m.FailureModes, "; "))
	}
	if m.Differentiator != "" {
		fmt.Fprintf(w, "Differentiator: %s\n", m.Differentiator)
	}
}
