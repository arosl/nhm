package kinds

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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
		GuardSettings: c.GuardSettings, CodeReview: true, GuardSupported: true, Cannot: c.Cannot}
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
	if err := checkResumeArgv(a.ResumeArgv); err != nil {
		return Adapter{}, fmt.Errorf("kinds.%s.resume_argv: %w", name, err)
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

// shells may not be a resume argv's program: the session id would become
// shell text.
var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "fish": true,
	"csh": true, "tcsh": true, "env": true, "busybox": true, "nu": true, "pwsh": true, "xargs": true}

// checkResumeArgv: a fixed program that is not a shell, then arguments;
// {session} only as a whole element, at least once; no control
// characters. The session id comes from a hook and must stay one argument.
func checkResumeArgv(argv []string) error {
	if argv == nil {
		return nil
	}
	if len(argv) < 2 {
		return fmt.Errorf("give the program and its arguments")
	}
	prog := argv[0]
	if prog == "" || strings.ContainsAny(prog, "{}") || hasControl(prog) {
		return fmt.Errorf("the program %q must be a fixed name", prog)
	}
	if shells[filepath.Base(prog)] {
		return fmt.Errorf("the program %q is a shell or a launcher", prog)
	}
	whole := 0
	for _, a := range argv[1:] {
		switch {
		case a == "{session}":
			whole++
		case strings.Contains(a, "{session}"):
			return fmt.Errorf("%q: {session} must be a whole argument", a)
		case hasControl(a):
			return fmt.Errorf("%q holds a control character", a)
		}
	}
	if whole == 0 {
		return fmt.Errorf("no {session} argument")
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
