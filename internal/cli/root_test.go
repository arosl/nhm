package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/arosl/nhm/internal/robot"
)

// runWith runs args on a root that also holds extra commands, as a verb
// on the shared parser would be.
func runWith(extra func(o *rootOptions, stdout, stderr io.Writer) *cobra.Command, args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	r := newRoot(&out, &errOut)
	r.cmd.AddCommand(extra(r.opts, &out, &errOut))
	code := r.execute(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// shortFlagsCmd has short flags of each kind, as ntm's verbs do (grep -iv,
// -C3).
type shortFlagsCmd struct {
	ignoreCase, invert bool
	context            int
	session            string
	args               []string
}

func (s *shortFlagsCmd) command(*rootOptions, io.Writer, io.Writer) *cobra.Command {
	cmd := &cobra.Command{Use: "probe", RunE: func(cmd *cobra.Command, args []string) error {
		s.args = args
		return nil
	}}
	cmd.Flags().BoolVarP(&s.ignoreCase, "ignore-case", "i", false, "")
	cmd.Flags().BoolVarP(&s.invert, "invert-match", "v", false, "")
	cmd.Flags().IntVarP(&s.context, "context", "C", 0, "")
	cmd.Flags().StringVar(&s.session, "session", "", "")
	return cmd
}

func TestCombinedShortFlags(t *testing.T) {
	for _, c := range []struct {
		args            []string
		ignore, invert  bool
		context         int
		positional      []string
		wantCode        int
		wantErrContains string
	}{
		{args: []string{"probe", "-iv", "x"}, ignore: true, invert: true, positional: []string{"x"}},
		{args: []string{"probe", "x", "-vi"}, ignore: true, invert: true, positional: []string{"x"}},
		{args: []string{"probe", "-ivC3"}, ignore: true, invert: true, context: 3},
		{args: []string{"probe", "-q"}, wantCode: 1, wantErrContains: "Error: unknown shorthand flag: 'q' in -q"},
	} {
		s := &shortFlagsCmd{}
		code, _, errOut := runWith(s.command, c.args...)
		if code != c.wantCode || (c.wantErrContains != "" && !strings.Contains(errOut, c.wantErrContains)) {
			t.Errorf("%v: exit %d, stderr %q", c.args, code, errOut)
			continue
		}
		if c.wantCode != 0 {
			continue
		}
		if s.ignoreCase != c.ignore || s.invert != c.invert || s.context != c.context || !slices.Equal(s.args, c.positional) {
			t.Errorf("%v: -i %v -v %v -C %d args %q", c.args, s.ignoreCase, s.invert, s.context, s.args)
		}
	}
}

func TestShortFlagWithValue(t *testing.T) {
	for _, args := range [][]string{{"probe", "-C3"}, {"probe", "-C", "3"}, {"probe", "--context=3"}, {"probe", "--context", "3"}} {
		s := &shortFlagsCmd{}
		if code, _, errOut := runWith(s.command, args...); code != 0 || s.context != 3 {
			t.Errorf("%v: exit %d, -C %d, stderr %q", args, code, s.context, errOut)
		}
	}
	s := &shortFlagsCmd{}
	if code, _, errOut := runWith(s.command, "probe", "-C"); code != 1 || errOut != "Error: flag needs an argument: 'C' in -C\n" {
		t.Errorf("-C without a value: exit %d, stderr %q", code, errOut)
	}
}

// ntm's flag error function: a typo of the verb's own flag gets "did you
// mean", a known mis-guess gets the curated hint.
func TestFlagErrorHints(t *testing.T) {
	s := &shortFlagsCmd{}
	code, _, errOut := runWith(s.command, "probe", "--sesion", "x")
	if code != 1 || errOut != "Error: unknown flag: --sesion (did you mean --session?)\n" {
		t.Errorf("probe --sesion: exit %d, stderr %q", code, errOut)
	}
	code, _, errOut = run("version", "--sesion")
	if code != 1 || errOut != "Error: unknown flag: --sesion\nHint: "+flagMisGuessHints["session"]+"\n" {
		t.Errorf("version --sesion: exit %d, stderr %q", code, errOut)
	}
}

// ntm's root flags reach every verb, before or after it: --json on a verb
// on the shared parser (version) and on a pass-through verb.
func TestRootFlagsBeforeTheVerb(t *testing.T) {
	for _, args := range [][]string{{"--json", "version"}, {"--no-color", "--json", "version", "-s"}} {
		code, out, errOut := run(args...)
		var info VersionResponse
		if code != 0 || errOut != "" || json.Unmarshal([]byte(out), &info) != nil || info.Version != Version {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, code, out, errOut)
		}
	}
}

func TestRootFlagsAfterTheVerb(t *testing.T) {
	for _, args := range [][]string{{"version", "--json"}, {"version", "-s", "--json", "--no-color"}} {
		code, out, errOut := run(args...)
		var info VersionResponse
		if code != 0 || errOut != "" || json.Unmarshal([]byte(out), &info) != nil || info.Version != Version {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, code, out, errOut)
		}
	}
}

func TestRootJSONBeforeTheVerb(t *testing.T) {
	for _, args := range [][]string{{"--json", "compatibility", "status"}, {"--no-color", "compatibility", "status", "--json"}} {
		code, out, errOut := run(args...)
		var env struct {
			Schema  string `json:"schema"`
			Command string `json:"command"`
		}
		if code != 0 || errOut != "" || json.Unmarshal([]byte(out), &env) != nil || env.Schema != "nhm.v1" {
			t.Errorf("%v: exit %d, stdout %.200q, stderr %q", args, code, out, errOut)
		}
	}
}

// A root flag whose effect nhm does not have yet is refused, before or
// after any verb, never run as if it were not there.
func TestRootFlagNotBuiltIsRefused(t *testing.T) {
	for _, c := range []struct {
		flag       []string
		name, bead string
	}{
		{[]string{"--redact", "off"}, "redact", "nhm-2p6.20"},
		{[]string{"--allow-secret"}, "allow-secret", "nhm-2p6.20"},
	} {
		want := "Error: --" + c.name + " is not in this nhm yet (deferred: " + c.bead + ")\n"
		for _, args := range [][]string{
			append(append([]string{}, c.flag...), "version"),
			append([]string{"version"}, c.flag...),
			append(append([]string{}, c.flag...), "compatibility", "status"),
			append([]string{"compatibility", "status"}, c.flag...),
		} {
			code, out, errOut := run(args...)
			if code != 1 || out != "" || errOut != want {
				t.Errorf("%v: exit %d, stdout %.200q, stderr %q", args, code, out, errOut)
			}
		}
	}
	// Under --json: ntm's error envelope on stdout, NOT_IMPLEMENTED, exit 2
	// as ntm's encoder gives it; nothing on stderr.
	code, out, errOut := run("--json", "--redact", "off", "compatibility", "status")
	var resp robot.RobotResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil || code != 2 || errOut != "" ||
		resp.Success || resp.ErrorCode != robot.ErrCodeNotImplemented || resp.Meta == nil || resp.Meta.ExitCode != 2 {
		t.Errorf("--json --redact: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

// Under --json an error that no verb wrote is ntm's robot error envelope
// on stdout, with nothing on stderr (Execute, root.go:3364-3383).
func TestRootErrorUnderJSON(t *testing.T) {
	for _, args := range [][]string{{"--json", "frobnicate"}, {"version", "--yaml", "--json"}} {
		code, out, errOut := run(args...)
		var resp robot.RobotResponse
		if err := json.Unmarshal([]byte(out), &resp); err != nil || code != 1 || errOut != "" {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, code, out, errOut)
			continue
		}
		if resp.Success || resp.ErrorCode != robot.ErrCodeInvalidFlag || resp.Meta == nil ||
			resp.Meta.ExitCode != 1 || resp.Meta.Command != "cli" || resp.Hint == "" {
			t.Errorf("%v: %+v", args, resp)
		}
	}
}

// A pass-through verb gets its arguments as given, root flags taken out,
// except --json and the root flags its own parser defines.
func TestPassThroughVerbArguments(t *testing.T) {
	var got []string
	var noColor string
	probe := func(own []string) func(o *rootOptions, stdout, stderr io.Writer) *cobra.Command {
		return func(o *rootOptions, stdout, stderr io.Writer) *cobra.Command {
			return legacyVerb{name: "probe", run: func(args []string, _, _ io.Writer) int {
				got, noColor = args, os.Getenv("NTM_NO_COLOR")
				return exitOK
			}, own: own}.command(o, stdout, stderr)
		}
	}
	t.Setenv("NTM_NO_COLOR", "")
	for _, c := range []struct {
		own, args, want []string
		noColor         string
	}{
		{nil, []string{"--json", "probe", "a"}, []string{"a", "--json"}, ""},
		{nil, []string{"probe", "a", "--no-color", "--json"}, []string{"a", "--json"}, "1"},
		{nil, []string{"--no-color", "probe", "a", "--", "--no-color"}, []string{"a", "--", "--no-color"}, "1"},
		{nil, []string{"--json", "probe", "a", "--", "b"}, []string{"a", "--json", "--", "b"}, ""},
		{[]string{"config"}, []string{"--config", "f", "probe", "a"}, []string{"a", "--config", "f"}, ""},
		{[]string{"config"}, []string{"probe", "--config=f"}, []string{"--config=f"}, ""},
		{[]string{"no-color"}, []string{"probe", "--no-color"}, []string{"--no-color"}, ""},
	} {
		got, noColor = nil, "unset"
		code, _, errOut := runWith(probe(c.own), c.args...)
		if code != 0 || !slices.Equal(got, c.want) || noColor != c.noColor {
			t.Errorf("own %v, %v: exit %d, args %q, NTM_NO_COLOR %q, stderr %q", c.own, c.args, code, got, noColor, errOut)
		}
		if v := os.Getenv("NTM_NO_COLOR"); v != "" {
			t.Errorf("%v: NTM_NO_COLOR outlived the run: %q", c.args, v)
		}
	}
	// A legacy verb's code passes through as it is (nhm-2p6.1.4 maps it).
	fail := func(o *rootOptions, stdout, stderr io.Writer) *cobra.Command {
		return legacyVerb{name: "probe", run: func([]string, io.Writer, io.Writer) int { return 3 }}.command(o, stdout, stderr)
	}
	if code, out, errOut := runWith(fail, "probe"); code != 3 || out != "" || errOut != "" {
		t.Errorf("a legacy code: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

// --config F names nhm's config file for the call, as NHM_CONFIG, made
// absolute (the operator's ruling, 2026-10-24), on a pass-through verb
// and on a verb on the shared parser; an empty --config is none.
func TestConfigFlagSetsNHMConfig(t *testing.T) {
	t.Setenv("NHM_CONFIG", "")
	dir := t.TempDir()
	t.Chdir(dir)
	var seen string
	legacy := func(o *rootOptions, stdout, stderr io.Writer) *cobra.Command {
		return legacyVerb{name: "probe", run: func([]string, io.Writer, io.Writer) int {
			seen = os.Getenv("NHM_CONFIG")
			return exitOK
		}}.command(o, stdout, stderr)
	}
	shared := func(*rootOptions, io.Writer, io.Writer) *cobra.Command {
		return &cobra.Command{Use: "probe", RunE: func(*cobra.Command, []string) error {
			seen = os.Getenv("NHM_CONFIG")
			return nil
		}}
	}
	want := filepath.Join(dir, "my.toml")
	for name, cmd := range map[string]func(*rootOptions, io.Writer, io.Writer) *cobra.Command{"legacy": legacy, "shared": shared} {
		for _, c := range []struct {
			args []string
			want string
		}{
			{[]string{"--config", "my.toml", "probe"}, want},
			{[]string{"probe", "--config=" + want}, want},
			{[]string{"--config=", "probe"}, ""},
		} {
			seen = "unset"
			if code, _, errOut := runWith(cmd, c.args...); code != 0 || seen != c.want {
				t.Errorf("%s %v: exit %d, NHM_CONFIG %q, stderr %q", name, c.args, code, seen, errOut)
			}
			if v := os.Getenv("NHM_CONFIG"); v != "" {
				t.Errorf("%s %v: NHM_CONFIG outlived the run: %q", name, c.args, v)
			}
		}
	}
}

// --profile-startup prints ntm's profile on stderr after a command that
// succeeded: the cli_init span and the verb's; with --json, nothing (ntm's
// PersistentPostRun skips a machine call).
func TestProfileStartupPrintsTheProfile(t *testing.T) {
	for _, c := range []struct {
		args []string
		verb string
	}{
		{[]string{"--profile-startup", "version", "-s"}, "version"},
		{[]string{"compatibility", "status", "--profile-startup"}, "compatibility"},
	} {
		code, out, errOut := run(c.args...)
		if code != 0 || out == "" || !strings.HasPrefix(errOut, "=== NHM Performance Profile ===\n") ||
			!strings.Contains(errOut, "cli_init [startup]") || !strings.Contains(errOut, c.verb+" [command]") {
			t.Errorf("%v: exit %d, stdout %.60q, stderr %q", c.args, code, out, errOut)
		}
	}
	if code, out, errOut := run("--profile-startup", "--json", "version"); code != 0 || errOut != "" || !strings.HasPrefix(out, "{") {
		t.Errorf("--json: exit %d, stdout %.60q, stderr %q", code, out, errOut)
	}
	// No profile after a failed command.
	if code, _, errOut := run("--profile-startup", "version", "--yaml"); code != 1 || strings.Contains(errOut, "Performance Profile") {
		t.Errorf("failed: exit %d, stderr %q", code, errOut)
	}
}

func TestTakeRootFlagsErrors(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"a", "--ssh"}, "flag needs an argument: --ssh"},
		{[]string{"--no-color=maybe"}, `invalid argument "maybe" for "--no-color" flag: strconv.ParseBool: parsing "maybe": invalid syntax`},
	} {
		o := &rootOptions{set: map[string]bool{}}
		if _, err := takeRootFlags(c.args, o, nil); err == nil || err.Error() != c.want {
			t.Errorf("%v: %v", c.args, err)
		}
	}
}

// A help request on a pass-through verb prints its usage, with root flags
// before or after the verb, and runs nothing; nhm help <verb> prints it
// too.
func TestPassThroughHelp(t *testing.T) {
	for _, args := range [][]string{{"--json", "status", "--help"}, {"status", "--no-color", "-h"}, {"help", "status"}} {
		code, out, errOut := run(args...)
		if code != 0 || out != statusUsage || errOut != "" {
			t.Errorf("%v: exit %d, stdout %.80q, stderr %q", args, code, out, errOut)
		}
	}
	if code, out, _ := run("help", "version"); code != 0 || out != versionUsage {
		t.Errorf("help version: exit %d, stdout %q", code, out)
	}
	if code, out, _ := run("version", "--help"); code != 0 || out != versionUsage {
		t.Errorf("version --help: exit %d, stdout %q", code, out)
	}
}
