// Package kinds is nhm's kind registry (plan-v8 2.14). Each agent kind is
// an adapter: how herdr starts it, its model flag, how its session
// resumes, its continuation rule, its keys, its quota pool, its hooks,
// its limit recogniser, its transcripts, whether it does code review, and
// what it cannot prove. nhm's core asks the registry and has no branch per
// kind of its own. ACFS may register a kind in config (LoadConfig).
//
// The facts of the built-ins come from ACFS's Bash (herdr_agents.sh,
// agent_quota.sh) and the experiments of plan-v8 section 6. A fact that
// nothing proved is left empty and named in Cannot.
package kinds

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/arosl/nhm/internal/integrations/kinds/claude"
	"github.com/arosl/nhm/internal/integrations/kinds/codex"
)

// Hooks are the hook events a kind runs in its pane.
type Hooks struct {
	SessionStart bool `json:"session_start" toml:"session_start"`
	Stop         bool `json:"stop" toml:"stop"`
	PostToolUse  bool `json:"post_tool_use" toml:"post_tool_use"`
	// Trust: a hook runs only after it is trusted (Codex, F.25).
	Trust bool `json:"trust" toml:"trust"`
}

// Transcript is where a kind writes its session transcripts.
type Transcript struct {
	// Dir is relative to the home directory.
	Dir    string `json:"dir,omitempty" toml:"dir"`
	Format string `json:"format,omitempty" toml:"format"`
}

// Event is a hook event that may say whether the occupant continues.
type Event struct {
	HookEvent  string
	Source     string
	SessionID  string
	Entrypoint string
}

// Decision is a continuation rule's answer: Verdict is Continue, Displace
// or Ignore.
type Decision struct {
	Verdict     string
	Reason      string
	Compacted   bool
	Resumed     bool
	TakeSession bool
}

// Continuation verdicts, the same for every kind.
const (
	Continue = "continue"
	Displace = "displace"
	Ignore   = "ignore"
)

// Adapter is one kind.
type Adapter struct {
	// Name is herdr's kind ("claude"), the value of agent.start's kind
	// and of AgentInfo.agent.
	Name string `json:"name"`
	// NTMType is ntm's agent type name in robot output.
	NTMType string `json:"ntm_type"`
	// Flags are the selector flags without dashes ("cc"); the name is
	// always a flag too.
	Flags []string `json:"flags,omitempty"`
	// LaunchArgs go to agent.start's args, before the model flag.
	LaunchArgs []string `json:"launch_args,omitempty"`
	// ModelFlag sets the model; "" means spawn cannot set one.
	ModelFlag string `json:"model_flag,omitempty"`
	// EffortArgs set the reasoning effort; "{effort}" is the value, and
	// DefaultEffort applies when the caller gives none (ntm's defaults).
	EffortArgs    []string `json:"effort_args,omitempty"`
	DefaultEffort string   `json:"default_effort,omitempty"`
	// SettingsFlag passes a settings JSON with the guard hooks (Claude's
	// --settings, as ntm does): the hooks then run whatever the user's own
	// settings hold.
	SettingsFlag string `json:"settings_flag,omitempty"`
	// ResumeArgv resumes a native session; "{session}" is the id. nil:
	// nhm cannot resume this kind.
	ResumeArgv []string `json:"resume_argv,omitempty"`
	// NewSession is the prompt that starts a fresh session in place
	// (recycle).
	NewSession string `json:"new_session,omitempty"`
	// ExitCommand is the prompt that ends the agent.
	ExitCommand string `json:"exit_command,omitempty"`
	// InterruptKeys stop a turn and keep the session; nil: not checked.
	// Key names are herdr's: "esc" is canonical (herdr agent send-keys
	// --help), "enter", "down".
	InterruptKeys []string `json:"interrupt_keys,omitempty"`
	// QuotaPool is the pool the kind's usage counts against;
	// QuotaSource says where nhm reads it ("" when the kind exposes none).
	QuotaPool   string `json:"quota_pool"`
	QuotaSource string `json:"quota_source,omitempty"`
	Hooks       Hooks  `json:"hooks"`
	// Limit recognises the kind's usage-limit message on the screen.
	Limit      *regexp.Regexp `json:"-"`
	Transcript Transcript     `json:"transcript"`
	// CodeReview false leaves the kind out of "--state done" selections
	// unless it is named (ACFS runs Codex for plan reviews only).
	CodeReview bool `json:"code_review"`
	// GuardSettings is the settings file (relative to home) that holds the
	// kind's guard hooks; GuardSupported false means the kind runs no
	// hooks and every spawn warns (pi, F.9).
	GuardSettings  string `json:"guard_settings,omitempty"`
	GuardSupported bool   `json:"guard_supported"`
	// Continue is the continuation rule for compaction and resume (2.7);
	// nil: the kind has none, and a hook event never continues a binding.
	Continue func(e Event, bound string, launchMatches bool) Decision `json:"-"`
	// Nested reports a hook event from a run nested in the agent (a
	// `claude -p` from inside Claude), which says nothing about the
	// pane's occupant.
	Nested func(e Event) bool `json:"-"`
	// SessionStartContext: what a SessionStart hook prints reaches the
	// agent's context (nhm's name reminder).
	SessionStartContext bool `json:"session_start_context"`
	// Program is the program name an Agent Mail identity records.
	Program string `json:"program"`
	// TrustKeys answers the kind's folder-trust dialog on a screen: the
	// keys that say yes, or nil when the screen is not exactly that
	// dialog (spawn --trust-folder).
	TrustKeys func(screen string) []string `json:"-"`
	// Cannot lists what nhm cannot prove for this kind.
	Cannot []string `json:"cannot,omitempty"`
	// Builtin is false for a kind from config.
	Builtin bool `json:"builtin"`
}

// GenericLimit is the limit recogniser of a kind without its own.
var GenericLimit = regexp.MustCompile(`(?i)usage limit reached|rate limit reached|quota exceeded|429 too many requests`)

// LimitPattern is the kind's limit recogniser, or the generic one.
func (a Adapter) LimitPattern() *regexp.Regexp {
	if a.Limit != nil {
		return a.Limit
	}
	return GenericLimit
}

// SessionPattern is a native session id nhm puts into an argv: it never
// starts with "-", so it cannot become an option.
var SessionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// EffortPattern is a reasoning effort nhm puts into an argv.
var EffortPattern = regexp.MustCompile(`^[a-z]{1,16}$`)

// Resume returns the resume argv for a native session, or nil when the
// kind has none or the id is not a plain session id.
func (a Adapter) Resume(session string) []string {
	if a.ResumeArgv == nil || !SessionPattern.MatchString(session) {
		return nil
	}
	out := make([]string, len(a.ResumeArgv))
	for i, s := range a.ResumeArgv {
		out[i] = strings.ReplaceAll(s, "{session}", session)
	}
	return out
}

// Effort returns the effort args for effort (the kind's default when
// empty), or nil when the kind takes none or the value is not a plain
// word.
func (a Adapter) Effort(effort string) []string {
	if effort == "" {
		effort = a.DefaultEffort
	}
	if len(a.EffortArgs) == 0 || !EffortPattern.MatchString(effort) {
		return nil
	}
	out := make([]string, len(a.EffortArgs))
	for i, s := range a.EffortArgs {
		out[i] = strings.ReplaceAll(s, "{effort}", effort)
	}
	return out
}

// HasFlag reports whether f (without dashes) selects the kind.
func (a Adapter) HasFlag(f string) bool {
	return f == a.Name || slices.Contains(a.Flags, f)
}

func fromClaude(e Event, bound string, launch bool) Decision {
	d := claude.Classify(claude.Event{HookEvent: e.HookEvent, Source: e.Source, SessionID: e.SessionID, Entrypoint: e.Entrypoint}, bound, launch)
	return Decision{Verdict: d.Verdict, Reason: d.Reason, Compacted: d.Compacted, Resumed: d.Resumed, TakeSession: d.TakeSession}
}

// The folder-trust dialogs, as ACFS's herdr_agents_trust_keys reads them:
// the exact question and the selected answer, or nothing.
var (
	claudeTrustCursorNo  = regexp.MustCompile(`(?m)❯ *No, exit`)
	claudeTrustYesLine   = regexp.MustCompile(`(?m)^ *Yes, I trust this folder`)
	claudeTrustCursorYes = regexp.MustCompile(`(?m)❯ *Yes, I trust this folder`)
	codexTrustCursor     = regexp.MustCompile(`(?m)› *1\. Trust and continue`)
)

func claudeTrust(screen string) []string {
	if !strings.Contains(screen, "Is this a project you created or one you trust?") {
		return nil
	}
	switch {
	case claudeTrustCursorNo.MatchString(screen) && claudeTrustYesLine.MatchString(screen):
		return []string{"down", "enter"}
	case claudeTrustCursorYes.MatchString(screen):
		return []string{"enter"}
	}
	return nil
}

func codexTrust(screen string) []string {
	if strings.Contains(screen, "Trust this folder?") && codexTrustCursor.MatchString(screen) {
		return []string{"enter"}
	}
	return nil
}

func fromCodex(e Event, bound string, launch bool) Decision {
	d := codex.Classify(codex.Event{HookEvent: e.HookEvent, Source: e.Source, SessionID: e.SessionID}, bound, launch)
	return Decision{Verdict: d.Verdict, Reason: d.Reason, Compacted: d.Compacted, Resumed: d.Resumed, TakeSession: d.TakeSession}
}

// builtins are the seven kinds of plan-v8 2.14, in report order.
func builtins() []Adapter {
	return []Adapter{
		{
			// ntm's launch: --dangerously-skip-permissions, --model,
			// --effort xhigh, and the dcg and rch PreToolUse hooks through
			// --settings (PreToolUse hooks run in every permission mode).
			Name: "claude", NTMType: "claude", Program: "claude-code", TrustKeys: claudeTrust, Flags: []string{"cc"},
			LaunchArgs: []string{"--dangerously-skip-permissions"}, ModelFlag: "--model",
			EffortArgs: []string{"--effort", "{effort}"}, DefaultEffort: "xhigh", SettingsFlag: "--settings",
			ResumeArgv: []string{"claude", "--dangerously-skip-permissions", "--resume", "{session}"}, NewSession: "/clear", ExitCommand: "/exit",
			InterruptKeys: []string{"esc"}, QuotaPool: "claude", QuotaSource: "claude statusLine (agent_quota.sh record-claude)",
			Hooks:      Hooks{SessionStart: true, Stop: true, PostToolUse: true},
			Limit:      regexp.MustCompile(`(?i)usage limit reached|you('|’)ve (hit|reached) your ([a-z0-9-]+ )?limit|(5-hour|session|weekly|opus|sonnet) limit reached|rate_limit_error|api error: 429`),
			Transcript: Transcript{Dir: ".claude/projects", Format: "claude-jsonl"}, CodeReview: true,
			GuardSettings: ".claude/settings.json", GuardSupported: true, Continue: fromClaude,
			Nested:              func(e Event) bool { return claude.Nested(claude.Event{Entrypoint: e.Entrypoint}) },
			SessionStartContext: true,
		},
		{
			// --no-daemon runs Codex's hooks in its own pane (F.22, exp.3).
			// ntm's launch: --dangerously-bypass-approvals-and-sandbox and
			// model_reasoning_effort xhigh; --no-daemon is the fork's (F.22).
			Name: "codex", NTMType: "codex", Program: "codex-cli", TrustKeys: codexTrust, Flags: []string{"cod"},
			LaunchArgs: []string{"--no-daemon", "--dangerously-bypass-approvals-and-sandbox"}, ModelFlag: "--model",
			EffortArgs: []string{"-c", "model_reasoning_effort={effort}"}, DefaultEffort: "xhigh",
			ResumeArgv: []string{"codex", "--no-daemon", "--dangerously-bypass-approvals-and-sandbox", "resume", "{session}"},
			NewSession: "/new", ExitCommand: "/quit",
			InterruptKeys: []string{"esc"}, QuotaPool: "codex", QuotaSource: "codex session logs",
			Hooks:      Hooks{SessionStart: true, Stop: true, PostToolUse: true, Trust: true},
			Limit:      regexp.MustCompile(`(?i)you('|’)ve hit your usage limit|usage limit (has been )?reached|rate limit reached|429 too many requests`),
			Transcript: Transcript{Dir: ".codex/sessions", Format: "codex-jsonl"}, CodeReview: false,
			GuardSettings: ".codex/hooks.json", GuardSupported: true, Continue: fromCodex,
			Cannot: []string{"what Codex does with a SessionStart hook's output"},
		},
		{
			Name: "agy", NTMType: "antigravity", Program: "antigravity", QuotaPool: "agy",
			Limit:      regexp.MustCompile(`(?i)resource_exhausted|quota (exceeded|exhausted)|(reached|exhausted) (your|the) [a-z0-9. -]{0,30}quota|usage limit reached|429 too many requests`),
			CodeReview: true, GuardSettings: ".gemini/config/hooks.json", GuardSupported: true,
			Cannot: []string{"its usage (no quota source)", "its interrupt key", "a SessionStart hook, so a name is not restored after a compaction",
				"its model: agy-locked pins it", "its session resume"},
		},
		{
			Name: "pi", NTMType: "pi", QuotaPool: "pi", CodeReview: true, GuardSupported: false,
			Cannot: []string{"any guard: pi runs no hooks (F.9)", "its usage", "its interrupt key", "its session resume"},
		},
		{
			Name: "omp", NTMType: "omp", InterruptKeys: []string{"esc"}, QuotaPool: "omp", CodeReview: true, GuardSupported: true,
			Cannot: []string{"its guard settings file", "its usage", "its session resume"},
		},
		{
			Name: "opencode", NTMType: "opencode", Flags: []string{"oc"}, QuotaPool: "opencode", CodeReview: true, GuardSupported: true,
			Cannot: []string{"its guard settings file", "its usage", "its interrupt key", "its session resume"},
		},
		{
			Name: "grok", NTMType: "grok", QuotaPool: "grok", CodeReview: true, GuardSupported: true,
			Cannot: []string{"its guard settings file", "its usage", "its interrupt key", "its session resume"},
		},
	}
}

// Registry holds the adapters by name, in registration order.
type Registry struct {
	mu    sync.RWMutex
	order []string
	by    map[string]Adapter
}

// NewRegistry returns a registry with the built-ins.
func NewRegistry() *Registry {
	r := &Registry{by: map[string]Adapter{}}
	for _, a := range builtins() {
		a.Builtin = true
		if a.Program == "" {
			a.Program = a.Name
		}
		r.order = append(r.order, a.Name)
		r.by[a.Name] = a
	}
	return r
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// reserved are the flags of nhm's verbs (send, spawn, wait, interrupt)
// and the robot spawn's "spawn-" flags without that prefix. A kind's name
// and flags become flags of the same verbs, so a kind called "force"
// would take --force.
var reserved = map[string]bool{}

func init() {
	for _, f := range strings.Fields(`all any after-op agent allow-unguarded assign bead cass-check count dry-run
		file force group h help include-self include-user interactive json label limit msg msg-file name
		no-cass no-cass-check no-kickoff op-id pane panes per-tab poll project prompt prompt-file ready-timeout
		require-observations socket stagger-delay stagger-mode state t template thread timeout trust-folder
		type until var version wait when with-cass with-memory yes
		no-cass-context effort memory-query
		dir no-user safety assign-work robot-format robot-spawn robot-send`) {
		reserved[f] = true
	}
}

// Reserved reports a name that a kind or kind flag cannot take: a flag of
// nhm's verbs. The CLI's tests check that every verb flag is here.
func Reserved(name string) bool { return reserved[name] }

// Register adds a kind. A built-in cannot be replaced, and a flag cannot
// name two kinds.
func (r *Registry) Register(a Adapter) error {
	if !namePattern.MatchString(a.Name) {
		return fmt.Errorf("kinds: invalid kind name %q", a.Name)
	}
	for _, n := range append([]string{a.Name}, a.Flags...) {
		if reserved[n] {
			return fmt.Errorf("kinds: %q is a flag of nhm's verbs; a kind or kind flag cannot take it", n)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.by[a.Name]; ok && old.Builtin {
		return fmt.Errorf("kinds: %s is a built-in kind", a.Name)
	}
	for _, n := range r.order {
		if other := r.by[n]; n != a.Name && other.HasFlag(a.Name) {
			return fmt.Errorf("kinds: %s is a flag of %s", a.Name, n)
		}
	}
	for _, f := range a.Flags {
		if !namePattern.MatchString(f) {
			return fmt.Errorf("kinds: %s: invalid flag %q", a.Name, f)
		}
		for _, n := range r.order {
			if other := r.by[n]; n != a.Name && other.HasFlag(f) {
				return fmt.Errorf("kinds: %s: flag %q already selects %s", a.Name, f, n)
			}
		}
	}
	if a.NTMType == "" {
		a.NTMType = a.Name
	}
	if a.Program == "" {
		a.Program = a.Name
	}
	if a.QuotaPool == "" {
		a.QuotaPool = a.Name
	}
	if _, ok := r.by[a.Name]; !ok {
		r.order = append(r.order, a.Name)
	}
	a.Builtin = false
	r.by[a.Name] = a
	return nil
}

// Get returns a kind's adapter.
func (r *Registry) Get(name string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.by[name]
	return a, ok
}

// All returns every adapter in registration order.
func (r *Registry) All() []Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Adapter, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.by[n])
	}
	return out
}

// Names returns the kind names in registration order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.order)
}

// ByFlag returns the kind a selector flag (without dashes) names.
func (r *Registry) ByFlag(flag string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, n := range r.order {
		if a := r.by[n]; a.HasFlag(flag) {
			return a, true
		}
	}
	return Adapter{}, false
}

var (
	defaultOnce sync.Once
	defaultReg  *Registry
	defaultErr  error
)

// ConfigPath is nhm's config file, $XDG_CONFIG_HOME/nhm/config.toml.
func ConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "nhm", "config.toml"), nil
}

// Default is the process's registry: the built-ins plus the [kinds.*]
// tables of nhm's config file, read once. A config that does not load
// adds nothing; ConfigError says why.
func Default() *Registry {
	defaultOnce.Do(func() {
		path, err := ConfigPath()
		if err != nil {
			defaultReg, defaultErr = NewRegistry(), err
			return
		}
		defaultReg, defaultErr = withConfig(path)
	})
	return defaultReg
}

// withConfig is the built-ins plus a config file's kinds. A config that
// fails half way registers none of its kinds.
func withConfig(path string) (*Registry, error) {
	r := NewRegistry()
	if err := r.LoadConfig(path); err != nil {
		return NewRegistry(), err
	}
	return r, nil
}

// ConfigError is why the config's kinds did not load, or nil. nhm doctor
// reports it.
func ConfigError() error {
	Default()
	return defaultErr
}

// Get returns a kind's adapter from Default.
func Get(name string) (Adapter, bool) { return Default().Get(name) }

// Nested reports whether any kind recognises e as a nested run. A hook
// reads it before it knows the pane's kind.
func Nested(e Event) bool {
	for _, a := range Default().All() {
		if a.Nested != nil && a.Nested(e) {
			return true
		}
	}
	return false
}

// Lookup returns a kind's adapter from Default, or an adapter of that
// name with nothing proved: a kind herdr reports that nobody registered.
func Lookup(name string) Adapter {
	if a, ok := Default().Get(name); ok {
		return a
	}
	return Adapter{Name: name, NTMType: ntmUnknown(name), QuotaPool: name, CodeReview: true, GuardSupported: true,
		Cannot: []string{"anything: the kind has no adapter"}}
}

func ntmUnknown(name string) string {
	if name == "" {
		return "unknown"
	}
	return name
}

// All returns Default's adapters.
func All() []Adapter { return Default().All() }

// Names returns Default's kind names.
func Names() []string { return Default().Names() }
