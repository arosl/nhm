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
// Only a kind named after an agent CLI that herdr starts can resume, with
// the CLI's own resume flags declared:
//
//	[kinds.gemini]
//	resume_argv = ["gemini", "--resume", "{session}"]
//	resume_flags = ["--resume"]
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
	ResumeFlags    []string   `toml:"resume_flags"`
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
		ResumeArgv: c.ResumeArgv, ResumeFlags: c.ResumeFlags, NewSession: c.NewSession, ExitCommand: c.ExitCommand, InterruptKeys: c.InterruptKeys,
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
	if err := checkResumeArgv(name, a.ResumeArgv, a.LaunchArgs, a.ResumeFlags); err != nil {
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
	a.InterruptKeys = slices.Clone(a.InterruptKeys)
	for i, k := range a.InterruptKeys {
		if oldCtrlKey.MatchString(k) {
			k = "ctrl+" + k[len("C-"):]
		}
		if !KeyAllowed(k) {
			return Adapter{}, fmt.Errorf("kinds.%s.interrupt_keys: %q is not a herdr key name nhm allows", name, k)
		}
		a.InterruptKeys[i] = k
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

// resumeFlagsByKind are the resume flags each CLI documents for a session
// id, checked against its docs (acfs-9hkh.79): claude --resume/-r, codex's
// resume subcommand, gemini --resume, opencode --session/-s, copilot
// --resume, cursor --resume. {session} after any other flag could name a
// file the CLI loads (--settings, --mcp-config, --session-file), and
// SessionPattern accepts "evil.json"; a shape rule let any short flag
// through. A kind without an entry has no resume until its flags are
// checked and added here.
var resumeFlagsByKind = map[string][]string{
	"claude":   {"--resume", "-r"},
	"codex":    {"resume"},
	"gemini":   {"--resume"},
	"opencode": {"--session", "-s"},
	"copilot":  {"--resume"},
	"cursor":   {"--resume"},
}

// checkResumeArgv: the program is the kind itself, by its bare name, and
// one of the agent CLIs herdr starts; then arguments without control
// characters and without "--" (nhm's flags would become prompt text).
// {session} is a whole argument exactly once, right after one of the
// kind's declared resume flags (--resume ID, codex's resume ID), and
// only the kind's own launch_args come before that flag: an argument
// between them could take the flag as its value (--mcp-config --resume
// ID). The session id comes from a hook and must stay one argument that
// the agent CLI reads as a session id.
func checkResumeArgv(kind string, argv, launchArgs, flags []string) error {
	if argv == nil {
		return nil
	}
	if len(argv) < 2 {
		return fmt.Errorf("give the program and its arguments")
	}
	if len(flags) == 0 {
		return fmt.Errorf("resume_flags: name the CLI's own resume flag")
	}
	known, ok := resumeFlagsByKind[kind]
	if !ok {
		return fmt.Errorf("resume_flags: nhm knows no resume flag of %s yet", kind)
	}
	for _, f := range flags {
		if !slices.Contains(known, f) {
			return fmt.Errorf("resume_flags: %q is not a resume flag of %s (%v)", f, kind, known)
		}
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
			// argv[i] is prev, the flag; argv[1:i] come before it.
			if i == 0 || !slices.Contains(flags, prev) {
				return fmt.Errorf("{session} must follow one of resume_flags %v", flags)
			}
			for _, before := range argv[1:i] {
				if !slices.Contains(launchArgs, before) {
					return fmt.Errorf("%q stands before the resume flag: only launch_args may", before)
				}
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
// canonical names (herdr agent send-keys --help: "esc") that herdr 0.9.3
// takes (agent.send_keys and pane.send_keys, probed for nhm-1b2). herdr
// refuses home, end, pageup, pagedown and delete. Ctrl chords are
// herdr's ctrl+a … ctrl+z.
var keyNames = map[string]bool{"esc": true, "escape": true, "enter": true, "tab": true, "backspace": true,
	"space": true, "up": true, "down": true, "left": true, "right": true}

var ctrlKey = regexp.MustCompile(`^ctrl\+[a-z]$`)

// oldCtrlKey is nhm's old spelling of a ctrl chord, C-a … C-z. herdr
// refuses it (invalid_key), but for C-c, so a config kind's key in it is
// read as ctrl+a … ctrl+z.
var oldCtrlKey = regexp.MustCompile(`^C-[a-z]$`)

// KeyAllowed reports a herdr key name that nhm sends.
func KeyAllowed(k string) bool { return keyNames[k] || ctrlKey.MatchString(k) }
