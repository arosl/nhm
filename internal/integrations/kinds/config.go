package kinds

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"

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
	if a.ResumeArgv != nil && !strings.Contains(strings.Join(a.ResumeArgv, " "), "{session}") {
		return Adapter{}, fmt.Errorf("kinds.%s.resume_argv: no {session}", name)
	}
	if strings.Contains(a.GuardSettings, "..") || strings.HasPrefix(a.GuardSettings, "/") {
		return Adapter{}, fmt.Errorf("kinds.%s.guard_settings: a path under the home directory", name)
	}
	return a, nil
}
