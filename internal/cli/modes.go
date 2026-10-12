package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/ensemble"
)

// nhm modes (plan-v8 2.11): ntm's reasoning-mode catalog, with ntm's
// grammar and output (internal/core/ensemble/output_ntm.go). --json
// gives nhm's envelope; --format json and yaml give ntm's types, and text
// ntm's table and card.

const modesUsage = `Usage: nhm modes list [--tier core|advanced|experimental] [--all] [--category NAME] [--format text|json|yaml] [--json]
       nhm modes explain <mode> [--format text|json|yaml] [--json]

The reasoning modes of ensemble presets: ntm's catalog of 80 modes in
twelve categories (A-L). list shows the core tier unless --tier or --all
says otherwise. explain takes a mode id (deductive) or its code (A1) and
prints ntm's explanation card.
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
	f := formatFlags{format: fs.String("format", def, "output format: text, json or yaml")}
	fs.StringVar(f.format, "f", def, "output format (shorthand)")
	f.asJSON = fs.Bool("json", false, "nhm's JSON envelope")
	return f
}

// ntmFormat is the format ntm's renderers take, lower-case; "" when
// --json asks for nhm's envelope instead.
func (f formatFlags) ntmFormat() string {
	if *f.asJSON {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(*f.format))
}

func (f formatFlags) check() error {
	switch strings.ToLower(strings.TrimSpace(*f.format)) {
	case "", "text", "table", "json", "yaml", "yml":
		return nil
	}
	return fmt.Errorf("--format %q: give text, json or yaml", *f.format)
}

// renderNTM prints a result with ntm's renderer, or as nhm's envelope
// under --json.
func renderNTM(stdout, stderr io.Writer, ff formatFlags, env envelope, result any, render func(io.Writer, string) error) int {
	if *ff.asJSON {
		env.Result = result
		return output(stdout, stderr, true, env, nil, exitOK, nil)
	}
	if err := render(stdout, ff.ntmFormat()); err != nil {
		fmt.Fprintf(stderr, "nhm %s: %v\n", env.Command, err)
		return exitError
	}
	return exitOK
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
	c, err := ensemble.Builtin()
	if err != nil {
		return output(stdout, stderr, *ff.asJSON, env, err, exitError, nil)
	}
	// As ntm: an unknown tier or category lists no modes.
	payload := ensemble.ModesList(c, *tier, *category, *all, time.Now())
	return renderNTM(stdout, stderr, ff, env, payload, func(w io.Writer, format string) error {
		return ensemble.RenderModesList(w, payload, format)
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
	card, err := c.ModeCard(pos[0])
	if err != nil {
		return output(stdout, stderr, *ff.asJSON, env, err, exitRefused, nil)
	}
	payload := ensemble.ModesExplainOutput{GeneratedAt: time.Now().UTC(), Card: card}
	return renderNTM(stdout, stderr, ff, env, payload, func(w io.Writer, format string) error {
		return ensemble.RenderModesExplain(w, payload, format)
	})
}
