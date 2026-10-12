package kinds

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"
)

// kindConfig is one [kinds.<name>] table of ~/.config/nhm/config.toml:
//
//	[kinds.mycli]
//	ntm_type = "mycli"
//	flags = ["my"]
//	launch_args = ["--quiet"]
//	model_flag = "--model"
//	resume_argv = ["mycli", "--resume", "{session}"]
//	new_session = "/new"
//	exit_command = "/quit"
//	interrupt_keys = ["esc"]
//	quota_pool = "mycli"
//	limit = "usage limit reached"
//	code_review = true
//	guard_settings = ".mycli/hooks.json"
//	guard_supported = true
//	cannot = ["its usage"]
//	[kinds.mycli.hooks]
//	session_start = true
//	[kinds.mycli.transcript]
//	dir = ".mycli/sessions"
//	format = "jsonl"
//
// A config kind has no continuation rule: compaction and resume never
// continue its bindings (that needs code). It cannot replace a built-in.
type kindConfig struct {
	NTMType        string     `toml:"ntm_type"`
	Flags          []string   `toml:"flags"`
	LaunchArgs     []string   `toml:"launch_args"`
	ModelFlag      string     `toml:"model_flag"`
	EffortArgs     []string   `toml:"effort_args"`
	DefaultEffort  string     `toml:"default_effort"`
	ResumeArgv     []string   `toml:"resume_argv"`
	NewSession     string     `toml:"new_session"`
	ExitCommand    string     `toml:"exit_command"`
	InterruptKeys  []string   `toml:"interrupt_keys"`
	QuotaPool      string     `toml:"quota_pool"`
	QuotaSource    string     `toml:"quota_source"`
	Hooks          Hooks      `toml:"hooks"`
	Limit          string     `toml:"limit"`
	Transcript     Transcript `toml:"transcript"`
	CodeReview     *bool      `toml:"code_review"`
	GuardSettings  string     `toml:"guard_settings"`
	GuardSupported *bool      `toml:"guard_supported"`
	Cannot         []string   `toml:"cannot"`
}

type configFile struct {
	Kinds map[string]kindConfig `toml:"kinds"`
}

// LoadConfig registers the [kinds.*] tables of a config file in r. A
// missing file adds nothing. Other sections belong to other parts of nhm;
// an unknown key inside a kind's table is an error, so a typo never
// passes as a kind without that fact.
func (r *Registry) LoadConfig(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var f configFile
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var unknown []string
	for _, k := range md.Undecoded() {
		if len(k) > 0 && k[0] == "kinds" {
			unknown = append(unknown, k.String())
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("%s: unknown keys: %s", path, strings.Join(unknown, ", "))
	}
	names := make([]string, 0, len(f.Kinds))
	for n := range f.Kinds {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		a, err := f.Kinds[n].adapter(n)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if err := r.Register(a); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
}

func (c kindConfig) adapter(name string) (Adapter, error) {
	a := Adapter{Name: name, NTMType: c.NTMType, Flags: c.Flags, LaunchArgs: c.LaunchArgs, ModelFlag: c.ModelFlag,
		ResumeArgv: c.ResumeArgv, NewSession: c.NewSession, ExitCommand: c.ExitCommand, InterruptKeys: c.InterruptKeys,
		QuotaPool: c.QuotaPool, QuotaSource: c.QuotaSource, Hooks: c.Hooks, Transcript: c.Transcript,
		GuardSettings: c.GuardSettings, CodeReview: true, GuardSupported: true, Cannot: c.Cannot,
		EffortArgs: c.EffortArgs, DefaultEffort: c.DefaultEffort}
	if c.CodeReview != nil {
		a.CodeReview = *c.CodeReview
	}
	if c.GuardSupported != nil {
		a.GuardSupported = *c.GuardSupported
	}
	if c.Limit != "" {
		re, err := regexp.Compile("(?i)" + c.Limit)
		if err != nil {
			return Adapter{}, fmt.Errorf("kinds.%s.limit: %w", name, err)
		}
		a.Limit = re
	}
	if err := checkResumeArgv(name, a.ResumeArgv, a.LaunchArgs); err != nil {
		return Adapter{}, fmt.Errorf("kinds.%s.resume_argv: %w", name, err)
	}
	if err := checkEffortArgs(a.EffortArgs, a.DefaultEffort); err != nil {
		return Adapter{}, fmt.Errorf("kinds.%s.effort_args: %w", name, err)
	}
	if strings.Contains(a.GuardSettings, "..") || strings.HasPrefix(a.GuardSettings, "/") {
		return Adapter{}, fmt.Errorf("kinds.%s.guard_settings: a path under the home directory", name)
	}
	// Fields that reach a pane (argv, typed text, keys) take no control
	// characters: a newline in a typed command submits it early.
	for field, values := range map[string][]string{
		"launch_args": a.LaunchArgs, "model_flag": {a.ModelFlag}, "new_session": {a.NewSession},
		"exit_command": {a.ExitCommand}, "flags": a.Flags, "ntm_type": {a.NTMType}, "quota_pool": {a.QuotaPool},
	} {
		for _, v := range values {
			if hasControl(v) {
				return Adapter{}, fmt.Errorf("kinds.%s.%s: %q holds a control character", name, field, v)
			}
		}
	}
	for _, v := range a.LaunchArgs {
		if v == "" {
			return Adapter{}, fmt.Errorf("kinds.%s.launch_args: an empty argument", name)
		}
	}
	if a.ModelFlag != "" && !strings.HasPrefix(a.ModelFlag, "-") {
		return Adapter{}, fmt.Errorf("kinds.%s.model_flag: %q is not a flag", name, a.ModelFlag)
	}
	for _, k := range a.InterruptKeys {
		if !KeyAllowed(k) {
			return Adapter{}, fmt.Errorf("kinds.%s.interrupt_keys: %q is not a herdr key name nhm allows", name, k)
		}
	}
	return a, nil
}

// herdrAgentKinds are the agent CLIs herdr 0.9.3 starts (`herdr agent
// start --kind`; herdr resolves each one's own executable). A resume argv
// runs one of these and nothing else: an allowlist, so no shell, launcher
// or interpreter can take the session id.
var herdrAgentKinds = map[string]bool{"pi": true, "claude": true, "codex": true, "gemini": true, "cursor": true,
	"devin": true, "agy": true, "cline": true, "omp": true, "mastracode": true, "opencode": true, "copilot": true,
	"kimi": true, "kiro": true, "droid": true, "amp": true, "grok": true, "hermes": true, "kilo": true,
	"qodercli": true, "qwen": true, "letta": true, "maki": true, "muse": true}

// resumeFlags are the flags whose value an agent CLI reads as a session
// to resume. {session} after any other flag could name a file the CLI
// loads (--settings, --mcp-config, -e), and SessionPattern accepts
// "evil.json".
var resumeFlags = map[string]bool{"--resume": true, "-r": true}

// checkResumeArgv: the program is the kind itself, by its bare name, and
// one of the agent CLIs herdr starts; then arguments without control
// characters and without "--" (nhm's flags would become prompt text).
// {session} is a whole argument exactly once: the value of a resume flag
// (--resume ID, -r ID), or right after the resume subcommand, which only
// the kind's own launch_args may come before (codex --no-daemon resume
// ID). The session id comes from a hook and must stay one argument that
// the agent CLI reads as a session id.
func checkResumeArgv(kind string, argv, launchArgs []string) error {
	if argv == nil {
		return nil
	}
	if len(argv) < 2 {
		return fmt.Errorf("give the program and its arguments")
	}
	prog := argv[0]
	switch {
	case strings.ContainsAny(prog, `/\`):
		return fmt.Errorf("the program %q must be a bare name, not a path", prog)
	case prog != kind:
		return fmt.Errorf("the program %q must be the kind itself (%s)", prog, kind)
	case !herdrAgentKinds[prog]:
		return fmt.Errorf("%q is not an agent CLI that herdr starts", prog)
	}
	whole := 0
	for i, a := range argv[1:] {
		prev := argv[i]
		switch {
		case hasControl(a):
			return fmt.Errorf("%q holds a control character", a)
		case a == "--":
			return fmt.Errorf(`"--" would make nhm's flags prompt text`)
		case a == "{session}":
			// argv[i] is prev; argv[0] is the kind, never "resume".
			sub := prev == "resume"
			if sub {
				for _, before := range argv[1:i] {
					sub = sub && slices.Contains(launchArgs, before)
				}
			}
			if !resumeFlags[prev] && !sub {
				return fmt.Errorf("{session} must be the value of --resume or -r, or follow the resume subcommand after only the kind's launch_args")
			}
			whole++
		case strings.Contains(a, "{session}"):
			return fmt.Errorf("%q: {session} must be a whole argument", a)
		}
	}
	if whole != 1 {
		return fmt.Errorf("{session} must appear exactly once")
	}
	return nil
}

// checkEffortArgs: no control characters; {effort} at least once. The
// effort value itself is a plain word (EffortPattern).
func checkEffortArgs(args []string, def string) error {
	if args == nil {
		if def != "" {
			return fmt.Errorf("default_effort without effort_args")
		}
		return nil
	}
	found := false
	for _, a := range args {
		if hasControl(a) || a == "" {
			return fmt.Errorf("%q is empty or holds a control character", a)
		}
		found = found || strings.Contains(a, "{effort}")
	}
	if !found {
		return fmt.Errorf("no {effort}")
	}
	if def != "" && !EffortPattern.MatchString(def) {
		return fmt.Errorf("default_effort %q is not a plain word", def)
	}
	return nil
}

func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

// keyNames are the herdr key names a config kind may use: herdr's
// canonical names (herdr agent send-keys --help: "esc"). Ctrl chords are
// C-a … C-z.
var keyNames = map[string]bool{"esc": true, "escape": true, "enter": true, "tab": true, "backspace": true,
	"space": true, "up": true, "down": true, "left": true, "right": true, "home": true, "end": true,
	"pageup": true, "pagedown": true, "delete": true}

var ctrlKey = regexp.MustCompile(`^C-[a-z]$`)

// KeyAllowed reports a herdr key name that nhm sends.
func KeyAllowed(k string) bool { return keyNames[k] || ctrlKey.MatchString(k) }
